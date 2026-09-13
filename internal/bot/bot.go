// Package bot wires the Discord session to the BF4DB client.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/cache"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/config"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/kuma"
	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/redact"
)

// maxMessageChars is Discord's per-MESSAGE embed budget, not per-embed: the API
// sums title+description+field.name+field.value+footer.text+author.name across
// every embed attached to the message and rejects the whole edit above 6000.
// render.go's maxEmbedChars (5500) only bounds a single embed — handleSearch can
// attach two (global-search + discord-user) to the same edit, and two embeds
// each near their own budget comfortably clear the combined one.
const maxMessageChars = 5900

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
	return true, b.heartbeat()
}

// heartbeat reports the last heartbeat latency, never negative: discordgo
// computes LastHeartbeatAck.Sub(LastHeartbeatSent), and Ack is only updated on
// the gateway's Op 11 — between sending a heartbeat and receiving its ack
// (one RTT) the subtraction goes against the previous cycle's Ack and comes
// out negative. Zero means "no fresh measurement", which is what the Kuma
// push and /ping should show instead.
func (b *Bot) heartbeat() time.Duration {
	d := b.session.HeartbeatLatency()
	if d > 0 {
		return d
	}
	// O clamp apaga o único sinal in-band de gateway zumbi: quando os Op 11
	// param, a latência crua fica cada vez mais negativa até o discordgo
	// reconectar. Nenhum alerta se perde (o Kuma alerta por AUSÊNCIA de push),
	// mas sem isto o sintoma some por completo.
	b.log.Debug("heartbeat sem medicao fresca", "raw_ms", d.Milliseconds())
	return 0
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
	// rejected by Discord and the user is left staring at "thinking…". O corte
	// vem ANTES do fallback de propósito: fitEmbeds pode zerar a lista (embed
	// único grande demais até sem campos), e se o fallback rodasse primeiro
	// esse caso mandaria `"embeds": []` ao Discord — exatamente o 400 e o
	// "Thinking..." eterno que esta mudança existe para evitar.
	embeds = fitEmbeds(embeds)
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

// embedChars sums exactly the fields Discord counts toward the 6000-char
// message-wide embed budget: title, description, footer text, author name and
// every field's name+value. Counted in runes, matching truncate() elsewhere in
// this package — Discord's own limit is rune-based, not byte-based.
func embedChars(e *discordgo.MessageEmbed) int {
	n := utf8.RuneCountInString(e.Title) + utf8.RuneCountInString(e.Description)
	if e.Footer != nil {
		n += utf8.RuneCountInString(e.Footer.Text)
	}
	if e.Author != nil {
		n += utf8.RuneCountInString(e.Author.Name)
	}
	for _, f := range e.Fields {
		n += utf8.RuneCountInString(f.Name) + utf8.RuneCountInString(f.Value)
	}
	return n
}

// fitEmbeds trims embeds, in order, to fit under maxMessageChars combined.
// Each embed already sits under the per-embed budget on its own (render.go);
// this only matters once two land in the same edit. The first embed to blow
// the remaining budget loses fields from the end until it fits; if it still
// doesn't fit with zero fields, it's dropped entirely rather than sent
// truncated to nothing.
func fitEmbeds(embeds []*discordgo.MessageEmbed) []*discordgo.MessageEmbed {
	fitted := make([]*discordgo.MessageEmbed, 0, len(embeds))
	remaining := maxMessageChars
	for _, e := range embeds {
		fe, n, ok := fitEmbed(e, remaining)
		if !ok {
			continue
		}
		fitted = append(fitted, fe)
		remaining -= n
	}
	return fitted
}

// fitEmbed drops e's trailing fields until embedChars fits within budget,
// marking the footer once a field is actually cut so the truncation isn't
// silent. Returns ok=false when even an empty embed doesn't fit the budget, or
// when fitting it would cost every single one of its fields: um embed que
// tinha conteúdo e sobra só como título+rodapé é uma caixa vazia no Discord,
// pior que a ausência dele.
func fitEmbed(e *discordgo.MessageEmbed, budget int) (fitted *discordgo.MessageEmbed, chars int, ok bool) {
	trimmed := *e
	fields := e.Fields
	cut := false
	for {
		trimmed.Fields = fields
		n := embedChars(&trimmed)
		if n <= budget {
			if len(trimmed.Fields) == 0 && len(e.Fields) > 0 {
				return nil, 0, false
			}
			return &trimmed, n, true
		}
		if len(fields) == 0 {
			return nil, 0, false
		}
		fields = fields[:len(fields)-1]
		if !cut {
			cut = true
			trimmed.Footer = truncatedFooter(e.Footer)
		}
	}
}

// truncatedFooter marks that an embed lost fields to the message-wide budget,
// preserving whatever footer text (e.g. pagination) was already there.
func truncatedFooter(existing *discordgo.MessageEmbedFooter) *discordgo.MessageEmbedFooter {
	const note = "resposta truncada"
	if existing == nil || existing.Text == "" {
		return &discordgo.MessageEmbedFooter{Text: note}
	}
	return &discordgo.MessageEmbedFooter{Text: existing.Text + " • " + note}
}

func scope(guildID string) string {
	if guildID == "" {
		return "global"
	}
	return "guild:" + guildID
}
