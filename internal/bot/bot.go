// Package bot wires the Discord session to the BF4DB client.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/cache"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/config"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/kuma"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/redact"
)

// Cache budgets. Lookups are cached long enough to absorb a channel checking
// the same suspect repeatedly; result sets only need to outlive the buttons.
const (
	lookupTTL     = 5 * time.Minute
	lookupMax     = 500
	suggestionTTL = 60 * time.Second
	suggestionMax = 200
	resultTTL     = 15 * time.Minute
	resultMax     = 200
)

// Bot is a running Discord bot.
type Bot struct {
	session *discordgo.Session
	client  *bf4db.Client
	log     *slog.Logger
	guildID string
	timeout time.Duration

	ipRoleIDs []string
	pusher    *kuma.Pusher
	connected atomic.Bool

	lookups     *cache.Cache[[]bf4db.Player]
	suggestions *cache.Cache[[]*discordgo.ApplicationCommandOptionChoice]
	results     *cache.Cache[resultSet]
}

// New builds a bot from validated configuration.
func New(cfg config.Config, client *bf4db.Client, log *slog.Logger) (*Bot, error) {
	session, err := discordgo.New("Bot " + cfg.BotToken)
	if err != nil {
		return nil, fmt.Errorf("discord session: %w", err)
	}
	// The bot only answers slash commands: no privileged intents needed.
	session.Identify.Intents = discordgo.IntentsNone

	b := &Bot{
		session:     session,
		client:      client,
		log:         log,
		guildID:     cfg.GuildID,
		timeout:     cfg.Timeout,
		ipRoleIDs:   cfg.IPRoleIDs,
		pusher:      kuma.NewPusher(cfg.KumaPushURL, log),
		lookups:     cache.New[[]bf4db.Player](lookupTTL, lookupMax),
		suggestions: cache.New[[]*discordgo.ApplicationCommandOptionChoice](suggestionTTL, suggestionMax),
		results:     cache.New[resultSet](resultTTL, resultMax),
	}

	session.AddHandler(b.route)
	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		b.connected.Store(true)
		log.Info("connected", "user", r.User.Username, "id", r.User.ID)
	})
	session.AddHandler(func(s *discordgo.Session, r *discordgo.Resumed) {
		b.connected.Store(true)
		log.Info("session resumed")
	})
	session.AddHandler(func(s *discordgo.Session, d *discordgo.Disconnect) {
		b.connected.Store(false)
		log.Warn("gateway disconnected")
	})

	return b, nil
}

// route dispatches every interaction type this bot answers. Components and
// autocomplete share the application with the PunkBuster bot, so anything not
// recognised is ignored rather than answered.
func (b *Bot) route(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		switch i.ApplicationCommandData().Name {
		case "ping":
			b.handlePing(s, i)
		case "bf4db":
			b.handleSearch(s, i)
		}
	case discordgo.InteractionApplicationCommandAutocomplete:
		if i.ApplicationCommandData().Name == "bf4db" {
			b.handleAutocomplete(s, i)
		}
	case discordgo.InteractionMessageComponent:
		b.handleComponent(s, i)
	}
}

// Run opens the session, registers the commands and blocks until ctx is done.
// When removeCommands is set the registered commands are deleted on shutdown.
func (b *Bot) Run(ctx context.Context, removeCommands bool) error {
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("opening session: %w", err)
	}
	defer func() {
		b.connected.Store(false)
		if err := b.session.Close(); err != nil {
			b.log.Error("closing session", "err", redact.Err(err))
		}
	}()

	registered, err := b.registerCommands()
	if err != nil {
		return err
	}
	b.log.Info("commands registered", "count", len(registered), "scope", scope(b.guildID))

	go b.pusher.Run(ctx, b.liveness)

	<-ctx.Done()
	b.log.Info("shutting down")

	if removeCommands {
		b.removeCommands(registered)
	}
	return nil
}

// liveness reports whether the gateway is connected, plus its latency.
func (b *Bot) liveness() (bool, time.Duration) {
	if !b.connected.Load() {
		return false, 0
	}
	return true, b.session.HeartbeatLatency()
}

func (b *Bot) registerCommands() ([]*discordgo.ApplicationCommand, error) {
	defs := commands()
	registered := make([]*discordgo.ApplicationCommand, 0, len(defs))
	for _, def := range defs {
		cmd, err := b.session.ApplicationCommandCreate(b.session.State.User.ID, b.guildID, def)
		if err != nil {
			// Registering is all-or-nothing for a usable bot, but a panic on a
			// transient Discord error was never the right answer.
			return registered, fmt.Errorf("registering %q: %w", def.Name, err)
		}
		registered = append(registered, cmd)
	}
	return registered, nil
}

func (b *Bot) removeCommands(registered []*discordgo.ApplicationCommand) {
	for _, cmd := range registered {
		if err := b.session.ApplicationCommandDelete(b.session.State.User.ID, b.guildID, cmd.ID); err != nil {
			b.log.Error("deleting command", "command", cmd.Name, "err", redact.Err(err))
		}
	}
	b.log.Info("commands removed", "count", len(registered))
}

// respond answers an interaction that has not been deferred.
func (b *Bot) respond(s *discordgo.Session, i *discordgo.InteractionCreate, embed *discordgo.MessageEmbed) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds:          []*discordgo.MessageEmbed{embed},
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		},
	})
	if err != nil {
		b.log.Error("responding", "err", redact.Err(err))
	}
}

// respondEphemeral answers only the user who interacted.
func (b *Bot) respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:         content,
			Flags:           discordgo.MessageFlagsEphemeral,
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		},
	})
	if err != nil {
		b.log.Error("responding ephemerally", "err", redact.Err(err))
	}
}

// edit completes a deferred interaction.
func (b *Bot) edit(s *discordgo.Session, i *discordgo.InteractionCreate, embeds []*discordgo.MessageEmbed, components []discordgo.MessageComponent) {
	// A deferred interaction must be edited with something; an empty payload is
	// rejected by Discord and the user is left staring at "thinking…".
	if len(embeds) == 0 {
		embeds = []*discordgo.MessageEmbed{{
			Title:       "Sem resultados",
			Description: "Nenhuma conta encontrada.",
			Color:       colorUnknown,
		}}
	}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Embeds:          &embeds,
		Components:      &components,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	}); err != nil {
		b.log.Error("editing response", "err", redact.Err(err))
	}
}

func scope(guildID string) string {
	if guildID == "" {
		return "global"
	}
	return "guild:" + guildID
}
