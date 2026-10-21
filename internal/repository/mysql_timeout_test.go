package repository

import (
	"testing"
	"time"
)

func TestMySQLDriverTimeoutDefaults(t *testing.T) {
	for _, params := range []string{"", "?timeout=0s&readTimeout=0s&writeTimeout=0s"} {
		cfg, err := parseMySQLConfig("user:password@tcp(localhost:3306)/corvus" + params)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Timeout != 5*time.Second || cfg.ReadTimeout != 10*time.Second || cfg.WriteTimeout != 10*time.Second {
			t.Fatalf("driver timeout defaults: dial=%v read=%v write=%v", cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout)
		}
	}
	cfg, err := parseMySQLConfig("user:password@tcp(localhost:3306)/corvus?timeout=1s&readTimeout=2s&writeTimeout=3s")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout != time.Second || cfg.ReadTimeout != 2*time.Second || cfg.WriteTimeout != 3*time.Second {
		t.Fatal("explicit driver timeouts were not preserved")
	}
}
