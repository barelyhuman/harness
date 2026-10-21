package lsp

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// ServerStatus is a snapshot of one server.
type ServerStatus struct {
	Key      string `json:"key"`
	ID       string `json:"id"`
	Root     string `json:"root"`
	State    string `json:"state"` // starting|running|idle|stopped|crashed
	Refcount int    `json:"refcount"`
	Pid      int    `json:"pid"`
}

const (
	stStarting = "starting"
	stRunning  = "running"
	stIdle     = "idle"
	stStopped  = "stopped"
	stCrashed  = "crashed"
)

// Options tunes a Manager.
type Options struct {
	Servers     []ServerConfig // overrides merged over DefaultServers
	IdleTTL     time.Duration  // default 5m
	KillGrace   time.Duration  // default 2s
	InitTimeout time.Duration  // default 30s
}

// Manager owns all language servers.
type Manager struct {
	idleTTL, killGrace, initTimeout time.Duration

	cfgMu sync.RWMutex
	cfgs  []ServerConfig

	mu      sync.Mutex
	servers map[string]*server

	subMu   sync.Mutex
	notifyM sync.Mutex
	subs    map[int]func([]ServerStatus)
	nextSub int
}

// NewManager creates a manager.
func NewManager(o Options) *Manager {
	m := &Manager{idleTTL: o.IdleTTL, killGrace: o.KillGrace, initTimeout: o.InitTimeout,
		servers: map[string]*server{}, subs: map[int]func([]ServerStatus){},
		cfgs: MergeConfigs(DefaultServers(), o.Servers)}
	if m.idleTTL <= 0 {
		m.idleTTL = 5 * time.Minute
	}
	if m.killGrace <= 0 {
		m.killGrace = 2 * time.Second
	}
	if m.initTimeout <= 0 {
		m.initTimeout = 30 * time.Second
	}
	return m
}

// SetServers re-merges the table from settings overrides (affects future starts).
func (m *Manager) SetServers(overrides []ServerConfig) {
	m.cfgMu.Lock()
	m.cfgs = MergeConfigs(DefaultServers(), overrides)
	m.cfgMu.Unlock()
}

func (m *Manager) cfgForLang(lang string) (ServerConfig, bool) {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	for _, c := range m.cfgs {
		for _, l := range c.Languages {
			if l == lang {
				return c, true
			}
		}
	}
	return ServerConfig{}, false
}

func serverKey(id, root string) string { return id + "@" + root }

// Acquire lazily starts the server for languageID in scopeRoot and takes a
// reference. Pair with Release (the bus channel close does this for you once
// the client attaches to lsp/<key>).
func (m *Manager) Acquire(scopeRoot, languageID string) (string, error) {
	cfg, ok := m.cfgForLang(languageID)
	if !ok {
		return "", ErrNoServer
	}
	key := serverKey(cfg.ID, scopeRoot)
	m.mu.Lock()
	s := m.servers[key]
	if s == nil {
		s = newServer(m, key, scopeRoot, cfg)
		m.servers[key] = s
	}
	m.mu.Unlock()

	s.startMu.Lock()
	defer s.startMu.Unlock()
	s.mu.Lock()
	if s.state == stRunning {
		s.refs++
		s.cancelIdleLocked()
		s.mu.Unlock()
		m.notify()
		return key, nil
	}
	s.mu.Unlock()
	bin, err := resolveBinary(cfg)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.state = stStarting
	s.mu.Unlock()
	m.notify()
	if err := s.start(bin); err != nil {
		s.mu.Lock()
		s.state = stStopped
		s.mu.Unlock()
		m.notify()
		return "", err
	}
	s.mu.Lock()
	s.refs = 1
	s.mu.Unlock()
	m.notify()
	return key, nil
}

// Release drops one reference; at zero the idle timer starts.
func (m *Manager) Release(key string) {
	m.mu.Lock()
	s := m.servers[key]
	m.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	s.releaseLocked()
	s.mu.Unlock()
	m.notify()
}

// Touch (agent edits) resets the idle timer of a running server for path's
// language in scopeRoot. It never starts a server. Reports whether one was touched.
func (m *Manager) Touch(scopeRoot, path string) bool {
	lang := LanguageForPath(path)
	cfg, ok := m.cfgForLang(lang)
	if !ok {
		return false
	}
	m.mu.Lock()
	s := m.servers[serverKey(cfg.ID, scopeRoot)]
	m.mu.Unlock()
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stRunning {
		return false
	}
	if s.refs == 0 {
		s.armIdleLocked()
	}
	return true
}

// Status returns a snapshot of all known servers.
func (m *Manager) Status() []ServerStatus {
	m.mu.Lock()
	list := make([]*server, 0, len(m.servers))
	for _, s := range m.servers {
		list = append(list, s)
	}
	m.mu.Unlock()
	out := make([]ServerStatus, 0, len(list))
	for _, s := range list {
		s.mu.Lock()
		st := s.state
		if st == stRunning && s.refs == 0 {
			st = stIdle
		}
		out = append(out, ServerStatus{Key: s.key, ID: s.cfg.ID, Root: s.root, State: st, Refcount: s.refs, Pid: s.pid})
		s.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Subscribe registers fn for status changes; the returned func unsubscribes.
func (m *Manager) Subscribe(fn func([]ServerStatus)) func() {
	m.subMu.Lock()
	m.nextSub++
	id := m.nextSub
	m.subs[id] = fn
	m.subMu.Unlock()
	return func() { m.subMu.Lock(); delete(m.subs, id); m.subMu.Unlock() }
}

func (m *Manager) notify() {
	m.subMu.Lock()
	if len(m.subs) == 0 {
		m.subMu.Unlock()
		return
	}
	fns := make([]func([]ServerStatus), 0, len(m.subs))
	for _, f := range m.subs {
		fns = append(fns, f)
	}
	m.subMu.Unlock()
	m.notifyM.Lock()
	defer m.notifyM.Unlock()
	st := m.Status()
	for _, f := range fns {
		f(st)
	}
}

// Close stops every server.
func (m *Manager) Close() {
	m.mu.Lock()
	list := make([]*server, 0, len(m.servers))
	for _, s := range m.servers {
		list = append(list, s)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.startMu.Lock()
			defer s.startMu.Unlock()
			s.mu.Lock()
			run := s.state == stRunning
			s.mu.Unlock()
			if run {
				s.shutdown()
			}
		}()
	}
	wg.Wait()
	m.notify()
}

// ErrNotRunning is returned by Attach for a server that is not running.
var ErrNotRunning = errors.New("lsp: server not running")

func splitKey(name string) string { return strings.TrimPrefix(name, "lsp/") }
