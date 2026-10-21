package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/zukan/corvus/internal/auth"
	"github.com/zukan/corvus/internal/config"
	"github.com/zukan/corvus/internal/httpapi"
	"github.com/zukan/corvus/internal/repository"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("service stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return errors.New("invalid service configuration")
	}
	tokens, err := auth.New(cfg.AppSecret, cfg.OrgSecret, cfg.TokenTTL, cfg.CredentialsRequired)
	if err != nil {
		return errors.New("could not initialize authentication")
	}
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelStartup()
	repo, err := repository.Open(startupCtx, cfg.MYSQLDSN)
	if err != nil {
		return errors.New("could not connect to database")
	}
	defer repo.Close()
	if cfg.AutoMigrate {
		if err := repo.Migrate(startupCtx); err != nil {
			return errors.New("database migration failed")
		}
	}
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.New(repo, tokens, cfg, logger),
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
	serveErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return errors.New("graceful HTTP shutdown failed")
		}
		if err := <-serveErr; err != nil {
			return errors.New("HTTP server failed")
		}
		return nil
	case err := <-serveErr:
		if err != nil {
			return errors.New("HTTP server failed")
		}
		return nil
	}
}
