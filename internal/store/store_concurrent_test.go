package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOpenEmptyPathUsesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := filepath.Join(home, "Library", "Application Support", "Harness", "harness.db")
	if s.Path != want {
		t.Fatalf("path %q", s.Path)
	}
}

func TestConcurrentKVAccess(t *testing.T) {
	s := open(t)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := string(rune('a' + n%26))
			_, err := s.DB.Exec(`INSERT INTO kv(key,json) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET json=excluded.json`, key, key)
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
}

func TestMigrateFromExistingDB(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.Migrate(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenReadOnlyParent(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	root := t.TempDir()
	parent := filepath.Join(root, "ro")
	if err := os.MkdirAll(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	_, err := Open(filepath.Join(parent, "sub", "h.db"))
	if err == nil {
		t.Fatal("expected mkdir failure")
	}
}
