// Package bot wires the Discord session to the BF4DB client.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/config"
)

// Bot is a running Discord bot.
type Bot struct {
	session *discordgo.Session
	client  *bf4db.Client
	log     *slog.Logger
	guildID string
	timeout time.Duration
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
		session: session,
		client:  client,
		log:     log,
		guildID: cfg.GuildID,
		timeout: cfg.Timeout,
	}

	handlers := map[string]func(*discordgo.Session, *discordgo.InteractionCreate){
		"ping":  b.handlePing,
		"bf4db": b.handleSearch,
	}
	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}
		if h, ok := handlers[i.ApplicationCommandData().Name]; ok {
			h(s, i)
		}
	})
	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Info("connected", "user", r.User.Username, "id", r.User.ID)
	})

	return b, nil
}

// Run opens the session, registers the commands and blocks until ctx is done.
// When removeCommands is set the registered commands are deleted on shutdown.
func (b *Bot) Run(ctx context.Context, removeCommands bool) error {
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("opening session: %w", err)
	}
	defer func() {
		if err := b.session.Close(); err != nil {
			b.log.Error("closing session", "err", err)
		}
	}()

	registered, err := b.registerCommands()
	if err != nil {
		return err
	}
	b.log.Info("commands registered", "count", len(registered), "scope", scope(b.guildID))

	<-ctx.Done()
	b.log.Info("shutting down")

	if removeCommands {
		b.removeCommands(registered)
	}
	return nil
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
			b.log.Error("deleting command", "command", cmd.Name, "err", err)
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
		b.log.Error("responding", "err", err)
	}
}

// edit completes a deferred interaction.
func (b *Bot) edit(s *discordgo.Session, i *discordgo.InteractionCreate, embeds []*discordgo.MessageEmbed) {
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
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	}); err != nil {
		b.log.Error("editing response", "err", err)
	}
}

func scope(guildID string) string {
	if guildID == "" {
		return "global"
	}
	return "guild:" + guildID
}
