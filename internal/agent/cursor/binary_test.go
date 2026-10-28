package cursor_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/agent/cursor"
)

func TestBinaryLookupOnPATH(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	p := &cursor.Provider{}
	cmd, err := p.Command(agent.StartOpts{Cwd: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != bin {
		t.Fatalf("got %q", cmd.Path)
	}
	in, err := p.Detect(context.Background())
	if err != nil || !in.Found || in.Path != bin {
		t.Fatalf("%+v %v", in, err)
	}
}

func TestBinaryHomeLocalBin(t *testing.T) {
	home := t.TempDir()
	local := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(local, "cursor-agent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho v\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty"))
	p := &cursor.Provider{}
	cmd, err := p.Command(agent.StartOpts{Cwd: "/w"})
	if err != nil || cmd.Path != bin {
		t.Fatalf("%+v %v", cmd, err)
	}
}

func TestBinaryNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	_, err := (&cursor.Provider{}).Command(agent.StartOpts{Cwd: "/w"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCachePathWithoutUserCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := &cursor.Provider{}
	ms, err := p.Models(context.Background())
	if err != nil || len(ms) == 0 {
		t.Fatalf("%v %v", ms, err)
	}
}
