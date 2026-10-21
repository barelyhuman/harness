package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppBootstrapAndServices(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	tmp := t.TempDir()
	a, err := New(Options{DataDir: filepath.Join(tmp, "data"), ConfigDir: filepath.Join(tmp, "cfg"), SkipModelRefresh: true, SkipRecover: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	if len(a.Services()) != 11 {
		t.Fatalf("services: %d", len(a.Services()))
	}
	if _, err := os.Stat(filepath.Join(tmp, "data", "harness.db")); err != nil {
		t.Fatal(err)
	}
	if ps, err := a.ProjectSvc.ListProjects(); err != nil || len(ps) != 0 {
		t.Fatal(ps, err)
	}
	if err := a.UIStateSvc.Set("k", `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	if v, _ := a.UIStateSvc.Get("k"); v != `{"a":1}` {
		t.Fatal(v)
	}
	if a.UIStateSvc.Set("k", "{") == nil {
		t.Fatal("invalid json accepted")
	}

	// FS + search + watch round trip
	root := filepath.Join(tmp, "proj")
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello world\n"), 0o644)
	if err := a.FSSvc.WatchScope("main:x", root); err != nil {
		t.Fatal(err)
	}
	defer a.FSSvc.UnwatchScope("main:x")
	r, err := a.SearchSvc.QuickOpen(root, "hel", 10)
	if err != nil || len(r) != 1 || r[0].Path != "hello.txt" {
		t.Fatalf("%+v %v", r, err)
	}
	fc, err := a.FSSvc.Read(filepath.Join(root, "hello.txt"))
	if err != nil || fc.Language != "plaintext" || fc.Etag == "" {
		t.Fatalf("%+v %v", fc, err)
	}
	if _, err := a.FSSvc.Write(filepath.Join(root, "hello.txt"), "x", "stale"); err == nil || err.(*AppError).Code != CodeConflict {
		t.Fatalf("want conflict, got %v", err)
	}
	id, err := a.SearchSvc.Grep(root, "world", GrepOptions{})
	if err != nil || id == "" {
		t.Fatal(id, err)
	}
	a.SearchSvc.CancelGrep(id)
	for i := 0; i < 100; i++ {
		a.SearchSvc.mu.Lock()
		n := len(a.SearchSvc.greps)
		a.SearchSvc.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if a.TermSvc.ShellsRunning() != 0 || !a.QuitAllowed() {
		t.Fatal("no shells: quit must be allowed")
	}
	ref, err := a.TermSvc.NewTab("main:x", root)
	if err != nil || ref.PtyID == "" {
		t.Fatal(ref, err)
	}
	if a.QuitAllowed() {
		t.Fatal("shell running: first quit request must be vetoed")
	}
	if !a.QuitAllowed() { // second request within 3s forces
		t.Fatal("second quit should pass")
	}
}
