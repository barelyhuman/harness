package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPathUsesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Library", "Application Support", "Harness", "harness.db")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestOpenCreatesParentDir(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "nested", "dir", "h.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if st, err := os.Stat(filepath.Dir(p)); err != nil || !st.IsDir() {
		t.Fatal(err)
	}
}

func TestVersionAfterMigrate(t *testing.T) {
	s := open(t)
	v, err := s.Version()
	if err != nil || v < 1 {
		t.Fatalf("version=%d err=%v", v, err)
	}
}
