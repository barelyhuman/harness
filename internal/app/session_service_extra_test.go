package app

import (
	"testing"

	"github.com/sid/harness/internal/workspace"
)

func TestSessionServiceLifecycle(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", workspace.ScopeMain)
	if _, err := e.svc.GetSession(s.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RenameSession(s.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	sc, err := e.svc.Scope(s.ID)
	if err != nil || sc.Root == "" {
		t.Fatalf("%+v %v", sc, err)
	}
	ms, err := e.svc.MainScope(e.proj.ID)
	if err != nil || ms.Root != e.proj.Path {
		t.Fatalf("%+v %v", ms, err)
	}
	if err := e.svc.ArchiveSession(s.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RestoreSession(s.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteSession(s.ID, false); err == nil {
		t.Fatal("expected not confirmed")
	}
	if err := e.svc.DeleteSession(s.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.GetSession(s.ID); err == nil {
		t.Fatal("expected gone")
	}
}

func TestSessionServiceListArchived(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", workspace.ScopeMain)
	if err := e.svc.ArchiveSession(s.ID, false); err != nil {
		t.Fatal(err)
	}
	l, err := e.svc.ListSessions(e.proj.ID, false)
	if err != nil || len(l) != 0 {
		t.Fatalf("%v %v", l, err)
	}
	l, err = e.svc.ListSessions(e.proj.ID, true)
	if err != nil || len(l) != 1 {
		t.Fatalf("%v %v", l, err)
	}
}
