package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/redact"

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
		// Same reasoning as the Warn below in suggest(): LOG_LEVEL=info in
		// production drops Debug entirely, and a rejected response here means
		// the whole choice list vanished for the user, not just one entry.
		b.log.Warn("autocomplete response failed", "err", redact.Err(err))
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
		// Autocomplete is best effort: an empty list just shows no hints, but
		// this is the highest-volume path to bf4db.com and LOG_LEVEL=info in
		// production hides Debug entirely — a block/outage would be silent.
		b.log.Warn("autocomplete lookup failed", "err", redact.Err(err))
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
	// The Discord API rejects the whole autocomplete response (400) if any
	// choice has an empty name, so a single unnamed row would wipe out all
	// up to 25 suggestions. A scraped row with a blank <a> tag (webNameRe in
	// namesearch.go) produces exactly that: Name == "".
	//
	// The TrimSpace is belt-and-braces, not a padding fix: webNameRe already
	// trims around its capture group and parseWebSearch trims again, so a name
	// only reaches here padded if that pipeline changes. Nothing about the
	// rendered label changes for names that merely had surrounding whitespace.
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = "(sem nome)"
	}
	if p.Banned() {
		if reason := strings.TrimSpace(p.BanReason); reason != "" {
			return fmt.Sprintf("%s — banido (%s)", name, reason)
		}
		return name + " — banido"
	}
	return name
}
