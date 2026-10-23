package claude_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/agent/claude"
)

func jsonEq(t *testing.T, got string, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got invalid json %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want invalid json: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("json mismatch\n got: %s\nwant: %s", got, want)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("missing trailing newline: %q", got)
	}
}

func TestEncodeUser(t *testing.T) {
	jsonEq(t, claude.EncodeUser(agent.UserMessage{Text: "hello"}),
		`{"type":"user","message":{"role":"user","content":"hello"}}`)
	jsonEq(t, claude.EncodeUser(agent.UserMessage{Text: "see", Images: []agent.Image{{MediaType: "image/png", Data: []byte("abc")}}}),
		`{"type":"user","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YWJj"}},{"type":"text","text":"see"}]}}`)
}

func TestEncodePermission(t *testing.T) {
	in := json.RawMessage(`{"file_path":"/a","content":"x"}`)
	jsonEq(t, claude.EncodePermission("r1", agent.Decision{Behavior: agent.BehaviorAllow}, "Write", in),
		`{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"allow","updatedInput":{"file_path":"/a","content":"x"}}}}`)
	jsonEq(t, claude.EncodePermission("r1", agent.Decision{Behavior: agent.BehaviorAllow, UpdatedInput: json.RawMessage(`{"file_path":"/b"}`)}, "Write", in),
		`{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"allow","updatedInput":{"file_path":"/b"}}}}`)
	jsonEq(t, claude.EncodePermission("r1", agent.Decision{Behavior: agent.BehaviorAllowAlways}, "Write", in),
		`{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"allow","updatedInput":{"file_path":"/a","content":"x"},"updatedPermissions":[{"type":"addRules","rules":[{"toolName":"Write"}],"behavior":"allow","destination":"session"}]}}}`)
	jsonEq(t, claude.EncodePermission("r1", agent.Decision{Behavior: agent.BehaviorDeny}, "Write", in),
		`{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"deny","message":"User denied this action"}}}`)
	jsonEq(t, claude.EncodePermission("r1", agent.Decision{Behavior: agent.BehaviorDeny, Message: "no, use Edit"}, "Write", in),
		`{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"behavior":"deny","message":"no, use Edit"}}}`)
}

func TestEncodeControl(t *testing.T) {
	jsonEq(t, claude.EncodeControlRequest("i1", "interrupt", nil), `{"type":"control_request","request_id":"i1","request":{"subtype":"interrupt"}}`)
	jsonEq(t, claude.EncodeControlRequest("m1", "set_model", map[string]any{"model": "sonnet"}),
		`{"type":"control_request","request_id":"m1","request":{"subtype":"set_model","model":"sonnet"}}`)
}

// session helper -----------------------------------------------------------

type sess struct {
	*claude.Session
	buf *bytes.Buffer
}

func newSess(t *testing.T, o agent.StartOpts) *sess {
	var buf bytes.Buffer
	n := 0
	return &sess{claude.NewSession(&buf, o, claude.WithRequestIDs(func() string { n++; return fmt.Sprintf("q%d", n) })), &buf}
}

func (s *sess) feed(t *testing.T, line string) []agent.Event {
	t.Helper()
	evs, err := s.Feed([]byte(line))
	if err != nil {
		t.Fatalf("feed %s: %v", line, err)
	}
	return evs
}

func kinds(evs []agent.Event) []agent.Kind {
	var k []agent.Kind
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return k
}

func TestCostDifferencing(t *testing.T) {
	s := newSess(t, agent.StartOpts{})
	res := func(total float64, extra string) agent.Usage {
		line := fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,"total_cost_usd":%v,"terminal_reason":"completed","usage":{"input_tokens":3,"output_tokens":4}%s}`, total, extra)
		for _, e := range s.feed(t, line) {
			if e.Kind == agent.KindUsage {
				return agent.MustDecode[agent.Usage](e)
			}
		}
		t.Fatal("no usage")
		return agent.Usage{}
	}
	if u := res(0.5, ""); u.CostUSD != 0.5 || u.TotalCostUSD != 0.5 || u.InputTokens != 3 || u.OutputTokens != 4 {
		t.Errorf("turn1 %+v", u)
	}
	if u := res(0.75, ""); u.CostUSD != 0.25 || u.TotalCostUSD != 0.75 {
		t.Errorf("turn2 %+v", u)
	}
	// An interrupted turn reports 0 and must not disturb the base.
	for _, e := range s.feed(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"total_cost_usd":0,"terminal_reason":"aborted_streaming","usage":{}}`) {
		if e.Kind == agent.KindUsage && agent.MustDecode[agent.Usage](e).CostUSD != 0 {
			t.Error("interrupted turn must cost 0")
		}
	}
	if u := res(1.0, ""); u.CostUSD != 0.25 {
		t.Errorf("turn3 %+v", u)
	}
	// Counter restart (lower than previous): treat as fresh.
	if u := res(0.1, ""); u.CostUSD != 0.1 {
		t.Errorf("restart %+v", u)
	}
}

func TestInterruptedAndErrorResults(t *testing.T) {
	s := newSess(t, agent.StartOpts{})
	evs := s.feed(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"total_cost_usd":0,"terminal_reason":"aborted_streaming","errors":["[ede_diagnostic] x"],"usage":{}}`)
	if got := agent.MustDecode[agent.RunResult](evs[len(evs)-1]); got.Status != agent.ResultInterrupted || len(got.Errors) != 0 {
		t.Errorf("interrupt result %+v", got)
	}
	for _, e := range evs {
		if e.Kind == agent.KindError {
			t.Error("interrupt must not raise an error event")
		}
	}
	evs = s.feed(t, `{"type":"result","subtype":"error_max_turns","is_error":true,"total_cost_usd":0.1,"errors":["too many turns"],"usage":{}}`)
	var sawErr bool
	for _, e := range evs {
		if e.Kind == agent.KindError {
			sawErr = agent.MustDecode[agent.ErrorData](e).Code == agent.ErrRunFailed
		}
	}
	if !sawErr || agent.MustDecode[agent.RunResult](evs[len(evs)-1]).Status != agent.ResultError {
		t.Errorf("error result events %v", kinds(evs))
	}
}

func TestPermissionFlow(t *testing.T) {
	s := newSess(t, agent.StartOpts{})
	s.feed(t, `{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"ls -la"}}]}}`)
	evs := s.feed(t, `{"type":"control_request","request_id":"R1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls -la"},"tool_use_id":"tu1","description":"list"}}`)
	if len(evs) != 1 || evs[0].Kind != agent.KindPermissionRequest {
		t.Fatalf("got %v", kinds(evs))
	}
	pr := agent.MustDecode[agent.PermissionRequest](evs[0])
	if pr.ReqID != "R1" || pr.Tool != "Bash" || len(pr.Options) != 3 {
		t.Errorf("%+v", pr)
	}
	if _, err := s.RespondPermission("nope", agent.Decision{Behavior: agent.BehaviorAllow}); err == nil {
		t.Error("expected error for unknown request")
	}
	out, err := s.RespondPermission("R1", agent.Decision{Behavior: agent.BehaviorAllowAlways})
	if err != nil || len(out) != 1 || out[0].Kind != agent.KindPermissionResolved {
		t.Fatalf("%v %v", out, err)
	}
	jsonEq(t, s.buf.String(), `{"type":"control_response","response":{"subtype":"success","request_id":"R1","response":{"behavior":"allow","updatedInput":{"command":"ls -la"},"updatedPermissions":[{"type":"addRules","rules":[{"toolName":"Bash"}],"behavior":"allow","destination":"session"}]}}}`)
	if _, err := s.RespondPermission("R1", agent.Decision{Behavior: agent.BehaviorAllow}); err == nil {
		t.Error("double answer must fail")
	}
}

func TestPlanProposalAndDeny(t *testing.T) {
	s := newSess(t, agent.StartOpts{})
	evs := s.feed(t, `{"type":"control_request","request_id":"P1","request":{"subtype":"can_use_tool","tool_name":"ExitPlanMode","input":{"plan":"# Plan\nstep","planFilePath":"/p.md"},"tool_use_id":"tu9","requires_user_interaction":true}}`)
	if len(evs) != 1 || evs[0].Kind != agent.KindPlanProposal {
		t.Fatalf("%v", kinds(evs))
	}
	if p := agent.MustDecode[agent.PlanProposal](evs[0]); p.ReqID != "P1" || p.Text != "# Plan\nstep" || p.FilePath != "/p.md" {
		t.Errorf("%+v", p)
	}
	if _, err := s.RespondPermission("P1", agent.Decision{Behavior: agent.BehaviorDeny, Message: "add tests"}); err != nil {
		t.Fatal(err)
	}
	jsonEq(t, s.buf.String(), `{"type":"control_response","response":{"subtype":"success","request_id":"P1","response":{"behavior":"deny","message":"add tests"}}}`)
}

func TestCancelAndClosed(t *testing.T) {
	s := newSess(t, agent.StartOpts{})
	s.feed(t, `{"type":"control_request","request_id":"A","request":{"subtype":"can_use_tool","tool_name":"Write","input":{},"tool_use_id":"t1"}}`)
	s.feed(t, `{"type":"control_request","request_id":"B","request":{"subtype":"can_use_tool","tool_name":"Write","input":{},"tool_use_id":"t2"}}`)
	evs := s.feed(t, `{"type":"control_cancel_request","request_id":"A"}`)
	if len(evs) != 1 || evs[0].Kind != agent.KindPermissionCancelled || agent.MustDecode[agent.PermissionCancelled](evs[0]).ReqID != "A" {
		t.Fatalf("%v", evs)
	}
	if evs := s.feed(t, `{"type":"control_cancel_request","request_id":"A"}`); len(evs) != 0 {
		t.Error("second cancel must be ignored")
	}
	cl := s.Closed(1, "boom")
	if len(cl) != 2 || cl[0].Kind != agent.KindPermissionCancelled || cl[1].Kind != agent.KindError {
		t.Fatalf("%v", kinds(cl))
	}
	if e := agent.MustDecode[agent.ErrorData](cl[1]); e.Code != agent.ErrProcessExit || !strings.Contains(e.Message, "boom") {
		t.Errorf("%+v", e)
	}
	if cl := s.Closed(0, ""); len(cl) != 0 {
		t.Errorf("clean exit: %v", kinds(cl))
	}
}

func TestUnknownCLIRequestIsRefused(t *testing.T) {
	s := newSess(t, agent.StartOpts{})
	if evs := s.feed(t, `{"type":"control_request","request_id":"H1","request":{"subtype":"hook_callback"}}`); len(evs) != 0 {
		t.Error("no events expected")
	}
	jsonEq(t, s.buf.String(), `{"type":"control_response","response":{"subtype":"error","request_id":"H1","error":"unsupported control request: hook_callback"}}`)
}

func TestNoisyLinesAndEchoes(t *testing.T) {
	s := newSess(t, agent.StartOpts{})
	for _, l := range []string{
		`{"type":"rate_limit_event","rate_limit_info":{}}`,
		`{"type":"user","message":{"role":"user","content":"hi"},"isReplay":true}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`,
		`{"type":"system","subtype":"hook_started"}`,
		``,
	} {
		if evs := s.feed(t, l); len(evs) != 0 {
			t.Errorf("%q produced %v", l, kinds(evs))
		}
	}
	if _, err := s.Feed([]byte("not json")); err == nil {
		t.Error("expected parse error")
	}
}

func TestSubagentParentAndTodo(t *testing.T) {
	s := newSess(t, agent.StartOpts{})
	evs := s.feed(t, `{"type":"assistant","parent_tool_use_id":"task1","message":{"id":"m2","content":[{"type":"tool_use","id":"c1","name":"Read","input":{"file_path":"/x"}}]}}`)
	for _, e := range evs {
		if e.ParentToolID != "task1" {
			t.Errorf("%s lost parent", e.Kind)
		}
	}
	evs = s.feed(t, `{"type":"assistant","message":{"id":"m3","content":[{"type":"tool_use","id":"td","name":"TodoWrite","input":{"todos":[{"content":"a","status":"pending","activeForm":"Doing a"}]}}]}}`)
	last := evs[len(evs)-1]
	if last.Kind != agent.KindTodoUpdate || agent.MustDecode[agent.TodoUpdate](last).Todos[0].Content != "a" {
		t.Errorf("%v", kinds(evs))
	}
	evs = s.feed(t, `{"type":"user","parent_tool_use_id":"task1","message":{"content":[{"type":"tool_result","tool_use_id":"c1","content":[{"type":"text","text":"abc"},{"type":"tool_reference","tool_name":"X"}]}]}}`)
	r := agent.MustDecode[agent.ToolResult](evs[0])
	if evs[0].ParentToolID != "task1" || r.Output != "abc" || !r.OK {
		t.Errorf("%+v", r)
	}
}

func TestEditResultDiffFromStructuredPatch(t *testing.T) {
	s := newSess(t, agent.StartOpts{})
	s.feed(t, `{"type":"assistant","message":{"id":"m","content":[{"type":"tool_use","id":"e1","name":"Edit","input":{"file_path":"/w/a.go","old_string":"x","new_string":"y"}}]}}`)
	evs := s.feed(t, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"e1","content":"ok"}]},"tool_use_result":{"filePath":"/w/a.go","structuredPatch":[{"oldStart":3,"oldLines":2,"newStart":3,"newLines":2,"lines":[" ctx","-x","+y"]}]}}`)
	r := agent.MustDecode[agent.ToolResult](evs[0])
	want := "--- a/w/a.go\n+++ b/w/a.go\n@@ -3,2 +3,2 @@\n ctx\n-x\n+y\n"
	if r.Diff != want {
		t.Errorf("diff %q", r.Diff)
	}
	// No structuredPatch (create with empty patch): synthesize from content.
	s.feed(t, `{"type":"assistant","message":{"id":"m","content":[{"type":"tool_use","id":"w1","name":"Write","input":{"file_path":"/w/n.txt","content":"a\nb\n"}}]}}`)
	evs = s.feed(t, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"w1","content":"created"}]},"tool_use_result":{"type":"create","filePath":"/w/n.txt","content":"a\nb\n","structuredPatch":[]}}`)
	if d := agent.MustDecode[agent.ToolResult](evs[0]).Diff; d != "--- /dev/null\n+++ b/w/n.txt\n@@ -0,0 +1,2 @@\n+a\n+b\n" {
		t.Errorf("create diff %q", d)
	}
}

func TestAssistantWithoutPartialMessages(t *testing.T) {
	s := newSess(t, agent.StartOpts{})
	evs := s.feed(t, `{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"hi"}]}}`)
	evs = append(evs, s.feed(t, `{"type":"assistant","message":{"id":"m","content":[{"type":"tool_use","id":"t","name":"Bash","input":{"command":"ls"}}]}}`)...)
	want := []agent.Kind{agent.KindMessageDone, agent.KindToolStart, agent.KindToolInputDone}
	if !reflect.DeepEqual(kinds(evs), want) {
		t.Errorf("%v", kinds(evs))
	}
	if d := agent.MustDecode[agent.MessageDone](evs[0]); d.Index != 0 || d.Text != "hi" {
		t.Errorf("%+v", d)
	}
	if d := agent.MustDecode[agent.ToolInputDone](evs[2]); d.Name != "Bash" {
		t.Errorf("%+v", d)
	}
}

func TestModeTrackingAndInitDedup(t *testing.T) {
	s := newSess(t, agent.StartOpts{Mode: "plan"})
	init := `{"type":"system","subtype":"init","session_id":"S","model":"claude-x","permissionMode":"plan","tools":["Bash"],"cwd":"/c"}`
	if evs := s.feed(t, init); !reflect.DeepEqual(kinds(evs), []agent.Kind{agent.KindSessionInit}) {
		t.Errorf("first init %v", kinds(evs))
	}
	if evs := s.feed(t, init); len(evs) != 0 {
		t.Errorf("identical init must be deduped: %v", kinds(evs))
	}
	if evs := s.feed(t, `{"type":"system","subtype":"status","status":null,"permissionMode":"default"}`); !reflect.DeepEqual(kinds(evs), []agent.Kind{agent.KindModeChanged}) {
		t.Errorf("%v", kinds(evs))
	}
	if evs := s.feed(t, `{"type":"system","subtype":"status","status":null,"permissionMode":"default"}`); len(evs) != 0 {
		t.Errorf("unchanged mode: %v", kinds(evs))
	}
	if err := s.SetMode("bogus"); err == nil {
		t.Error("bogus mode must be refused locally")
	}
}

func TestControlRequestsAndResponses(t *testing.T) {
	s := newSess(t, agent.StartOpts{})
	if err := s.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModel("sonnet"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMode("acceptEdits"); err != nil {
		t.Fatal(err)
	}
	if err := s.ListModels(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(s.buf.String()), "\n")
	want := []string{
		`{"type":"control_request","request_id":"q1","request":{"subtype":"interrupt"}}`,
		`{"type":"control_request","request_id":"q2","request":{"subtype":"set_model","model":"sonnet"}}`,
		`{"type":"control_request","request_id":"q3","request":{"subtype":"set_permission_mode","mode":"acceptEdits"}}`,
		`{"type":"control_request","request_id":"q4","request":{"subtype":"list_models"}}`,
	}
	for i := range want {
		jsonEq(t, lines[i]+"\n", want[i])
	}
	if evs := s.feed(t, `{"type":"control_response","response":{"subtype":"success","request_id":"q2"}}`); len(evs) != 1 ||
		agent.MustDecode[agent.ModelChanged](evs[0]).Model != "sonnet" {
		t.Errorf("model_changed: %v", evs)
	}
	if evs := s.feed(t, `{"type":"control_response","response":{"subtype":"success","request_id":"q3","response":{"mode":"acceptEdits"}}}`); len(evs) != 1 || evs[0].Kind != agent.KindModeChanged {
		t.Errorf("mode_changed: %v", evs)
	}
	if evs := s.feed(t, `{"type":"control_response","response":{"subtype":"success","request_id":"unknown"}}`); len(evs) != 0 {
		t.Error("unknown response must be ignored")
	}
}

func TestCommand(t *testing.T) {
	p := &claude.Provider{Binary: "/bin/claude"}
	c, err := p.Command(agent.StartOpts{
		Cwd: "/w", Model: "sonnet", Mode: "plan", Effort: "high", ResumeID: "SID",
		MCPConfig: []string{"m.json"}, AddDirs: []string{"/a", "/b"},
		AllowedTools: []string{"Read", "Bash(ls:*)"}, DisallowedTools: []string{"WebFetch"},
		Settings:           []json.RawMessage{json.RawMessage(`{"a":{"x":1,"y":2},"b":1}`), json.RawMessage(`{"a":{"y":3}}`)},
		AppendSystemPrompt: []string{"one", "", "two"},
		Agents:             map[string]agent.SubagentDef{"explorer": {Description: "d", Prompt: "p", Tools: []string{"Read"}, Model: "haiku"}},
		Env:                []string{"K=V"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--print", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose",
		"--include-partial-messages", "--replay-user-messages", "--permission-prompt-tool", "stdio",
		"--permission-mode", "plan", "--model", "sonnet", "--effort", "high", "--resume", "SID",
		"--mcp-config", "m.json", "--add-dir", "/a", "--add-dir", "/b",
		"--allowedTools", "Read,Bash(ls:*)", "--disallowedTools", "WebFetch",
		"--settings", `{"a":{"x":1,"y":3},"b":1}`,
		"--agents", `{"explorer":{"description":"d","prompt":"p","tools":["Read"],"model":"haiku"}}`,
		"--append-system-prompt", "one\n\ntwo"}
	if !reflect.DeepEqual(c.Args, want) {
		t.Errorf("args\n got %q\nwant %q", c.Args, want)
	}
	if c.Path != "/bin/claude" || c.Dir != "/w" || !reflect.DeepEqual(c.Env, []string{"K=V"}) {
		t.Errorf("%+v", c)
	}
	c, _ = p.Command(agent.StartOpts{})
	for _, a := range c.Args {
		if a == "--model" || a == "--settings" || a == "--resume" || a == "--agents" {
			t.Errorf("unexpected %s in minimal argv", a)
		}
	}
	if _, err := p.Command(agent.StartOpts{Settings: []json.RawMessage{json.RawMessage(`{bad`)}}); err == nil {
		t.Error("bad settings must fail")
	}
}

func fakeClaude(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetect(t *testing.T) {
	p := &claude.Provider{Binary: fakeClaude(t, `echo "2.1.306 (Claude Code)"`)}
	in, err := p.Detect(context.Background())
	if err != nil || !in.Found || in.Version != "2.1.306" {
		t.Errorf("%+v %v", in, err)
	}
	in, err = (&claude.Provider{Binary: fakeClaude(t, `exit 3`)}).Detect(context.Background())
	if err != nil || in.Found {
		t.Errorf("failing binary: %+v %v", in, err)
	}
}

func TestModelsLiveAndFallback(t *testing.T) {
	live := fakeClaude(t, `read line
id=$(printf '%s' "$line" | sed 's/.*"request_id":"\([^"]*\)".*/\1/')
printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":{"models":[{"value":"opus","resolvedModel":"claude-opus-5-5","displayName":"Opus 5.5","description":"d","supportsEffort":true,"supportedEffortLevels":["low","high"]},{"value":"haiku","displayName":"Haiku"}]}}}\n' "$id"
sleep 5`)
	ms, err := (&claude.Provider{Binary: live}).Models(context.Background())
	if err != nil || len(ms) != 2 || ms[0].ID != "opus" || ms[0].Provider != "claude" || ms[0].Resolved != "claude-opus-5-5" ||
		!reflect.DeepEqual(ms[0].Efforts, []string{"low", "high"}) || ms[1].Name != "Haiku" {
		t.Errorf("live models: %+v %v", ms, err)
	}
	ms, err = (&claude.Provider{Binary: fakeClaude(t, `exit 1`)}).Models(context.Background())
	if err != nil || len(ms) == 0 || ms[0].Provider != "claude" {
		t.Errorf("fallback: %+v %v", ms, err)
	}
	ms, err = (&claude.Provider{Binary: "/nonexistent/claude"}).Models(context.Background())
	if err != nil || len(ms) != len(claude.StaticModels()) {
		t.Errorf("missing binary fallback: %+v %v", ms, err)
	}
}
