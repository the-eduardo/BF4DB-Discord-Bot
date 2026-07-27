package bot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/the-eduardo/BF4DB-Discord-Bot/internal/bf4db"
)

func TestMaySearchIP(t *testing.T) {
	b := newTestBot()
	b.ipRoleIDs = []string{"role-mod"}

	cases := []struct {
		name   string
		member *discordgo.Member
		want   bool
	}{
		{"nil member (DM)", nil, false},
		{"plain member", &discordgo.Member{}, false},
		{"server manager", &discordgo.Member{Permissions: discordgo.PermissionManageServer}, true},
		{"administrator", &discordgo.Member{Permissions: discordgo.PermissionAdministrator}, true},
		{"administrator with the full computed set", &discordgo.Member{Permissions: discordgo.PermissionAll}, true},
		{"allowed role", &discordgo.Member{Roles: []string{"role-other", "role-mod"}}, true},
		{"unrelated role", &discordgo.Member{Roles: []string{"role-other"}}, false},
	}
	for _, tc := range cases {
		if got := b.maySearchIP(tc.member); got != tc.want {
			t.Errorf("%s: maySearchIP = %v, want %v", tc.name, got, tc.want)
		}
	}

	// Without configured roles, only managers pass.
	b.ipRoleIDs = nil
	if b.maySearchIP(&discordgo.Member{Roles: []string{"role-mod"}}) {
		t.Error("role should not grant access once it is unconfigured")
	}
}

func TestCachedLookupAvoidsRepeatedRequests(t *testing.T) {
	b := newTestBot()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"data":{"player_id":988768601,"name":"EdUwUardo","is_banned":2}}`)
	}))
	defer srv.Close()

	client, err := bf4db.New(strings.Repeat("a", 64), bf4db.WithBaseURL(srv.URL+"/api"))
	if err != nil {
		t.Fatal(err)
	}
	b.client = client

	for range 3 {
		players, err := b.cachedLookup(context.Background(), "988768601")
		if err != nil || len(players) != 1 || players[0].Name != "EdUwUardo" {
			t.Fatalf("lookup = %+v, %v", players, err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("made %d requests for the same query, want 1", calls.Load())
	}

	// A different query is a different cache entry.
	if _, err := b.cachedLookup(context.Background(), "988768602"); err != nil {
		t.Fatalf("second query: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("made %d requests, want 2", calls.Load())
	}
}

func TestCachedLookupDoesNotCacheFailures(t *testing.T) {
	b := newTestBot()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"no"}`)
	}))
	defer srv.Close()

	client, err := bf4db.New(strings.Repeat("a", 64), bf4db.WithBaseURL(srv.URL+"/api"), bf4db.WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	b.client = client

	for range 2 {
		if _, err := b.cachedLookup(context.Background(), "988768601"); err == nil {
			t.Fatal("want an error")
		}
	}
	if calls.Load() != 2 {
		t.Errorf("a failure was cached: %d requests for 2 attempts", calls.Load())
	}
}
