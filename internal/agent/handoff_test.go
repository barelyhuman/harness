package agent_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sid/harness/internal/agent"
)

func user(t string) agent.Event {
	return agent.NewUserMessageEvent(agent.UserMessage{Text: t})
}

func asst(msg string, idx int, t string) agent.Event {
	return agent.NewEvent(agent.KindMessageDone, "", agent.MessageDone{MessageID: msg, Index: idx, BlockType: "text", Text: t})
}

func tool(id, name, input string, ok bool) []agent.Event {
	return []agent.Event{
		agent.NewEvent(agent.KindToolInputDone, "", agent.ToolInputDone{ID: id, Name: name, Input: []byte(input)}),
		agent.NewEvent(agent.KindToolResult, "", agent.ToolResult{ID: id, OK: ok}),
	}
}

func sampleEvents() []agent.Event {
	evs := []agent.Event{
		user("Add a --verbose flag to the CLI"),
		agent.NewEvent(agent.KindThinkingDelta, "", agent.BlockDelta{MessageID: "t1", Index: 0, Text: "secret thoughts"}),
		agent.NewEvent(agent.KindTextDelta, "", agent.BlockDelta{MessageID: "t1", Index: 1, Text: "I will read "}),
		agent.NewEvent(agent.KindTextDelta, "", agent.BlockDelta{MessageID: "t1", Index: 1, Text: "main.go."}),
	}
	evs = append(evs, tool("a", "Read", `{"file_path":"README.md"}`, true)...)
	evs = append(evs, tool("b", "Edit", `{"file_path":"main.go"}`, true)...)
	evs = append(evs, tool("c", "Edit", `{"file_path":"failed.go"}`, false)...)
	evs = append(evs, tool("d", "Bash", `{"command":"ls"}`, true)...)
	evs = append(evs,
		agent.NewEvent(agent.KindTodoUpdate, "", agent.TodoUpdate{Todos: []agent.TodoItem{{Content: "old", Status: "pending"}}}),
		agent.NewEvent(agent.KindTodoUpdate, "", agent.TodoUpdate{Todos: []agent.TodoItem{
			{Content: "parse flag", Status: "completed"}, {Content: "wire it", Status: "in_progress"}, {Content: "tests", Status: "pending"}}}),
		asst("t2", 0, "Done with the flag."),
		user("Now add tests"),
		asst("t3", 0, "Writing tests."),
	)
	return evs
}

func TestBuildHandoffContent(t *testing.T) {
	out := agent.BuildHandoff(sampleEvents(), " main.go | 3 ++-\n 1 file changed", "diff --git a/main.go b/main.go\n+x\n", 0)
	for _, want := range []string{
		"## Original request\n\nAdd a --verbose flag to the CLI",
		"**User:** Now add tests",
		"**Assistant:** I will read main.go.",
		"I will read main.go.\n\nDone with the flag.",
		"- [x] parse flag", "- [~] wire it", "- [ ] tests",
		"Edited:\n- main.go\n", "Read:\n- README.md\n",
		"main.go | 3 ++-",
		"```diff\ndiff --git a/main.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, bad := range []string{"secret thoughts", "failed.go", "- [ ] old", "ls\n"} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected %q in:\n%s", bad, out)
		}
	}
	// The original prompt is not repeated as a conversation turn.
	if strings.Contains(out, "**User:** Add a --verbose") {
		t.Errorf("first prompt duplicated:\n%s", out)
	}
	if !strings.HasSuffix(out, agent.HandoffClosing+"\n") {
		t.Errorf("closing line missing:\n%s", out)
	}
	if out != agent.BuildHandoff(sampleEvents(), " main.go | 3 ++-\n 1 file changed", "diff --git a/main.go b/main.go\n+x\n", 0) {
		t.Error("not deterministic")
	}
}

func TestBuildHandoffEmpty(t *testing.T) {
	out := agent.BuildHandoff(nil, "", "", 1000)
	if !strings.HasSuffix(out, agent.HandoffClosing+"\n") || strings.Contains(out, "## ") {
		t.Errorf("%q", out)
	}
}

func TestBuildHandoffBudget(t *testing.T) {
	var evs []agent.Event
	evs = append(evs, user("first prompt"))
	for i := 0; i < 40; i++ {
		evs = append(evs, asst(fmt.Sprintf("m%d", i), 0, fmt.Sprintf("answer %d %s", i, strings.Repeat("lorem ipsum ", 300))))
		evs = append(evs, user(fmt.Sprintf("question %d", i)))
	}
	hunks := strings.Repeat("+added line of code here\n", 5000)
	for _, budget := range []int{600, 2000, 5000, 20000} {
		out := agent.BuildHandoff(evs, " f | 1 +\n", hunks, budget)
		if len(out) > budget*4 {
			t.Errorf("budget %d tokens: output %d chars > %d", budget, len(out), budget*4)
		}
		if !strings.HasSuffix(out, agent.HandoffClosing+"\n") || !strings.Contains(out, "first prompt") {
			t.Errorf("budget %d: lost fixed parts:\n%.300s", budget, out)
		}
		// Newest turn survives, oldest is dropped first.
		if budget >= 2000 && !strings.Contains(out, "question 39") {
			t.Errorf("budget %d: newest turn missing", budget)
		}
		if budget <= 5000 && strings.Contains(out, "answer 1 ") {
			t.Errorf("budget %d: old turn kept", budget)
		}
	}
	// Hunks are capped at 8k tokens even with a huge budget.
	out := agent.BuildHandoff(evs, "", hunks, 1_000_000)
	i := strings.Index(out, "## Diff hunks")
	j := strings.Index(out[i:], "\n"+agent.HandoffClosing)
	if hunkLen := j; hunkLen > agent.HandoffMaxHunkTokens*4+64 || !strings.Contains(out, "(diff truncated)") {
		t.Errorf("hunk section %d chars, truncated=%v", hunkLen, strings.Contains(out, "(diff truncated)"))
	}
	// A diff that fits is not marked truncated.
	if strings.Contains(agent.BuildHandoff(evs, "", "+a\n", 100000), "truncated") {
		t.Error("small diff marked truncated")
	}
}

func TestBuildHandoffFenceAndUTF8(t *testing.T) {
	out := agent.BuildHandoff([]agent.Event{user("p")}, "", "+```go\n+x\n+```\n", 0)
	if !strings.Contains(out, "````diff\n+```go") {
		t.Errorf("fence not widened:\n%s", out)
	}
	long := strings.Repeat("héllo wörld ", 2000)
	out = agent.BuildHandoff([]agent.Event{user(long)}, "", "", 500)
	if !strings.Contains(out, "[trimmed]") {
		t.Errorf("long prompt not trimmed")
	}
	for _, r := range out {
		if r == '�' {
			t.Fatal("split a rune")
		}
	}
}
