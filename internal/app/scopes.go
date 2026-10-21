package app

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/sid/harness/internal/fs"
	"github.com/sid/harness/internal/search"
	"github.com/sid/harness/internal/stream"
)

const scmDebounce = 150 * time.Millisecond

// scopeHub ties together, per scope key, the fs watcher, the `fs/<key>` and
// `scm/<key>` bus channels and the SCM refresh. FSService.WatchScope and
// GitService both use it.
type scopeHub struct {
	bus     *stream.Bus
	watcher *fs.Watcher

	mu     sync.Mutex
	scopes map[string]*scopeState
}

type scopeState struct {
	key, root string
	fsCh      *stream.Channel
	scmCh     *stream.Channel

	pushMu sync.Mutex // serialises scm pushes so they cannot reorder
	timer  *time.Timer
}

func newScopeHub(bus *stream.Bus, w *fs.Watcher) *scopeHub {
	return &scopeHub{bus: bus, watcher: w, scopes: map[string]*scopeState{}}
}

func cleanRoot(root string) string {
	root = fs.Expand(root)
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return filepath.Clean(root)
}

// watch (re)starts watching root for key and opens its bus channels.
func (h *scopeHub) watch(key, root string) error {
	root = cleanRoot(root)
	h.mu.Lock()
	st := h.scopes[key]
	if st == nil {
		st = &scopeState{key: key, root: root}
		if h.bus != nil {
			st.fsCh = h.bus.Open("fs/" + key)
			st.scmCh = h.bus.Open("scm/" + key)
		}
		h.scopes[key] = st
	}
	st.root = root
	h.mu.Unlock()

	err := h.watcher.Watch(key, root, func(paths []string) {
		search.Invalidate(root, paths)
		if st.fsCh != nil {
			_ = st.fsCh.SendJSON(map[string]any{"paths": paths})
		}
		h.scheduleSCM(st)
	})
	if err != nil {
		return err
	}
	go h.pushSCM(st)
	return nil
}

func (h *scopeHub) unwatch(key string) {
	h.watcher.Unwatch(key)
	h.mu.Lock()
	st := h.scopes[key]
	delete(h.scopes, key)
	h.mu.Unlock()
	if st == nil {
		return
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	if st.fsCh != nil {
		st.fsCh.Close()
	}
	if st.scmCh != nil {
		st.scmCh.Close()
	}
}

func (h *scopeHub) scheduleSCM(st *scopeState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st.timer != nil {
		st.timer.Stop()
	}
	st.timer = time.AfterFunc(scmDebounce, func() { h.pushSCM(st) })
}

// pushSCM computes the SCM view-model for the scope and sends it.
func (h *scopeHub) pushSCM(st *scopeState) {
	st.pushMu.Lock()
	defer st.pushMu.Unlock()
	if st.scmCh == nil {
		return
	}
	state, err := scmState(st.root)
	if err != nil {
		return // not a git repo or transient failure; keep the last state
	}
	_ = st.scmCh.SendJSON(state)
}

// refreshRoot pushes SCM state to every scope whose root is root (called after
// mutating git calls). With key != "" that scope is refreshed too.
func (h *scopeHub) refreshRoot(root string) {
	root = cleanRoot(root)
	h.mu.Lock()
	var hit []*scopeState
	for _, st := range h.scopes {
		if st.root == root {
			hit = append(hit, st)
		}
	}
	h.mu.Unlock()
	for _, st := range hit {
		go h.pushSCM(st)
	}
}

func (h *scopeHub) refreshKey(key string) {
	h.mu.Lock()
	st := h.scopes[key]
	h.mu.Unlock()
	if st != nil {
		go h.pushSCM(st)
	}
}
