package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sid/harness/internal/fs"
	"github.com/sid/harness/internal/workspace"
)

func TestKeybindingsParse(t *testing.T) {
	r := parseKeybindings([]byte(`[
	  // comment
	  {"key": "Cmd+K  Cmd+S", "command": "a", "when": "editorFocus"},
	  {"key": "cmd+k cmd+s", "command": "b", "when": "editorFocus",},
	  {"key": "cmd+d", "command": "-split.old"},
	  {"key": "", "command": "x"},
	  {"key": "cmd+p", "command": "quickOpen", "args": {"x": 1}},
	]`))
	if len(r.Bindings) != 4 || len(r.Errors) != 1 {
		t.Fatalf("%+v", r)
	}
	if r.Bindings[0].Key != "cmd+k cmd+s" || !r.Bindings[2].Remove || r.Bindings[2].Command != "split.old" {
		t.Fatalf("%+v", r.Bindings)
	}
	if len(r.Conflicts) != 1 || r.Conflicts[0].Key != "cmd+k cmd+s" || len(r.Conflicts[0].Commands) != 2 {
		t.Fatalf("conflicts %+v", r.Conflicts)
	}
	if bad := parseKeybindings([]byte(`{"a":1}`)); len(bad.Errors) != 1 || len(bad.Bindings) != 0 {
		t.Fatalf("%+v", bad)
	}
}

func TestSettingsDefaultsAndRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	w := fs.NewWatcher()
	defer w.Close()
	s, err := newSettingsService(nil, w, dir)
	if err != nil {
		t.Fatal(err)
	}
	if r := s.GetSettings(); r.Error != "" || len(r.Values) != 0 {
		t.Fatalf("%+v", r)
	}
	if r := s.GetKeybindings(); len(r.Errors) != 0 || len(r.Bindings) != 0 {
		t.Fatalf("%+v", r)
	}
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{\n // c\n \"editor.fontSize\": 14, }"), 0o644)
	if r := s.GetSettings(); r.Error != "" || r.Values["editor.fontSize"] != float64(14) {
		t.Fatalf("%+v", r)
	}
	if p, err := s.OpenSettingsFile("keybindings"); err != nil || filepath.Base(p) != "keybindings.json" {
		t.Fatal(p, err)
	}
	if _, err := s.OpenSettingsFile("x"); err == nil {
		t.Fatal("want error")
	}
}

func TestWrapErr(t *testing.T) {
	d := wrapErr(&workspace.ErrDirty{Files: []string{"a"}}).(*AppError)
	if d.Code != CodeDirty || len(d.Files) != 1 {
		t.Fatalf("%+v", d)
	}
	if c := wrapErr(&fs.ErrConflict{Path: "p", Current: "e1"}).(*AppError); c.Code != CodeConflict || c.Etag != "e1" {
		t.Fatalf("%+v", c)
	}
	if wrapErr(workspace.ErrNotConfirmed).(*AppError).Code != CodeNotConfirmed || wrapErr(nil) != nil {
		t.Fatal("codes")
	}
}

func TestLanguageFor(t *testing.T) {
	for p, want := range map[string]string{"/a/b.go": "go", "x.TSX": "typescript", "Dockerfile": "dockerfile", "noext": "plaintext"} {
		if got := LanguageFor(p); got != want {
			t.Errorf("%s: %s want %s", p, got, want)
		}
	}
}
