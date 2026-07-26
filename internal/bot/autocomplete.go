package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

const (
	// minAutocompleteLen keeps single keystrokes from hammering bf4db.com.
	minAutocompleteLen = 3
	// autocompleteBudget is well inside Discord's 3s deadline for a reply.
	autocompleteBudget = 2 * time.Second
	// maxChoices is Discord's cap on autocomplete options.
	maxChoices = 25
	// maxChoiceName is Discord's cap on a choice label.
	maxChoiceName = 100
)

// handleAutocomplete suggests player names while the user types.
//
// The suggestion carries the persona id as its value, so picking one turns the
// search into an exact id lookup instead of another name search.
func (b *Bot) handleAutocomplete(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()

	var query string
	for _, opt := range data.Options {
		if opt.Focused && opt.Name == optionSearch {
			query = strings.TrimSpace(opt.StringValue())
			break
		}
	}

	choices := b.suggest(query)
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{Choices: choices},
	})
	if err != nil {
		b.log.Debug("autocomplete response failed", "err", err)
	}
}

func (b *Bot) suggest(query string) []*discordgo.ApplicationCommandOptionChoice {
	// An IP or an id is already an exact query, and short prefixes match half
	// the database: neither is worth a request.
	if len(query) < minAutocompleteLen || isIP(query) || isPersonaID(query) {
		return nil
	}

	key := "suggest:" + strings.ToLower(query)
	if cached, ok := b.suggestions.Get(key); ok {
		return cached
	}

	ctx, cancel := context.WithTimeout(context.Background(), autocompleteBudget)
	defer cancel()

	players, err := b.client.SuggestNames(ctx, query, maxChoices)
	if err != nil {
		// Autocomplete is best effort: an empty list just shows no hints.
		b.log.Debug("autocomplete lookup failed", "err", err)
		return nil
	}

	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(players))
	for _, p := range players {
		id := p.PersonaID()
		if id == 0 {
			continue
		}
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  truncate(choiceLabel(p), maxChoiceName),
			Value: strconv.Itoa(id),
		})
	}

	b.suggestions.Set(key, choices)
	return choices
}

func choiceLabel(p bf4db.Player) string {
	if p.Banned() {
		if reason := strings.TrimSpace(p.BanReason); reason != "" {
			return fmt.Sprintf("%s — banido (%s)", p.Name, reason)
		}
		return p.Name + " — banido"
	}
	return p.Name
}
