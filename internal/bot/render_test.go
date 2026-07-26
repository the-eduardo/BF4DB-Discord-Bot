package bot

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

var testTime = time.Date(2026, 7, 26, 15, 4, 0, 0, time.UTC)

func TestResultEmbedRendersPlayers(t *testing.T) {
	players := []bf4db.Player{
		{ID: 815195001, Name: "Vanina22", IsBanned: bf4db.BanActive, BanReason: "Linked Account", CheatScore: 100},
		{PlayerID: 988768601, Name: "EdUwUardo", IsBanned: bf4db.BanNone, BanReason: "No Active BF4 Ban"},
	}

	embed := resultEmbed("Busca: teste", players, testTime)
	if len(embed.Fields) != 2 {
		t.Fatalf("got %d fields, want 2", len(embed.Fields))
	}
	if !strings.Contains(embed.Fields[0].Name, "Vanina22") || !strings.Contains(embed.Fields[0].Name, "banido") {
		t.Errorf("field name = %q", embed.Fields[0].Name)
	}
	if !strings.Contains(embed.Fields[0].Value, "Linked Account") ||
		!strings.Contains(embed.Fields[0].Value, "Cheat score: 100") ||
		!strings.Contains(embed.Fields[0].Value, "https://bf4db.com/player/815195001/") {
		t.Errorf("field value = %q", embed.Fields[0].Value)
	}
	if embed.Color != colorBanned {
		t.Errorf("color = %#x, want the banned colour", embed.Color)
	}
	if embed.Footer != nil {
		t.Errorf("unexpected footer: %+v", embed.Footer)
	}

	empty := resultEmbed("Busca: nada", nil, testTime)
	if !strings.Contains(empty.Description, "Nenhuma conta") || len(empty.Fields) != 0 {
		t.Errorf("empty embed = %+v", empty)
	}
}

// The old build concatenated player data into the string it then passed to
// fmt.Sprintf, so a name with a verb in it corrupted the whole message.
func TestResultEmbedIsNotAFormatString(t *testing.T) {
	players := []bf4db.Player{
		{ID: 1, Name: "%s%d%!v(MISSING)", IsBanned: bf4db.BanActive, BanReason: "Aimbot %s"},
	}
	embed := resultEmbed("Busca: %s", players, testTime)

	if !strings.Contains(embed.Fields[0].Name, "%s%d") {
		t.Errorf("name mangled: %q", embed.Fields[0].Name)
	}
	if strings.Contains(embed.Fields[0].Name, "MISSING)") && !strings.Contains(embed.Fields[0].Name, "%!v(MISSING)") {
		t.Errorf("verb was expanded: %q", embed.Fields[0].Name)
	}
	if !strings.Contains(embed.Fields[0].Value, "Aimbot %s") {
		t.Errorf("reason mangled: %q", embed.Fields[0].Value)
	}
}

func TestSanitizeNeutralizesMarkupAndMentions(t *testing.T) {
	got := sanitize("**@everyone** `code` _x_ |spoiler| >quote")
	for _, forbidden := range []string{"@everyone", "`"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("sanitize left %q in %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "\\*\\*") || !strings.Contains(got, "\\_") {
		t.Errorf("markup not escaped: %q", got)
	}
	if sanitize("   ") != "(sem nome)" {
		t.Errorf("blank name = %q", sanitize("   "))
	}
}

func TestResultEmbedRespectsDiscordLimits(t *testing.T) {
	longName := strings.Repeat("n", 400)
	longReason := strings.Repeat("r", 2000)

	var players []bf4db.Player
	for i := range 40 {
		players = append(players, bf4db.Player{
			ID: bf4db.FlexInt(i + 1), Name: longName, BanReason: longReason, IsBanned: bf4db.BanActive,
		})
	}

	embed := resultEmbed("Busca: grande", players, testTime)
	if len(embed.Fields) == 0 || len(embed.Fields) > maxEmbedFields {
		t.Fatalf("got %d fields, want between 1 and %d", len(embed.Fields), maxEmbedFields)
	}

	total := utf8.RuneCountInString(embed.Title)
	for _, f := range embed.Fields {
		if n := utf8.RuneCountInString(f.Name); n > maxFieldName {
			t.Errorf("field name is %d chars, over the %d limit", n, maxFieldName)
		}
		if n := utf8.RuneCountInString(f.Value); n > maxFieldValue {
			t.Errorf("field value is %d chars, over the %d limit", n, maxFieldValue)
		}
		total += utf8.RuneCountInString(f.Name) + utf8.RuneCountInString(f.Value)
	}
	if total > 6000 {
		t.Errorf("embed is %d chars, over Discord's 6000 limit", total)
	}
	if embed.Footer == nil || !strings.Contains(embed.Footer.Text, "de 40") {
		t.Errorf("footer should report the truncation, got %+v", embed.Footer)
	}
}

func TestResultEmbedSkipsRecordsWithoutID(t *testing.T) {
	embed := resultEmbed("t", []bf4db.Player{{Name: "Ghost"}}, testTime)
	if len(embed.Fields) != 0 {
		t.Errorf("record without persona id should be skipped: %+v", embed.Fields)
	}
}

func TestStatusLabels(t *testing.T) {
	cases := map[int]string{
		bf4db.BanActive:      "banido",
		bf4db.BanNone:        "limpo",
		bf4db.BanUnderReview: "análise",
		bf4db.BanNotReported: "não reportado",
		bf4db.BanStaff:       "staff",
		bf4db.BanGlitch:      "glitch",
		bf4db.BanExploit:     "exploit",
	}
	for code, want := range cases {
		got := statusLabel(bf4db.Player{IsBanned: bf4db.FlexInt(code)})
		if !strings.Contains(got, want) {
			t.Errorf("is_banned=%d -> %q, want it to mention %q", code, got, want)
		}
	}
}

func TestErrorEmbedExplainsKnownFailures(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{bf4db.ErrNameSearchUnavailable, "busca por nome"},
		{&bf4db.APIError{StatusCode: http.StatusUnauthorized}, "chave da API"},
		{&bf4db.APIError{StatusCode: http.StatusNotFound}, "Nenhum jogador"},
		{context.DeadlineExceeded, "demorou"},
		{errors.New("boom"), "Erro ao consultar"},
	}
	for _, tc := range cases {
		got := errorEmbed("t", tc.err)
		if !strings.Contains(got.Description, tc.want) {
			t.Errorf("errorEmbed(%v) = %q, want it to mention %q", tc.err, got.Description, tc.want)
		}
	}
}

func TestQueryClassification(t *testing.T) {
	if !isIP("203.0.113.7") || isIP("Player123") {
		t.Error("isIP misclassifies")
	}
	if !isPersonaID("988768601") || isPersonaID("123") || isPersonaID("EdUwUardo") {
		t.Error("isPersonaID misclassifies")
	}
	for in, want := range map[string]string{"1.1.1.1": "ip", "988768601": "player_id", "eduardo": "name"} {
		if got := queryKind(in); got != want {
			t.Errorf("queryKind(%q) = %q, want %q", in, got, want)
		}
	}
}
