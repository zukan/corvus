package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/zukan/corvus/internal/migration"
)

func TestWriteReportPrintsPartialCountsAndSanitizedErrorContext(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	report := map[string]migration.Counts{"organizations": {Read: 2, Inserted: 1}}
	writeReport(&output, &errorOutput, report, errors.New("insert organizations record 2 failed"))
	if !strings.Contains(output.String(), "organizations read=2 inserted=1 skipped=0") || !strings.Contains(errorOutput.String(), "migration failed: insert organizations record 2 failed") {
		t.Fatalf("partial report or error location missing: stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
}

func TestParseFlagsHelpDoesNotExposeEnvironmentDSNs(t *testing.T) {
	const marker = "synthetic-secret-value"
	var output bytes.Buffer
	_, err := parseFlags([]string{"--help"}, func(string) string { return marker }, &output)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected help status, got %v", err)
	}
	if strings.Contains(output.String(), marker) {
		t.Fatal("help output exposed an environment DSN")
	}
}

func TestParseFlagsErrorDoesNotEchoInvalidFlagValue(t *testing.T) {
	const marker = "synthetic-secret-value"
	var output bytes.Buffer
	_, err := parseFlags([]string{"--dry-run=" + marker}, func(string) string { return "" }, &output)
	if err == nil {
		t.Fatal("expected invalid boolean value to fail")
	}
	if strings.Contains(output.String(), marker) {
		t.Fatal("flag error output exposed the supplied value")
	}
}
