package acp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/agent/acp"
)

// fakeAgent answers the handshake. If async, responses are fed from another
// goroutine (the realistic case); otherwise Feed is called re-entrantly from
// inside Write, the nastiest ordering for a pending map.
type fakeAgent struct {
	mu    sync.Mutex
	s     *acp.Session
	async bool
	lines []map[string]any
	evs   []agent.Event
}

func (f *fakeAgent) Write(p []byte) (int, error) {
	var m map[string]any
	if err := json.Unmarshal(p, &m); err != nil {
		return 0, err
	}
	f.mu.Lock()
	f.lines = append(f.lines, m)
	f.mu.Unlock()
	if m["id"] == nil || m["method"] == nil || m["method"] == "session/prompt" {
		return len(p), nil
	}
	var res any = map[string]any{}
	switch m["method"] {
	case "initialize":
		res = map[string]any{"protocolVersion": 1, "authMethods": []any{map[string]any{"id": "cursor_login"}}}
	case "session/new":
		res = map[string]any{
			"sessionId": "s1",
			"modes":     map[string]any{"currentModeId": "agent"},
			"models": map[string]any{"currentModelId": "default[]", "availableModels": []any{
				map[string]any{"modelId": "default[]", "name": "Auto"},
				map[string]any{"modelId": "composer-2.5[fast=true]", "name": "composer-2.5"}}},
		}
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": res})
	feed := func() {
		evs, err := f.s.Feed(b)
		if err != nil {
			panic(err)
		}
		f.mu.Lock()
		f.evs = append(f.evs, evs...)
		f.mu.Unlock()
	}
	if f.async {
		go feed()
	} else {
		feed()
	}
	return len(p), nil
}

func TestStartFeedConcurrency(t *testing.T) {
	for _, async := range []bool{true, false} {
		for i := 0; i < 50; i++ {
			f := &fakeAgent{async: async}
			f.s = acp.NewSession(f, agent.StartOpts{Cwd: "/w", Model: "composer-2.5[fast=true]", Mode: "plan"}, acp.Config{ProviderID: "cursor"})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := f.s.Start(ctx); err != nil {
				t.Fatalf("async=%v: %v", async, err)
			}
			cancel()
			f.mu.Lock()
			var methods []string
			for _, l := range f.lines {
				methods = append(methods, l["method"].(string))
			}
			kinds := map[agent.Kind]int{}
			for _, e := range f.evs {
				kinds[e.Kind]++
			}
			f.mu.Unlock()
			want := "initialize,authenticate,session/new,session/set_model,session/set_mode"
			if strings.Join(methods, ",") != want {
				t.Fatalf("methods %v", methods)
			}
			if kinds[agent.KindSessionInit] != 1 || kinds[agent.KindModelsList] != 1 || kinds[agent.KindModelChanged] != 1 || kinds[agent.KindModeChanged] != 1 {
				t.Fatalf("events %v", kinds)
			}
		}
	}
}

func TestStartContextAndClose(t *testing.T) {
	var buf syncBuf
	s := acp.NewSession(&buf, agent.StartOpts{Cwd: "/w"}, acp.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.Start(ctx); err == nil {
		t.Fatal("expected ctx error")
	}
	s2 := acp.NewSession(&buf, agent.StartOpts{Cwd: "/w"}, acp.Config{})
	done := make(chan error, 1)
	go func() { done <- s2.Start(context.Background()) }()
	buf.waitLines(t, 2) // s's initialize + s2's initialize
	s2.Closed(1, "boom")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Start must fail when the process exits")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start blocked after Closed")
	}
}

// ---- permission mapping ----

var cursorOpts = []agent.PermissionOption{
	{ID: "allow-once", Name: "Allow once", Kind: agent.OptAllowOnce},
	{ID: "allow-always", Name: "Allow always", Kind: agent.OptAllowAlways},
	{ID: "reject-once", Name: "Reject", Kind: agent.OptRejectOnce},
}

func optionID(m map[string]any) string {
	oc := obj(m, "outcome")
	return str(oc, "optionId") + "/" + str(oc, "outcome")
}

func TestPermissionOutcome(t *testing.T) {
	cases := []struct {
		d    agent.Decision
		want string
	}{
		{agent.Decision{Behavior: agent.BehaviorAllow}, "allow-once/selected"},
		{agent.Decision{Behavior: agent.BehaviorAllowAlways}, "allow-always/selected"},
		{agent.Decision{Behavior: agent.BehaviorDeny}, "reject-once/selected"},
		{agent.Decision{Behavior: agent.BehaviorDeny, OptionID: "custom"}, "custom/selected"},
	}
	for _, c := range cases {
		if got := optionID(acp.PermissionOutcome(cursorOpts, c.d)); got != c.want {
			t.Errorf("%v: got %s want %s", c.d, got, c.want)
		}
	}
	// No allow_always offered: fall back to allow_once. Never "cancelled".
	only := []agent.PermissionOption{{ID: "y", Kind: agent.OptAllowOnce}, {ID: "n", Kind: agent.OptRejectOnce}}
	if got := optionID(acp.PermissionOutcome(only, agent.Decision{Behavior: agent.BehaviorAllowAlways})); got != "y/selected" {
		t.Errorf("fallback: %s", got)
	}
	if got := optionID(acp.PermissionOutcome(nil, agent.Decision{Behavior: agent.BehaviorDeny})); got != "reject-once/selected" {
		t.Errorf("deny without options: %s", got)
	}
}

// newStarted returns a started session over a recording writer.
func newStarted(t *testing.T, cwd string, cfg acp.Config) (*acp.Session, *fakeAgent) {
	t.Helper()
	f := &fakeAgent{async: false}
	f.s = acp.NewSession(f, agent.StartOpts{Cwd: cwd}, cfg)
	if err := f.s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f.s, f
}

func (f *fakeAgent) last() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lines[len(f.lines)-1]
}

func feedJSON(t *testing.T, s *acp.Session, v any) []agent.Event {
	t.Helper()
	b, _ := json.Marshal(v)
	evs, err := s.Feed(b)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestPermissionFlowUsesEarlierToolCall(t *testing.T) {
	s, f := newStarted(t, "/w", acp.Config{})
	feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "s1",
		"update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": "tc\n1", "title": "`touch x`", "kind": "execute",
			"status": "pending", "rawInput": map[string]any{"command": "touch x"}}}})
	evs := feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "id": 0, "method": "session/request_permission", "params": map[string]any{
		"sessionId": "s1",
		"toolCall": map[string]any{"toolCallId": "tc\n1", "title": "`touch x`", "kind": "execute",
			"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "Not in allowlist: touch"}}}},
		"options": []any{
			map[string]any{"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"},
			map[string]any{"optionId": "allow-always", "name": "Allow always", "kind": "allow_always"},
			map[string]any{"optionId": "reject-once", "name": "Reject", "kind": "reject_once"}}}})
	if len(evs) != 1 || evs[0].Kind != agent.KindPermissionRequest {
		t.Fatalf("events %+v", evs)
	}
	pr := agent.MustDecode[agent.PermissionRequest](evs[0])
	if pr.ReqID != "0" || pr.Tool != "Bash" || string(pr.Input) != `{"command":"touch x"}` ||
		pr.ToolUseID != "tc\n1" || len(pr.Options) != 3 || pr.DecisionReason != "Not in allowlist: touch" {
		t.Fatalf("request %+v", pr)
	}
	rev, err := s.RespondPermission("0", agent.Decision{Behavior: agent.BehaviorDeny, Message: "no"})
	if err != nil || len(rev) != 1 || rev[0].Kind != agent.KindPermissionResolved {
		t.Fatalf("%v %v", rev, err)
	}
	if got := optionID(obj(f.last(), "result")); got != "reject-once/selected" {
		t.Fatalf("wrote %s", got)
	}
	if f.last()["id"].(float64) != 0 {
		t.Fatalf("response id %v", f.last()["id"])
	}
	// Denied tool: completed without output is reported as denied.
	evs = feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "s1",
		"update": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tc\n1", "status": "completed"}}})
	res := agent.MustDecode[agent.ToolResult](evs[len(evs)-1])
	if !res.Denied || res.OK {
		t.Fatalf("result %+v", res)
	}
	if _, err := s.RespondPermission("0", agent.Decision{Behavior: agent.BehaviorAllow}); err == nil {
		t.Fatal("double response accepted")
	}
}

func TestPlanProposalAnswers(t *testing.T) {
	for _, c := range []struct {
		b    agent.Behavior
		want string
	}{{agent.BehaviorAllow, "accepted"}, {agent.BehaviorDeny, "rejected"}} {
		s, f := newStarted(t, "/w", acp.Config{})
		evs := feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "id": 7, "method": "cursor/create_plan",
			"params": map[string]any{"toolCallId": "p1", "name": "n", "plan": "# Plan"}})
		if len(evs) != 1 || evs[0].Kind != agent.KindPlanProposal {
			t.Fatalf("events %+v", evs)
		}
		if pp := agent.MustDecode[agent.PlanProposal](evs[0]); pp.ReqID != "7" || pp.Text != "# Plan" || pp.ToolUseID != "p1" {
			t.Fatalf("%+v", pp)
		}
		if _, err := s.RespondPermission("7", agent.Decision{Behavior: c.b}); err != nil {
			t.Fatal(err)
		}
		if got := str(obj(obj(f.last(), "result"), "outcome"), "outcome"); got != c.want {
			t.Errorf("%s: wrote %s", c.b, got)
		}
	}
}

func TestExtensionAndUnknownRequests(t *testing.T) {
	s, f := newStarted(t, "/w", acp.Config{})
	evs := feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "cursor/update_todos",
		"params": map[string]any{"todos": []any{map[string]any{"id": "1", "content": "a", "status": "in_progress"}, map[string]any{"id": "2", "content": "b", "status": "TODO_STATUS_PENDING"}}}})
	if len(evs) != 1 || evs[0].Kind != agent.KindTodoUpdate || f.last()["result"] == nil {
		t.Fatalf("%+v %v", evs, f.last())
	}
	evs = feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "id": 4, "method": "cursor/update_todos",
		"params": map[string]any{"merge": true, "todos": []any{map[string]any{"id": "1", "status": "completed"}}}})
	td := agent.MustDecode[agent.TodoUpdate](evs[0])
	if len(td.Todos) != 2 || td.Todos[0].Status != "completed" || td.Todos[0].Content != "a" || td.Todos[1].Status != "pending" {
		t.Fatalf("merge: %+v", td)
	}
	feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "id": 5, "method": "cursor/ask_question", "params": map[string]any{}})
	if f.last()["error"] == nil || f.last()["id"].(float64) != 5 {
		t.Fatalf("ask_question: %v", f.last())
	}
	feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "id": 6, "method": "no/such", "params": map[string]any{}})
	if code := obj(f.last(), "error")["code"].(float64); code != -32601 {
		t.Fatalf("code %v", code)
	}
	feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "cursor/task", "params": map[string]any{}})
	if f.last()["result"] == nil {
		t.Fatalf("task: %v", f.last())
	}
}

func TestFSAndTerminalScopedToCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("l1\nl2\nl3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, f := newStarted(t, dir, acp.Config{})
	req := func(id int, method string, params map[string]any) map[string]any {
		feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		return f.last()
	}
	r := req(1, "fs/read_text_file", map[string]any{"path": filepath.Join(dir, "a.txt"), "line": 2, "limit": 1})
	if str(obj(r, "result"), "content") != "l2\n" {
		t.Fatalf("read: %v", r)
	}
	if r := req(2, "fs/write_text_file", map[string]any{"path": "sub/b.txt", "content": "hi"}); r["error"] != nil {
		t.Fatalf("write: %v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "sub", "b.txt")); string(b) != "hi" {
		t.Fatalf("file %q", b)
	}
	for _, p := range []string{"/etc/passwd", "../x", filepath.Join(dir, "..", "x")} {
		if r := req(3, "fs/read_text_file", map[string]any{"path": p}); r["error"] == nil {
			t.Errorf("escape allowed for %s", p)
		}
	}
	if r := req(4, "fs/write_text_file", map[string]any{"path": "/tmp/harness-acp-escape", "content": "x"}); r["error"] == nil {
		t.Errorf("write escape allowed")
	}
	// terminal
	r = req(5, "terminal/create", map[string]any{"command": "sh", "args": []any{"-c", "echo out; echo err >&2; exit 3"}})
	tid := str(obj(r, "result"), "terminalId")
	if tid == "" {
		t.Fatalf("create: %v", r)
	}
	r = req(6, "terminal/wait_for_exit", map[string]any{"terminalId": tid})
	_ = r // wait_for_exit runs async; poll the output instead
	deadline := time.Now().Add(5 * time.Second)
	for {
		r = req(7, "terminal/output", map[string]any{"terminalId": tid})
		if st := obj(obj(r, "result"), "exitStatus"); st != nil {
			if st["exitCode"].(float64) != 3 {
				t.Fatalf("exit %v", st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal never exited")
		}
		time.Sleep(5 * time.Millisecond)
	}
	out := str(obj(r, "result"), "output")
	if !strings.Contains(out, "out") || !strings.Contains(out, "err") {
		t.Fatalf("output %q", out)
	}
	if r := req(8, "terminal/create", map[string]any{"command": "ls", "cwd": "/"}); r["error"] == nil {
		t.Errorf("terminal cwd escape allowed")
	}
	if r := req(9, "terminal/release", map[string]any{"terminalId": tid}); r["error"] != nil {
		t.Errorf("release: %v", r)
	}
}

func TestDisabledClientCapabilities(t *testing.T) {
	s, f := newStarted(t, t.TempDir(), acp.Config{DisableFS: true, DisableTerminal: true})
	feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "fs/read_text_file", "params": map[string]any{"path": "x"}})
	if obj(f.last(), "error")["code"].(float64) != -32601 {
		t.Fatalf("%v", f.last())
	}
	f.mu.Lock()
	caps := obj(obj(f.lines[0], "params"), "clientCapabilities")
	f.mu.Unlock()
	if caps["terminal"] != false || obj(caps, "fs")["readTextFile"] != false {
		t.Fatalf("caps %v", caps)
	}
}

// ---- diff marker stripping ----

func TestStripDiffMarkers(t *testing.T) {
	o, n, isNew, isDel := acp.StripDiffMarkers("-- /dev/null", "++ b//abs/notes.md\n# Notes\nmore\n")
	if !isNew || isDel || o != "" || n != "# Notes\nmore\n" {
		t.Fatalf("new file: %q %q %v %v", o, n, isNew, isDel)
	}
	o, n, isNew, isDel = acp.StripDiffMarkers("-- a/x\nold\n", "++ /dev/null")
	if isNew || !isDel || o != "old\n" || n != "" {
		t.Fatalf("delete: %q %q", o, n)
	}
	o, n, isNew, isDel = acp.StripDiffMarkers("hello world\n", "hello acp\n")
	if isNew || isDel || o != "hello world\n" || n != "hello acp\n" {
		t.Fatalf("modify must be untouched: %q %q", o, n)
	}
	// A new file whose content happens to start with "++": only the header line goes.
	_, n, isNew, _ = acp.StripDiffMarkers("-- /dev/null", "++ b/p\n++ keep\n")
	if !isNew || n != "++ keep\n" {
		t.Fatalf("%q", n)
	}
}

func TestUnifiedDiff(t *testing.T) {
	got := acp.UnifiedDiff("f", "a\nb\nc\nd\ne\nf\ng\nh\n", "a\nb\nc\nd\nE\nf\ng\nh\n")
	want := "--- a/f\n+++ b/f\n@@ -2,7 +2,7 @@\n b\n c\n d\n-e\n+E\n f\n g\n h\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if acp.UnifiedDiff("f", "x\n", "x\n") != "" {
		t.Fatal("equal texts must give no diff")
	}
	if g := acp.UnifiedDiff("f", "", "new\n"); !strings.Contains(g, "@@ -0,0 +1,1 @@\n+new\n") {
		t.Fatalf("empty old: %s", g)
	}
}

// ---- event mapping details ----

func TestUpdateMapping(t *testing.T) {
	s, _ := newStarted(t, "/w", acp.Config{ProviderID: "cursor"})
	upd := func(u map[string]any) []agent.Event {
		return feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "s1", "update": u}})
	}
	// text -> thinking -> text: message_done closes each block, indexes advance.
	var kinds []agent.Kind
	for _, u := range []map[string]any{
		{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "hm"}},
		{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "a"}},
		{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "b"}},
		{"sessionUpdate": "session_info_update", "title": "x"},
		{"sessionUpdate": "tool_call", "toolCallId": "t", "title": "Edit File", "kind": "edit", "rawInput": map[string]any{}},
	} {
		for _, e := range upd(u) {
			kinds = append(kinds, e.Kind)
			if e.Kind == agent.KindMessageDone {
				d := agent.MustDecode[agent.MessageDone](e)
				if d.BlockType == "text" && (d.Text != "ab" || d.Index != 1) {
					t.Fatalf("done %+v", d)
				}
			}
		}
	}
	want := "thinking_delta message_done text_delta text_delta message_done tool_start tool_input_done"
	var ks []string
	for _, k := range kinds {
		ks = append(ks, string(k))
	}
	if strings.Join(ks, " ") != want {
		t.Fatalf("kinds: %v", ks)
	}
	// Edit completes with a new-file diff: markers stripped, path relative.
	evs := upd(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t", "status": "completed",
		"content": []any{map[string]any{"type": "diff", "path": "/w/n.md", "oldText": "-- /dev/null", "newText": "++ b//w/n.md\nhi\n"}}})
	r := agent.MustDecode[agent.ToolResult](evs[len(evs)-1])
	if !r.OK || r.Diff != "--- /dev/null\n+++ b/n.md\n@@ -0,0 +1,1 @@\n+hi\n" {
		t.Fatalf("%+v", r)
	}
	// Execute: rawOutput becomes output + exit code; non-zero is not OK.
	upd(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "x", "title": "`false`", "kind": "execute", "rawInput": map[string]any{"command": "false"}})
	evs = upd(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "x", "status": "completed",
		"rawOutput": map[string]any{"exitCode": 1, "stdout": "o\n", "stderr": "e\n"}})
	r = agent.MustDecode[agent.ToolResult](evs[len(evs)-1])
	if r.OK || r.ExitCode == nil || *r.ExitCode != 1 || r.Output != "o\ne\n" {
		t.Fatalf("%+v", r)
	}
	// mode and plan
	if e := upd(map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": "plan"}); agent.MustDecode[agent.ModeChanged](e[0]).Mode != "plan" {
		t.Fatalf("%+v", e)
	}
	e := upd(map[string]any{"sessionUpdate": "plan", "entries": []any{map[string]any{"content": "c", "priority": "high", "status": "in_progress"}}})
	if td := agent.MustDecode[agent.TodoUpdate](e[0]); len(td.Todos) != 1 || td.Todos[0].Status != "in_progress" {
		t.Fatalf("%+v", td)
	}
}

func TestPromptLifecycleAndClosed(t *testing.T) {
	s, f := newStarted(t, "/w", acp.Config{})
	evs, err := s.SendUser(agent.UserMessage{Text: "hi", Images: []agent.Image{{MediaType: "image/png", Data: []byte{1, 2}}}})
	if err != nil || evs[len(evs)-1].Kind != agent.KindUserMessage {
		t.Fatalf("%v %v", evs, err)
	}
	pr := f.last()
	blocks := obj(pr, "params")["prompt"].([]any)
	if pr["method"] != "session/prompt" || len(blocks) != 2 || blocks[1].(map[string]any)["data"] != "AQI=" {
		t.Fatalf("%v", pr)
	}
	if _, err := s.SendUser(agent.UserMessage{Text: "again"}); err == nil {
		t.Fatal("second concurrent prompt must be refused")
	}
	if err := s.Interrupt(); err != nil || f.last()["method"] != "session/cancel" || f.last()["id"] != nil {
		t.Fatalf("%v %v", err, f.last())
	}
	rid := pr["id"]
	evs = feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "id": rid, "result": map[string]any{"stopReason": "cancelled"}})
	rr := agent.MustDecode[agent.RunResult](evs[len(evs)-1])
	if rr.Status != agent.ResultInterrupted || rr.StopReason != "cancelled" || rr.ProviderSessionID != "s1" {
		t.Fatalf("%+v", rr)
	}
	// Abnormal exit with an open permission request and a running prompt.
	if _, err := s.SendUser(agent.UserMessage{Text: "go"}); err != nil {
		t.Fatal(err)
	}
	feedJSON(t, s, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/request_permission", "params": map[string]any{
		"toolCall": map[string]any{"toolCallId": "z", "title": "`x`", "kind": "execute"}, "options": []any{}}})
	cl := s.Closed(137, "killed")
	var kinds []agent.Kind
	for _, e := range cl {
		kinds = append(kinds, e.Kind)
	}
	if len(kinds) != 3 || kinds[0] != agent.KindPermissionCancelled || kinds[1] != agent.KindError || kinds[2] != agent.KindRunResult {
		t.Fatalf("closed: %v", kinds)
	}
	if ed := agent.MustDecode[agent.ErrorData](cl[1]); ed.Code != agent.ErrProcessExit || !ed.Fatal || !strings.Contains(ed.Message, "killed") {
		t.Fatalf("%+v", ed)
	}
}

func TestMalformedLine(t *testing.T) {
	s, _ := newStarted(t, "/w", acp.Config{})
	if _, err := s.Feed([]byte("not json")); err == nil {
		t.Fatal("expected error")
	}
	if evs, err := s.Feed([]byte("  ")); err != nil || evs != nil {
		t.Fatal("blank line must be ignored")
	}
}
