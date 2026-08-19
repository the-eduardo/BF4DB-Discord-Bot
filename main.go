// Command bf4db-bot is a Discord bot that searches the BF4DB cheater database.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bot"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/config"
)

// version is overwritten at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	removeCommands := flag.Bool("rmcmd", false, "remove the registered commands on shutdown")
	guildID := flag.String("guild", "", "guild id for command registration (default: global)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration", "err", err)
		os.Exit(1)
	}
	if *guildID != "" {
		cfg.GuildID = *guildID
	}

	log := newLogger(cfg.LogLevel)
	logStartup(log)

	client, err := bf4db.New(cfg.BF4DBToken,
		bf4db.WithBaseURL(cfg.BaseURL),
		bf4db.WithWebBaseURL(cfg.WebURL),
		bf4db.WithNameLimit(cfg.NameLimit),
		bf4db.WithNotifier(func(format string, args ...any) {
			log.Info(fmt.Sprintf(format, args...), "source", "bf4db")
		}),
		bf4db.WithLogger(log),
	)
	if err != nil {
		log.Error("bf4db client", "err", err)
		os.Exit(1)
	}

	b, err := bot.New(cfg, client, log)
	if err != nil {
		log.Error("bot", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := b.Run(ctx, *removeCommands); err != nil {
		log.Error("run", "err", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

func logStartup(log *slog.Logger) {
	log.Info("starting", "version", version)
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.ToUpper(strings.TrimSpace(level)))); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
