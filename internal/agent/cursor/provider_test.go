package cursor_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/agent/cursor"
)

func TestDetectAndCommand(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 2026.10.01-e373342\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &cursor.Provider{Binary: bin}
	in, err := p.Detect(context.Background())
	if err != nil || !in.Found || in.Version != "2026.10.01-e373342" || in.Path != bin {
		t.Fatalf("%+v %v", in, err)
	}
	cmd, err := p.Command(agent.StartOpts{Cwd: "/w", Env: []string{"A=1"}})
	if err != nil || cmd.Path != bin || strings.Join(cmd.Args, " ") != "acp" || cmd.Dir != "/w" || cmd.Env[0] != "A=1" {
		t.Fatalf("%+v %v", cmd, err)
	}
	in, _ = (&cursor.Provider{Binary: filepath.Join(dir, "nope")}).Detect(context.Background())
	if in.Found {
		t.Fatal("missing binary reported as found")
	}
	if p.ID() != "cursor" {
		t.Fatal(p.ID())
	}
}

type pipeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (p *pipeBuf) Write(b []byte) (int, error) { p.mu.Lock(); defer p.mu.Unlock(); return p.b.Write(b) }
func (p *pipeBuf) String() string              { p.mu.Lock(); defer p.mu.Unlock(); return p.b.String() }

func TestModelsCacheRoundTrip(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "c", "models.json")
	p := &cursor.Provider{CachePath: cache}
	ms, _ := p.Models(context.Background())
	if len(ms) == 0 || ms[1].ID != "composer-2.5[fast=true]" || ms[1].Provider != "cursor" {
		t.Fatalf("static: %+v", ms)
	}
	// A session/new response refreshes the cache through NewSession.
	var w pipeBuf
	s := p.NewSession(&w, agent.StartOpts{Cwd: "/w"})
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background()) }()
	respond := func(id int, res any) {
		deadline := time.Now().Add(3 * time.Second)
		for strings.Count(w.String(), "\n") < id {
			if time.Now().After(deadline) {
				t.Fatal("timeout")
			}
			time.Sleep(time.Millisecond)
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": res})
		if _, err := s.Feed(b); err != nil {
			t.Fatal(err)
		}
	}
	respond(1, map[string]any{"authMethods": []any{map[string]any{"id": "cursor_login"}}})
	respond(2, map[string]any{})
	respond(3, map[string]any{"sessionId": "s", "modes": map[string]any{"currentModeId": "agent"},
		"models": map[string]any{"currentModelId": "x[a=1]", "availableModels": []any{map[string]any{"modelId": "x[a=1]", "name": "x"}}}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ms, _ = p.Models(context.Background())
	if len(ms) != 1 || ms[0].ID != "x[a=1]" || ms[0].Provider != "cursor" {
		t.Fatalf("cached: %+v", ms)
	}
	var first map[string]any
	_ = json.Unmarshal([]byte(strings.SplitN(w.String(), "\n", 2)[0]), &first)
	if first["method"] != "initialize" {
		t.Fatalf("%v", first)
	}
}

// TestIntegrationRealAgent runs the real `agent acp`. Opt in with
// HARNESS_IT_CURSOR=1 (needs a logged-in Cursor CLI; uses a cheap model).
func TestIntegrationRealAgent(t *testing.T) {
	if os.Getenv("HARNESS_IT_CURSOR") != "1" {
		t.Skip("set HARNESS_IT_CURSOR=1 to run against the real Cursor agent")
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	p := &cursor.Provider{CachePath: filepath.Join(t.TempDir(), "models.json")}
	opts := agent.StartOpts{Cwd: repo, Model: "composer-2.5[fast=true]"}
	cmd, err := p.Command(opts)
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Dir = cmd.Dir
	stdin, _ := c.StdinPipe()
	stdout, _ := c.StdoutPipe()
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	s := p.NewSession(stdin, opts)

	evCh := make(chan agent.Event, 4096)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for sc.Scan() {
			evs, err := s.Feed(sc.Bytes())
			if err != nil {
				t.Logf("feed: %v", err)
			}
			for _, e := range evs {
				evCh <- e
			}
		}
		c.Wait()
		for _, e := range s.Closed(c.ProcessState.ExitCode(), stderr.String()) {
			evCh <- e
		}
		close(evCh)
	}()
	defer func() { _ = stdin.Close(); _ = c.Process.Kill() }()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v\n%s", err, stderr.String())
	}
	if _, err := s.SendUser(agent.UserMessage{Text: "Reply with just the word ok. Do not use any tools."}); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	sawInit, sawModels := false, false
	for e := range evCh {
		switch e.Kind {
		case agent.KindSessionInit:
			sawInit = true
		case agent.KindModelsList:
			sawModels = len(agent.MustDecode[agent.ModelsList](e).Models) > 0
		case agent.KindTextDelta:
			text.WriteString(agent.MustDecode[agent.BlockDelta](e).Text)
		case agent.KindPermissionRequest:
			id := agent.MustDecode[agent.PermissionRequest](e).ReqID
			if _, err := s.RespondPermission(id, agent.Decision{Behavior: agent.BehaviorDeny}); err != nil {
				t.Fatal(err)
			}
		case agent.KindUsage:
			t.Error("ACP must not report usage")
		case agent.KindError:
			t.Fatalf("error event: %s", e.Data)
		case agent.KindRunResult:
			rr := agent.MustDecode[agent.RunResult](e)
			if rr.Status != agent.ResultSuccess {
				t.Fatalf("run result %+v", rr)
			}
			if !sawInit || !sawModels || !strings.Contains(strings.ToLower(text.String()), "ok") {
				t.Fatalf("init=%v models=%v text=%q", sawInit, sawModels, text.String())
			}
			return
		}
	}
	t.Fatalf("process ended without run_result\n%s", stderr.String())
}
