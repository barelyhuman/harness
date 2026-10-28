package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreWhenWorktreePathOccupied(t *testing.T) {
	_, ss, p := setup(t)
	a, err := ss.Create(p.ID, CreateOpts{Title: "occupy", ScopeKind: ScopeWorktree})
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.Archive(a.ID, false); err != nil {
		t.Fatal(err)
	}
	// Block the original worktree path so Restore picks a suffixed directory.
	if err := os.MkdirAll(a.WorktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := ss.Restore(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.WorktreePath == a.WorktreePath {
		t.Fatalf("expected alternate path, got %q", r.WorktreePath)
	}
	if _, err := os.Stat(filepath.Join(r.WorktreePath, "a.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshPREmptyURL(t *testing.T) {
	_, ss, p := setup(t)
	s, err := ss.Create(p.ID, CreateOpts{Title: "no pr"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := ss.RefreshPR(s.ID)
	if err != nil || st != "" {
		t.Fatalf("%q %v", st, err)
	}
}

func TestRaisePRNotFound(t *testing.T) {
	_, ss, _ := setup(t)
	if _, err := ss.RaisePR("missing", "t", "b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestSetAgentRolesNotFound(t *testing.T) {
	ps, _, _ := setup(t)
	if err := ps.SetAgentRoles("nope", AgentRoles{Planner: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestSetSetupCmdsNotFound(t *testing.T) {
	ps, _, _ := setup(t)
	if err := ps.SetSetupCmds("nope", []string{"x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}
