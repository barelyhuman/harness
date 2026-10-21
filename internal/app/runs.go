package app

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/git"
	"github.com/sid/harness/internal/host"
	"github.com/sid/harness/internal/stream"
	"github.com/sid/harness/internal/transcript"
	"github.com/sid/harness/internal/workspace"
)

// Session statuses pushed on the `sessions` channel.
const (
	StatusRunning       = "running"
	StatusNeedsApproval = "needs_approval"
	StatusIdle          = "idle"
	StatusError         = "error"
	StatusArchived      = "archived"
)

const (
	defaultFlushEvery = 16 * time.Millisecond
	offsetEvery       = 250 * time.Millisecond
	dirtyTTL          = 3 * time.Second
)

// sink is where the supervisor publishes to bus channels. The production
// implementation (busSink) opens server channels lazily, caches them and
// reopens closed ones; tests inject a recorder.
type sink interface {
	Ensure(channel string)
	Send(channel string, v any)
}

type busSink struct {
	bus   *stream.Bus
	mu    sync.Mutex
	chans map[string]*stream.Channel
}

func newBusSink(b *stream.Bus) *busSink { return &busSink{bus: b, chans: map[string]*stream.Channel{}} }

func (b *busSink) get(name string) *stream.Channel {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := b.chans[name]
	if ch == nil {
		ch = b.bus.Open(name)
		b.chans[name] = ch
	}
	return ch
}

func (b *busSink) Ensure(name string) { b.get(name) }

func (b *busSink) Send(name string, v any) {
	ch := b.get(name)
	err := ch.SendJSON(v)
	if errors.Is(err, stream.ErrClosed) {
		b.mu.Lock()
		if b.chans[name] == ch {
			delete(b.chans, name)
		}
		b.mu.Unlock()
		_ = b.get(name).SendJSON(v)
	}
}

// SessionRow is one entry of the `sessions` channel payload.
type SessionRow struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Title     string `json:"title"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Mode      string `json:"mode"`
	Effort    string `json:"effort"`
	Status    string `json:"status"`
	Branch    string `json:"branch"`
	Dirty     int    `json:"dirty"`
	PRURL     string `json:"prUrl"`
	PRState   string `json:"prState"`
}

// runState is the event-derived state of a session's current turn.
type runState struct {
	busy      bool
	errored   bool
	perms     map[string]bool
	toolPaths map[string]string
}

func newRunState() runState {
	return runState{perms: map[string]bool{}, toolPaths: map[string]string{}}
}

func (r *runState) status() string {
	switch {
	case len(r.perms) > 0:
		return StatusNeedsApproval
	case r.busy:
		return StatusRunning
	case r.errored:
		return StatusError
	}
	return StatusIdle
}

// observe folds one event into the state. It returns a tool path to touch
// (for a successful tool_result) and the provider session id from session_init.
func (r *runState) observe(ev agent.Event) (touchPath, psid string) {
	switch ev.Kind {
	case agent.KindUserMessage:
		r.busy, r.errored = true, false
	case agent.KindStatus:
		if agent.MustDecode[agent.Status](ev).State == "requesting" {
			r.busy = true
		}
	case agent.KindSessionInit:
		psid = agent.MustDecode[agent.SessionInit](ev).ProviderSessionID
	case agent.KindPermissionRequest:
		r.perms[agent.MustDecode[agent.PermissionRequest](ev).ReqID] = true
	case agent.KindPlanProposal:
		r.perms[agent.MustDecode[agent.PlanProposal](ev).ReqID] = true
	case agent.KindPermissionResolved:
		delete(r.perms, agent.MustDecode[agent.PermissionResolved](ev).ReqID)
	case agent.KindPermissionCancelled:
		delete(r.perms, agent.MustDecode[agent.PermissionCancelled](ev).ReqID)
	case agent.KindRunResult:
		r.busy = false
		r.perms = map[string]bool{}
		r.errored = agent.MustDecode[agent.RunResult](ev).Status == agent.ResultError
	case agent.KindError:
		if agent.MustDecode[agent.ErrorData](ev).Fatal {
			r.busy, r.errored = false, true
			r.perms = map[string]bool{}
		}
	case agent.KindToolStart:
		d := agent.MustDecode[agent.ToolStart](ev)
		if p := inputPath(d.Input); p != "" {
			r.toolPaths[d.ID] = p
		}
	case agent.KindToolInputDone:
		d := agent.MustDecode[agent.ToolInputDone](ev)
		if p := inputPath(d.Input); p != "" {
			r.toolPaths[d.ID] = p
		}
	case agent.KindToolResult:
		d := agent.MustDecode[agent.ToolResult](ev)
		if d.OK {
			touchPath = r.toolPaths[d.ID]
		}
		delete(r.toolPaths, d.ID)
	}
	return
}

func inputPath(in json.RawMessage) string {
	if len(in) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(in, &m) != nil {
		return ""
	}
	for _, k := range []string{"file_path", "notebook_path", "path"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// supervisor runs one goroutine per active session: it owns the host client,
// the transcript builder, the queue of held messages and the status.
type supervisor struct {
	dataDir  string
	db       *sql.DB
	sessions *workspace.Sessions
	projects *workspace.Projects
	out      sink
	touch    func(root, path string) bool

	flushEvery time.Duration

	mu     sync.Mutex
	runs   map[string]*sessRun
	closed bool
	wg     sync.WaitGroup // consume goroutines + recovery

	pushMu    sync.Mutex
	pushTimer *time.Timer
	dirtyMu   sync.Mutex
	dirty     map[string]dirtyEnt
}

type dirtyEnt struct {
	n  int
	at time.Time
}

func newSupervisor(dataDir string, db *sql.DB, ss *workspace.Sessions, ps *workspace.Projects, out sink,
	touch func(root, path string) bool) *supervisor {
	if touch == nil {
		touch = func(string, string) bool { return false }
	}
	return &supervisor{dataDir: dataDir, db: db, sessions: ss, projects: ps, out: out, touch: touch,
		flushEvery: defaultFlushEvery, runs: map[string]*sessRun{}, dirty: map[string]dirtyEnt{}}
}

func newRunID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "r" + hex.EncodeToString(b)
}

type sessRun struct {
	s  *supervisor
	id string

	launchMu sync.Mutex // serialises host launches
	sendMu   sync.Mutex // serialises patch delivery (ordering)

	mu       sync.Mutex
	loaded   bool
	b        *transcript.Builder
	replayed runState // state derived from logs at load; adopted when a live host is attached
	st       runState
	status   string
	client   *host.Client
	runID    string
	lastSeq  uint64
	queue    []agent.UserMessage
	pending  []transcript.Patch
	timer    *time.Timer
	offsetAt time.Time
	root     string
}

func (s *supervisor) get(id string) *sessRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	sr := s.runs[id]
	if sr == nil {
		sr = &sessRun{s: s, id: id, b: transcript.NewBuilder(), st: newRunState()}
		s.runs[id] = sr
	}
	return sr
}

// ---- runs table ----

type runRow struct{ ID, Status string }

func (s *supervisor) runRows(sessionID string) ([]runRow, error) {
	rows, err := s.db.Query(`SELECT id, status FROM runs WHERE session_id = ? ORDER BY started, rowid`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runRow
	for rows.Next() {
		var r runRow
		if err := rows.Scan(&r.ID, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *supervisor) endRun(runID, status string) {
	_, _ = s.db.Exec(`UPDATE runs SET status = ?, ended = ? WHERE id = ?`, status, time.Now().UnixMilli(), runID)
}

func (s *supervisor) setOffset(runID string, seq uint64) {
	_, _ = s.db.Exec(`UPDATE runs SET "offset" = ? WHERE id = ?`, seq, runID)
}

// allEvents returns every logged event of a session, runs in order.
func (s *supervisor) allEvents(sessionID string) ([]agent.Event, error) {
	rr, err := s.runRows(sessionID)
	if err != nil {
		return nil, err
	}
	var out []agent.Event
	for _, r := range rr {
		evs, err := host.ReadLog(s.dataDir, r.ID, 0)
		if err != nil && len(evs) == 0 {
			continue // run never wrote a log
		}
		out = append(out, evs...)
	}
	return out, nil
}

// load rebuilds the transcript from every run's log, in order. Idempotent.
func (sr *sessRun) load() error {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.loaded {
		return nil
	}
	rr, err := sr.s.runRows(sr.id)
	if err != nil {
		return err
	}
	rs := newRunState()
	for _, r := range rr {
		evs, err := host.ReadLog(sr.s.dataDir, r.ID, 0)
		if err != nil && len(evs) == 0 {
			continue
		}
		var last uint64
		for _, ev := range evs {
			rs.observe(ev)
			sr.b.Apply(ev)
			last = ev.Seq
		}
		sr.runID, sr.lastSeq = r.ID, last
	}
	sr.replayed = rs
	if sess, err := sr.s.sessions.Get(sr.id); err == nil {
		sr.status = sess.Status
	}
	sr.loaded = true
	return nil
}

// ---- event handling ----

func isDelta(p transcript.Patch) bool {
	return p.Op == transcript.OpUpdate && p.TextDelta != "" && p.Block == nil
}

// enqueue adds patches to the pending frame (sr.mu held), merging consecutive
// text deltas of one block. It reports whether a non-delta patch is pending
// (flush immediately to preserve order and latency).
func (sr *sessRun) enqueue(ps []transcript.Patch) (now bool) {
	for _, p := range ps {
		if n := len(sr.pending); isDelta(p) && n > 0 && isDelta(sr.pending[n-1]) &&
			sr.pending[n-1].BlockID == p.BlockID && sr.pending[n-1].ParentID == p.ParentID {
			sr.pending[n-1].TextDelta += p.TextDelta
			continue
		}
		sr.pending = append(sr.pending, p)
		if !isDelta(p) {
			now = true
		}
	}
	return now
}

func (sr *sessRun) flush() {
	sr.sendMu.Lock()
	defer sr.sendMu.Unlock()
	sr.mu.Lock()
	ps := sr.pending
	sr.pending = nil
	if sr.timer != nil {
		sr.timer.Stop()
		sr.timer = nil
	}
	sr.mu.Unlock()
	for _, p := range ps {
		sr.s.out.Send("blocks/"+sr.id, p)
	}
}

// publish flushes now or arms the ≤16ms coalescing timer.
func (sr *sessRun) publish(now bool) {
	if now {
		sr.flush()
		return
	}
	sr.mu.Lock()
	if sr.timer == nil && len(sr.pending) > 0 {
		sr.timer = time.AfterFunc(sr.s.flushEvery, sr.flush)
	}
	sr.mu.Unlock()
}

// recalcLocked recomputes the status; reports whether it changed.
func (sr *sessRun) recalcLocked() (string, bool) {
	ns := sr.st.status()
	if ns == sr.status {
		return ns, false
	}
	sr.status = ns
	return ns, true
}

func (sr *sessRun) statusChanged(ns string) {
	if sr.s.isClosed() {
		return
	}
	_ = sr.s.sessions.SetStatus(sr.id, ns)
	sr.s.schedulePush()
}

func (s *supervisor) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (sr *sessRun) handle(c *host.Client, runID string, ev agent.Event) {
	sr.mu.Lock()
	if sr.client != c || (ev.Seq != 0 && ev.Seq <= sr.lastSeq) {
		sr.mu.Unlock()
		return
	}
	if ev.Seq != 0 {
		sr.lastSeq = ev.Seq
	}
	touchPath, psid := sr.st.observe(ev)
	now := sr.enqueue(sr.b.Apply(ev))
	ns, changed := sr.recalcLocked()
	root := sr.root
	seq := sr.lastSeq
	saveOff := ev.Kind != agent.KindTextDelta && ev.Kind != agent.KindThinkingDelta || time.Since(sr.offsetAt) > offsetEvery
	if saveOff {
		sr.offsetAt = time.Now()
	}
	drain := ev.Kind == agent.KindRunResult && len(sr.queue) > 0
	sr.mu.Unlock()

	sr.publish(now)
	if sr.s.isClosed() {
		return
	}
	if saveOff {
		sr.s.setOffset(runID, seq)
	}
	if psid != "" {
		if sess, err := sr.s.sessions.Get(sr.id); err == nil && sess.ProviderSessionID != psid {
			_ = sr.s.sessions.SetProviderSessionID(sr.id, psid)
		}
	}
	if touchPath != "" {
		sr.s.invalidateDirty()
	}
	if touchPath != "" && root != "" {
		if !filepath.IsAbs(touchPath) {
			touchPath = filepath.Join(root, touchPath)
		}
		sr.s.touch(root, touchPath)
	}
	if changed {
		sr.statusChanged(ns)
	}
	if drain {
		sr.drainQueue()
	}
}

// consume pumps one client's events until the connection ends.
func (sr *sessRun) consume(c *host.Client, runID string) {
	defer sr.s.wg.Done()
	for ev := range c.Events() {
		sr.handle(c, runID, ev)
	}
	sr.ended(c, runID)
}

func (sr *sessRun) ended(c *host.Client, runID string) {
	if sr.s.isClosed() {
		return
	}
	code, exited := c.ExitCode()
	clean := exited && code == 0
	if clean {
		sr.s.endRun(runID, "exited")
	} else {
		sr.s.endRun(runID, "crashed")
	}
	sr.mu.Lock()
	if sr.client != c {
		sr.mu.Unlock()
		return
	}
	sr.client = nil
	if !clean && (sr.st.busy || len(sr.st.perms) > 0) {
		sr.st.errored = true
	}
	sr.st.busy = false
	sr.st.perms = map[string]bool{}
	var now bool
	if !clean && sr.st.errored {
		msg := "agent host exited unexpectedly"
		if e := c.LastError(); e != "" {
			msg += ": " + e
		}
		now = sr.enqueue(sr.b.Apply(agent.NewEvent(agent.KindError, "",
			agent.ErrorData{Code: agent.ErrProcessExit, Message: msg, Fatal: true})))
	}
	ns, changed := sr.recalcLocked()
	hasQueue := len(sr.queue) > 0
	sr.mu.Unlock()
	sr.publish(now)
	if changed {
		sr.statusChanged(ns)
	}
	if hasQueue {
		sr.drainQueue()
	}
}

// ---- sending ----

func clientAlive(c *host.Client) bool {
	if c == nil {
		return false
	}
	select {
	case <-c.Done():
		return false
	default:
	}
	_, exited := c.ExitCode()
	return !exited
}

func (sr *sessRun) liveClient() *host.Client {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if clientAlive(sr.client) {
		return sr.client
	}
	return nil
}

// drainQueue sends the oldest held message after a finished turn.
func (sr *sessRun) drainQueue() {
	sr.mu.Lock()
	if len(sr.queue) == 0 || sr.st.busy {
		sr.mu.Unlock()
		return
	}
	um := sr.queue[0]
	sr.queue = sr.queue[1:]
	sr.st.busy, sr.st.errored = true, false
	ns, changed := sr.recalcLocked()
	sr.mu.Unlock()
	if changed {
		sr.statusChanged(ns)
	}
	if err := sr.deliver(um); err != nil {
		log.Printf("harness: queued message for %s: %v", sr.id, err)
	}
}

// deliver sends um to the live host, relaunching with resume when it is gone.
func (sr *sessRun) deliver(um agent.UserMessage) error {
	if c := sr.liveClient(); c != nil {
		if err := c.Send(um); err == nil {
			return nil
		}
		sr.dropClient(c)
	}
	err := sr.launch(um)
	if err != nil {
		sr.fail(err)
	}
	return err
}

func (sr *sessRun) dropClient(c *host.Client) {
	sr.mu.Lock()
	if sr.client == c {
		sr.client = nil
	}
	sr.mu.Unlock()
	_ = c.Close()
}

// fail records a launch failure as an error block and status.
func (sr *sessRun) fail(err error) {
	sr.mu.Lock()
	sr.st.busy, sr.st.errored = false, true
	now := sr.enqueue(sr.b.Apply(agent.NewEvent(agent.KindError, "",
		agent.ErrorData{Code: "launch_failed", Message: err.Error(), Fatal: true})))
	ns, changed := sr.recalcLocked()
	sr.mu.Unlock()
	sr.publish(now)
	if changed {
		sr.statusChanged(ns)
	}
}

// launch starts a new host (new run id; the transcript builder continues) with
// ResumeID = the session's provider session id, then sends um.
func (sr *sessRun) launch(um agent.UserMessage) error {
	sr.launchMu.Lock()
	defer sr.launchMu.Unlock()
	if c := sr.liveClient(); c != nil { // another caller launched meanwhile
		return c.Send(um)
	}
	sess, err := sr.s.sessions.Get(sr.id)
	if err != nil {
		return err
	}
	sc, err := sr.s.sessions.Scope(sr.id)
	if err != nil {
		return err
	}
	runID := newRunID()
	spec := host.Spec{RunID: runID, SessionID: sr.id, Provider: sess.Provider, DataDir: sr.s.dataDir,
		StartOpts: agent.StartOpts{RunID: runID, Cwd: sc.Root, Model: localModelID(sess.Provider, sess.Model),
			Mode: sess.Mode, Effort: sess.Effort, ResumeID: sess.ProviderSessionID}}
	c, err := host.Launch(spec)
	if err != nil {
		return fmt.Errorf("launch agent host: %w", err)
	}
	var pid int
	for _, in := range host.Scan(sr.s.dataDir) {
		if in.RunID == runID {
			pid = in.PID
		}
	}
	_, err = sr.s.db.Exec(`INSERT INTO runs (id, session_id, host_pid, socket, jsonl_path, "offset", status, started)
VALUES (?,?,?,?,?,0,'running',?)`, runID, sr.id, pid, host.SocketPath(sr.s.dataDir, runID),
		filepath.Join(sr.s.dataDir, "runs", runID+".jsonl"), time.Now().UnixMilli())
	if err != nil {
		_ = c.Shutdown()
		_ = c.Close()
		return err
	}
	sr.mu.Lock()
	sr.client, sr.runID, sr.lastSeq, sr.root = c, runID, 0, sc.Root
	sr.mu.Unlock()
	sr.s.wg.Add(1)
	go sr.consume(c, runID)
	return c.Send(um)
}

// localModelID strips a "<provider>/" prefix from a stored model key.
func localModelID(provider, model string) string {
	if provider != "" && strings.HasPrefix(model, provider+"/") {
		return model[len(provider)+1:]
	}
	return model
}

// ---- startup / shutdown ----

// Recover re-attaches to live hosts after an app restart and marks dead runs
// that still say "running" as crashed.
func (s *supervisor) Recover() {
	rows, err := s.db.Query(`SELECT id, session_id, "offset" FROM runs WHERE status = 'running' ORDER BY started, rowid`)
	if err != nil {
		return
	}
	type rr struct {
		id, sid string
		off     uint64
	}
	var list []rr
	for rows.Next() {
		var r rr
		if rows.Scan(&r.id, &r.sid, &r.off) == nil {
			list = append(list, r)
		}
	}
	rows.Close()
	infos := map[string]host.HostInfo{}
	for _, in := range host.Scan(s.dataDir) {
		infos[in.RunID] = in
	}
	attached := map[string]bool{}
	for _, r := range list {
		if s.isClosed() {
			return
		}
		sr := s.get(r.sid)
		if sr.load() != nil {
			continue
		}
		info, ok := infos[r.id]
		if ok && info.Alive {
			sr.mu.Lock()
			from := sr.lastSeq + 1
			if sr.runID != r.id {
				from = r.off + 1
				sr.lastSeq = r.off
			}
			sr.mu.Unlock()
			if c, err := host.Attach(s.dataDir, r.id, from); err == nil {
				sess, _ := s.sessions.Get(r.sid)
				root := ""
				if sc, err := s.sessions.Scope(r.sid); err == nil {
					root = sc.Root
				}
				_ = sess
				sr.mu.Lock()
				sr.client, sr.runID, sr.root = c, r.id, root
				sr.st = sr.replayed
				ns, changed := sr.recalcLocked()
				sr.mu.Unlock()
				s.wg.Add(1)
				go sr.consume(c, r.id)
				if changed {
					sr.statusChanged(ns)
				}
				attached[r.sid] = true
				continue
			}
		}
		if ok && info.ExitCode != nil && *info.ExitCode == 0 {
			s.endRun(r.id, "exited")
			continue
		}
		s.endRun(r.id, "crashed")
		sr.mu.Lock()
		sr.st.errored, sr.st.busy = true, false
		sr.st.perms = map[string]bool{}
		ns, changed := sr.recalcLocked()
		sr.mu.Unlock()
		if changed {
			sr.statusChanged(ns)
		}
	}
	// Sessions that claim to be busy without a live host are idle now.
	srows, err := s.db.Query(`SELECT id FROM sessions WHERE status IN ('running','needs_approval')`)
	if err == nil {
		var ids []string
		for srows.Next() {
			var id string
			if srows.Scan(&id) == nil && !attached[id] {
				ids = append(ids, id)
			}
		}
		srows.Close()
		for _, id := range ids {
			_ = s.sessions.SetStatus(id, StatusIdle)
		}
	}
	s.schedulePush()
}

// Close drops the host connections (the hosts keep running) and waits for the
// consumer goroutines.
func (s *supervisor) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	var clients []*host.Client
	for _, sr := range s.runs {
		sr.mu.Lock()
		if sr.client != nil {
			clients = append(clients, sr.client)
		}
		if sr.timer != nil {
			sr.timer.Stop()
		}
		sr.mu.Unlock()
	}
	s.mu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	s.pushMu.Lock()
	if s.pushTimer != nil {
		s.pushTimer.Stop()
	}
	s.pushMu.Unlock()
}

// ---- sessions channel ----

// schedulePush debounces a full-list push on the `sessions` channel.
func (s *supervisor) schedulePush() {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if s.pushTimer != nil || s.isClosed() {
		return
	}
	s.pushTimer = time.AfterFunc(30*time.Millisecond, func() {
		s.pushMu.Lock()
		s.pushTimer = nil
		s.pushMu.Unlock()
		s.pushNow()
	})
}

func (s *supervisor) dirtyCount(root string) int {
	s.dirtyMu.Lock()
	e, ok := s.dirty[root]
	s.dirtyMu.Unlock()
	if ok && time.Since(e.at) < dirtyTTL {
		return e.n
	}
	n := 0
	if st, err := git.StatusOf(root); err == nil {
		n = len(st.Paths())
	}
	s.dirtyMu.Lock()
	s.dirty[root] = dirtyEnt{n, time.Now()}
	s.dirtyMu.Unlock()
	return n
}

// Rows builds the `sessions` payload: every session of every project.
func (s *supervisor) Rows() []SessionRow {
	out := []SessionRow{}
	ps, err := s.projects.List()
	if err != nil {
		return out
	}
	for _, p := range ps {
		list, err := s.sessions.List(p.ID, true)
		if err != nil {
			continue
		}
		for _, x := range list {
			st := x.Status
			if x.ArchivedAt != nil {
				st = StatusArchived
			}
			root := p.Path
			if x.ScopeKind == workspace.ScopeWorktree {
				root = x.WorktreePath
			}
			d := 0
			if x.ArchivedAt == nil {
				d = s.dirtyCount(root)
			}
			out = append(out, SessionRow{ID: x.ID, ProjectID: x.ProjectID, Title: x.Title, Provider: x.Provider,
				Model: x.Model, Mode: x.Mode, Effort: x.Effort, Status: st, Branch: x.Branch, Dirty: d,
				PRURL: x.PRURL, PRState: x.PRState})
		}
	}
	return out
}

func (s *supervisor) pushNow() {
	if s.isClosed() {
		return
	}
	s.out.Send("sessions", s.Rows())
}

// invalidateDirty forgets cached dirty counts (after the agent edits files).
func (s *supervisor) invalidateDirty() {
	s.dirtyMu.Lock()
	s.dirty = map[string]dirtyEnt{}
	s.dirtyMu.Unlock()
}

// release stops the session's host (archive/delete) and forgets its state.
func (s *supervisor) release(id string) {
	s.mu.Lock()
	sr := s.runs[id]
	delete(s.runs, id)
	s.mu.Unlock()
	if sr == nil {
		return
	}
	sr.mu.Lock()
	c := sr.client
	sr.client = nil
	if sr.timer != nil {
		sr.timer.Stop()
	}
	sr.mu.Unlock()
	if c != nil {
		_ = c.Shutdown()
		_ = c.Close()
	}
}
