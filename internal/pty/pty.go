// Package pty manages pseudo-terminal shells and attaches them to bus
// channels named pty/<id>.
package pty

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	cpty "github.com/creack/pty"

	"github.com/sid/harness/internal/stream"
)

const (
	ringSize     = 1 << 20
	maxPending   = 4 << 20
	killGrace    = 2 * time.Second
	maxOSCLength = 4096
)

// ErrNotFound is returned for unknown pty ids.
var ErrNotFound = errors.New("pty: not found")

// Conn is the subset of *stream.Channel the manager needs.
type Conn interface {
	SendBytes([]byte) error
	SendJSON(v any) error
	OnData(func([]byte))
	OnJSON(func(json.RawMessage))
	OnClose(func())
	Close()
}

var _ Conn = (*stream.Channel)(nil)

// Info describes one PTY.
type Info struct {
	ID       string `json:"id"`
	Scope    string `json:"scope"`
	Kind     string `json:"kind"`
	Pid      int    `json:"pid"`
	Cwd      string `json:"cwd"`
	Cols     uint16 `json:"cols"`
	Rows     uint16 `json:"rows"`
	Exited   bool   `json:"exited"`
	ExitCode int    `json:"exitCode"`
}

// Manager owns all PTYs.
type Manager struct {
	mu   sync.Mutex
	ptys map[string]*PTY
	next int
}

func NewManager() *Manager { return &Manager{ptys: map[string]*PTY{}} }

// PTY is one running terminal.
type PTY struct {
	id, scope, kind string
	cmd             *exec.Cmd
	f               *os.File
	spawnCwd        string
	input           *queue
	done            chan struct{}

	mu       sync.Mutex
	ring     []byte
	oscTail  []byte
	cwd      string
	cols     uint16
	rows     uint16
	exited   bool
	exitCode int
	subs     map[*sub]struct{}
	killing  bool
}

// Spawn starts `$SHELL -l` (fallback /bin/zsh).
func (m *Manager) Spawn(scope, cwd string, cols, rows uint16, kind string, env []string) (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	return m.SpawnCmd(scope, cwd, cols, rows, kind, env, shell, "-l")
}

// SpawnCmd is Spawn with an explicit command (used by tests and setup runs).
func (m *Manager) SpawnCmd(scope, cwd string, cols, rows uint16, kind string, env []string, name string, args ...string) (string, error) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(filterEnv(env, "TERM", "COLORTERM"), "TERM=xterm-256color", "COLORTERM=truecolor")
	// Setsid makes the child a session+group leader (pgid == pid), which is
	// what Setpgid would give us; the two cannot be combined.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	f, err := cpty.StartWithAttrs(cmd, &cpty.Winsize{Cols: cols, Rows: rows}, cmd.SysProcAttr)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.next++
	id := "t" + strconv.Itoa(m.next)
	p := &PTY{id: id, scope: scope, kind: kind, cmd: cmd, f: f, spawnCwd: cwd, cols: cols, rows: rows,
		subs: map[*sub]struct{}{}, input: newQueue(), done: make(chan struct{})}
	m.ptys[id] = p
	m.mu.Unlock()
	go p.writeLoop()
	go p.readLoop()
	go p.waitLoop()
	return id, nil
}

func filterEnv(env []string, drop ...string) []string {
	out := make([]string, 0, len(env)+2)
next:
	for _, kv := range env {
		for _, d := range drop {
			if strings.HasPrefix(kv, d+"=") {
				continue next
			}
		}
		out = append(out, kv)
	}
	return out
}

func (m *Manager) get(id string) (*PTY, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.ptys[id]
	if p == nil {
		return nil, ErrNotFound
	}
	return p, nil
}

// Resize changes the window size.
func (m *Manager) Resize(id string, cols, rows uint16) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	return p.resize(cols, rows)
}

func (p *PTY) resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return errors.New("pty: bad size")
	}
	p.mu.Lock()
	if p.exited {
		p.mu.Unlock()
		return nil
	}
	p.cols, p.rows = cols, rows
	p.mu.Unlock()
	return cpty.Setsize(p.f, &cpty.Winsize{Cols: cols, Rows: rows})
}

// Kill sends SIGHUP to the process group, then SIGKILL after 2s. Killing an
// already-exited PTY removes it from the manager.
func (m *Manager) Kill(id string) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.exited {
		p.mu.Unlock()
		m.mu.Lock()
		delete(m.ptys, id)
		m.mu.Unlock()
		return nil
	}
	if p.killing {
		p.mu.Unlock()
		return nil
	}
	p.killing = true
	p.mu.Unlock()
	pid := p.cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	time.AfterFunc(killGrace, func() {
		p.mu.Lock()
		done := p.exited
		p.mu.Unlock()
		if !done {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	return nil
}

// KillAll kills every PTY and waits up to the grace period for them to exit.
func (m *Manager) KillAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.ptys))
	for id := range m.ptys {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Kill(id)
	}
	deadline := time.Now().Add(killGrace + 500*time.Millisecond)
	for _, id := range ids {
		p, err := m.get(id)
		if err != nil {
			continue
		}
		for time.Now().Before(deadline) {
			p.mu.Lock()
			d := p.exited
			p.mu.Unlock()
			if d {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// List returns PTYs of a scope ("" = all), ordered by id creation.
func (m *Manager) List(scope string) []Info {
	m.mu.Lock()
	ps := make([]*PTY, 0, len(m.ptys))
	for _, p := range m.ptys {
		if scope == "" || p.scope == scope {
			ps = append(ps, p)
		}
	}
	m.mu.Unlock()
	out := make([]Info, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.info())
	}
	sortInfos(out)
	return out
}

func sortInfos(s []Info) {
	n := func(id string) int { v, _ := strconv.Atoi(strings.TrimPrefix(id, "t")); return v }
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && n(s[j].ID) < n(s[j-1].ID); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Info returns details for one PTY.
func (m *Manager) Info(id string) (Info, error) {
	p, err := m.get(id)
	if err != nil {
		return Info{}, err
	}
	return p.info(), nil
}

func (p *PTY) info() Info {
	p.mu.Lock()
	in := Info{ID: p.id, Scope: p.scope, Kind: p.kind, Pid: p.cmd.Process.Pid, Cwd: p.cwd,
		Cols: p.cols, Rows: p.rows, Exited: p.exited, ExitCode: p.exitCode}
	p.mu.Unlock()
	if in.Cwd == "" {
		if !in.Exited {
			in.Cwd = lsofCwd(in.Pid)
		}
		if in.Cwd == "" {
			in.Cwd = p.spawnCwd
		}
	}
	return in
}

func lsofCwd(pid int) string {
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-p", strconv.Itoa(pid), "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "n") {
			return l[1:]
		}
	}
	return ""
}

// ---- I/O loops ----

func (p *PTY) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := p.f.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			p.onOutput(chunk)
		}
		if err != nil {
			break
		}
	}
	// Output is finished; wait for the process status, then notify.
	<-p.waitDone()
	p.mu.Lock()
	subs := make([]*sub, 0, len(p.subs))
	for s := range p.subs {
		subs = append(subs, s)
	}
	p.mu.Unlock()
	for _, s := range subs {
		s.finish()
	}
}

func (p *PTY) waitDone() chan struct{} { return p.done }

func (p *PTY) waitLoop() {
	err := p.cmd.Wait()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
			if code < 0 { // killed by signal
				if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
					code = 128 + int(ws.Signal())
				}
			}
		} else {
			code = -1
		}
	}
	p.mu.Lock()
	p.exited = true
	p.exitCode = code
	p.mu.Unlock()
	p.input.close()
	// Background children may hold the slave open; stop reading shortly.
	time.AfterFunc(300*time.Millisecond, func() { p.f.Close() })
	close(p.waitDone())
}

func (p *PTY) writeLoop() {
	for {
		b, ok := p.input.pop()
		if !ok {
			return
		}
		for len(b) > 0 {
			n, err := p.f.Write(b)
			if err != nil {
				return
			}
			b = b[n:]
		}
	}
}

func (p *PTY) onOutput(chunk []byte) {
	p.mu.Lock()
	p.ring = append(p.ring, chunk...)
	if len(p.ring) > ringSize {
		p.ring = append([]byte(nil), p.ring[len(p.ring)-ringSize:]...)
	}
	p.scanOSC7(chunk)
	subs := make([]*sub, 0, len(p.subs))
	for s := range p.subs {
		subs = append(subs, s)
	}
	p.mu.Unlock()
	for _, s := range subs {
		s.push(chunk)
	}
}

var osc7 = []byte("\x1b]7;")

// scanOSC7 updates p.cwd; p.mu must be held.
func (p *PTY) scanOSC7(chunk []byte) {
	buf := append(p.oscTail, chunk...)
	p.oscTail = nil
	for {
		i := bytes.Index(buf, osc7)
		if i < 0 {
			if len(buf) >= 3 {
				buf = buf[len(buf)-3:]
			}
			p.oscTail = append([]byte(nil), buf...)
			return
		}
		rest := buf[i+len(osc7):]
		end, adv := -1, 0
		if j := bytes.IndexByte(rest, 0x07); j >= 0 {
			end, adv = j, j+1
		}
		if j := bytes.Index(rest, []byte("\x1b\\")); j >= 0 && (end < 0 || j < end) {
			end, adv = j, j+2
		}
		if end < 0 {
			if len(rest) < maxOSCLength {
				p.oscTail = append([]byte(nil), buf[i:]...)
			}
			return
		}
		if u, err := url.Parse(string(rest[:end])); err == nil && u.Scheme == "file" && u.Path != "" {
			p.cwd = u.Path
		}
		buf = rest[adv:]
	}
}

// ---- attachments ----

// sub is one attached channel with its own pending queue and goroutine.
type sub struct {
	conn    Conn
	mu      sync.Mutex
	pending []byte
	first   []byte
	wake    chan struct{}
	done    chan struct{} // closed on detach
	fin     bool          // process output finished
	once    sync.Once
}

func (s *sub) push(b []byte) {
	s.mu.Lock()
	s.pending = append(s.pending, b...)
	if len(s.pending) > maxPending {
		s.pending = append([]byte(nil), s.pending[len(s.pending)-ringSize:]...)
	}
	s.mu.Unlock()
	s.signal()
}

func (s *sub) finish() {
	s.mu.Lock()
	s.fin = true
	s.mu.Unlock()
	s.signal()
}

func (s *sub) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *sub) detach() { s.once.Do(func() { close(s.done) }) }

func (p *PTY) run(s *sub) {
	if len(s.first) > 0 {
		if s.conn.SendBytes(s.first) != nil {
			p.remove(s)
			return
		}
	}
	s.first = nil
	exitSent := false
	for {
		s.mu.Lock()
		b, fin := s.pending, s.fin
		s.pending = nil
		s.mu.Unlock()
		if len(b) > 0 {
			if s.conn.SendBytes(b) != nil {
				p.remove(s)
				return
			}
			continue
		}
		if fin && !exitSent {
			exitSent = true
			p.mu.Lock()
			code := p.exitCode
			p.mu.Unlock()
			_ = s.conn.SendJSON(map[string]any{"type": "exit", "code": code})
		}
		select {
		case <-s.wake:
		case <-s.done:
			return
		}
	}
}

func (p *PTY) remove(s *sub) {
	p.mu.Lock()
	delete(p.subs, s)
	p.mu.Unlock()
	s.detach()
}

// Attach replays the ring buffer then streams live output to ch. Input from
// ch goes to the PTY. Closing ch detaches without killing the PTY.
func (m *Manager) Attach(id string, ch Conn) error {
	p, err := m.get(id)
	if err != nil {
		return err
	}
	s := &sub{conn: ch, wake: make(chan struct{}, 1), done: make(chan struct{})}
	p.mu.Lock()
	s.first = append([]byte(nil), p.ring...)
	p.subs[s] = struct{}{}
	exited := p.exited
	p.mu.Unlock()
	if exited {
		s.fin = true
	}
	ch.OnData(func(b []byte) { p.input.push(append([]byte(nil), b...)) })
	ch.OnJSON(func(raw json.RawMessage) {
		var msg struct {
			Type string `json:"type"`
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		}
		if json.Unmarshal(raw, &msg) == nil && msg.Type == "resize" {
			go p.resize(msg.Cols, msg.Rows) // ioctl is fast, but keep the read loop free
		}
	})
	ch.OnClose(func() { p.remove(s) })
	go p.run(s)
	return nil
}

// Register handles client-opened channels named pty/<id>.
func (m *Manager) Register(bus *stream.Bus) {
	bus.Handle("pty/", func(ch *stream.Channel) {
		id := strings.TrimPrefix(ch.Name(), "pty/")
		if err := m.Attach(id, ch); err != nil {
			ch.Close()
		}
	})
}

// queue is an unbounded byte-chunk FIFO so channel read loops never block on
// a full PTY input buffer.
type queue struct {
	mu     sync.Mutex
	items  [][]byte
	wake   chan struct{}
	closed bool
}

func newQueue() *queue { return &queue{wake: make(chan struct{}, 1)} }

func (q *queue) push(b []byte) {
	q.mu.Lock()
	if !q.closed {
		q.items = append(q.items, b)
	}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *queue) pop() ([]byte, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			b := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return b, true
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return nil, false
		}
		<-q.wake
	}
}
