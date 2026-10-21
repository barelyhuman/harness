package agent_test

import (
	"testing"
	"time"

	"github.com/sid/harness/internal/agent"
)

func reg() *agent.ModelRegistry {
	r := agent.NewModelRegistry()
	r.SetProviderModels("claude", []agent.Model{{ID: "opus"}, {ID: "sonnet"}, {ID: "haiku"}})
	r.SetProviderModels("cursor", []agent.Model{{ID: "composer-2.5[fast=true]"}, {ID: "default[]"}})
	return r
}

func TestCycleAcrossProviders(t *testing.T) {
	r := reg()
	r.SetFavourites([]string{"claude/sonnet", "cursor/composer-2.5[fast=true]", "claude/opus", "claude/gone", "claude/sonnet"})
	if got := len(r.Favourites()); got != 3 {
		t.Fatalf("favourites %d (unknown and duplicate keys must be skipped)", got)
	}
	steps := []struct {
		cur  string
		dir  int
		want string
	}{
		{"claude/sonnet", 1, "cursor/composer-2.5[fast=true]"},
		{"cursor/composer-2.5[fast=true]", 1, "claude/opus"},
		{"claude/opus", 1, "claude/sonnet"},  // wraps
		{"claude/sonnet", -1, "claude/opus"}, // wraps backwards
		{"claude/opus", -1, "cursor/composer-2.5[fast=true]"},
		{"claude/haiku", 1, "claude/sonnet"}, // not a favourite: first
		{"claude/haiku", -1, "claude/opus"},  // not a favourite: last
		{"", 1, "claude/sonnet"},
	}
	for _, s := range steps {
		m, ok := r.Cycle(s.cur, s.dir)
		if !ok || m.Key() != s.want {
			t.Errorf("Cycle(%q,%d)=%q want %q", s.cur, s.dir, m.Key(), s.want)
		}
	}
}

func TestCycleFallsBackToAllAndEmpty(t *testing.T) {
	r := reg()
	if m, _ := r.Cycle("claude/haiku", 1); m.Key() != "cursor/composer-2.5[fast=true]" {
		t.Errorf("no favourites: cycle all, got %q", m.Key())
	}
	if _, ok := agent.NewModelRegistry().Cycle("x/y", 1); ok {
		t.Error("empty registry")
	}
}

func TestSameProvider(t *testing.T) {
	r := reg()
	if !r.SameProvider("claude/opus", "claude/haiku") || r.SameProvider("claude/opus", "cursor/default[]") ||
		r.SameProvider("opus", "opus") {
		t.Error("SameProvider")
	}
	if agent.ModelProvider("cursor/claude-sonnet-5[thinking=true]") != "cursor" ||
		agent.ModelLocalID("cursor/a/b") != "a/b" {
		t.Error("key parsing")
	}
}

func TestSequencer(t *testing.T) {
	now := time.Unix(100, 0)
	s := &agent.Sequencer{RunID: "r", Now: func() time.Time { return now }}
	a := s.Stamp([]agent.Event{{Kind: agent.KindStatus}, {Kind: agent.KindStatus}})
	b := s.Stamp([]agent.Event{{Kind: agent.KindStatus}})
	if a[0].Seq != 1 || a[1].Seq != 2 || b[0].Seq != 3 || b[0].RunID != "r" || !b[0].TS.Equal(now) {
		t.Errorf("%+v %+v", a, b)
	}
}
