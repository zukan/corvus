//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestMySQLIntegrationReadinessTracksSchemaAndRepair(t *testing.T) {
	dsn := os.Getenv("MYSQL_READY_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_READY_TEST_DSN is not configured")
	}
	cfg, err := parseMySQLConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = ""
	adminDB, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminDB.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := fmt.Sprintf("corvus_ready_%d", time.Now().UnixNano())
	if _, err = adminDB.ExecContext(ctx, "CREATE DATABASE `"+name+"`"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := adminDB.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS `"+name+"`"); cleanupErr != nil {
			t.Errorf("drop disposable readiness database: %v", cleanupErr)
		}
	})

	cfg.DBName = name
	rawDB, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	r := &MySQL{db: rawDB}
	t.Cleanup(func() { _ = r.Close() })
	requireNotReady := func(label string) {
		t.Helper()
		if err := r.Ready(ctx); err == nil {
			t.Fatalf("Ready succeeded with %s", label)
		}
	}
	requireReady := func(label string) {
		t.Helper()
		if err := r.Ready(ctx); err != nil {
			t.Fatalf("Ready failed after repairing %s: %v", label, err)
		}
	}

	requireNotReady("an empty schema")
	if err := r.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	requireReady("the empty schema")

	if _, err := rawDB.ExecContext(ctx, "UPDATE schema_migrations SET version='000_stale'"); err != nil {
		t.Fatal(err)
	}
	requireNotReady("a stale migration version")
	version, err := latestSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.ExecContext(ctx, "UPDATE schema_migrations SET version=?", version); err != nil {
		t.Fatal(err)
	}
	requireReady("the migration version")

	if _, err := rawDB.ExecContext(ctx, "ALTER TABLE comments DROP INDEX idx_comments_tenant_ref, DROP COLUMN ref_id"); err != nil {
		t.Fatal(err)
	}
	requireNotReady("a missing required column")
	if _, err := rawDB.ExecContext(ctx, "ALTER TABLE comments ADD COLUMN ref_id MEDIUMTEXT NULL, ADD KEY idx_comments_tenant_ref (application_id,ref_id(191),deleted,created_at,id)"); err != nil {
		t.Fatal(err)
	}
	requireReady("the missing required column")

	if _, err := rawDB.ExecContext(ctx, "DROP TABLE comment_replies"); err != nil {
		t.Fatal(err)
	}
	requireNotReady("a missing required table")
	if _, err := rawDB.ExecContext(ctx, "DROP TABLE schema_migrations"); err != nil {
		t.Fatal(err)
	}
	if err := r.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	requireReady("the missing required table")
}
