package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRequiresBothTokens(t *testing.T) {
	t.Setenv(EnvBotToken, "")
	t.Setenv(EnvBF4DBToken, "")

	_, err := Load()
	if err == nil {
		t.Fatal("want an error when both tokens are missing")
	}
	for _, want := range []string{EnvBotToken, EnvBF4DBToken} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s: %v", want, err)
		}
	}

	t.Setenv(EnvBotToken, "bot-token")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), EnvBF4DBToken) {
		t.Errorf("want an error naming %s, got %v", EnvBF4DBToken, err)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv(EnvBotToken, "  bot-token  ")
	t.Setenv(EnvBF4DBToken, "api-token")
	t.Setenv(EnvGuildID, "")
	t.Setenv(EnvNameLimit, "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BotToken != "bot-token" || cfg.BF4DBToken != "api-token" {
		t.Errorf("tokens not trimmed: %+v", cfg)
	}
	if cfg.GuildID != "" {
		t.Errorf("GuildID = %q, want empty (global registration)", cfg.GuildID)
	}
	if cfg.NameLimit != 15 || cfg.Timeout != 25*time.Second {
		t.Errorf("defaults = %d / %s", cfg.NameLimit, cfg.Timeout)
	}
}

func TestLoadNameLimit(t *testing.T) {
	t.Setenv(EnvBotToken, "bot-token")
	t.Setenv(EnvBF4DBToken, "api-token")

	t.Setenv(EnvNameLimit, "40")
	cfg, err := Load()
	if err != nil || cfg.NameLimit != 40 {
		t.Fatalf("NameLimit = %d, %v", cfg.NameLimit, err)
	}

	for _, bad := range []string{"0", "-3", "abc"} {
		t.Setenv(EnvNameLimit, bad)
		if _, err := Load(); err == nil {
			t.Errorf("%s=%q should fail", EnvNameLimit, bad)
		}
	}
}
