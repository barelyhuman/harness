package app

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"github.com/sid/harness/internal/pty"
	"github.com/sid/harness/internal/store"
	"github.com/sid/harness/internal/stream"
	"github.com/sid/harness/internal/termlayout"
)

// persistDelay debounces ui_state writes of terminal layouts (spec 03).
var persistDelay = 500 * time.Millisecond

// PaneRef identifies a freshly created pane and its shell.
type PaneRef struct {
	TabID  string `json:"tabId"`
	PaneID string `json:"paneId"`
	PtyID  string `json:"ptyId"`
}

type savedTerm struct {
	Layout json.RawMessage   `json:"layout"`
	Cwds   map[string]string `json:"cwds"` // paneId -> cwd at save time
}

type termScope struct {
	ch    *stream.Channel
	unsub func()

	mu    sync.Mutex
	timer *time.Timer
}

// TermService: PTYs plus the Go-owned split layout. Layout intents mutate the
// termlayout.Manager, auto-spawn a shell for each new pane and kill the shell
// of each closed pane. The Layout is pushed on `termlayout/<scopeKey>`.
type TermService struct {
	bus     *stream.Bus // may be nil in tests
	pty     *pty.Manager
	layouts *termlayout.Manager
	ui      *uiStore
	env     func() []string

	mu     sync.Mutex
	scopes map[string]*termScope
}

func newTermService(bus *stream.Bus, p *pty.Manager, l *termlayout.Manager, st *store.Store, env func() []string) *TermService {
	return &TermService{bus: bus, pty: p, layouts: l, ui: &uiStore{db: st.DB}, env: env, scopes: map[string]*termScope{}}
}

// ensure opens the scope's push channel and subscribes to layout changes.
func (s *TermService) ensure(scopeKey string) *termScope {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ts := s.scopes[scopeKey]; ts != nil {
		return ts
	}
	ts := &termScope{}
	if s.bus != nil {
		ts.ch = s.bus.Open("termlayout/" + scopeKey)
	}
	ts.unsub = s.layouts.Subscribe(scopeKey, func(l termlayout.Layout) {
		if ts.ch != nil {
			_ = ts.ch.SendJSON(l)
		}
		s.schedulePersist(scopeKey, ts)
	})
	s.scopes[scopeKey] = ts
	return ts
}

func (s *TermService) schedulePersist(scopeKey string, ts *termScope) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.timer != nil {
		ts.timer.Stop()
	}
	ts.timer = time.AfterFunc(persistDelay, func() {
		if err := s.persist(scopeKey); err != nil {
			log.Printf("term: persist %s: %v", scopeKey, err)
		}
	})
}

// persist writes the scope's layout and pane cwds to ui_state["term:<key>"].
func (s *TermService) persist(scopeKey string) error {
	l := s.layouts.Get(scopeKey)
	lj, err := json.Marshal(l)
	if err != nil {
		return err
	}
	cwds := map[string]string{}
	for _, n := range leaves(&l) {
		if n.PtyID == "" {
			continue
		}
		if in, err := s.pty.Info(n.PtyID); err == nil && in.Cwd != "" {
			cwds[n.PaneID] = in.Cwd
		}
	}
	b, err := json.Marshal(savedTerm{Layout: lj, Cwds: cwds})
	if err != nil {
		return err
	}
	return s.ui.set("term:"+scopeKey, string(b))
}

// Flush persists every scope with a pending debounce (called on shutdown).
func (s *TermService) Flush() {
	s.mu.Lock()
	keys := make([]string, 0, len(s.scopes))
	for k, ts := range s.scopes {
		ts.mu.Lock()
		if ts.timer != nil && ts.timer.Stop() {
			keys = append(keys, k)
		}
		ts.mu.Unlock()
	}
	s.mu.Unlock()
	for _, k := range keys {
		if err := s.persist(k); err != nil {
			log.Printf("term: flush %s: %v", k, err)
		}
	}
}

func leaves(l *termlayout.Layout) []termlayout.Node {
	var out []termlayout.Node
	var walk func(n termlayout.Node)
	walk = func(n termlayout.Node) {
		if n.Type == termlayout.TypeLeaf {
			out = append(out, n)
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, t := range l.Tabs {
		walk(t.Root)
	}
	return out
}

func findLeaf(l *termlayout.Layout, pane string) (termlayout.Node, bool) {
	for _, n := range leaves(l) {
		if n.PaneID == pane {
			return n, true
		}
	}
	return termlayout.Node{}, false
}

func (s *TermService) spawn(scopeKey, cwd, kind string, cols, rows uint16) (string, error) {
	if cwd != "" {
		if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
			cwd = ""
		}
	}
	return s.pty.Spawn(scopeKey, cwd, cols, rows, kind, s.env())
}

// ---- raw PTY calls ----

// Spawn starts a standalone shell (not tracked by the layout) and returns its
// pty id. Connect to bus channel `pty/<id>` to stream it.
func (s *TermService) Spawn(scopeKey, cwd, kind string, cols, rows int) (string, error) {
	if kind == "" {
		kind = "shell"
	}
	id, err := s.spawn(scopeKey, cwd, kind, uint16(cols), uint16(rows))
	return id, wrapErr(err)
}

func (s *TermService) Kill(ptyID string) error { return wrapErr(s.pty.Kill(ptyID)) }

func (s *TermService) Resize(ptyID string, cols, rows int) error {
	return wrapErr(s.pty.Resize(ptyID, uint16(cols), uint16(rows)))
}

// List returns the PTYs of a scope ("" = all).
func (s *TermService) List(scopeKey string) []pty.Info { return s.pty.List(scopeKey) }

// ShellsRunning counts live PTYs, for the quit warning.
func (s *TermService) ShellsRunning() int {
	n := 0
	for _, in := range s.pty.List("") {
		if !in.Exited {
			n++
		}
	}
	return n
}

// KillScope kills the scope's PTYs and forgets its layout and saved state.
func (s *TermService) KillScope(scopeKey string) {
	for _, in := range s.pty.List(scopeKey) {
		_ = s.pty.Kill(in.ID)
	}
	s.mu.Lock()
	ts := s.scopes[scopeKey]
	delete(s.scopes, scopeKey)
	s.mu.Unlock()
	if ts != nil {
		ts.mu.Lock()
		if ts.timer != nil {
			ts.timer.Stop()
		}
		ts.mu.Unlock()
		ts.unsub()
		if ts.ch != nil {
			ts.ch.Close()
		}
	}
	s.layouts.Remove(scopeKey)
	_, _ = s.ui.db.Exec(`DELETE FROM ui_state WHERE scope_key = ?`, "term:"+scopeKey)
}

// ---- layout ----

// Layout returns the scope's current layout (and starts pushing updates on
// `termlayout/<scopeKey>`). Call it after attaching to the channel.
func (s *TermService) Layout(scopeKey string) termlayout.Layout {
	s.ensure(scopeKey)
	return s.layouts.Get(scopeKey)
}

// NewTab adds a tab with one shell started in cwd.
func (s *TermService) NewTab(scopeKey, cwd string) (PaneRef, error) {
	s.ensure(scopeKey)
	var ref PaneRef
	err := s.layouts.Update(scopeKey, func(l *termlayout.Layout) error {
		tab, pane := l.NewTab(termlayout.KindShell)
		id, err := s.spawn(scopeKey, cwd, "shell", 0, 0)
		if err != nil {
			_, _ = l.Close(pane)
			return err
		}
		if err := l.AttachPty(pane, id); err != nil {
			_ = s.pty.Kill(id)
			return err
		}
		ref = PaneRef{TabID: tab, PaneID: pane, PtyID: id}
		return nil
	})
	return ref, wrapErr(err)
}

// Split splits paneID ("row" = new pane to the right, "col" = below) and
// starts a shell in cwd (default: the source pane's current directory).
func (s *TermService) Split(scopeKey, paneID, dir, cwd string) (PaneRef, error) {
	d := termlayout.Dir(dir)
	if d != termlayout.DirRow && d != termlayout.DirCol {
		return PaneRef{}, badArg("dir must be row or col")
	}
	s.ensure(scopeKey)
	var ref PaneRef
	err := s.layouts.Update(scopeKey, func(l *termlayout.Layout) error {
		src, _ := findLeaf(l, paneID)
		np, err := l.Split(paneID, d)
		if err != nil {
			return err
		}
		if cwd == "" && src.PtyID != "" {
			if in, err := s.pty.Info(src.PtyID); err == nil {
				cwd = in.Cwd
			}
		}
		id, err := s.spawn(scopeKey, cwd, "shell", 0, 0)
		if err != nil {
			_, _ = l.Close(np)
			return err
		}
		if err := l.AttachPty(np, id); err != nil {
			_ = s.pty.Kill(id)
			return err
		}
		ref = PaneRef{PaneID: np, PtyID: id}
		for _, t := range l.Tabs {
			if t.Focused == np {
				ref.TabID = t.ID
			}
		}
		return nil
	})
	return ref, wrapErr(err)
}

// ClosePane removes the pane (and its tab if it was the last pane) and kills
// its shell.
func (s *TermService) ClosePane(scopeKey, paneID string) error {
	s.ensure(scopeKey)
	var ptyID string
	err := s.layouts.Update(scopeKey, func(l *termlayout.Layout) error {
		n, _ := findLeaf(l, paneID)
		ptyID = n.PtyID
		_, err := l.Close(paneID)
		return err
	})
	if err != nil {
		return wrapErr(err)
	}
	if ptyID != "" {
		_ = s.pty.Kill(ptyID)
	}
	return nil
}

func (s *TermService) update(scopeKey string, fn func(*termlayout.Layout) error) error {
	s.ensure(scopeKey)
	return wrapErr(s.layouts.Update(scopeKey, fn))
}

func parseDirection(d string) (termlayout.Direction, error) {
	switch d {
	case "left":
		return termlayout.Left, nil
	case "right":
		return termlayout.Right, nil
	case "up":
		return termlayout.Up, nil
	case "down":
		return termlayout.Down, nil
	}
	return 0, badArg("direction must be left|right|up|down")
}

func (s *TermService) FocusPane(scopeKey, paneID string) error {
	return s.update(scopeKey, func(l *termlayout.Layout) error { return l.Focus(paneID) })
}

// FocusDir moves focus to the neighbouring pane (dir: left|right|up|down).
func (s *TermService) FocusDir(scopeKey, dir string) error {
	d, err := parseDirection(dir)
	if err != nil {
		return err
	}
	return s.update(scopeKey, func(l *termlayout.Layout) error { _, _, err := l.FocusDir(d); return err })
}

// ResizeDir nudges the focused pane's boundary by 5% (dir: left|right|up|down).
func (s *TermService) ResizeDir(scopeKey, dir string) error {
	d, err := parseDirection(dir)
	if err != nil {
		return err
	}
	return s.update(scopeKey, func(l *termlayout.Layout) error { _, err := l.ResizeDir(d); return err })
}

// SetRatios sets the ratios of the split at path (child indices from the tab
// root; empty = root split) after a divider drag.
func (s *TermService) SetRatios(scopeKey, tabID string, path []int, ratios []float64) error {
	return s.update(scopeKey, func(l *termlayout.Layout) error { return l.SetRatios(tabID, path, ratios) })
}

func (s *TermService) ToggleMaximize(scopeKey string) error {
	return s.update(scopeKey, func(l *termlayout.Layout) error { return l.ToggleMaximize() })
}

func (s *TermService) NextTab(scopeKey string) error {
	return s.update(scopeKey, func(l *termlayout.Layout) error { return l.NextTab() })
}

func (s *TermService) PrevTab(scopeKey string) error {
	return s.update(scopeKey, func(l *termlayout.Layout) error { return l.PrevTab() })
}

func (s *TermService) ActivateTab(scopeKey, tabID string) error {
	return s.update(scopeKey, func(l *termlayout.Layout) error { return l.ActivateTab(tabID) })
}

func (s *TermService) RenameTab(scopeKey, tabID, title string) error {
	return s.update(scopeKey, func(l *termlayout.Layout) error { return l.RenameTab(tabID, title) })
}

func (s *TermService) MoveTab(scopeKey string, from, to int) error {
	return s.update(scopeKey, func(l *termlayout.Layout) error { return l.MoveTab(from, to) })
}

// RestoreLayout loads the saved layout for the scope (if the scope has no live
// layout yet), respawns a shell for every pane in its saved cwd (falling back
// to cwd) and attaches it. Returns the layout; Tabs is empty when nothing was
// saved (call NewTab then).
func (s *TermService) RestoreLayout(scopeKey, cwd string) (termlayout.Layout, error) {
	s.ensure(scopeKey)
	if cur := s.layouts.Get(scopeKey); len(cur.Tabs) > 0 {
		return cur, nil
	}
	j, ok, err := s.ui.get("term:" + scopeKey)
	if err != nil {
		return termlayout.Layout{}, wrapErr(err)
	}
	if !ok {
		return s.layouts.Get(scopeKey), nil
	}
	var saved savedTerm
	if err := json.Unmarshal([]byte(j), &saved); err != nil {
		log.Printf("term: bad saved layout for %s: %v", scopeKey, err)
		return s.layouts.Get(scopeKey), nil
	}
	if err := s.layouts.Restore(scopeKey, saved.Layout); err != nil {
		log.Printf("term: restore %s: %v", scopeKey, err)
		return s.layouts.Get(scopeKey), nil
	}
	err = s.layouts.Update(scopeKey, func(l *termlayout.Layout) error {
		for _, n := range leaves(l) {
			_ = l.AttachPty(n.PaneID, "") // saved pty ids are dead
			if n.Kind == termlayout.KindAgent {
				continue // read-only mirror, no PTY
			}
			c := saved.Cwds[n.PaneID]
			if c == "" {
				c = cwd
			}
			id, err := s.spawn(scopeKey, c, string(n.Kind), 0, 0)
			if err != nil {
				log.Printf("term: respawn %s/%s: %v", scopeKey, n.PaneID, err)
				continue
			}
			_ = l.AttachPty(n.PaneID, id)
		}
		return nil
	})
	return s.layouts.Get(scopeKey), wrapErr(err)
}
