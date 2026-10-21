package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sid/harness/internal/pty"
	"github.com/sid/harness/internal/store"
	"github.com/sid/harness/internal/termlayout"
)

func newTestTerm(t *testing.T) (*TermService, *pty.Manager, *store.Store) {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	persistDelay = 30 * time.Millisecond
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	pm := pty.NewManager()
	ts := newTermService(nil, pm, termlayout.NewManager(), st, os.Environ)
	t.Cleanup(func() { pm.KillAll(); st.Close() })
	return ts, pm, st
}

func alive(pm *pty.Manager, id string) bool {
	in, err := pm.Info(id)
	return err == nil && !in.Exited
}

func waitGone(t *testing.T, pm *pty.Manager, id string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if !alive(pm, id) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pty %s still alive", id)
}

func TestTermPaneLifecycle(t *testing.T) {
	ts, pm, _ := newTestTerm(t)
	dir := t.TempDir()

	tab, err := ts.NewTab("s1", dir)
	if err != nil || tab.PtyID == "" || tab.PaneID == "" {
		t.Fatalf("NewTab: %+v %v", tab, err)
	}
	if !alive(pm, tab.PtyID) {
		t.Fatal("shell not running")
	}
	sp, err := ts.Split("s1", tab.PaneID, "row", "")
	if err != nil || sp.PtyID == "" || sp.PtyID == tab.PtyID || sp.TabID != tab.TabID {
		t.Fatalf("Split: %+v %v", sp, err)
	}
	l := ts.Layout("s1")
	if got := leaves(&l); len(got) != 2 || got[0].PtyID != tab.PtyID || got[1].PtyID != sp.PtyID {
		t.Fatalf("layout: %+v", got)
	}
	if n := ts.ShellsRunning(); n != 2 {
		t.Fatalf("ShellsRunning=%d", n)
	}
	if len(ts.List("s1")) != 2 || len(ts.List("other")) != 0 {
		t.Fatal("List scoping")
	}

	if err := ts.ClosePane("s1", sp.PaneID); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pm, sp.PtyID)
	if !alive(pm, tab.PtyID) {
		t.Fatal("sibling shell must survive")
	}
	if err := ts.ClosePane("s1", tab.PaneID); err != nil { // last pane closes the tab
		t.Fatal(err)
	}
	waitGone(t, pm, tab.PtyID)
	if l := ts.Layout("s1"); len(l.Tabs) != 0 {
		t.Fatalf("tab should be gone: %+v", l)
	}
	if err := ts.ClosePane("s1", "nope"); err == nil {
		t.Fatal("want error")
	} else if ae := err.(*AppError); ae.Code != CodeNotFound {
		t.Fatalf("code %s", ae.Code)
	}
	if _, err := ts.Split("s1", "p1", "diag", ""); err == nil {
		t.Fatal("bad dir")
	}
}

func TestTermPersistAndRestore(t *testing.T) {
	ts, pm, st := newTestTerm(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)

	a, _ := ts.NewTab("s2", dir)
	b, err := ts.Split("s2", a.PaneID, "col", sub)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.RenameTab("s2", a.TabID, "work"); err != nil {
		t.Fatal(err)
	}
	// debounced save
	var raw string
	for i := 0; i < 100 && raw == ""; i++ {
		time.Sleep(30 * time.Millisecond)
		raw, _, _ = (&uiStore{db: st.DB}).get("term:s2")
		if raw != "" && !contains(raw, "work") {
			raw = ""
		}
	}
	if raw == "" {
		t.Fatal("layout not persisted")
	}
	ts.Flush()

	// A fresh service over the same db restores it with new shells.
	ts2 := newTermService(nil, pm, termlayout.NewManager(), st, os.Environ)
	l, err := ts2.RestoreLayout("s2", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Tabs) != 1 || l.Tabs[0].Title != "work" {
		t.Fatalf("restored: %+v", l)
	}
	lv := leaves(&l)
	if len(lv) != 2 || lv[0].PtyID == "" || lv[0].PtyID == a.PtyID || lv[1].PtyID == b.PtyID {
		t.Fatalf("expected fresh ptys: %+v (old %s %s)", lv, a.PtyID, b.PtyID)
	}
	for _, n := range lv {
		if !alive(pm, n.PtyID) {
			t.Fatalf("pty %s not alive", n.PtyID)
		}
	}
	wantCwd := map[string]string{lv[0].PaneID: dir, lv[1].PaneID: sub}
	for _, n := range lv {
		in, _ := pm.Info(n.PtyID)
		got, _ := filepath.EvalSymlinks(in.Cwd)
		want, _ := filepath.EvalSymlinks(wantCwd[n.PaneID])
		if got != want {
			t.Fatalf("pane %s cwd %q want %q", n.PaneID, got, want)
		}
	}
	// restoring again is a no-op while live
	l2, _ := ts2.RestoreLayout("s2", dir)
	if leaves(&l2)[0].PtyID != lv[0].PtyID {
		t.Fatal("second restore respawned")
	}
	ts2.KillScope("s2")
	if _, ok, _ := (&uiStore{db: st.DB}).get("term:s2"); ok {
		t.Fatal("KillScope should drop saved state")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
