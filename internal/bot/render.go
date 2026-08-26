package bot

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

// Discord's documented limits for a single message.
const (
	maxEmbedFields = 25
	maxEmbedChars  = 5500 // below the 6000 hard limit, leaving room for titles
	maxFieldValue  = 1024
	maxFieldName   = 256
)

// Embed colours per ban status.
const (
	colorBanned  = 0xE53935
	colorClean   = 0x43A047
	colorReview  = 0xFB8C00
	colorUnknown = 0x546E7A
)

// resultEmbed renders a search result. Player data is never used as a format
// string or interpolated into one: names come straight from BF4DB and a name
// containing %s or Discord markup used to corrupt the whole message.
func resultEmbed(title string, players []bf4db.Player, now time.Time) *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Title:     title,
		Color:     colorUnknown,
		Timestamp: now.Format(time.RFC3339),
	}
	if len(players) == 0 {
		embed.Description = "Nenhuma conta encontrada."
		return embed
	}

	size := len(title)
	shown := 0
	for _, p := range players {
		if shown >= maxEmbedFields {
			break
		}
		id := p.PersonaID()
		if id == 0 {
			continue
		}

		name := truncate(fmt.Sprintf("%s — %s", sanitize(p.Name), statusLabel(p)), maxFieldName)
		value := truncate(playerLines(p, id, now), maxFieldValue)
		if size+len(name)+len(value) > maxEmbedChars {
			break
		}
		size += len(name) + len(value)
		shown++

		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:  name,
			Value: value,
		})
		embed.Color = worstColor(embed.Color, p)
	}

	if shown < len(players) {
		embed.Footer = &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Mostrando %d de %d contas", shown, len(players)),
		}
	}
	return embed
}

func playerLines(p bf4db.Player, id int, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Motivo: %s • Cheat score: %d\n", sanitize(p.Reason()), int(p.CheatScore))
	fmt.Fprintf(&b, "[BF4DB](%s) • [Cheat Report](%s) • [BF Agency](%s)",
		bf4db.ProfileURL(id), bf4db.CheatReportURL(id, now), bf4db.AgencyURL(id))
	return b.String()
}

func statusLabel(p bf4db.Player) string {
	switch int(p.IsBanned) {
	case bf4db.BanActive:
		return "🔴 banido"
	case bf4db.BanNone:
		return "🟢 limpo"
	case bf4db.BanUnderReview:
		return "🟡 em análise"
	case bf4db.BanNotReported:
		return "⚪ não reportado"
	case bf4db.BanStaff:
		return "🔵 staff BF4DB"
	case bf4db.BanGlitch:
		return "🟠 glitch"
	case bf4db.BanExploit:
		return "🟠 exploit"
	default:
		return p.Status()
	}
}

// worstColor keeps the most alarming status visible in the embed stripe.
func worstColor(current int, p bf4db.Player) int {
	if current == colorBanned {
		return current
	}
	switch int(p.IsBanned) {
	case bf4db.BanActive, bf4db.BanGlitch, bf4db.BanExploit:
		return colorBanned
	case bf4db.BanUnderReview:
		if current != colorBanned {
			return colorReview
		}
	case bf4db.BanNone:
		if current == colorUnknown {
			return colorClean
		}
	}
	return current
}

// sanitize neutralizes Discord markup and mentions in player-supplied text.
// BF4 names legitimately contain *, _, ` and @.
var markupReplacer = strings.NewReplacer(
	"`", "'",
	"*", "\\*",
	"_", "\\_",
	"~", "\\~",
	"|", "\\|",
	">", "\\>",
	"@", "@​", // zero-width space defuses @everyone / @here
)

// stripInvisible drops Unicode characters that render as nothing but change how
// the rest of the text is displayed. Names here come from scraping bf4db.com, so
// they are attacker-controlled: a right-to-left override (U+202E) reverses the
// text after it, letting a name read as something else entirely in the client,
// and zero-width characters pad a name past a length check while looking short.
//
// This runs BEFORE markupReplacer, never after: the replacer deliberately INSERTS
// a zero-width space into "@" to defuse @everyone, and stripping afterwards would
// undo that defence and re-arm the mention.
//
// Cf also covers ZWJ (U+200D), so an emoji sequence in a name degrades into its
// separate emoji. That is a cosmetic loss on a field that BF4 names barely use,
// traded for closing a spoofing vector — worth it here, and the reason this is
// applied to display labels rather than to stored data.
func stripInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r), // bidi overrides, zero-width, BOM
			unicode.Is(unicode.Cc, r), // C0/C1 controls
			unicode.Is(unicode.Zl, r), // line separator
			unicode.Is(unicode.Zp, r): // paragraph separator
			return -1
		}
		return r
	}, s)
}

func sanitize(s string) string {
	s = strings.TrimSpace(stripInvisible(s))
	if s == "" {
		return "(sem nome)"
	}
	return markupReplacer.Replace(s)
}

// truncate cuts to limit characters, counting runes the way Discord does. A
// byte-based cut overshoots the limit as soon as the ellipsis (3 bytes) or any
// non-ASCII character is involved, and can split a rune in half.
func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit-1]) + "…"
}
