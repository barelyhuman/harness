package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sid/harness/internal/host"
)

// TestClaudeEndToEnd drives SessionService against the real Claude CLI.
// Opt in with HARNESS_IT_CLAUDE=1 (uses the haiku model).
func TestClaudeEndToEnd(t *testing.T) {
	if os.Getenv("HARNESS_IT_CLAUDE") != "1" {
		t.Skip("set HARNESS_IT_CLAUDE=1")
	}
	e := newEnv(t)
	s := e.session(t, "claude", "haiku", "")
	t.Cleanup(func() {
		if c := e.sup.get(s.ID).liveClient(); c != nil {
			c.Shutdown()
		}
	})
	_ = host.ErrHostGone
	if err := e.svc.Send(s.ID, SendMessage{Text: "Reply with exactly the word: pong"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if tr := e.transcript(t, s.ID); strings.Contains(strings.ToLower(tr), "pong") && e.dbStatus(s.ID) == StatusIdle {
			t.Log(tr)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no reply; status=%s transcript=%s", e.dbStatus(s.ID), e.transcript(t, s.ID))
}
