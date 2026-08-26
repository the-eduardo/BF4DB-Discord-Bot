package bot

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/redact"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

// pageSize is how many accounts fit in one readable embed page.
const pageSize = 5

// customIDPrefix namespaces this bot's components inside the shared Discord
// application (the PunkBuster bot lives in the same one).
const customIDPrefix = "bf4db:p:"

// resultSet is a finished search kept around so the buttons have something to
// page through.
type resultSet struct {
	title   string
	players []bf4db.Player
	created time.Time
	owner   string // discord user id that ran the search; "" = unowned
}

// interactionUserID returns who triggered an interaction, whether it came
// from a guild (Member) or a DM (User).
func interactionUserID(i *discordgo.InteractionCreate) string {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}

// resultOwnedBy reports whether userID may page through set. An empty owner
// keeps old/unset entries open to anyone rather than locking everyone out.
func resultOwnedBy(set resultSet, userID string) bool {
	return set.owner == "" || set.owner == userID
}

// newResultKey is a random handle for a result set; ids are never guessable so
// one channel cannot page through another channel's search.
func newResultKey() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func pageCount(total int) int {
	if total == 0 {
		return 1
	}
	return (total + pageSize - 1) / pageSize
}

// pageOf returns the slice of players shown on a page, clamped to range.
func pageOf(players []bf4db.Player, page int) ([]bf4db.Player, int) {
	pages := pageCount(len(players))
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	start := page * pageSize
	end := min(start+pageSize, len(players))
	return players[start:end], page
}

// paginationComponents builds the prev/next row, or nothing when a single page
// holds everything.
func paginationComponents(key string, page, total int) []discordgo.MessageComponent {
	pages := pageCount(total)
	if pages <= 1 {
		return nil
	}
	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Anterior",
					Style:    discordgo.SecondaryButton,
					CustomID: fmt.Sprintf("%s%s:%d", customIDPrefix, key, page-1),
					Disabled: page == 0,
					Emoji:    &discordgo.ComponentEmoji{Name: "◀"},
				},
				discordgo.Button{
					Label:    fmt.Sprintf("%d/%d", page+1, pages),
					Style:    discordgo.SecondaryButton,
					CustomID: customIDPrefix + "noop",
					Disabled: true,
				},
				discordgo.Button{
					Label:    "Próxima",
					Style:    discordgo.SecondaryButton,
					CustomID: fmt.Sprintf("%s%s:%d", customIDPrefix, key, page+1),
					Disabled: page >= pages-1,
					Emoji:    &discordgo.ComponentEmoji{Name: "▶"},
				},
			},
		},
	}
}

// parseCustomID reads back the key and page a button carries.
func parseCustomID(customID string) (key string, page int, ok bool) {
	rest, found := strings.CutPrefix(customID, customIDPrefix)
	if !found {
		return "", 0, false
	}
	key, rawPage, found := strings.Cut(rest, ":")
	if !found || key == "" {
		return "", 0, false
	}
	page, err := strconv.Atoi(rawPage)
	if err != nil {
		return "", 0, false
	}
	return key, page, true
}

// handleComponent answers a pagination button press.
func (b *Bot) handleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	key, page, ok := parseCustomID(i.MessageComponentData().CustomID)
	if !ok {
		return // not ours: the PunkBuster bot shares this application
	}

	set, found := b.results.Get(key)
	if !found {
		b.respondEphemeral(s, i, "Esta busca expirou. Rode `/bf4db` de novo.")
		return
	}
	if !resultOwnedBy(set, interactionUserID(i)) {
		b.respondEphemeral(s, i, "Essa busca é de outra pessoa. Rode `/bf4db` você mesmo.")
		return
	}

	players, page := pageOf(set.players, page)
	embed := resultEmbed(set.title, players, time.Now())
	embed.Footer = &discordgo.MessageEmbedFooter{
		Text: fmt.Sprintf("Página %d de %d • %d contas", page+1, pageCount(len(set.players)), len(set.players)),
	}

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Embeds:          []*discordgo.MessageEmbed{embed},
			Components:      paginationComponents(key, page, len(set.players)),
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		},
	})
	if err != nil {
		b.log.Error("pagination update failed", "err", redact.Err(err))
	}
}
