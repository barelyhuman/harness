package app

import (
	"encoding/json"
	"testing"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/stream"
)

func TestRunStateObserve(t *testing.T) {
	toolIn, _ := json.Marshal(map[string]string{"file_path": "/tmp/x.go"})
	evs := []struct {
		kind agent.Kind
		data any
	}{
		{agent.KindUserMessage, agent.UserMessageData{Text: "hi"}},
		{agent.KindStatus, agent.Status{State: "requesting"}},
		{agent.KindSessionInit, agent.SessionInit{ProviderSessionID: "ps"}},
		{agent.KindPermissionRequest, agent.PermissionRequest{ReqID: "r1"}},
		{agent.KindPlanProposal, agent.PlanProposal{ReqID: "p1", Text: "plan"}},
		{agent.KindPermissionResolved, agent.PermissionResolved{ReqID: "r1"}},
		{agent.KindPermissionCancelled, agent.PermissionCancelled{ReqID: "p1"}},
		{agent.KindToolStart, agent.ToolStart{ID: "t1", Input: toolIn}},
		{agent.KindToolInputDone, agent.ToolInputDone{ID: "t1", Input: toolIn}},
		{agent.KindToolResult, agent.ToolResult{ID: "t1", OK: true}},
		{agent.KindRunResult, agent.RunResult{Status: agent.ResultError}},
	}
	st := newRunState()
	if st.status() != StatusIdle {
		t.Fatal(st.status())
	}
	var psid string
	for _, e := range evs {
		ev := agent.NewEvent(e.kind, "", e.data)
		touch, p := st.observe(ev)
		if e.kind == agent.KindSessionInit && p != "ps" {
			t.Fatalf("psid %q", p)
		}
		psid = p
		if e.kind == agent.KindToolResult && touch != "/tmp/x.go" {
			t.Fatalf("touch %q", touch)
		}
	}
	_ = psid
	if st.status() != StatusError {
		t.Fatalf("status %s", st.status())
	}
	st = newRunState()
	st.observe(agent.NewEvent(agent.KindError, "", agent.ErrorData{Fatal: true, Message: "boom"}))
	if st.status() != StatusError || st.busy {
		t.Fatalf("%+v", st)
	}
	st = newRunState()
	st.observe(agent.NewEvent(agent.KindPermissionRequest, "", agent.PermissionRequest{ReqID: "x"}))
	if st.status() != StatusNeedsApproval {
		t.Fatal(st.status())
	}
}

func TestInputPath(t *testing.T) {
	if p := inputPath(json.RawMessage(`{"path":"/a"}`)); p != "/a" {
		t.Fatal(p)
	}
	if p := inputPath(json.RawMessage(`{`)); p != "" {
		t.Fatal(p)
	}
	if p := inputPath(nil); p != "" {
		t.Fatal(p)
	}
}

func TestBusSinkReopenAfterClose(t *testing.T) {
	bus := stream.New()
	if err := bus.Start(); err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	s := newBusSink(bus)
	s.Ensure("t")
	s.Send("t", map[string]any{"n": 1})
	s.get("t").Close()
	s.Send("t", map[string]any{"n": 2})
}

func TestClientAlive(t *testing.T) {
	if clientAlive(nil) {
		t.Fatal("nil alive")
	}
}
