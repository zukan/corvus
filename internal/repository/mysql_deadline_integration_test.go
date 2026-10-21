//go:build integration

package repository

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestMySQLIntegrationDeadlineReleasesConnection(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.db.SetMaxOpenConns(1)
	queryCtx, queryCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer queryCancel()
	var result int
	err = r.db.QueryRowContext(queryCtx, "SELECT SLEEP(30)").Scan(&result)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled query error = %v", err)
	}
	// A canceled query must not retain the only pool connection.
	if err := r.db.QueryRowContext(ctx, "SELECT 1").Scan(&result); err != nil || result != 1 {
		t.Fatalf("query after cancellation: result=%d error=%v", result, err)
	}
}
