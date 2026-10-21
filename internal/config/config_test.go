package config

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"
)

func setDefaults(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"MYSQL_DSN":  "user:password@tcp(localhost:3306)/corvus?parseTime=true",
		"APP_SECRET": base64.StdEncoding.EncodeToString([]byte("app secret")),
		"ORG_SECRET": base64.StdEncoding.EncodeToString([]byte("org secret")),
		"ADDR":       ":4000", "TOKEN_TTL": "720h", "READ_TIMEOUT": "10s",
		"READ_HEADER_TIMEOUT": "5s", "WRITE_TIMEOUT": "15s", "IDLE_TIMEOUT": "60s",
		"SHUTDOWN_TIMEOUT": "10s", "MAX_BODY_BYTES": "1048576", "RATE_LIMIT": "120",
		"CORS_ORIGINS": "", "DISABLE_AUTH": "true", "AUTO_MIGRATE": "true",
	} {
		t.Setenv(key, value)
	}
}

func TestLoadDefaults(t *testing.T) {
	setDefaults(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":4000" || cfg.TokenTTL != 720*time.Hour {
		t.Fatalf("unexpected address/token TTL: %#v", cfg)
	}
	if cfg.ReadTimeout != 10*time.Second || cfg.ReadHeaderTimeout != 5*time.Second || cfg.WriteTimeout != 15*time.Second || cfg.IdleTimeout != time.Minute || cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected timeout defaults: %#v", cfg)
	}
	if cfg.MaxBodyBytes != 1048576 || cfg.RateLimit != 120 || len(cfg.CORSOrigins) != 0 || !cfg.AutoMigrate || !cfg.CredentialsRequired {
		t.Fatalf("unexpected remaining defaults: %#v", cfg)
	}
}

func TestLoadConfiguredValues(t *testing.T) {
	setDefaults(t)
	for key, value := range map[string]string{
		"ADDR": "127.0.0.1:9000", "TOKEN_TTL": "2h", "READ_TIMEOUT": "3s",
		"READ_HEADER_TIMEOUT": "4s", "WRITE_TIMEOUT": "5s", "IDLE_TIMEOUT": "6s",
		"SHUTDOWN_TIMEOUT": "7s", "MAX_BODY_BYTES": "4096", "RATE_LIMIT": "9",
		"CORS_ORIGINS": "https://one.example, https://two.example", "DISABLE_AUTH": "false", "AUTO_MIGRATE": "false",
	} {
		t.Setenv(key, value)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9000" || cfg.TokenTTL != 2*time.Hour || cfg.ReadTimeout != 3*time.Second || cfg.ReadHeaderTimeout != 4*time.Second || cfg.WriteTimeout != 5*time.Second || cfg.IdleTimeout != 6*time.Second || cfg.ShutdownTimeout != 7*time.Second {
		t.Fatalf("unexpected configured values: %#v", cfg)
	}
	if cfg.MaxBodyBytes != 4096 || cfg.RateLimit != 9 || strings.Join(cfg.CORSOrigins, ",") != "https://one.example,https://two.example" || cfg.CredentialsRequired || cfg.AutoMigrate {
		t.Fatalf("unexpected configured values: %#v", cfg)
	}
}

func TestLoadRejectsInvalidOrMissingEnvironment(t *testing.T) {
	tests := []struct{ name, key, value string }{
		{"missing DSN", "MYSQL_DSN", ""}, {"bad duration", "TOKEN_TTL", "forever"},
		{"zero body", "MAX_BODY_BYTES", "0"}, {"bad rate", "RATE_LIMIT", "wat"},
		{"bad boolean", "AUTO_MIGRATE", "sometimes"}, {"bad secret", "APP_SECRET", "not-base64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setDefaults(t)
			if tt.value == "" {
				_ = os.Unsetenv(tt.key)
			} else {
				t.Setenv(tt.key, tt.value)
			}
			_, err := Load()
			if err == nil {
				t.Fatal("expected configuration error")
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("error %q should identify %s", err, tt.key)
			}
			if tt.key == "MYSQL_DSN" && strings.Contains(err.Error(), "password") {
				t.Fatalf("error leaked DSN: %q", err)
			}
		})
	}
}
