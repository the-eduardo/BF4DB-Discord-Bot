package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestLogStartupIncludesVersion(t *testing.T) {
	old := version
	version = "test-1.2.3"
	defer func() { version = old }()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	logStartup(log)

	out := buf.String()
	if !strings.Contains(out, `"version":"test-1.2.3"`) {
		t.Fatalf("expected version in startup log, got: %s", out)
	}
}
