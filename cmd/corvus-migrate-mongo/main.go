package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zukan/corvus/internal/migration"
	"github.com/zukan/corvus/internal/repository"
)

type configuration struct {
	mongoURI, mongoDatabase, mysqlDSN string
	dryRun                            bool
}

func parseFlags(args []string, getenv func(string) string, output io.Writer) (configuration, error) {
	var cfg configuration
	flags := flag.NewFlagSet("corvus-migrate-mongo", flag.ContinueOnError)
	var diagnostics bytes.Buffer
	flags.SetOutput(&diagnostics)
	flags.StringVar(&cfg.mongoURI, "mongo-uri", "", "MongoDB connection URI")
	flags.StringVar(&cfg.mongoDatabase, "mongo-database", "", "MongoDB database name")
	flags.StringVar(&cfg.mysqlDSN, "mysql-dsn", "", "MySQL connection DSN")
	flags.BoolVar(&cfg.dryRun, "dry-run", false, "validate and count without connecting to MySQL")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.Copy(output, &diagnostics)
		}
		return cfg, err
	}
	if cfg.mongoURI == "" {
		cfg.mongoURI = getenv("MONGO_URI")
	}
	if cfg.mongoDatabase == "" {
		cfg.mongoDatabase = getenv("MONGO_DATABASE")
	}
	if cfg.mysqlDSN == "" {
		cfg.mysqlDSN = getenv("MYSQL_DSN")
	}
	return cfg, nil
}

func main() {
	cfg, err := parseFlags(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid command-line flags")
		os.Exit(2)
	}
	if cfg.mongoURI == "" || cfg.mongoDatabase == "" || (!cfg.dryRun && cfg.mysqlDSN == "") {
		fmt.Fprintln(os.Stderr, "required configuration is missing")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mongoCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	source, err := migration.OpenMongo(mongoCtx, cfg.mongoURI, cfg.mongoDatabase)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "migration failed: MongoDB connection")
		os.Exit(1)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = source.Close(closeCtx)
	}()

	var sink migration.Sink
	if !cfg.dryRun {
		mysqlCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		db, e := repository.Open(mysqlCtx, cfg.mysqlDSN)
		cancel()
		if e != nil {
			fmt.Fprintln(os.Stderr, "migration failed: MySQL connection")
			os.Exit(1)
		}
		defer db.Close()
		sink = db
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	report, err := migration.Run(runCtx, source, sink, cfg.dryRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migration failed")
		os.Exit(1)
	}
	for _, name := range []string{"organizations", "admins", "applications", "comments", "replies"} {
		count := report[name]
		fmt.Printf("%s read=%d inserted=%d skipped=%d\n", name, count.Read, count.Inserted, count.Skipped)
	}
}
