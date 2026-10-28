package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuotePath(t *testing.T) {
	if quotePath("plain.txt") != "plain.txt" {
		t.Fatal()
	}
	got := quotePath("sp ace\n\t\x01")
	if got[0] != '"' || got[len(got)-1] != '"' {
		t.Fatalf("%q", got)
	}
	if !contains(got, `\n`) || !contains(got, `\t`) {
		t.Fatalf("%q", got)
	}
}

func TestParseHunkHeaderErrors(t *testing.T) {
	if _, err := parseHunkHeader("@@ bad"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := parseHunkHeader("@@"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseRange(t *testing.T) {
	s, n, err := parseRange("3,5")
	if err != nil || s != 3 || n != 5 {
		t.Fatalf("%d %d %v", s, n, err)
	}
	if _, _, err := parseRange("x"); err == nil {
		t.Fatal()
	}
}

func TestLookBinGit(t *testing.T) {
	if p := lookBin("git"); p == "" {
		t.Fatal("empty git path")
	}
}

func TestUnstageBeforeFirstCommit(t *testing.T) {
	d := t.TempDir()
	if _, err := runGit(d, nil, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "a.txt")
	if err := os.WriteFile(p, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StageFiles(d, []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := UnstageFiles(d, []string{"a.txt"}); err != nil {
		t.Fatal(err)
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
