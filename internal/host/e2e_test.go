package host

import (
	"testing"

	"github.com/sid/harness/internal/agent"
)

// TestE2EWorkflow exercises start -> stream -> permission round-trip -> JSONL
// replay -> reconnect on the Unix socket while the host is still alive -> shutdown.
func TestE2EWorkflow(t *testing.T) {
	dir := tdir(t)
	script := []string{
		txt("hello"),
		`{"t":"perm","id":"perm1"}`,
	}
	c, spec := launch(t, dir, "e2e1", script)
	head := collect(t, c, isKind(agent.KindPermissionRequest))
	if err := c.Permission("perm1", agent.Decision{Behavior: agent.BehaviorAllow}); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(agent.UserMessage{Text: "continue"}); err != nil {
		t.Fatal(err)
	}
	tail := collect(t, c, isKind(agent.KindRunResult))
	all := append(head, tail...)
	contiguous(t, all, 1)

	log, err := ReadLog(dir, spec.RunID, 1)
	if err != nil || len(log) != len(all) {
		t.Fatalf("log len=%d all=%d err=%v", len(log), len(all), err)
	}
	contiguous(t, log, 1)

	c.Close()
	c2, err := Attach(spec.DataDir, spec.RunID, 1)
	if err != nil {
		t.Fatal(err)
	}
	replay := collect(t, c2, isKind(agent.KindRunResult))
	if len(replay) != len(log) {
		t.Fatalf("full replay %d != log %d", len(replay), len(log))
	}
	contiguous(t, replay, 1)
	c2.Close()

	mid := log[len(log)/2].Seq
	c3, err := Attach(spec.DataDir, spec.RunID, mid)
	if err != nil {
		t.Fatal(err)
	}
	partial := collect(t, c3, isKind(agent.KindRunResult))
	wantPartial := len(log) - int(mid) + 1
	if len(partial) != wantPartial {
		t.Fatalf("partial %d want %d", len(partial), wantPartial)
	}
	contiguous(t, partial, mid)
	c3.Shutdown()
	collect(t, c3, nil)
	waitScanDead(t, dir)
	if _, err := Attach(spec.DataDir, spec.RunID, 0); err != ErrHostGone {
		t.Fatalf("attach after exit: %v", err)
	}
}

func TestE2ECrashLeavesLog(t *testing.T) {
	dir := tdir(t)
	c, spec := launch(t, dir, "e2e2", []string{txt("x")}, "FAKE_EXIT=7")
	collect(t, c, nil)
	if code, ok := c.ExitCode(); !ok || code != 7 {
		t.Fatalf("exit %d ok=%v", code, ok)
	}
	waitScanDead(t, dir)
	log, err := ReadLog(dir, spec.RunID, 1)
	if err != nil || len(log) == 0 {
		t.Fatalf("log %v %v", len(log), err)
	}
	if _, err := Attach(spec.DataDir, spec.RunID, 0); err != ErrHostGone {
		t.Fatalf("attach: %v", err)
	}
}
