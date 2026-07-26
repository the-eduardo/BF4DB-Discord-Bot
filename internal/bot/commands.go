package bot

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

const (
	optionSearch  = "global-search"
	optionDiscord = "discord-user"
)

// commands are the slash commands the bot registers.
func commands() []*discordgo.ApplicationCommand {
	dmPermission := false
	return []*discordgo.ApplicationCommand{
		{
			Name:        "ping",
			Description: "Ping the bot to check ms latency",
		},
		{
			Name:         "bf4db",
			Description:  "Search players in the BF4DB cheater database",
			DMPermission: &dmPermission,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         optionSearch,
					Description:  "Player name, IP address or BF4DB player id",
					Required:     false,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        optionDiscord,
					Description: "Search for BF4 accounts linked to a Discord user",
					Required:    false,
				},
			},
		},
	}
}

func (b *Bot) handlePing(s *discordgo.Session, i *discordgo.InteractionCreate) {
	start := time.Now()

	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: "Testing ping..."},
	}); err != nil {
		b.log.Error("ping: initial response failed", "err", err)
		return
	}

	content := fmt.Sprintf("🏓 Pong!\nAPI: %dms\nBot: %dms",
		s.HeartbeatLatency().Milliseconds(), time.Since(start).Milliseconds())
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content}); err != nil {
		b.log.Error("ping: edit failed", "err", err)
	}
}

func (b *Bot) handleSearch(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := map[string]*discordgo.ApplicationCommandInteractionDataOption{}
	for _, opt := range i.ApplicationCommandData().Options {
		options[opt.Name] = opt
	}

	if len(options) == 0 {
		b.respond(s, i, &discordgo.MessageEmbed{
			Title:       "Nada para buscar",
			Description: "Informe `global-search` (nome, IP ou id do BF4DB) e/ou `discord-user`.",
			Color:       colorUnknown,
		})
		return
	}

	// An IP tells you who shares a household, not who cheats: keep it behind a
	// moderator check instead of letting the whole server run it.
	if opt, ok := options[optionSearch]; ok && isIP(opt.StringValue()) && !b.maySearchIP(i.Member) {
		b.respondEphemeral(s, i, "Busca por IP é restrita a moderadores do servidor.")
		return
	}

	// A lookup can take longer than Discord's 3s interaction window — the old
	// build simply lost the response. Acknowledge first, edit later.
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		b.log.Error("search: deferring failed", "err", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()

	var (
		embeds     []*discordgo.MessageEmbed
		components []discordgo.MessageComponent
		now        = time.Now()
	)

	if opt, ok := options[optionSearch]; ok {
		query := strings.TrimSpace(opt.StringValue())
		title := fmt.Sprintf("Busca: %s", sanitize(query))
		if isIP(query) {
			title = "Busca por IP (endereço omitido)" // never echo an IP into a channel
		}

		players, err := b.cachedLookup(ctx, query)
		switch {
		case err != nil:
			b.log.Error("search failed", "query_kind", queryKind(query), "err", err)
			embeds = append(embeds, errorEmbed(title, err))
		default:
			b.log.Info("search done", "query_kind", queryKind(query), "results", len(players))
			embed, comps := b.paginated(title, players, now)
			embeds = append(embeds, embed)
			components = append(components, comps...)
		}
	}

	if opt, ok := options[optionDiscord]; ok {
		user := opt.UserValue(s)
		title := fmt.Sprintf("Contas de %s", sanitize(user.Username))

		players, err := b.client.SearchDiscord(ctx, user.ID)
		if err != nil {
			b.log.Error("discord search failed", "user_id", user.ID, "err", err)
			embeds = append(embeds, errorEmbed(title, err))
		} else {
			b.log.Info("discord search done", "user_id", user.ID, "results", len(players))
			embeds = append(embeds, resultEmbed(title, players, now))
		}
	}

	b.edit(s, i, embeds, components)
}

// paginated renders the first page and, when there is more, keeps the full
// result set around for the buttons.
func (b *Bot) paginated(title string, players []bf4db.Player, now time.Time) (*discordgo.MessageEmbed, []discordgo.MessageComponent) {
	if len(players) <= pageSize {
		return resultEmbed(title, players, now), nil
	}

	key := newResultKey()
	b.results.Set(key, resultSet{title: title, players: players, created: now})

	page, _ := pageOf(players, 0)
	embed := resultEmbed(title, page, now)
	embed.Footer = &discordgo.MessageEmbedFooter{
		Text: fmt.Sprintf("Página 1 de %d • %d contas", pageCount(len(players)), len(players)),
	}
	return embed, paginationComponents(key, 0, len(players))
}

// maySearchIP allows server managers, plus any role listed in BF4DB_IP_ROLE_IDS.
func (b *Bot) maySearchIP(member *discordgo.Member) bool {
	if member == nil {
		return false
	}
	if member.Permissions&discordgo.PermissionManageServer != 0 {
		return true
	}
	for _, roleID := range b.ipRoleIDs {
		if slices.Contains(member.Roles, roleID) {
			return true
		}
	}
	return false
}

// cachedLookup serves repeated questions from memory: the same suspect gets
// checked by several people in a row, and the API allows 70 requests a minute.
func (b *Bot) cachedLookup(ctx context.Context, query string) ([]bf4db.Player, error) {
	key := queryKind(query) + ":" + strings.ToLower(query)
	if players, ok := b.lookups.Get(key); ok {
		b.log.Debug("lookup served from cache", "query_kind", queryKind(query))
		return players, nil
	}

	players, err := b.lookup(ctx, query)
	if err != nil {
		return nil, err
	}
	b.lookups.Set(key, players)
	return players, nil
}

// lookup picks the endpoint that matches the query, the same way the CLI does.
func (b *Bot) lookup(ctx context.Context, query string) ([]bf4db.Player, error) {
	switch {
	case query == "":
		return nil, errors.New("busca vazia")
	case isIP(query):
		return b.client.SearchIP(ctx, query)
	case isPersonaID(query):
		player, err := b.client.Player(ctx, query)
		if err != nil {
			return nil, err
		}
		return []bf4db.Player{player}, nil
	default:
		return b.client.SearchName(ctx, query)
	}
}

func errorEmbed(title string, err error) *discordgo.MessageEmbed {
	description := "Erro ao consultar o BF4DB. Tente novamente em instantes."

	var apiErr *bf4db.APIError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		description = "A consulta ao BF4DB demorou demais e foi cancelada."
	case errors.Is(err, bf4db.ErrNameSearchUnavailable):
		description = "A busca por nome do BF4DB está fora do ar no momento. Tente por IP ou pelo id do jogador."
	case errors.As(err, &apiErr) && apiErr.Unauthorized():
		description = "O BF4DB recusou a chave da API do bot. Avise o administrador."
	case errors.As(err, &apiErr) && apiErr.NotFound():
		description = "Nenhum jogador com esse id no BF4DB."
	}

	return &discordgo.MessageEmbed{
		Title:       title,
		Description: description,
		Color:       colorReview,
	}
}

func isIP(s string) bool { return net.ParseIP(strings.TrimSpace(s)) != nil }

// isPersonaID matches BF4DB persona ids without swallowing numeric nicknames.
func isPersonaID(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 4 || len(s) > 19 {
		return false
	}
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

// queryKind keeps player names and IPs out of the logs.
func queryKind(s string) string {
	switch {
	case isIP(s):
		return "ip"
	case isPersonaID(s):
		return "player_id"
	default:
		return "name"
	}
}
