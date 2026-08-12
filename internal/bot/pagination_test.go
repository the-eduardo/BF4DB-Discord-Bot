package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

func players(n int) []bf4db.Player {
	out := make([]bf4db.Player, 0, n)
	for i := range n {
		out = append(out, bf4db.Player{ID: bf4db.FlexInt(i + 1), Name: "p" + string(rune('a'+i%26))})
	}
	return out
}

func TestPageOfClampsAndSlices(t *testing.T) {
	all := players(12)

	first, page := pageOf(all, 0)
	if len(first) != pageSize || page != 0 || first[0].PersonaID() != 1 {
		t.Errorf("page 0 = %d players starting at %d", len(first), first[0].PersonaID())
	}

	last, page := pageOf(all, 2)
	if len(last) != 2 || page != 2 || last[0].PersonaID() != 11 {
		t.Errorf("page 2 = %d players starting at %d", len(last), last[0].PersonaID())
	}

	// Out of range in both directions clamps instead of panicking.
	if _, page := pageOf(all, 99); page != 2 {
		t.Errorf("page 99 clamped to %d, want 2", page)
	}
	if _, page := pageOf(all, -5); page != 0 {
		t.Errorf("page -5 clamped to %d, want 0", page)
	}
	if got := pageCount(0); got != 1 {
		t.Errorf("pageCount(0) = %d, want 1", got)
	}
	if got := pageCount(10); got != 2 {
		t.Errorf("pageCount(10) = %d, want 2", got)
	}
}

func TestPaginationComponents(t *testing.T) {
	if got := paginationComponents("k", 0, 3); got != nil {
		t.Errorf("single page should carry no buttons, got %+v", got)
	}

	row, ok := paginationComponents("abc", 0, 12)[0].(discordgo.ActionsRow)
	if !ok {
		t.Fatal("first component is not an action row")
	}
	prev := row.Components[0].(discordgo.Button)
	label := row.Components[1].(discordgo.Button)
	next := row.Components[2].(discordgo.Button)

	if !prev.Disabled {
		t.Error("previous should be disabled on the first page")
	}
	if next.Disabled {
		t.Error("next should be enabled when more pages exist")
	}
	if label.Label != "1/3" {
		t.Errorf("label = %q, want 1/3", label.Label)
	}
	if !strings.HasPrefix(next.CustomID, customIDPrefix) {
		t.Errorf("custom id %q is not namespaced", next.CustomID)
	}

	lastRow := paginationComponents("abc", 2, 12)[0].(discordgo.ActionsRow)
	if !lastRow.Components[2].(discordgo.Button).Disabled {
		t.Error("next should be disabled on the last page")
	}
}

func TestParseCustomID(t *testing.T) {
	key, page, ok := parseCustomID(customIDPrefix + "deadbeef:3")
	if !ok || key != "deadbeef" || page != 3 {
		t.Errorf("parse = %q, %d, %v", key, page, ok)
	}
	// The PunkBuster bot shares this application: its components must be ignored.
	for _, id := range []string{"pbss:page:2", "", customIDPrefix + "noop", customIDPrefix + "key:notanumber"} {
		if _, _, ok := parseCustomID(id); ok {
			t.Errorf("parseCustomID(%q) should not match", id)
		}
	}
}

func TestInteractionUserID(t *testing.T) {
	guild := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Member: &discordgo.Member{User: &discordgo.User{ID: "111"}},
	}}
	if got := interactionUserID(guild); got != "111" {
		t.Errorf("guild member id = %q, want 111", got)
	}

	dm := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		User: &discordgo.User{ID: "222"},
	}}
	if got := interactionUserID(dm); got != "222" {
		t.Errorf("dm user id = %q, want 222", got)
	}

	empty := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{}}
	if got := interactionUserID(empty); got != "" {
		t.Errorf("neither member nor user set: id = %q, want empty", got)
	}
}

func TestResultOwnedBy(t *testing.T) {
	cases := []struct {
		name  string
		owner string
		user  string
		want  bool
	}{
		{"empty owner (legacy/unset entry) allows anyone", "", "999", true},
		{"same owner allowed", "111", "111", true},
		{"different owner denied", "111", "222", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resultOwnedBy(resultSet{owner: c.owner}, c.user); got != c.want {
				t.Errorf("resultOwnedBy(owner=%q, user=%q) = %v, want %v", c.owner, c.user, got, c.want)
			}
		})
	}
}

func TestPaginatedStoresOwner(t *testing.T) {
	b := newTestBot()
	_, comps := b.paginated("Busca: x", players(12), time.Now(), "111")

	row := comps[0].(discordgo.ActionsRow)
	next := row.Components[2].(discordgo.Button)
	key, _, ok := parseCustomID(next.CustomID)
	if !ok {
		t.Fatal("could not recover the result key from the button's custom id")
	}
	set, found := b.results.Get(key)
	if !found || set.owner != "111" {
		t.Errorf("stored owner = %q, found = %v, want 111/true", set.owner, found)
	}
}

func TestResultKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		k := newResultKey()
		if k == "" || seen[k] {
			t.Fatalf("weak result key: %q", k)
		}
		seen[k] = true
	}
}

func TestPaginatedStoresOnlyWhenNeeded(t *testing.T) {
	b := newTestBot()

	embed, comps := b.paginated("Busca: x", players(3), time.Now(), "111")
	if comps != nil || embed.Footer != nil {
		t.Error("a single page needs neither buttons nor a footer")
	}
	if b.results.Len() != 0 {
		t.Error("nothing should be stored for a single page")
	}

	embed, comps = b.paginated("Busca: x", players(12), time.Now(), "111")
	if len(comps) == 0 {
		t.Fatal("multi-page result carries no buttons")
	}
	if embed.Footer == nil || !strings.Contains(embed.Footer.Text, "12 contas") {
		t.Errorf("footer = %+v", embed.Footer)
	}
	if len(embed.Fields) != pageSize {
		t.Errorf("first page shows %d fields, want %d", len(embed.Fields), pageSize)
	}
	if b.results.Len() != 1 {
		t.Errorf("result set not stored: %d", b.results.Len())
	}
}
