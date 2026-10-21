package transcript_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/transcript"
)

type rig struct {
	t *testing.T
	b *transcript.Builder
	r []transcript.Block // reduced from patches
	n uint64
}

func newRig(t *testing.T) *rig { return &rig{t: t, b: transcript.NewBuilder()} }

func (r *rig) ev(kind agent.Kind, parent string, payload any) []transcript.Patch {
	r.n++
	e := agent.NewEvent(kind, parent, payload)
	e.Seq = r.n
	ps := r.b.Apply(e)
	for _, p := range ps {
		r.r = transcript.Reduce(r.r, p)
	}
	return ps
}

func (r *rig) check() []transcript.Block {
	r.t.Helper()
	snap := r.b.Snapshot()
	a, _ := json.Marshal(r.r)
	b, _ := json.Marshal(snap)
	if string(a) != string(b) {
		r.t.Fatalf("patches diverge from snapshot\n%s\n%s", a, b)
	}
	return snap
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestStreamingTextUpdatesInPlace(t *testing.T) {
	r := newRig(t)
	p1 := r.ev(agent.KindTextDelta, "", agent.BlockDelta{MessageID: "m", Index: 0, Text: "Hel"})
	p2 := r.ev(agent.KindTextDelta, "", agent.BlockDelta{MessageID: "m", Index: 0, Text: "lo"})
	if p1[0].Op != transcript.OpAppend || !p1[0].Block.Streaming {
		t.Errorf("first delta: %+v", p1)
	}
	if p2[0].Op != transcript.OpUpdate || p2[0].TextDelta != "lo" || p2[0].BlockID != p1[0].Block.ID {
		t.Errorf("second delta: %+v", p2)
	}
	r.ev(agent.KindMessageDone, "", agent.MessageDone{MessageID: "m", Index: 0, BlockType: "text", Text: "Hello"})
	s := r.check()
	if len(s) != 1 || s[0].Text != "Hello" || s[0].Streaming {
		t.Errorf("%+v", s)
	}
	// Done without prior deltas still produces the block; empty thinking is dropped.
	r.ev(agent.KindMessageDone, "", agent.MessageDone{MessageID: "m2", Index: 0, BlockType: "text", Text: "whole"})
	r.ev(agent.KindMessageDone, "", agent.MessageDone{MessageID: "m2", Index: 1, BlockType: "thinking", Text: ""})
	r.ev(agent.KindMessageDone, "", agent.MessageDone{MessageID: "m2", Index: 2, BlockType: "thinking", Text: "hmm"})
	s = r.check()
	if len(s) != 3 || s[1].Text != "whole" || s[2].Type != transcript.BlockThinking || !s[2].Collapsed {
		t.Errorf("%+v", s)
	}
}

func TestToolLifecycleAndTitles(t *testing.T) {
	r := newRig(t)
	r.ev(agent.KindSessionInit, "", agent.SessionInit{Cwd: "/w"})
	r.ev(agent.KindToolStart, "", agent.ToolStart{ID: "e", Name: "Edit"})
	r.ev(agent.KindToolInputDone, "", agent.ToolInputDone{ID: "e", Name: "Edit", Input: raw(`{"file_path":"/w/src/app.ts","old_string":"a","new_string":"b"}`)})
	s := r.check()
	tb := s[0].Tool
	if s[0].Title != "Edit src/app.ts" || tb.Status != transcript.ToolRunning || tb.Kind != transcript.KindEdit ||
		!tb.DiffPreview || tb.Added != 1 || tb.Removed != 1 {
		t.Errorf("%+v %+v", s[0], tb)
	}
	r.ev(agent.KindToolResult, "", agent.ToolResult{ID: "e", OK: true, Output: "done", Diff: "--- a/x\n+++ b/x\n@@ -1 +1,2 @@\n-a\n+b\n+c\n"})
	s = r.check()
	if tb = s[0].Tool; tb.Status != transcript.ToolOK || tb.DiffPreview || tb.Added != 2 || tb.Removed != 1 || s[0].Collapsed {
		t.Errorf("%+v", tb)
	}

	r.ev(agent.KindToolInputDone, "", agent.ToolInputDone{ID: "b", Name: "Bash", Input: raw(`{"command":"npm test"}`)}) // no start event
	long := strings.Repeat("line\n", 25)
	r.ev(agent.KindToolResult, "", agent.ToolResult{ID: "b", OK: true, Output: long})
	s = r.check()
	if s[1].Title != "Bash: npm test" || !s[1].Collapsed || s[1].Tool.OutputLines != 25 || s[1].Tool.Command != "npm test" {
		t.Errorf("%+v %+v", s[1], s[1].Tool)
	}
	r.ev(agent.KindToolInputDone, "", agent.ToolInputDone{ID: "b2", Name: "Bash", Input: raw(`{"command":"false"}`)})
	ec := 1
	r.ev(agent.KindToolResult, "", agent.ToolResult{ID: "b2", OK: false, Output: "x", ExitCode: &ec})
	s = r.check()
	if s[2].Tool.Status != transcript.ToolError || s[2].Collapsed || *s[2].Tool.ExitCode != 1 {
		t.Errorf("%+v", s[2])
	}
}

func TestTitleVariants(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"Read", `{"file_path":"/w/a.go"}`, "Read a.go"},
		{"Grep", `{"pattern":"foo","path":"/w/pkg"}`, `Grep "foo" in pkg`},
		{"Glob", `{"pattern":"**/*.go"}`, "Glob **/*.go"},
		{"Task", `{"description":"find bugs","prompt":"x"}`, "Task: find bugs"},
		{"WebFetch", `{"url":"https://x.dev"}`, "Fetch https://x.dev"},
		{"mcp__claude_ai_Gmail__search", `{}`, "claude ai Gmail: search"},
		{"Bash", `{"command":"echo a\necho b"}`, "Bash: echo a …"},
		{"Weird", `{}`, "Weird"},
	}
	for _, c := range cases {
		r := newRig(t)
		r.ev(agent.KindSessionInit, "", agent.SessionInit{Cwd: "/w"})
		r.ev(agent.KindToolInputDone, "", agent.ToolInputDone{ID: "x", Name: c.name, Input: raw(c.in)})
		if got := r.check()[0].Title; got != c.want {
			t.Errorf("%s: %q want %q", c.name, got, c.want)
		}
	}
}

func TestSubagentChildrenNest(t *testing.T) {
	r := newRig(t)
	r.ev(agent.KindToolInputDone, "", agent.ToolInputDone{ID: "task", Name: "Task", Input: raw(`{"description":"explore"}`)})
	r.ev(agent.KindToolInputDone, "task", agent.ToolInputDone{ID: "c1", Name: "Read", Input: raw(`{"file_path":"/x"}`)})
	r.ev(agent.KindMessageDone, "task", agent.MessageDone{MessageID: "sm", Index: 0, BlockType: "text", Text: "sub says"})
	r.ev(agent.KindToolResult, "task", agent.ToolResult{ID: "c1", OK: true, Output: "data"})
	ps := r.ev(agent.KindToolResult, "", agent.ToolResult{ID: "task", OK: true, Output: "final"})
	if ps[0].Block.Children != nil {
		t.Error("update patches must not carry children")
	}
	s := r.check()
	if len(s) != 1 || len(s[0].Children) != 2 || s[0].Children[0].Tool.Status != transcript.ToolOK ||
		s[0].Children[1].Text != "sub says" || s[0].Tool.Status != transcript.ToolOK {
		t.Errorf("%+v", s)
	}
	// An orphan parent id falls back to the top level.
	r.ev(agent.KindMessageDone, "ghost", agent.MessageDone{MessageID: "o", Index: 0, BlockType: "text", Text: "orphan"})
	if s = r.check(); len(s) != 2 {
		t.Errorf("orphan: %d top-level blocks", len(s))
	}
}

func TestPermissionCardFollowsToolAndResolves(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"a", "b"} {
		r.ev(agent.KindToolInputDone, "", agent.ToolInputDone{ID: id, Name: "Write", Input: raw(`{"file_path":"/f` + id + `","content":"x"}`)})
	}
	r.ev(agent.KindPermissionRequest, "", agent.PermissionRequest{ReqID: "R", ToolUseID: "a", Tool: "Write"})
	s := r.check()
	if len(s) != 3 || s[1].Type != transcript.BlockPermission || s[1].Permission.State != transcript.StatePending ||
		s[0].Tool.Status != transcript.ToolPending || s[2].Type != transcript.BlockTool {
		t.Fatalf("order/state: %+v", s)
	}
	r.ev(agent.KindPermissionResolved, "", agent.PermissionResolved{ReqID: "R", ToolUseID: "a", Behavior: agent.BehaviorAllowAlways})
	s = r.check()
	if p := s[1].Permission; p.State != transcript.StateAllowed || !p.Always || !s[1].Collapsed || s[0].Tool.Status != transcript.ToolRunning {
		t.Errorf("%+v %+v", p, s[0].Tool)
	}
	// deny path
	r.ev(agent.KindPermissionRequest, "", agent.PermissionRequest{ReqID: "R2", ToolUseID: "b", Tool: "Write"})
	r.ev(agent.KindPermissionResolved, "", agent.PermissionResolved{ReqID: "R2", ToolUseID: "b", Behavior: agent.BehaviorDeny, Message: "nope"})
	r.ev(agent.KindToolResult, "", agent.ToolResult{ID: "b", OK: false, Output: "nope", Denied: true})
	s = r.check()
	last := s[2]
	if s[3].Permission.State != transcript.StateDenied || !last.Tool.Denied || last.Tool.Diff != "" || !last.Collapsed {
		t.Errorf("deny: %+v %+v", s[3].Permission, last.Tool)
	}
	// cancel path
	r.ev(agent.KindPermissionRequest, "", agent.PermissionRequest{ReqID: "R3", Tool: "Bash"})
	r.ev(agent.KindPermissionCancelled, "", agent.PermissionCancelled{ReqID: "R3"})
	s = r.check()
	if s[len(s)-1].Permission.State != transcript.StateCancelled {
		t.Errorf("cancel: %+v", s[len(s)-1])
	}
}

func TestPlanFlow(t *testing.T) {
	r := newRig(t)
	r.ev(agent.KindToolStart, "", agent.ToolStart{ID: "pt", Name: "ExitPlanMode"})
	r.ev(agent.KindToolInputDone, "", agent.ToolInputDone{ID: "pt", Name: "ExitPlanMode", Input: raw(`{"plan":"p"}`)})
	r.ev(agent.KindPlanProposal, "", agent.PlanProposal{ReqID: "P", ToolUseID: "pt", Text: "# Plan"})
	s := r.check()
	if len(s) != 1 || s[0].Type != transcript.BlockPlan || s[0].Plan.State != transcript.StatePending || s[0].Text != "# Plan" || s[0].Collapsed {
		t.Fatalf("%+v", s)
	}
	r.ev(agent.KindPermissionResolved, "", agent.PermissionResolved{ReqID: "P", ToolUseID: "pt", Behavior: agent.BehaviorDeny, Message: "more tests"})
	r.ev(agent.KindToolResult, "", agent.ToolResult{ID: "pt", OK: false})
	s = r.check()
	if len(s) != 1 || s[0].Plan.State != transcript.StateDenied || s[0].Plan.Message != "more tests" {
		t.Errorf("%+v", s[0].Plan)
	}
}

func TestTodoPinnedAndUpdatedInPlace(t *testing.T) {
	r := newRig(t)
	r.ev(agent.KindToolInputDone, "", agent.ToolInputDone{ID: "w", Name: "TodoWrite", Input: raw(`{}`)})
	r.ev(agent.KindTodoUpdate, "", agent.TodoUpdate{Todos: []agent.TodoItem{{Content: "a", Status: "pending"}, {Content: "b", Status: "pending"}}})
	r.ev(agent.KindToolResult, "", agent.ToolResult{ID: "w", OK: true, Output: "Todos modified"})
	r.ev(agent.KindTodoUpdate, "", agent.TodoUpdate{Todos: []agent.TodoItem{{Content: "a", Status: "completed"}, {Content: "b", Status: "pending"}}})
	s := r.check()
	if len(s) != 1 || s[0].Title != "Todos 1/2" || s[0].Todos[0].Status != "completed" {
		t.Errorf("%+v", s)
	}
}

func TestRunResultFinishesDanglingWork(t *testing.T) {
	r := newRig(t)
	r.ev(agent.KindUserMessage, "", agent.UserMessageData{Text: "go"})
	r.ev(agent.KindTextDelta, "", agent.BlockDelta{MessageID: "m", Index: 0, Text: "partial"})
	r.ev(agent.KindToolInputDone, "", agent.ToolInputDone{ID: "t", Name: "Bash", Input: raw(`{"command":"sleep 99"}`)})
	if !r.b.Meta().Running {
		t.Error("should be running after a user message")
	}
	r.ev(agent.KindUsage, "", agent.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.01234, TotalCostUSD: 1})
	r.ev(agent.KindRunResult, "", agent.RunResult{Status: agent.ResultInterrupted, DurationMS: 2500})
	s := r.check()
	if s[1].Streaming || s[2].Tool.Status != transcript.ToolError || !s[2].Tool.Interrupted {
		t.Errorf("dangling: %+v %+v", s[1], s[2].Tool)
	}
	f := s[3].Footer
	if f == nil || f.Summary != "Interrupted · 10 in · 5 out · $0.0123 · 2.5s" {
		t.Errorf("%+v", f)
	}
	if m := r.b.Meta(); m.Running || m.TotalCostUSD != 1 {
		t.Errorf("%+v", m)
	}
}

func TestErrorBlocksAndIDsStable(t *testing.T) {
	r := newRig(t)
	r.ev(agent.KindError, "", agent.ErrorData{Code: agent.ErrResumeNotFound, Message: "gone", Fatal: true})
	r.ev(agent.KindError, "", agent.ErrorData{Code: "x", Message: "y"})
	s := r.check()
	if s[0].ID != "error:1" || s[1].ID != "error:2" || s[0].Title != "Session not found" || !s[0].Error.Fatal {
		t.Errorf("%+v", s)
	}
	// Determinism: replaying the same events gives identical output.
	r2 := newRig(t)
	r2.ev(agent.KindError, "", agent.ErrorData{Code: agent.ErrResumeNotFound, Message: "gone", Fatal: true})
	r2.ev(agent.KindError, "", agent.ErrorData{Code: "x", Message: "y"})
	if !reflect.DeepEqual(s, r2.check()) {
		t.Error("builder not deterministic")
	}
}

func TestSnapshotIsDeepCopy(t *testing.T) {
	r := newRig(t)
	r.ev(agent.KindToolInputDone, "", agent.ToolInputDone{ID: "t", Name: "Bash", Input: raw(`{"command":"ls"}`)})
	snap := r.b.Snapshot()
	snap[0].Tool.Status = "mutated"
	snap[0].Title = "mutated"
	if got := r.b.Snapshot()[0]; got.Tool.Status == "mutated" || got.Title == "mutated" {
		t.Error("Snapshot leaked internal state")
	}
	if p := r.b.SnapshotPatch(); p.Op != transcript.OpSnapshot || len(p.Blocks) != 1 {
		t.Errorf("%+v", p)
	}
}
