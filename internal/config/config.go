// Package config loads and validates the bot's runtime configuration.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variables read by the bot.
const (
	EnvBotToken   = "DISCORD_BOT_TOKEN"
	EnvBF4DBToken = "BF4DB_API"
	EnvGuildID    = "DISCORD_GUILD_ID"
	EnvBaseURL    = "BF4DB_BASE_URL"
	EnvWebURL     = "BF4DB_WEB_URL"
	EnvLogLevel   = "LOG_LEVEL"
	EnvNameLimit  = "BF4DB_NAME_LIMIT"
)

// Config is everything the bot needs to run.
type Config struct {
	BotToken   string
	BF4DBToken string
	GuildID    string // empty registers commands globally
	BaseURL    string
	WebURL     string
	LogLevel   string
	NameLimit  int
	Timeout    time.Duration
}

// Load reads the environment and fails fast when a required value is missing,
// instead of starting a bot that answers every command with an error.
func Load() (Config, error) {
	cfg := Config{
		BotToken:   strings.TrimSpace(os.Getenv(EnvBotToken)),
		BF4DBToken: strings.TrimSpace(os.Getenv(EnvBF4DBToken)),
		GuildID:    strings.TrimSpace(os.Getenv(EnvGuildID)),
		BaseURL:    strings.TrimSpace(os.Getenv(EnvBaseURL)),
		WebURL:     strings.TrimSpace(os.Getenv(EnvWebURL)),
		LogLevel:   strings.TrimSpace(os.Getenv(EnvLogLevel)),
		NameLimit:  15,
		Timeout:    25 * time.Second,
	}

	var missing []string
	if cfg.BotToken == "" {
		missing = append(missing, EnvBotToken)
	}
	if cfg.BF4DBToken == "" {
		missing = append(missing, EnvBF4DBToken)
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required environment variable(s): %s", strings.Join(missing, ", "))
	}

	if raw := strings.TrimSpace(os.Getenv(EnvNameLimit)); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("%s must be a positive integer, got %q", EnvNameLimit, raw)
		}
		cfg.NameLimit = n
	}
	return cfg, nil
}
