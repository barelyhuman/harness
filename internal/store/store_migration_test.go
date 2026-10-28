package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesFileInNewDir(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a", "b", "h.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

func TestOpenOnRegularFileFails(t *testing.T) {
	f := filepath.Join(t.TempDir(), "notdir")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(filepath.Join(f, "h.db"))
	if err == nil {
		t.Fatal("expected error opening under file path")
	}
}

func TestMigrateNoOpWhenCurrent(t *testing.T) {
	s := open(t)
	v1, _ := s.Version()
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	v2, _ := s.Version()
	if v1 != v2 {
		t.Fatalf("version changed %d -> %d", v1, v2)
	}
}

func TestVersionTracksMigrations(t *testing.T) {
	s := open(t)
	v, err := s.Version()
	if err != nil || v < 2 {
		t.Fatalf("v=%d err=%v", v, err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	s := open(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("second close", err)
	}
}
