package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
)

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
