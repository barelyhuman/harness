package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/sid/harness/internal/agent"
)

// HostInfo is persisted to <dataDir>/runs/<runID>.host.json.
type HostInfo struct {
	RunID     string    `json:"runID"`
	PID       int       `json:"pid"`
	Socket    string    `json:"socket"`
	StartedAt time.Time `json:"startedAt"`
	Alive     bool      `json:"alive,omitempty"` // filled by Scan only
	ExitCode  *int      `json:"exitCode,omitempty"`
	ExitedAt  time.Time `json:"exitedAt,omitempty"`
}

// Main is the entry point for `harness --agent-host --run <id> --spec <file>`.
func Main(args []string) error {
	fs := flag.NewFlagSet("agent-host", flag.ContinueOnError)
	_ = fs.Bool("agent-host", true, "")
	run := fs.String("run", "", "run id")
	specFile := fs.String("spec", "", "spec json path")
	if len(args) > 0 {
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *specFile == "" {
		return errors.New("host: --spec required")
	}
	b, err := os.ReadFile(*specFile)
	if err != nil {
		return err
	}
	var spec Spec
	if err := json.Unmarshal(b, &spec); err != nil {
		return fmt.Errorf("host: bad spec: %w", err)
	}
	if spec.RunID == "" {
		spec.RunID = *run
	}
	if *run != "" && spec.RunID != *run {
		return fmt.Errorf("host: --run %q != spec runID %q", *run, spec.RunID)
	}
	return runHost(spec)
}

type conn struct {
	c      net.Conn
	mu     sync.Mutex
	cond   *sync.Cond
	q      [][]byte
	closed bool // stop immediately
	drain  bool // close once queue is empty
	hello  bool // guarded by host.mu
}

func newConn(c net.Conn) *conn {
	cn := &conn{c: c}
	cn.cond = sync.NewCond(&cn.mu)
	return cn
}

func (c *conn) push(b []byte) {
	c.mu.Lock()
	if !c.closed {
		c.q = append(c.q, b)
		c.cond.Signal()
	}
	c.mu.Unlock()
}

func (c *conn) finish() {
	c.mu.Lock()
	c.drain = true
	c.cond.Signal()
	c.mu.Unlock()
}

func (c *conn) close() {
	c.mu.Lock()
	c.closed = true
	c.cond.Signal()
	c.mu.Unlock()
	c.c.Close()
}

func (c *conn) writeLoop() {
	defer c.c.Close()
	for {
		c.mu.Lock()
		for len(c.q) == 0 && !c.closed && !c.drain {
			c.cond.Wait()
		}
		if c.closed || (len(c.q) == 0 && c.drain) {
			c.mu.Unlock()
			return
		}
		batch := c.q
		c.q = nil
		c.mu.Unlock()
		var buf bytes.Buffer
		for _, b := range batch {
			buf.Write(b)
		}
		if _, err := c.c.Write(buf.Bytes()); err != nil {
			c.mu.Lock()
			c.closed = true
			c.mu.Unlock()
			return
		}
	}
}

type tailBuf struct {
	mu sync.Mutex
	b  []byte
}

const tailMax = 8 << 10

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.b = append(t.b, p...)
	if len(t.b) > tailMax {
		t.b = append([]byte(nil), t.b[len(t.b)-tailMax:]...)
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

type hostState struct {
	spec    Spec
	session agent.Session
	seq     *agent.Sequencer

	mu       sync.Mutex
	logf     *os.File
	lastSeq  uint64
	dirty    bool
	clients  map[*conn]struct{}
	pending  map[string]agent.Event // reqID -> permission_request
	idle     bool                   // a turn finished, waiting for follow-up
	idleT    *time.Timer
	stopping string // "", "idle", "shutdown", "signal"

	stdin     io.WriteCloser
	stdinOnce sync.Once
	proc      *os.Process
	wg        sync.WaitGroup // connection goroutines
}

func (h *hostState) closeStdin() {
	h.stdinOnce.Do(func() { h.stdin.Close() })
}

// emit stamps, persists and broadcasts events.
func (h *hostState) emit(evs []agent.Event) {
	if len(evs) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.emitLocked(evs)
}

func (h *hostState) emitLocked(evs []agent.Event) {
	h.seq.Stamp(evs)
	var buf bytes.Buffer
	urgent := false
	lines := make([][]byte, len(evs))
	for i := range evs {
		b, err := json.Marshal(evs[i])
		if err != nil {
			log.Printf("host: marshal event: %v", err)
			continue
		}
		buf.Write(b)
		buf.WriteByte('\n')
		lines[i] = encode(msg{T: tEvent, Event: &evs[i]})
		h.lastSeq = evs[i].Seq
		switch evs[i].Kind {
		case agent.KindPermissionRequest:
			urgent = true
			id := agent.MustDecode[agent.PermissionRequest](evs[i]).ReqID
			h.pending[id] = evs[i]
			h.idleStopLocked()
		case agent.KindPermissionResolved:
			delete(h.pending, agent.MustDecode[agent.PermissionResolved](evs[i]).ReqID)
		case agent.KindPermissionCancelled:
			delete(h.pending, agent.MustDecode[agent.PermissionCancelled](evs[i]).ReqID)
		case agent.KindRunResult:
			h.idle = true
		}
	}
	if _, err := h.logf.Write(buf.Bytes()); err != nil {
		log.Printf("host: append log: %v", err)
	}
	h.dirty = true
	if urgent {
		h.syncLocked()
	}
	for c := range h.clients {
		for _, l := range lines {
			if l != nil {
				c.push(l)
			}
		}
	}
	h.idleArmLocked()
}

func (h *hostState) syncLocked() {
	if h.dirty {
		_ = h.logf.Sync()
		h.dirty = false
	}
}

func (h *hostState) idleStopLocked() {
	if h.idleT != nil {
		h.idleT.Stop()
		h.idleT = nil
	}
}

// idleArmLocked starts the idle timer when a turn is over and nothing is
// pending.
func (h *hostState) idleArmLocked() {
	if !h.idle || len(h.pending) > 0 || h.stopping != "" || h.idleT != nil {
		return
	}
	h.idleT = time.AfterFunc(h.spec.idleTTL(), func() {
		h.mu.Lock()
		if !h.idle || len(h.pending) > 0 || h.stopping != "" {
			h.idleT = nil
			h.mu.Unlock()
			return
		}
		h.stopping = "idle"
		h.emitLocked([]agent.Event{agent.NewEvent(agent.KindStatus, "", agent.Status{State: "idle_exited"})})
		h.mu.Unlock()
		h.closeStdin()
		h.armKill()
	})
}

func (h *hostState) armKill() {
	time.AfterFunc(5*time.Second, func() {
		if h.proc != nil {
			_ = syscall.Kill(-h.proc.Pid, syscall.SIGKILL)
			_ = h.proc.Kill()
		}
	})
}

func (h *hostState) userActivity() {
	h.mu.Lock()
	h.idle = false
	h.idleStopLocked()
	h.mu.Unlock()
}

// requestStop begins an orderly stop of the child.
func (h *hostState) requestStop(reason string, interrupt bool) {
	h.mu.Lock()
	if h.stopping == "" {
		h.stopping = reason
	}
	h.idleStopLocked()
	h.mu.Unlock()
	if interrupt {
		_ = h.session.Interrupt()
	}
	h.closeStdin()
	h.armKill()
}

func (h *hostState) replay(fromSeq uint64) []agent.Event {
	evs, _ := ReadLog(h.spec.DataDir, h.spec.RunID, fromSeq)
	return evs
}

func (h *hostState) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		h.wg.Add(1)
		go h.handle(c)
	}
}

func (h *hostState) handle(nc net.Conn) {
	defer h.wg.Done()
	cn := newConn(nc)
	go cn.writeLoop()
	defer func() {
		h.mu.Lock()
		delete(h.clients, cn)
		h.mu.Unlock()
		cn.close()
	}()
	sc := bufio.NewReaderSize(nc, 64<<10)
	for {
		line, err := sc.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var m msg
			if jerr := json.Unmarshal(line, &m); jerr != nil {
				cn.push(encode(msg{T: tError, Error: "bad message: " + jerr.Error()}))
			} else {
				h.dispatch(cn, m)
			}
		}
		if err != nil {
			return
		}
	}
}

func (h *hostState) dispatch(cn *conn, m msg) {
	if m.T == tHello {
		h.mu.Lock()
		if cn.hello {
			h.mu.Unlock()
			return
		}
		cn.hello = true
		h.syncLocked()
		from := m.FromSeq
		if from == 0 {
			from = 1
		}
		// Replay under the lock so no live event can slip between replay and
		// registration (no gaps, no duplicates).
		if from <= h.lastSeq {
			for _, e := range h.replay(from) {
				e := e
				cn.push(encode(msg{T: tEvent, Event: &e}))
			}
		}
		// Re-announce unresolved permissions the client has not been sent.
		for _, e := range h.pending {
			if e.Seq < from {
				e := e
				cn.push(encode(msg{T: tEvent, Event: &e}))
			}
		}
		h.clients[cn] = struct{}{}
		h.mu.Unlock()
		return
	}
	h.mu.Lock()
	ok := cn.hello
	h.mu.Unlock()
	if !ok {
		cn.push(encode(msg{T: tError, Error: "hello required first"}))
		return
	}
	fail := func(err error) {
		if err != nil {
			cn.push(encode(msg{T: tError, Error: err.Error()}))
		}
	}
	switch m.T {
	case tSend:
		if m.Msg == nil {
			fail(errors.New("send: missing message"))
			return
		}
		h.userActivity()
		evs, err := h.session.SendUser(*m.Msg)
		h.emit(evs)
		fail(err)
	case tPermission:
		if m.Decision == nil {
			fail(errors.New("permission: missing decision"))
			return
		}
		evs, err := h.session.RespondPermission(m.ReqID, *m.Decision)
		h.emit(evs)
		fail(err)
	case tInterrupt:
		fail(h.session.Interrupt())
	case tSetModel:
		fail(h.session.SetModel(m.Model))
	case tSetMode:
		fail(h.session.SetMode(m.Mode))
	case tShutdown:
		h.requestStop("shutdown", true)
	default:
		fail(fmt.Errorf("unknown message %q", m.T))
	}
}

func lastSeqOf(path string) uint64 {
	evs, _ := readLogFile(path, 0)
	if len(evs) == 0 {
		return 0
	}
	return evs[len(evs)-1].Seq
}

func writeInfo(dataDir string, info HostInfo) error {
	info.Alive = false
	b, _ := json.Marshal(info)
	tmp := infoPath(dataDir, info.RunID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, infoPath(dataDir, info.RunID))
}

func runHost(spec Spec) (retErr error) {
	if spec.RunID == "" || spec.DataDir == "" {
		return errors.New("host: spec needs runID and dataDir")
	}
	prov, err := lookupProvider(spec.Provider)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runsDir(spec.DataDir), 0o755); err != nil {
		return err
	}
	sockPath := SocketPath(spec.DataDir, spec.RunID)
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(sockPath)

	opts := spec.StartOpts
	if opts.RunID == "" {
		opts.RunID = spec.RunID
	}
	cmdSpec, err := prov.Command(opts)
	if err != nil {
		return err
	}

	lp := logPath(spec.DataDir, spec.RunID)
	first := lastSeqOf(lp) + 1
	logf, err := os.OpenFile(lp, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	rawf, err := os.OpenFile(rawPath(spec.DataDir, spec.RunID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer rawf.Close()
	raw := bufio.NewWriterSize(rawf, 64<<10)
	var rawMu sync.Mutex

	cmd := exec.Command(cmdSpec.Path, cmdSpec.Args...)
	cmd.Dir = cmdSpec.Dir
	if cmd.Dir == "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = append(os.Environ(), cmdSpec.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	h := &hostState{
		spec:    spec,
		seq:     &agent.Sequencer{RunID: spec.RunID, FirstSeq: first},
		logf:    logf,
		clients: map[*conn]struct{}{},
		pending: map[string]agent.Event{},
		stdin:   stdin,
	}
	h.session = prov.NewSession(stdin, opts)

	// Listen before spawning so clients can connect immediately.
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("host: listen %s: %w", sockPath, err)
	}
	defer os.Remove(sockPath)
	info := HostInfo{RunID: spec.RunID, PID: os.Getpid(), Socket: sockPath, StartedAt: time.Now()}
	if err := writeInfo(spec.DataDir, info); err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		code := -1
		h.emit(append([]agent.Event{agent.NewEvent(agent.KindError, "", agent.ErrorData{Code: agent.ErrProcessExit, Message: err.Error()})}, h.session.Closed(code, err.Error())...))
		retErr = err
		info.ExitCode = &code
		info.ExitedAt = time.Now()
		_ = writeInfo(spec.DataDir, info)
		return retErr
	}
	h.proc = cmd.Process

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)

	go h.serve(ln)
	h.emit([]agent.Event{agent.NewEvent(agent.KindRunStarted, "", agent.RunStarted{
		Provider: spec.Provider, Model: opts.Model, Mode: opts.Mode, Cwd: cmd.Dir, ResumeID: opts.ResumeID,
	})})

	tail := &tailBuf{}
	errDone := make(chan struct{})
	go func() {
		defer close(errDone)
		_, _ = io.Copy(tail, stderr)
	}()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		r := bufio.NewReaderSize(stdout, 256<<10)
		for {
			line, err := r.ReadBytes('\n')
			if t := bytes.TrimRight(line, "\r\n"); len(t) > 0 {
				rawMu.Lock()
				raw.Write(t)
				raw.WriteByte('\n')
				rawMu.Unlock()
				evs, ferr := h.session.Feed(t)
				if ferr != nil {
					log.Printf("host: feed: %v", ferr)
				}
				h.emit(evs)
			}
			if err != nil {
				return
			}
		}
	}()

	// Handshake (after the reader is running).
	startCtx, cancelStart := context.WithCancel(context.Background())
	go func() {
		if err := h.session.Start(startCtx); err != nil && startCtx.Err() == nil {
			h.emit([]agent.Event{agent.NewEvent(agent.KindError, "", agent.ErrorData{Code: agent.ErrProtocol, Message: "start: " + err.Error()})})
		}
	}()

	// Periodic flush.
	tickDone := make(chan struct{})
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				h.mu.Lock()
				h.syncLocked()
				h.mu.Unlock()
				rawMu.Lock()
				raw.Flush()
				rawMu.Unlock()
			case <-tickDone:
				return
			}
		}
	}()

	waitCh := make(chan *os.ProcessState, 1)
	go func() {
		ps, _ := cmd.Process.Wait()
		waitCh <- ps
	}()

	var ps *os.ProcessState
loop:
	for {
		select {
		case <-sigs:
			go h.requestStop("signal", true)
		case ps = <-waitCh:
			break loop
		}
	}
	cancelStart()
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
	}
	stdout.Close()
	select {
	case <-errDone:
	case <-time.After(time.Second):
	}
	code := -1
	if ps != nil {
		code = ps.ExitCode()
	}
	h.closeStdin()
	stderrTail := tail.String()
	h.mu.Lock()
	reason := h.stopping
	h.mu.Unlock()
	if reason == "" {
		_ = os.WriteFile(filepath.Join(runsDir(spec.DataDir), spec.RunID+".stderr"), []byte(stderrTail), 0o644)
	}
	closed := h.session.Closed(code, stderrTail)
	h.mu.Lock()
	h.idleStopLocked()
	h.emitLocked(closed)
	h.syncLocked()
	h.stopping = "done"
	ln.Close()
	_ = os.Remove(sockPath)
	exitLine := encode(msg{T: tExit, Code: &code})
	for c := range h.clients {
		c.push(exitLine)
		c.finish()
	}
	h.mu.Unlock()

	rawMu.Lock()
	raw.Flush()
	rawMu.Unlock()
	close(tickDone)

	info.ExitCode = &code
	info.ExitedAt = time.Now()
	_ = writeInfo(spec.DataDir, info)

	// Let queues drain, then tear down.
	for deadline := time.Now().Add(1500 * time.Millisecond); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		h.mu.Lock()
		n := len(h.clients)
		h.mu.Unlock()
		if n == 0 {
			break
		}
	}
	h.mu.Lock()
	for c := range h.clients {
		c.close()
	}
	h.mu.Unlock()
	return nil
}
