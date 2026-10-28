package search

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func setup(t *testing.T) string {
	dir := t.TempDir()
	w := func(p, c string) {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	w(".gitignore", "ignored/\n*.log\n")
	w("a.txt", "hello World\nnothing\nworld again, World\n")
	w("sub/b.go", "package b // Hello\n")
	w("ignored/c.txt", "hello\n")
	w("x.log", "hello\n")
	w(".git/config", "hello\n")
	w(".hidden", "hello hidden\n")
	return dir
}

func TestFiles(t *testing.T) {
	dir := setup(t)
	want := []string{".gitignore", ".hidden", "a.txt", "sub/b.go"}
	got, err := Files(context.Background(), dir)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("rg: %v %v", got, err)
	}
	wf, err := walkFiles(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wf, want) {
		t.Fatalf("walk: %v", wf)
	}
	// Cache + invalidation.
	os.WriteFile(filepath.Join(dir, "new.txt"), nil, 0o644)
	got, _ = Files(context.Background(), dir)
	if len(got) != 4 {
		t.Fatalf("expected cached list, got %v", got)
	}
	Invalidate(dir, []string{filepath.Join(dir, "a.txt")}) // modify only: keeps cache
	got, _ = Files(context.Background(), dir)
	if len(got) != 4 {
		t.Fatalf("modify should not invalidate: %v", got)
	}
	Invalidate(dir, []string{filepath.Join(dir, "new.txt")})
	got, _ = Files(context.Background(), dir)
	if len(got) != 5 {
		t.Fatalf("expected refresh, got %v", got)
	}
}

func collect(ch <-chan Result) (rs []Result) {
	for r := range ch {
		rs = append(rs, r)
	}
	return
}

func TestGrep(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep (rg) not on PATH")
	}
	dir := setup(t)
	rs := collect(Grep(context.Background(), dir, "world", GrepOpts{}))
	if len(rs) != 1 || rs[0].Path != "a.txt" || len(rs[0].Matches) != 2 {
		t.Fatalf("%+v", rs)
	}
	m := rs[0].Matches[1]
	if m.Line != 3 || m.Column != 1 || len(m.Submatches) != 2 || m.Submatches[1] != (Range{13, 18}) {
		t.Fatalf("%+v", m)
	}
	rs = collect(Grep(context.Background(), dir, "World", GrepOpts{CaseSensitive: true}))
	if len(rs) != 1 || len(rs[0].Matches) != 2 {
		t.Fatalf("%+v", rs)
	}
	rs = collect(Grep(context.Background(), dir, `hel+o`, GrepOpts{Regex: true, Glob: "*.go"}))
	if len(rs) != 1 || rs[0].Path != "sub/b.go" {
		t.Fatalf("%+v", rs)
	}
	rs = collect(Grep(context.Background(), dir, `hel+o`, GrepOpts{}))
	if len(rs) != 0 { // literal "hel+o" doesn't occur
		t.Fatalf("%+v", rs)
	}
	rs = collect(Grep(context.Background(), dir, "hello", GrepOpts{}))
	if len(rs) != 3 { // a.txt, sub/b.go, .hidden (not ignored/.git/log)
		t.Fatalf("%+v", rs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	collect(Grep(ctx, dir, "hello", GrepOpts{})) // must terminate
}
