package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/host"
	"github.com/sid/harness/internal/store"
	"github.com/sid/harness/internal/transcript"
	"github.com/sid/harness/internal/workspace"
)

// ---- fake providers: the test binary doubles as the CLI ----

type fakeProvider struct{ id string }

func (p fakeProvider) ID() string { return p.id }
func (fakeProvider) Detect(context.Context) (agent.Install, error) {
	return agent.Install{Found: true}, nil
}
func (fakeProvider) Models(context.Context) ([]agent.Model, error) { return nil, nil }
func (fakeProvider) Command(o agent.StartOpts) (agent.Cmd, error) {
	exe, _ := os.Executable()
	return agent.Cmd{Path: exe, Env: append([]string{"APP_FAKE_CLI=1", "FAKE_RESUME=" + o.ResumeID}, o.Env...), Dir: o.Cwd}, nil
}
func (fakeProvider) NewSession(w io.Writer, o agent.StartOpts) agent.Session {
	return &fakeSession{w: w}
}

type fakeSession struct{ w io.Writer }

func (s *fakeSession) Feed(line []byte) ([]agent.Event, error) {
	var m struct{ T, V, ID string }
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	switch m.T {
	case "init":
		return []agent.Event{agent.NewEvent(agent.KindSessionInit, "", agent.SessionInit{ProviderSessionID: m.ID})}, nil
	case "text":
		return []agent.Event{agent.NewEvent(agent.KindTextDelta, "", agent.BlockDelta{MessageID: "m", Text: m.V})}, nil
	case "perm":
		return []agent.Event{agent.NewEvent(agent.KindPermissionRequest, "", agent.PermissionRequest{ReqID: m.ID, Tool: "Bash"})}, nil
	case "plan":
		return []agent.Event{agent.NewEvent(agent.KindPlanProposal, "", agent.PlanProposal{ReqID: m.ID, Text: m.V})}, nil
	case "mode":
		return []agent.Event{agent.NewEvent(agent.KindModeChanged, "", agent.ModeChanged{Mode: m.V})}, nil
	case "result":
		return []agent.Event{agent.NewEvent(agent.KindRunResult, "", agent.RunResult{Status: "success"})}, nil
	}
	return nil, nil
}
func (s *fakeSession) Start(context.Context) error { return nil }
func (s *fakeSession) SendUser(m agent.UserMessage) ([]agent.Event, error) {
	b, _ := json.Marshal(map[string]string{"user": m.Text})
	s.w.Write(append(b, '\n'))
	return []agent.Event{agent.NewEvent(agent.KindUserMessage, "", agent.UserMessageData{Text: m.Text})}, nil
}
func (s *fakeSession) RespondPermission(id string, d agent.Decision) ([]agent.Event, error) {
	b, _ := json.Marshal(map[string]string{"allow": id, "behavior": string(d.Behavior)})
	s.w.Write(append(b, '\n'))
	return []agent.Event{agent.NewEvent(agent.KindPermissionResolved, "", agent.PermissionResolved{ReqID: id, Behavior: d.Behavior})}, nil
}
func (s *fakeSession) Interrupt() error      { return nil }
func (s *fakeSession) SetModel(string) error { return nil }
func (s *fakeSession) SetMode(string) error  { return nil }
func (s *fakeSession) Closed(code int, tail string) []agent.Event {
	if code != 0 {
		return []agent.Event{agent.NewEvent(agent.KindError, "", agent.ErrorData{Code: agent.ErrProcessExit, Message: fmt.Sprintf("exit %d", code)})}
	}
	return nil
}

func printJSON(m map[string]string) { b, _ := json.Marshal(m); fmt.Println(string(b)) }

// fakeCLI: on {"user":X}: init (psid = FAKE_RESUME or "psid-1"); X=="ask" requests
// a permission and waits for {"allow":..}; X=="plan" proposes a plan, waits
// for the answer and reports mode "default" when allowed; X=="slow" delays
// 400ms; then echoes "echo X" and a result.
func fakeCLI() {
	dec := json.NewDecoder(os.Stdin)
	for {
		var m map[string]string
		if dec.Decode(&m) != nil {
			os.Exit(0)
		}
		u, ok := m["user"]
		if !ok {
			continue
		}
		id := os.Getenv("FAKE_RESUME")
		if id == "" {
			id = "psid-1"
		}
		printJSON(map[string]string{"t": "init", "id": id})
		switch u {
		case "plan":
			printJSON(map[string]string{"t": "plan", "id": "req1", "v": "# The plan\n\n1. do it"})
			for {
				var a map[string]string
				if dec.Decode(&a) != nil {
					os.Exit(0)
				}
				if a["allow"] != "" {
					if a["behavior"] == "allow" {
						printJSON(map[string]string{"t": "mode", "v": "default"})
					}
					break
				}
			}
		case "ask":
			printJSON(map[string]string{"t": "perm", "id": "req1"})
			for {
				var a map[string]string
				if dec.Decode(&a) != nil {
					os.Exit(0)
				}
				if a["allow"] != "" {
					break
				}
			}
		case "slow":
			time.Sleep(400 * time.Millisecond)
		}
		printJSON(map[string]string{"t": "text", "v": "echo " + u})
		printJSON(map[string]string{"t": "result"})
	}
}

func registerFakes() {
	host.RegisterProvider("fake", func() agent.Provider { return fakeProvider{"fake"} })
	host.RegisterProvider("fake2", func() agent.Provider { return fakeProvider{"fake2"} })
}

func TestMain(m *testing.M) {
	if os.Getenv("APP_FAKE_CLI") == "1" {
		fakeCLI()
		return
	}
	registerFakes()
	for _, a := range os.Args {
		if a == "--agent-host" {
			if err := host.Main(os.Args); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// ---- harness ----

type rec struct {
	ch string
	v  json.RawMessage
}
type recorder struct {
	mu   sync.Mutex
	msgs []rec
}

func (r *recorder) Ensure(string) {}
func (r *recorder) Send(ch string, v any) {
	b, _ := json.Marshal(v)
	r.mu.Lock()
	r.msgs = append(r.msgs, rec{ch, b})
	r.mu.Unlock()
}
func (r *recorder) patches(session string) []transcript.Patch {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []transcript.Patch
	for _, m := range r.msgs {
		if m.ch == "blocks/"+session {
			var p transcript.Patch
			json.Unmarshal(m.v, &p)
			out = append(out, p)
		}
	}
	return out
}
func (r *recorder) statuses(session string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.msgs {
		if m.ch != "sessions" {
			continue
		}
		var rows []SessionRow
		json.Unmarshal(m.v, &rows)
		for _, row := range rows {
			if row.ID == session && (len(out) == 0 || out[len(out)-1] != row.Status) {
				out = append(out, row.Status)
			}
		}
	}
	return out
}

type env struct {
	dir, repo string
	st        *store.Store
	rec       *recorder
	sup       *supervisor
	svc       *SessionService
	proj      workspace.Project
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ha")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	for _, c := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		cmd := exec.Command("git", c...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out))
		}
	}
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	for _, c := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", c...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out))
		}
	}
	st, err := store.Open(filepath.Join(dir, "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{dir: dir, repo: repo, st: st, rec: &recorder{}}
	ps := workspace.NewProjects(st.DB)
	e.proj, err = ps.Add(repo)
	if err != nil {
		t.Fatal(err)
	}
	e.sup = e.newSup(t)
	reg := agent.NewModelRegistry()
	reg.SetProviderModels("fake", []agent.Model{{ID: "m1", Name: "M1"}, {ID: "m2"}})
	reg.SetProviderModels("fake2", []agent.Model{{ID: "x1"}})
	e.svc = &SessionService{sessions: workspace.NewSessions(st.DB), projects: ps, db: st.DB, sup: e.sup, models: reg}
	e.sup.roles = e.svc.agentRoles
	return e
}

func (e *env) newSup(t *testing.T) *supervisor {
	s := newSupervisor(filepath.Join(e.dir, "data"), e.st.DB, workspace.NewSessions(e.st.DB), workspace.NewProjects(e.st.DB), e.rec, nil)
	t.Cleanup(func() {
		// stop hosts of this test
		for id, sr := range s.runs {
			_ = id
			if c := sr.liveClient(); c != nil {
				c.Shutdown()
			}
		}
		s.Close()
	})
	return s
}

func (e *env) session(t *testing.T, provider, model, scope string) workspace.Session {
	t.Helper()
	s, err := e.svc.CreateSession(e.proj.ID, CreateSessionOpts{Title: "t " + provider, Provider: provider, Model: model, ScopeKind: scope})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func texts(bs []transcript.Block) string {
	var sb strings.Builder
	for _, b := range bs {
		sb.WriteString(string(b.Type) + ":" + b.Text + "|")
	}
	return sb.String()
}

func (e *env) transcript(t *testing.T, id string) string {
	bs, err := e.svc.Transcript(id)
	if err != nil {
		t.Fatal(err)
	}
	return texts(bs)
}

func hasStatus(e *env, id, want string) bool {
	for _, s := range e.rec.statuses(id) {
		if s == want {
			return true
		}
	}
	return false
}

func (e *env) dbStatus(id string) string {
	s, _ := e.svc.sessions.Get(id)
	return s.Status
}

// ---- tests ----

func TestSendLaunchesHostAndPatches(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", "")
	if err := e.svc.Send(s.ID, SendMessage{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "transcript", func() bool { return strings.Contains(e.transcript(t, s.ID), "assistant_text:echo hello") })
	waitFor(t, "idle", func() bool { return e.dbStatus(s.ID) == StatusIdle })
	// patches replay (via Reduce) to the same blocks as the snapshot
	var blocks []transcript.Block
	waitFor(t, "patches", func() bool {
		blocks = nil
		for _, p := range e.rec.patches(s.ID) {
			blocks = transcript.Reduce(blocks, p)
		}
		return strings.Contains(texts(blocks), "echo hello")
	})
	if texts(blocks) != e.transcript(t, s.ID) {
		t.Fatalf("patch replay %q != snapshot %q", texts(blocks), e.transcript(t, s.ID))
	}
	sess, _ := e.svc.sessions.Get(s.ID)
	if sess.ProviderSessionID != "psid-1" {
		t.Fatalf("provider session id %q", sess.ProviderSessionID)
	}
	var runs int
	var status string
	var off int
	e.st.DB.QueryRow(`SELECT COUNT(*), MAX(status), MAX("offset") FROM runs WHERE session_id = ?`, s.ID).Scan(&runs, &status, &off)
	if runs != 1 || status != "running" || off == 0 {
		t.Fatalf("runs row: %d %s %d", runs, status, off)
	}
	// follow-up goes to the same live host and keeps the transcript continuous
	if err := e.svc.Send(s.ID, SendMessage{Text: "again"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second", func() bool { return strings.Contains(e.transcript(t, s.ID), "echo again") })
	e.st.DB.QueryRow(`SELECT COUNT(*) FROM runs WHERE session_id = ?`, s.ID).Scan(&runs)
	if runs != 1 {
		t.Fatalf("expected one run, got %d", runs)
	}
}

func TestTitleFrom(t *testing.T) {
	long := strings.Repeat("word ", 30)
	for in, want := range map[string]string{
		"":                         "",
		"  \n\n  ":                 "",
		"\n  fix   the bug \nmore": "fix the bug",
	} {
		if got := titleFrom(in); got != want {
			t.Fatalf("titleFrom(%q) = %q, want %q", in, got, want)
		}
	}
	if got := titleFrom(long); len([]rune(got)) > 61 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long title %q", got)
	}
}

func TestSendDerivesTitleOnce(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", "")
	if err := e.svc.sessions.Rename(s.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Send(s.ID, SendMessage{Text: "first prompt\nsecond line"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "idle", func() bool { return e.dbStatus(s.ID) == StatusIdle })
	if err := e.svc.Send(s.ID, SendMessage{Text: "another"}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.sessions.Get(s.ID)
	if got.Title != "first prompt" {
		t.Fatalf("title %q", got.Title)
	}
}

func TestStatusAndRespond(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", "")
	if err := e.svc.Send(s.ID, SendMessage{Text: "ask"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "needs_approval", func() bool { return e.dbStatus(s.ID) == StatusNeedsApproval })
	time.Sleep(150 * time.Millisecond) // let the debounced push go out
	if _, err := e.svc.Respond(s.ID, "req1", RespondOpts{Behavior: "bogus"}); err == nil {
		t.Fatal("bad behavior accepted")
	}
	if _, err := e.svc.Respond(s.ID, "req1", RespondOpts{Behavior: "allow_always"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "idle", func() bool { return e.dbStatus(s.ID) == StatusIdle })
	waitFor(t, "pushed statuses", func() bool {
		st := e.rec.statuses(s.ID)
		return len(st) > 0 && st[len(st)-1] == StatusIdle && hasStatus(e, s.ID, StatusNeedsApproval)
	})
	if !strings.Contains(e.transcript(t, s.ID), "echo ask") {
		t.Fatal(e.transcript(t, s.ID))
	}
	if _, err := e.svc.Respond(s.ID, "req1", RespondOpts{Behavior: "allow"}); err != nil {
		t.Fatal(err) // reply to a finished request is harmless
	}
}

func TestQueueDelivery(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", "")
	if err := e.svc.Send(s.ID, SendMessage{Text: "slow"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { return e.dbStatus(s.ID) == StatusRunning })
	if err := e.svc.Send(s.ID, SendMessage{Text: "queued", Delivery: "queue"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(e.transcript(t, s.ID), "queued") {
		t.Fatal("queued message went out while busy")
	}
	waitFor(t, "queued delivered", func() bool { return strings.Contains(e.transcript(t, s.ID), "echo queued") })
	tr := e.transcript(t, s.ID)
	if strings.Index(tr, "echo slow") > strings.Index(tr, "user:queued") {
		t.Fatalf("order: %s", tr)
	}
	waitFor(t, "idle", func() bool { return e.dbStatus(s.ID) == StatusIdle })
}

func TestRestartReattach(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", "")
	if err := e.svc.Send(s.ID, SendMessage{Text: "ask"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "needs_approval", func() bool { return e.dbStatus(s.ID) == StatusNeedsApproval })
	e.sup.Close() // app "quits"; the host keeps running

	e.rec = &recorder{}
	sup2 := newSupervisor(e.sup.dataDir, e.st.DB, workspace.NewSessions(e.st.DB), workspace.NewProjects(e.st.DB), e.rec, nil)
	t.Cleanup(sup2.Close)
	e.svc.sup = sup2
	e.sup = sup2
	sup2.Recover()
	if got := e.transcript(t, s.ID); !strings.Contains(got, "user:ask") {
		t.Fatalf("transcript not rebuilt: %s", got)
	}
	if _, err := e.svc.Respond(s.ID, "req1", RespondOpts{Behavior: "allow"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "finished after reattach", func() bool {
		return strings.Contains(e.transcript(t, s.ID), "echo ask") && e.dbStatus(s.ID) == StatusIdle
	})
	if len(e.rec.patches(s.ID)) == 0 {
		t.Fatal("no live patches after reattach")
	}
	// stop the host, then simulate a crash record for a dead one
	sess, _ := e.svc.sessions.Get(s.ID)
	_ = sess
	if sr := sup2.get(s.ID); sr.liveClient() != nil {
		sr.liveClient().Shutdown()
	}
}

func TestRecoverMarksDeadRunCrashed(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", "")
	e.st.DB.Exec(`INSERT INTO runs (id, session_id, host_pid, status, started) VALUES ('dead1', ?, 999999, 'running', 1)`, s.ID)
	e.sup.Recover()
	waitFor(t, "error status", func() bool { return e.dbStatus(s.ID) == StatusError })
	var st string
	e.st.DB.QueryRow(`SELECT status FROM runs WHERE id = 'dead1'`).Scan(&st)
	if st != "crashed" {
		t.Fatal(st)
	}
}

func TestSetModelSameProviderPersists(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", "")
	r, err := e.svc.SetModel(s.ID, "fake/m2")
	if err != nil || r.ForkedSessionID != "" {
		t.Fatal(r, err)
	}
	if got, _ := e.svc.sessions.Get(s.ID); got.Model != "fake/m2" {
		t.Fatal(got.Model)
	}
	r, err = e.svc.CycleModel(s.ID, +1) // m2 -> x1 (all models, other provider)
	if err != nil || r.ForkedSessionID == "" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestSetModelOtherProviderForksInSameWorktree(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", "worktree")
	if err := e.svc.Send(s.ID, SendMessage{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "idle", func() bool {
		return strings.Contains(e.transcript(t, s.ID), "echo hello") && e.dbStatus(s.ID) == StatusIdle
	})
	os.WriteFile(filepath.Join(s.WorktreePath, "a.txt"), []byte("changed\n"), 0o644)

	r, err := e.svc.SetModel(s.ID, "fake2/x1")
	if err != nil || r.ForkedSessionID == "" {
		t.Fatalf("%+v %v", r, err)
	}
	f, err := e.svc.sessions.Get(r.ForkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if f.ForkedFrom != s.ID || f.WorktreePath != s.WorktreePath || f.Branch != s.Branch || f.ScopeKind != "worktree" || f.Provider != "fake2" {
		t.Fatalf("fork row: %+v", f)
	}
	if sc, err := e.svc.Scope(f.ID); err != nil || sc.Root != s.WorktreePath {
		t.Fatalf("%+v %v", sc, err)
	}
	if e.dbStatus(s.ID) != StatusIdle {
		t.Fatal("source not idle")
	}
	waitFor(t, "handoff sent", func() bool {
		tr := e.transcript(t, f.ID)
		return strings.Contains(tr, "Handoff") && strings.Contains(tr, "a.txt")
	})
	waitFor(t, "fork idle", func() bool { return e.dbStatus(f.ID) == StatusIdle })
	// the fork's host runs in the shared worktree
	if sr := e.sup.get(f.ID); sr.root != s.WorktreePath {
		t.Fatalf("fork root %q", sr.root)
	}
}

func TestModesAndEffort(t *testing.T) {
	if n, _ := nextMode("cursor", "agent"); n != "plan" {
		t.Fatal(n)
	}
	if n, _ := nextMode("cursor", "ask"); n != "agent" {
		t.Fatal(n)
	}
	if n, _ := nextMode("claude", "default"); n != "acceptEdits" {
		t.Fatal(n)
	}
	if n, ok := normalizeMode("claude", "Accept edits"); !ok || n != "acceptEdits" {
		t.Fatal(n)
	}
	e := newEnv(t)
	s := e.session(t, "claude", "claude/haiku", "")
	if err := e.svc.CycleMode(s.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetEffort(s.ID, "high"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.sessions.Get(s.ID)
	if got.Mode != "acceptEdits" || got.Effort != "high" {
		t.Fatalf("%+v", got)
	}
}

// ListSessions (used by the frontend's refresh) must report archived sessions
// with the same status as the `sessions` push, or they reappear as live.
func TestListSessionsReportsArchivedStatus(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "m1", workspace.ScopeMain)
	if err := e.svc.ArchiveSession(s.ID, false); err != nil {
		t.Fatal(err)
	}
	l, err := e.svc.ListSessions(e.proj.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 1 || l[0].Status != StatusArchived {
		t.Fatalf("ListSessions status = %+v, want %q", l, StatusArchived)
	}
}
