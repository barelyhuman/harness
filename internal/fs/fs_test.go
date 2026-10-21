package fs

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestEtagConflictAndAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.sh")
	os.WriteFile(p, []byte("one"), 0o755)
	data, etag, err := Read(p)
	if err != nil || string(data) != "one" || etag == "" {
		t.Fatal(err, data, etag)
	}
	etag2, err := Write(p, []byte("two!"), etag)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", st.Mode())
	}
	_, err = Write(p, []byte("three"), etag) // stale
	var c *ErrConflict
	if !errors.As(err, &c) || !IsConflict(err) {
		t.Fatalf("want conflict, got %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two!" {
		t.Fatal("content changed on conflict")
	}
	if _, err := Write(p, []byte("three"), etag2); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(p, []byte("x"), ""); err != nil { // unconditional
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftover temp files: %v", ents)
	}
}

func TestListDir(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "beta"), 0o755)
	os.Mkdir(filepath.Join(dir, "Alpha"), 0o755)
	os.Mkdir(filepath.Join(dir, "repo"), 0o755)
	os.Mkdir(filepath.Join(dir, "repo", ".git"), 0o755)
	os.Mkdir(filepath.Join(dir, ".hid"), 0o755)
	os.WriteFile(filepath.Join(dir, "afile"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "wt"), nil, 0o644)
	os.Symlink(filepath.Join(dir, "beta"), filepath.Join(dir, "link"))
	names := func(es []Entry) (n []string) {
		for _, e := range es {
			n = append(n, e.Name)
		}
		return
	}
	es, err := ListDir(dir, ListOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(es), []string{"Alpha", "beta", "link", "repo", "afile", "wt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for _, e := range es {
		if (e.Name == "repo") != e.IsRepo {
			t.Fatalf("IsRepo wrong for %s", e.Name)
		}
		if e.Name == "link" && (!e.Symlink || !e.IsDir) {
			t.Fatalf("link %+v", e)
		}
	}
	es, _ = ListDir(dir, ListOpts{DirsOnly: true, Hidden: true})
	if got, want := names(es), []string{".hid", "Alpha", "beta", "link", "repo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCreateRenameDelete(t *testing.T) {
	dir := t.TempDir()
	if err := Create(filepath.Join(dir, "d/f.txt"), false); err != nil {
		t.Fatal(err)
	}
	if err := Create(filepath.Join(dir, "d/f.txt"), false); err == nil {
		t.Fatal("expected exists")
	}
	if err := Rename(filepath.Join(dir, "d/f.txt"), filepath.Join(dir, "g.txt")); err != nil {
		t.Fatal(err)
	}
	var got string
	trashFn = func(p string) error { got = p; return os.Remove(p) }
	defer func() { trashFn = trash }()
	if err := Delete(filepath.Join(dir, "g.txt")); err != nil || got == "" {
		t.Fatal(err, got)
	}
}

func TestRepoBadges(t *testing.T) {
	dir := t.TempDir()
	if err := runGit(dir, "init", "-q", "-b", "main"); err != nil {
		t.Skip("git unavailable")
	}
	runGit(dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x")
	b := RepoBadges([]string{dir, t.TempDir()}, 5*time.Second)
	if b[dir].Branch != "main" || b[dir].Worktrees != 1 || len(b) != 1 {
		t.Fatalf("%+v", b)
	}
}

type collector struct {
	mu    sync.Mutex
	calls [][]string
}

func (c *collector) fn(p []string) { c.mu.Lock(); c.calls = append(c.calls, p); c.mu.Unlock() }
func (c *collector) all() (out []string, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, x := range c.calls {
		out = append(out, x...)
	}
	return out, len(c.calls)
}

func TestWatcher(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("secret.txt\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n*.log\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	w := NewWatcher()
	defer w.Close()
	c := &collector{}
	if err := w.Watch("s", dir, c.fn); err != nil {
		t.Fatal(err)
	}
	// Ignored writes first, then a real one; also a new nested dir.
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "a.js"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "debug.log"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), nil, 0o644)
	f := filepath.Join(dir, "src", "main.go")
	for i := 0; i < 5; i++ { // many events, one debounced batch
		os.WriteFile(f, []byte{byte(i)}, 0o644)
	}
	waitUntil(t, func() bool { p, _ := c.all(); return len(p) > 0 })
	time.Sleep(300 * time.Millisecond)
	paths, n := c.all()
	if n != 1 {
		t.Fatalf("want 1 batch, got %d: %v", n, c.calls)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Fatalf("duplicate %s", p)
		}
		seen[p] = true
	}
	if !seen[f] || len(paths) != 1 {
		t.Fatalf("paths %v", paths)
	}
	// New directory is picked up recursively.
	os.MkdirAll(filepath.Join(dir, "new", "deep"), 0o755)
	time.Sleep(300 * time.Millisecond)
	deep := filepath.Join(dir, "new", "deep", "z.txt")
	os.WriteFile(deep, nil, 0o644)
	waitUntil(t, func() bool {
		p, _ := c.all()
		for _, x := range p {
			if x == deep {
				return true
			}
		}
		return false
	})
	w.Unwatch("s")
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("timeout")
}

func fdCount(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skip("no /dev/fd")
	}
	return len(ents)
}

func TestWatcherManyDirs(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	const n = 2100
	deepest := dir
	for i := 0; i < 30; i++ {
		deepest = filepath.Join(deepest, "d")
	}
	os.MkdirAll(deepest, 0o755)
	for i := 0; i < n; i++ {
		os.MkdirAll(filepath.Join(dir, "wide", "a"+string(rune('a'+i%26)), "b", "c"+itoa(i)), 0o755)
	}
	before := fdCount(t)
	w := NewWatcher()
	defer w.Close()
	c := &collector{}
	if err := w.Watch("big", dir, c.fn); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	f := filepath.Join(deepest, "z.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	waitUntil(t, func() bool {
		p, _ := c.all()
		for _, x := range p {
			if x == f {
				return true
			}
		}
		return false
	})
	if after := fdCount(t); after-before > 50 {
		t.Fatalf("fd count grew from %d to %d", before, after)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestWatcherSymlinkRoot(t *testing.T) {
	real, _ := filepath.EvalSymlinks(t.TempDir())
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	w := NewWatcher()
	defer w.Close()
	c := &collector{}
	if err := w.Watch("s", link, c.fn); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	f := filepath.Join(link, "a.txt")
	os.WriteFile(f, []byte("x"), 0o644)
	waitUntil(t, func() bool { p, _ := c.all(); return len(p) == 1 && p[0] == f })
}
