package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sid/harness/internal/agent"
)

// ---- fake provider: the test binary doubles as the CLI ----

type fakeProvider struct{}

func (fakeProvider) ID() string { return "fake" }
func (fakeProvider) Detect(context.Context) (agent.Install, error) {
	return agent.Install{Found: true}, nil
}
func (fakeProvider) Models(context.Context) ([]agent.Model, error) { return nil, nil }
func (fakeProvider) Command(o agent.StartOpts) (agent.Cmd, error) {
	exe, _ := os.Executable()
	return agent.Cmd{Path: exe, Env: append([]string{"HOST_FAKE_CLI=1"}, o.Env...), Dir: o.Cwd}, nil
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
	case "text":
		return []agent.Event{agent.NewEvent(agent.KindTextDelta, "", map[string]string{"text": m.V})}, nil
	case "perm":
		return []agent.Event{agent.NewEvent(agent.KindPermissionRequest, "", agent.PermissionRequest{ReqID: m.ID, Tool: "Bash"})}, nil
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
	return []agent.Event{agent.NewEvent(agent.KindPermissionResolved, "", agent.PermissionResolved{ReqID: id})}, nil
}
func (s *fakeSession) Interrupt() error      { return nil }
func (s *fakeSession) SetModel(string) error { return nil }
func (s *fakeSession) SetMode(string) error  { return nil }
func (s *fakeSession) Closed(code int, tail string) []agent.Event {
	if code != 0 {
		return []agent.Event{agent.NewEvent(agent.KindError, "", agent.ErrorData{Code: agent.ErrProcessExit, Message: fmt.Sprintf("exit %d: %s", code, tail)})}
	}
	return nil
}

func TestMain(m *testing.M) {
	if os.Getenv("HOST_FAKE_CLI") == "1" {
		fakeCLI()
		return
	}
	for _, a := range os.Args {
		if a == "--agent-host" {
			RegisterProvider("fake", func() agent.Provider { return fakeProvider{} })
			if err := Main(os.Args); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// fakeCLI prints FAKE_SCRIPT (JSON array of lines, 40ms apart), optionally
// exits with FAKE_EXIT, otherwise answers user lines until stdin closes.
func fakeCLI() {
	var script []string
	json.Unmarshal([]byte(os.Getenv("FAKE_SCRIPT")), &script)
	for _, l := range script {
		if os.Getenv("FAKE_NO_DELAY") != "1" {
			time.Sleep(40 * time.Millisecond)
		}
		fmt.Println(l)
	}
	if c := os.Getenv("FAKE_EXIT"); c != "" {
		fmt.Fprintln(os.Stderr, "fake boom")
		var n int
		fmt.Sscan(c, &n)
		os.Exit(n)
	}
	dec := json.NewDecoder(os.Stdin)
	for {
		var m map[string]string
		if dec.Decode(&m) != nil {
			os.Exit(0)
		}
		if u, ok := m["user"]; ok {
			b, _ := json.Marshal(map[string]string{"t": "text", "v": "echo " + u})
			fmt.Println(string(b))
			fmt.Println(`{"t":"result"}`)
		}
	}
}

// ---- helpers ----

func tdir(t *testing.T) string {
	d, err := os.MkdirTemp("/tmp", "h")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func txt(v string) string { return fmt.Sprintf(`{"t":"text","v":%q}`, v) }

func launch(t *testing.T, dir, id string, script []string, extra ...string) (*Client, Spec) {
	extra = append([]string{"FAKE_NO_DELAY=1"}, extra...)
	sb, _ := json.Marshal(script)
	spec := Spec{RunID: id, SessionID: "s", Provider: "fake", DataDir: dir,
		StartOpts: agent.StartOpts{Cwd: dir, Env: append([]string{"FAKE_SCRIPT=" + string(sb)}, extra...)},
		IdleTTLMs: 60000}
	return launchSpec(t, spec)
}

func launchSpec(t *testing.T, spec Spec) (*Client, Spec) {
	t.Helper()
	exe, _ := os.Executable()
	c, err := (&Launcher{Exe: exe}).Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cc, err := Attach(spec.DataDir, spec.RunID, 1<<60); err == nil {
			cc.Shutdown()
			time.Sleep(50 * time.Millisecond)
			cc.Close()
		}
		c.Close()
	})
	return c, spec
}

// collect reads until pred is true for an event (inclusive) or timeout.
func collect(t *testing.T, c *Client, pred func(agent.Event) bool) []agent.Event {
	t.Helper()
	var out []agent.Event
	to := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				return out
			}
			out = append(out, e)
			if pred != nil && pred(e) {
				return out
			}
		case <-to:
			t.Fatalf("timeout; got %d events: %v", len(out), kinds(out))
		}
	}
}

func kinds(evs []agent.Event) []agent.Kind {
	var k []agent.Kind
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return k
}

func isKind(k agent.Kind) func(agent.Event) bool {
	return func(e agent.Event) bool { return e.Kind == k }
}

func waitScanDead(t *testing.T, dir string) {
	t.Helper()
	for i := 0; i < 50000; i++ {
		s := Scan(dir)
		if len(s) == 1 && !s[0].Alive {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("host still alive: %+v", Scan(dir))
}

func contiguous(t *testing.T, evs []agent.Event, from uint64) {
	t.Helper()
	for i, e := range evs {
		if e.Seq != from+uint64(i) {
			t.Fatalf("seq gap/dup at %d: got %d want %d (%v)", i, e.Seq, from+uint64(i), kinds(evs))
		}
	}
}

// ---- tests ----

func TestLaunchEventsSendShutdown(t *testing.T) {
	dir := tdir(t)
	c, spec := launch(t, dir, "r1", []string{txt("a"), txt("b"), `{"t":"result"}`})
	evs := collect(t, c, isKind(agent.KindRunResult))
	if got := kinds(evs); len(got) != 4 || got[0] != agent.KindRunStarted || got[3] != agent.KindRunResult {
		t.Fatalf("kinds %v", got)
	}
	contiguous(t, evs, 1)
	if err := c.Send(agent.UserMessage{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	more := collect(t, c, isKind(agent.KindRunResult))
	if got := kinds(more); len(got) != 3 || got[0] != agent.KindUserMessage {
		t.Fatalf("follow-up kinds %v", got)
	}
	// Live host appears in Scan as alive.
	if s := Scan(dir); len(s) != 1 || !s[0].Alive {
		t.Fatalf("scan %+v", s)
	}
	c.Shutdown()
	collect(t, c, nil) // until the connection closes
	if code, ok := c.ExitCode(); !ok || code != 0 {
		t.Fatalf("exit %d %v", code, ok)
	}
	log, err := ReadLog(dir, spec.RunID, 1)
	if err != nil || len(log) != 7 {
		t.Fatalf("log %d %v", len(log), err)
	}
	contiguous(t, log, 1)
	waitScanDead(t, dir)
	if _, err := Attach(dir, "r1", 0); err != ErrHostGone {
		t.Fatalf("attach after exit: %v", err)
	}
	if _, err := os.Stat(SocketPath(dir, "r1")); !os.IsNotExist(err) {
		t.Fatalf("socket not removed: %v", err)
	}
	if _, err := os.Stat(rawPath(dir, "r1")); err != nil {
		t.Fatal(err)
	}
}

func TestReattachNoGapsNoDups(t *testing.T) {
	dir := tdir(t)
	c, spec := launch(t, dir, "r2", []string{txt("1"), txt("2"), txt("3"), txt("4"), txt("5"), txt("6"), `{"t":"result"}`})
	first := collect(t, c, func(e agent.Event) bool { return e.Seq >= 3 })
	c.Close()
	last := first[len(first)-1].Seq
	c2, err := Attach(spec.DataDir, spec.RunID, last+1)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	rest := collect(t, c2, isKind(agent.KindRunResult))
	all := append(first, rest...)
	contiguous(t, all, 1)
	if len(all) != 8 {
		t.Fatalf("got %d: %v", len(all), kinds(all))
	}
}

func TestTwoClients(t *testing.T) {
	dir := tdir(t)
	a, spec := launch(t, dir, "r3", []string{txt("1"), txt("2"), txt("3"), `{"t":"result"}`})
	b, err := Attach(spec.DataDir, spec.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ea := collect(t, a, isKind(agent.KindRunResult))
	eb := collect(t, b, isKind(agent.KindRunResult))
	contiguous(t, ea, 1)
	contiguous(t, eb, 1)
	if len(ea) != len(eb) {
		t.Fatalf("%d vs %d", len(ea), len(eb))
	}
	a.Close() // one client leaving does not affect the other or the run
	if err := b.Send(agent.UserMessage{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	collect(t, b, isKind(agent.KindRunResult))
}

func TestPermissionReannounced(t *testing.T) {
	dir := tdir(t)
	c, spec := launch(t, dir, "r4", []string{txt("a"), `{"t":"perm","id":"req1"}`})
	evs := collect(t, c, isKind(agent.KindPermissionRequest))
	seq := evs[len(evs)-1].Seq
	c.Close()
	c2, err := Attach(spec.DataDir, spec.RunID, seq+1)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	select {
	case e := <-c2.Events():
		if e.Kind != agent.KindPermissionRequest || e.Seq != seq || agent.MustDecode[agent.PermissionRequest](e).ReqID != "req1" {
			t.Fatalf("got %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not re-announced")
	}
	if err := c2.Permission("req1", agent.Decision{Behavior: agent.BehaviorAllow}); err != nil {
		t.Fatal(err)
	}
	collect(t, c2, isKind(agent.KindPermissionResolved))
	// Resolved: a new attach no longer re-announces it.
	c3, err := Attach(spec.DataDir, spec.RunID, seq+10)
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	select {
	case e := <-c3.Events():
		t.Fatalf("unexpected %+v", e)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestIdleTTLExit(t *testing.T) {
	dir := tdir(t)
	sb, _ := json.Marshal([]string{txt("a"), `{"t":"result"}`})
	c, spec := launchSpec(t, Spec{RunID: "r5", Provider: "fake", DataDir: dir, IdleTTLMs: 300,
		StartOpts: agent.StartOpts{Cwd: dir, Env: []string{"FAKE_SCRIPT=" + string(sb)}}})
	evs := collect(t, c, nil)
	var sawIdle bool
	for _, e := range evs {
		if e.Kind == agent.KindStatus && agent.MustDecode[agent.Status](e).State == "idle_exited" {
			sawIdle = true
		}
	}
	if !sawIdle {
		t.Fatalf("no idle_exited: %v", kinds(evs))
	}
	if code, ok := c.ExitCode(); !ok || code != 0 {
		t.Fatalf("exit %d %v", code, ok)
	}
	if _, err := os.Stat(SocketPath(spec.DataDir, spec.RunID)); !os.IsNotExist(err) {
		t.Fatalf("socket remains: %v", err)
	}
}

func TestCrashedChild(t *testing.T) {
	dir := tdir(t)
	c, _ := launch(t, dir, "r6", []string{txt("a")}, "FAKE_EXIT=3")
	evs := collect(t, c, nil)
	last := evs[len(evs)-1]
	if last.Kind != agent.KindError || agent.MustDecode[agent.ErrorData](last).Code != agent.ErrProcessExit ||
		!strings.Contains(agent.MustDecode[agent.ErrorData](last).Message, "fake boom") {
		t.Fatalf("last %+v (%v)", last, kinds(evs))
	}
	if code, ok := c.ExitCode(); !ok || code != 3 {
		t.Fatalf("exit %d %v", code, ok)
	}
}

func TestLongSocketPathFallback(t *testing.T) {
	dir := filepath.Join(tdir(t), strings.Repeat("d", 60), strings.Repeat("e", 60))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := SocketPath(dir, "r7")
	if !strings.HasPrefix(sock, "/tmp/harness-") || len(sock) >= 104 {
		t.Fatalf("sock %q", sock)
	}
	c, _ := launch(t, dir, "r7", []string{`{"t":"result"}`})
	collect(t, c, isKind(agent.KindRunResult))
	info, err := readInfo(dir, "r7")
	if err != nil || info.Socket != sock {
		t.Fatalf("info %+v %v", info, err)
	}
	c.Shutdown()
	collect(t, c, nil)
}

func TestClaudeIntegration(t *testing.T) {
	if os.Getenv("HARNESS_IT_CLAUDE") != "1" {
		t.Skip("set HARNESS_IT_CLAUDE=1")
	}
	dir := tdir(t)
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=a@b", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	c, spec := launchSpec(t, Spec{RunID: "it1", Provider: "claude", DataDir: dir, IdleTTLMs: 60000,
		StartOpts: agent.StartOpts{Cwd: repo, Model: "haiku"}})
	if err := c.Send(agent.UserMessage{Text: "Reply with exactly the word pong and nothing else."}); err != nil {
		t.Fatal(err)
	}
	to := time.After(120 * time.Second)
	var got []agent.Event
loop:
	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				break loop
			}
			got = append(got, e)
			if e.Kind == agent.KindRunResult {
				break loop
			}
		case <-to:
			t.Fatalf("timeout: %v", kinds(got))
		}
	}
	if r := agent.MustDecode[agent.RunResult](got[len(got)-1]); r.Status != "success" {
		t.Fatalf("result %+v", r)
	}
	// The host is detached: it is a separate live process.
	info, _ := readInfo(dir, spec.RunID)
	if info.PID == os.Getpid() || !pidAlive(info.PID) {
		t.Fatalf("host pid %d", info.PID)
	}
	c.Shutdown()
	collect(t, c, nil)
	if log, _ := ReadLog(dir, spec.RunID, 1); len(log) < 3 {
		t.Fatalf("log %d", len(log))
	}
}
