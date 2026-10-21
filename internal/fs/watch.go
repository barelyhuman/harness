package fs

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

const debounce = 100 * time.Millisecond

// event is a backend-neutral filesystem event for an absolute path under the
// resolved root.
type event struct {
	name                                string
	create, write, remove, chmod, isDir bool
}

// backend delivers raw events for a recursive tree.
type backend interface {
	events() <-chan event
	errors() <-chan error
	// addDir ensures dir is watched (a no-op for recursive backends).
	addDir(dir string) error
	close()
}

// Watcher runs one recursive watch per scope.
type Watcher struct {
	mu      sync.Mutex
	watches map[string]*watch
}

func NewWatcher() *Watcher { return &Watcher{watches: map[string]*watch{}} }

type watch struct {
	root string // symlink-resolved root, used for all matching
	user string // root as the caller passed it (absolute); emitted paths are under it
	fn   func([]string)
	b    backend
	stop chan struct{}
	done chan struct{}

	mu      sync.Mutex
	matcher gitignore.Matcher
	pats    map[string][]gitignore.Pattern // dir -> patterns from its .gitignore
	warned  bool
}

// Watch starts (replacing any existing) a watch of root for scope. fn is
// called from a separate goroutine with deduplicated absolute paths.
func (wt *Watcher) Watch(scope, root string, fn func(paths []string)) error {
	wt.Unwatch(scope)
	root, err := filepath.Abs(Expand(root))
	if err != nil {
		return err
	}
	user := root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	b, err := newBackend(root)
	if err != nil {
		return err
	}
	w := &watch{root: root, user: user, fn: fn, b: b, stop: make(chan struct{}), done: make(chan struct{}), pats: map[string][]gitignore.Pattern{}}
	w.loadExclude()
	w.addTree(root)
	wt.mu.Lock()
	wt.watches[scope] = w
	wt.mu.Unlock()
	go w.loop()
	return nil
}

// Unwatch stops the scope's watch.
func (wt *Watcher) Unwatch(scope string) {
	wt.mu.Lock()
	w := wt.watches[scope]
	delete(wt.watches, scope)
	wt.mu.Unlock()
	if w != nil {
		close(w.stop)
		w.b.close()
		<-w.done
	}
}

// Close stops all watches.
func (wt *Watcher) Close() {
	wt.mu.Lock()
	scopes := make([]string, 0, len(wt.watches))
	for s := range wt.watches {
		scopes = append(scopes, s)
	}
	wt.mu.Unlock()
	for _, s := range scopes {
		wt.Unwatch(s)
	}
}

func (w *watch) loadExclude() {
	b, err := os.ReadFile(filepath.Join(w.root, ".git", "info", "exclude"))
	if err != nil {
		return
	}
	w.pats[""] = parsePatterns(string(b), nil)
	w.rebuild()
}

func parsePatterns(content string, domain []string) []gitignore.Pattern {
	var ps []gitignore.Pattern
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.HasPrefix(l, "#") || strings.TrimSpace(l) == "" {
			continue
		}
		ps = append(ps, gitignore.ParsePattern(l, domain))
	}
	return ps
}

// rebuild recomputes the matcher; w.mu need not be held in setup, and is held
// by callers at runtime.
func (w *watch) rebuild() {
	dirs := make([]string, 0, len(w.pats))
	for d := range w.pats {
		dirs = append(dirs, d)
	}
	// Shallower patterns first so deeper .gitignore files take precedence.
	sort.Slice(dirs, func(i, j int) bool {
		return strings.Count(dirs[i], "/") < strings.Count(dirs[j], "/") || (dirs[i] == "" && dirs[j] != "")
	})
	var all []gitignore.Pattern
	for _, d := range dirs {
		all = append(all, w.pats[d]...)
	}
	w.matcher = gitignore.NewMatcher(all)
}

func (w *watch) readGitignore(dir string) {
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	rel := w.rel(dir)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		if _, had := w.pats[rel]; had && rel != "" {
			delete(w.pats, rel)
			w.rebuild()
		}
		return
	}
	var domain []string
	if rel != "" {
		domain = strings.Split(rel, "/")
	}
	ps := parsePatterns(string(b), domain)
	if rel == "" { // keep info/exclude: store root .gitignore after it
		if ex, err := os.ReadFile(filepath.Join(w.root, ".git", "info", "exclude")); err == nil {
			ps = append(parsePatterns(string(ex), nil), ps...)
		}
	}
	w.pats[rel] = ps
	w.rebuild()
}

func (w *watch) rel(p string) string {
	r, err := filepath.Rel(w.root, p)
	if err != nil || r == "." {
		return ""
	}
	return filepath.ToSlash(r)
}

func (w *watch) ignored(p string, isDir bool) bool {
	r := w.rel(p)
	if r == "" {
		return false
	}
	parts := strings.Split(r, "/")
	for _, s := range parts {
		if s == ".git" {
			return true
		}
	}
	w.mu.Lock()
	m := w.matcher
	w.mu.Unlock()
	return m != nil && m.Match(parts, isDir)
}

// addTree adds dir and its non-ignored subdirectories.
func (w *watch) addTree(dir string) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != w.root && w.ignored(p, true) {
			return filepath.SkipDir
		}
		w.readGitignore(p)
		if err := w.b.addDir(p); err != nil {
			if isFDLimit(err) {
				if !w.warned {
					w.warned = true
					log.Printf("fs: watcher hit file descriptor limit at %s; some directories are not watched", p)
				}
				return filepath.SkipAll
			}
		}
		return nil
	})
}

func (w *watch) loop() {
	defer close(w.done)
	pending := map[string]struct{}{}
	var timer *time.Timer
	var tc <-chan time.Time
	flush := func() {
		if len(pending) == 0 {
			return
		}
		paths := make([]string, 0, len(pending))
		for p := range pending {
			paths = append(paths, p)
		}
		for i, p := range paths {
			paths[i] = w.outPath(p)
		}
		sort.Strings(paths)
		pending = map[string]struct{}{}
		w.fn(paths)
	}
	for {
		select {
		case <-w.stop:
			if timer != nil {
				timer.Stop()
			}
			return
		case err, ok := <-w.b.errors():
			if !ok {
				return
			}
			log.Printf("fs: watcher error: %v", err)
		case ev, ok := <-w.b.events():
			if !ok {
				return
			}
			if w.handle(ev) {
				pending[ev.name] = struct{}{}
				if timer == nil {
					timer = time.NewTimer(debounce)
				} else if tc == nil {
					timer.Reset(debounce)
				}
				tc = timer.C
			}
		case <-tc:
			tc = nil
			flush()
		}
	}
}

// outPath maps a path under the resolved root back to the caller's root.
func (w *watch) outPath(p string) string {
	if w.user == w.root {
		return p
	}
	if r, err := filepath.Rel(w.root, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.Join(w.user, r)
	}
	return p
}

func isFDLimit(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.ENFILE) || strings.Contains(err.Error(), "too many open files")
}

// handle processes one event and reports whether it should be emitted.
func (w *watch) handle(ev event) bool {
	name := ev.name
	if filepath.Base(name) == ".gitignore" && (ev.remove || ev.create || ev.write) {
		w.readGitignore(filepath.Dir(name))
	}
	if w.ignored(name, ev.isDir) {
		return false
	}
	if ev.create && ev.isDir {
		w.addTree(name)
	}
	if ev.chmod && !ev.write && !ev.create && !ev.remove {
		return false
	}
	return true
}
