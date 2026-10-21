// Package config loads and validates Corvus service configuration.
package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config contains the validated settings needed to run the service.
type Config struct {
	MYSQLDSN            string
	AppSecret           string
	OrgSecret           string
	Addr                string
	TokenTTL            time.Duration
	ReadTimeout         time.Duration
	ReadHeaderTimeout   time.Duration
	WriteTimeout        time.Duration
	IdleTimeout         time.Duration
	ShutdownTimeout     time.Duration
	MaxBodyBytes        int64
	RateLimit           int
	CORSOrigins         []string
	CredentialsRequired bool
	AutoMigrate         bool
}

// Load reads configuration from the process environment. Errors identify the
// setting that needs attention without including its value.
func Load() (Config, error) {
	var cfg Config
	var err error
	if cfg.MYSQLDSN, err = required("MYSQL_DSN"); err != nil {
		return Config{}, err
	}
	if cfg.AppSecret, err = required("APP_SECRET"); err != nil {
		return Config{}, err
	}
	if err = validateSecret("APP_SECRET", cfg.AppSecret); err != nil {
		return Config{}, err
	}
	if cfg.OrgSecret, err = required("ORG_SECRET"); err != nil {
		return Config{}, err
	}
	if err = validateSecret("ORG_SECRET", cfg.OrgSecret); err != nil {
		return Config{}, err
	}
	if cfg.Addr, err = value("ADDR", ":4000"); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(cfg.Addr) == "" {
		return Config{}, fmt.Errorf("ADDR must not be empty")
	}
	if cfg.TokenTTL, err = duration("TOKEN_TTL", "720h"); err != nil {
		return Config{}, err
	}
	if cfg.ReadTimeout, err = duration("READ_TIMEOUT", "10s"); err != nil {
		return Config{}, err
	}
	if cfg.ReadHeaderTimeout, err = duration("READ_HEADER_TIMEOUT", "5s"); err != nil {
		return Config{}, err
	}
	if cfg.WriteTimeout, err = duration("WRITE_TIMEOUT", "15s"); err != nil {
		return Config{}, err
	}
	if cfg.IdleTimeout, err = duration("IDLE_TIMEOUT", "60s"); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = duration("SHUTDOWN_TIMEOUT", "10s"); err != nil {
		return Config{}, err
	}
	if cfg.MaxBodyBytes, err = positiveInt64("MAX_BODY_BYTES", "1048576"); err != nil {
		return Config{}, err
	}
	if cfg.RateLimit, err = positiveInt("RATE_LIMIT", "120"); err != nil {
		return Config{}, err
	}
	var origins string
	if origins, err = value("CORS_ORIGINS", ""); err != nil {
		return Config{}, err
	}
	for _, origin := range strings.Split(origins, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			cfg.CORSOrigins = append(cfg.CORSOrigins, origin)
		}
	}
	disableAuth, err := boolean("DISABLE_AUTH", "true")
	if err != nil {
		return Config{}, err
	}
	// The legacy name is inverted: true means requests require bearer credentials.
	cfg.CredentialsRequired = disableAuth
	if cfg.AutoMigrate, err = boolean("AUTO_MIGRATE", "true"); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func value(name, fallback string) (string, error) {
	if v, ok := os.LookupEnv(name); ok {
		return v, nil
	}
	return fallback, nil
}

func required(name string) (string, error) {
	v, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return v, nil
}

func validateSecret(name, encoded string) error {
	secret, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(secret) == 0 {
		return fmt.Errorf("%s must be non-empty standard base64", name)
	}
	return nil
}

func duration(name, fallback string) (time.Duration, error) {
	v, _ := value(name, fallback)
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return d, nil
}

func positiveInt(name, fallback string) (int, error) {
	v, _ := value(name, fallback)
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}

func positiveInt64(name, fallback string) (int64, error) {
	v, _ := value(name, fallback)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}

func boolean(name, fallback string) (bool, error) {
	v, _ := value(name, fallback)
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return b, nil
}
