package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/config"
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

func TestLoadConfigLogsErrorToInjectedLogger(t *testing.T) {
	t.Setenv(config.EnvBotToken, "")
	t.Setenv(config.EnvBF4DBToken, "")

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	if _, ok := loadConfig(log, ""); ok {
		t.Fatal("expected loadConfig to fail with required vars unset")
	}

	if !strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Fatalf("config error did not reach the structured logger, got: %q", buf.String())
	}
}
