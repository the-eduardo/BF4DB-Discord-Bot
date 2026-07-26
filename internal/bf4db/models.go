// Package bf4db is a small, dependency-free client for the BF4DB player API.
//
// Endpoints confirmed against the live API on 2026-07-26:
//
//	GET /api/player/{ip}/search                        paginated players seen on an IP
//	GET /api/player/{player_id}                        a single player
//	GET /api/player/{discord_id}discordAccount/discord BF4 accounts linked to a Discord user
//
// Note the missing separator in the Discord path: it is not a typo, it is how
// the upstream route is defined. Adding a slash returns 404.
package bf4db

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FlexInt is an int that also accepts JSON strings ("42"), booleans and null.
// BF4DB is a Laravel API and serializes numeric columns inconsistently across
// endpoints; a strict int field would break the whole payload.
type FlexInt int

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	switch s {
	case "", "null":
		*f = 0
		return nil
	case "true":
		*f = 1
		return nil
	case "false":
		*f = 0
		return nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		*f = FlexInt(n)
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		*f = FlexInt(v)
		return nil
	}
	return fmt.Errorf("bf4db: cannot decode %q as number", s)
}

// Timestamp accepts every date shape BF4DB has returned: RFC 3339 with
// microseconds, Laravel's "2006-01-02 15:04:05", bare dates, "" and null.
type Timestamp struct {
	time.Time
}

var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999Z",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func (t *Timestamp) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` || s == "" {
		t.Time = time.Time{}
		return nil
	}
	s = strings.Trim(s, `"`)
	for _, layout := range timeLayouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("bf4db: unrecognized timestamp %q", s)
}

func (t Timestamp) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.Time)
}

// Ban states used by the is_banned column. Codes -1, 0, 1 and 2 were verified
// against live records; the full set is BF4DB's own table.
const (
	BanNotReported = -1 // never reported
	BanUnderReview = 0  // reported, no verdict yet
	BanActive      = 1  // found cheating; ban_reason says how (Aimbot, Multihack, Linked Account…)
	BanNone        = 2  // checked, not found cheating ("No Active BF4 Ban")
	BanStaff       = 3  // account belongs to BF4DB staff
	BanGlitch      = 4  // found glitching for an unfair advantage
	BanExploit     = 5  // found using a game exploit for an unfair advantage
)

// Player is one BF4DB player record.
type Player struct {
	ID         FlexInt   `json:"id"`
	PlayerID   FlexInt   `json:"player_id"`
	Name       string    `json:"name"`
	Avatar     string    `json:"avatar"`
	IsBanned   FlexInt   `json:"is_banned"`
	BanReason  string    `json:"ban_reason"`
	EaGUID     string    `json:"ea_guid"`
	PbGUID     string    `json:"pb_guid"`
	CheatScore FlexInt   `json:"cheat_score"`
	CreatedAt  Timestamp `json:"created_at"`
	UpdatedAt  Timestamp `json:"updated_at"`
}

// PersonaID returns the id to build report links with. The IP search returns
// "id" while the single-player and Discord endpoints return "player_id".
func (p Player) PersonaID() int {
	if p.ID != 0 {
		return int(p.ID)
	}
	return int(p.PlayerID)
}

// Banned reports whether BF4DB holds a cheating ban for the player. Glitch and
// exploit verdicts are sanctions for an unfair advantage rather than cheating
// bans, so they are reported through Status, not here.
func (p Player) Banned() bool { return p.IsBanned == BanActive }

// Status is a short label for the is_banned code.
func (p Player) Status() string {
	switch p.IsBanned {
	case BanNotReported:
		return "not reported"
	case BanUnderReview:
		return "under review"
	case BanActive:
		return "banned"
	case BanNone:
		return "clean"
	case BanStaff:
		return "staff member"
	case BanGlitch:
		return "glitch"
	case BanExploit:
		return "exploit"
	default:
		return fmt.Sprintf("unknown (%d)", int(p.IsBanned))
	}
}

// Reason is BanReason, falling back to the status label when BF4DB leaves the
// column empty (which it does for players still under review).
func (p Player) Reason() string {
	if reason := strings.TrimSpace(p.BanReason); reason != "" {
		return reason
	}
	label := p.Status()
	return strings.ToUpper(label[:1]) + label[1:]
}

// listResponse is the shape of the paginated search endpoints. The Discord
// endpoint reuses "data" and adds "updated_at" instead of meta/links.
type listResponse struct {
	Data      []Player  `json:"data"`
	UpdatedAt Timestamp `json:"updated_at"`
	Meta      struct {
		CurrentPage FlexInt `json:"current_page"`
		LastPage    FlexInt `json:"last_page"`
		PerPage     FlexInt `json:"per_page"`
		Total       FlexInt `json:"total"`
	} `json:"meta"`
}

// singleResponse is the shape of GET /api/player/{id}: "data" is an object.
type singleResponse struct {
	Data Player `json:"data"`
}
