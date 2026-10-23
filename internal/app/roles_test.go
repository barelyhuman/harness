package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sid/harness/internal/workspace"
)

func TestSettingString(t *testing.T) {
	v := map[string]any{"agents": map[string]any{"planner": "claude/opus"}, "agents.executor": "cursor/x"}
	if got := settingString(v, "planner"); got != "claude/opus" {
		t.Fatalf("nested: %q", got)
	}
	if got := settingString(v, "executor"); got != "cursor/x" {
		t.Fatalf("dotted: %q", got)
	}
	if got := settingString(v, "explorer"); got != "" {
		t.Fatalf("missing: %q", got)
	}
}

func TestSetAgentRoleOverridesAndClears(t *testing.T) {
	e := newEnv(t)
	info, err := e.svc.AgentRoles(e.proj.ID)
	if err != nil || info.Roles.SpecsDir != DefaultSpecsDir || info.Roles.Planner != "" {
		t.Fatalf("defaults: %+v %v", info, err)
	}
	if _, err := e.svc.SetAgentRole(e.proj.ID, "planner", "nope"); err == nil {
		t.Fatal("bad model key accepted")
	}
	if _, err := e.svc.SetAgentRole(e.proj.ID, "boss", "fake/m1"); err == nil {
		t.Fatal("bad role accepted")
	}
	info, err = e.svc.SetAgentRole(e.proj.ID, "planner", "fake/m1")
	if err != nil || info.Roles.Planner != "fake/m1" || info.Project.Planner != "fake/m1" {
		t.Fatalf("set: %+v %v", info, err)
	}
	info, _ = e.svc.SetAgentRole(e.proj.ID, "planner", "")
	if info.Roles.Planner != "" {
		t.Fatalf("clear: %+v", info)
	}
}

func TestCreateSessionPlanUsesPlanner(t *testing.T) {
	e := newEnv(t)
	e.svc.SetAgentRole(e.proj.ID, "planner", "fake2/x1")
	s, err := e.svc.CreateSession(e.proj.ID, CreateSessionOpts{Title: "p", Mode: "plan"})
	if err != nil || s.Provider != "fake2" || s.Model != "fake2/x1" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestSpecFileWriteAndStatus(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.md")
	sess := workspace.Session{ID: "abcdef123", Title: "Add a thing", Model: "claude/opus"}
	if err := writeAtomic(p, renderSpec(sess, SpecProposed, "# Plan", time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := setSpecStatus(p, SpecApproved); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "status: approved\n") || !strings.HasSuffix(string(b), "# Plan\n") {
		t.Fatal(string(b))
	}
	if n := specFileName(sess, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)); n != "2026-01-02-add-a-thing-abcdef.md" {
		t.Fatal(n)
	}
}

func specOf(t *testing.T, e *env, id string) string {
	t.Helper()
	s, _ := e.svc.sessions.Get(id)
	if s.SpecPath == "" {
		return ""
	}
	b, _ := os.ReadFile(s.SpecPath)
	return string(b)
}

func proposePlan(t *testing.T, e *env, id string) {
	t.Helper()
	if err := e.svc.Send(id, SendMessage{Text: "plan"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "plan proposed", func() bool {
		return e.dbStatus(id) == StatusNeedsApproval && strings.Contains(specOf(t, e, id), "status: proposed")
	})
	s, _ := e.svc.sessions.Get(id)
	if !strings.HasPrefix(s.SpecPath, filepath.Join(e.proj.Path, DefaultSpecsDir)) {
		t.Fatalf("spec path %q", s.SpecPath)
	}
	if !strings.Contains(specOf(t, e, id), "1. do it") {
		t.Fatal(specOf(t, e, id))
	}
}

func TestPlanApproveWithoutExecutorPersistsMode(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", "")
	e.svc.setCol(s.ID, "mode", "plan")
	proposePlan(t, e, s.ID)
	r, err := e.svc.Respond(s.ID, "req1", RespondOpts{Behavior: "allow"})
	if err != nil || r.ForkedSessionID != "" {
		t.Fatalf("%+v %v", r, err)
	}
	waitFor(t, "mode from provider", func() bool {
		got, _ := e.svc.sessions.Get(s.ID)
		return got.Mode == "default"
	})
	if !strings.Contains(specOf(t, e, s.ID), "status: approved") {
		t.Fatal(specOf(t, e, s.ID))
	}
}

func TestPlanApproveSwitchesToSameProviderExecutor(t *testing.T) {
	e := newEnv(t)
	e.svc.SetAgentRole(e.proj.ID, "executor", "fake/m2")
	s := e.session(t, "fake", "fake/m1", "")
	proposePlan(t, e, s.ID)
	r, err := e.svc.Respond(s.ID, "req1", RespondOpts{Behavior: "allow"})
	if err != nil || r.ForkedSessionID != "" {
		t.Fatalf("%+v %v", r, err)
	}
	got, _ := e.svc.sessions.Get(s.ID)
	if got.Model != "fake/m2" {
		t.Fatalf("model %q", got.Model)
	}
}

func TestPlanFeedbackKeepsProposed(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", "")
	proposePlan(t, e, s.ID)
	if _, err := e.svc.Respond(s.ID, "req1", RespondOpts{Behavior: "deny", Message: "smaller"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "idle", func() bool { return e.dbStatus(s.ID) == StatusIdle })
	if !strings.Contains(specOf(t, e, s.ID), "status: proposed") {
		t.Fatal(specOf(t, e, s.ID))
	}
}

func TestPlanApproveForksCrossProviderExecutor(t *testing.T) {
	e := newEnv(t)
	s := e.session(t, "fake", "fake/m1", "worktree")
	proposePlan(t, e, s.ID)
	// A per-approval executor overrides the (unset) project role.
	r, err := e.svc.Respond(s.ID, "req1", RespondOpts{Behavior: "allow", Executor: "fake2/x1"})
	if err != nil || r.ForkedSessionID == "" {
		t.Fatalf("%+v %v", r, err)
	}
	f, _ := e.svc.sessions.Get(r.ForkedSessionID)
	if f.Provider != "fake2" || f.WorktreePath != s.WorktreePath {
		t.Fatalf("fork %+v", f)
	}
	waitFor(t, "handoff carries plan", func() bool {
		tr := e.transcript(t, f.ID)
		return strings.Contains(tr, "Approved plan") && strings.Contains(tr, "1. do it")
	})
	if !strings.Contains(specOf(t, e, s.ID), "status: approved") {
		t.Fatal(specOf(t, e, s.ID))
	}
	info, _ := e.svc.AgentRoles(e.proj.ID)
	if info.Roles.Executor != "" {
		t.Fatal("per-approval executor leaked into project roles")
	}
}
