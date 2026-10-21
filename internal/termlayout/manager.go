package termlayout

import (
	"encoding/json"
	"sync"
)

// Manager holds one Layout per scope string and is safe for concurrent use.
type Manager struct {
	mu      sync.Mutex
	notify  sync.Mutex // held across handoff so subscribers see mutations in order
	layouts map[string]*Layout
	subs    map[string]map[int]func(Layout)
	nextSub int
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{layouts: map[string]*Layout{}, subs: map[string]map[int]func(Layout){}}
}

// Update runs fn on the scope's layout (created empty on first use) under the
// manager lock. If fn returns nil, subscribers of the scope are called with a
// deep copy, in mutation order, after the lock is released. Subscribers run
// synchronously and must not call back into the Manager.
func (m *Manager) Update(scope string, fn func(*Layout) error) error {
	m.mu.Lock()
	l := m.layouts[scope]
	if l == nil {
		l = New()
		m.layouts[scope] = l
	}
	if err := fn(l); err != nil {
		m.mu.Unlock()
		return err
	}
	snap := l.Clone()
	cbs := make([]func(Layout), 0, len(m.subs[scope]))
	for _, cb := range m.subs[scope] {
		cbs = append(cbs, cb)
	}
	m.notify.Lock()
	m.mu.Unlock()
	defer m.notify.Unlock()
	for _, cb := range cbs {
		cb(snap.Clone())
	}
	return nil
}

// Get returns a deep copy of the scope's layout (empty if unknown).
func (m *Manager) Get(scope string) Layout {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l := m.layouts[scope]; l != nil {
		return l.Clone()
	}
	return *New()
}

// Subscribe registers fn for mutations of scope and returns an unsubscribe func.
func (m *Manager) Subscribe(scope string, fn func(Layout)) (unsubscribe func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.subs[scope] == nil {
		m.subs[scope] = map[int]func(Layout){}
	}
	id := m.nextSub
	m.nextSub++
	m.subs[scope][id] = fn
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.subs[scope], id)
	}
}

// Restore replaces a scope's layout with persisted JSON after validating it.
func (m *Manager) Restore(scope string, data []byte) error {
	var in Layout
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	return m.Update(scope, func(l *Layout) error { *l = in; return nil })
}

// Remove drops a scope's layout and subscribers.
func (m *Manager) Remove(scope string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.layouts, scope)
	delete(m.subs, scope)
}
