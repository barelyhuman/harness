package acp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/agent/acp"
	"github.com/sid/harness/internal/transcript"
)

var update = flag.Bool("update", false, "rewrite golden files")

// syncBuf is a goroutine-safe writer the replay can wait on.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, l := range strings.Split(s.b.String(), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (s *syncBuf) waitLines(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(s.lines()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("session wrote %d lines, fixture needs %d:\n%s", len(s.lines()), n, strings.Join(s.lines(), "\n"))
		}
		time.Sleep(time.Millisecond)
	}
}

type fxLine struct {
	Dir string         `json:"dir"`
	Msg map[string]any `json:"msg"`
	raw []byte
}

func loadFixture(t *testing.T, path string) []fxLine {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []fxLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var l fxLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		if l.Dir == "stderr" {
			continue
		}
		l.raw, _ = json.Marshal(l.Msg)
		out = append(out, l)
	}
	return out
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func obj(m map[string]any, k string) map[string]any {
	o, _ := m[k].(map[string]any)
	return o
}
func idStr(m map[string]any) string { return fmt.Sprint(m["id"]) }

func fixtureNames(t *testing.T) []string {
	m, err := filepath.Glob("testdata/*.jsonl")
	if err != nil || len(m) == 0 {
		t.Fatal("no fixtures", err)
	}
	var names []string
	for _, p := range m {
		names = append(names, strings.TrimSuffix(filepath.Base(p), ".jsonl"))
	}
	return names
}

var handshakeMethods = map[string]bool{
	"initialize": true, "authenticate": true, "session/new": true, "session/load": true,
	"session/set_model": true, "session/set_mode": true,
}

// decisionFor maps what the spike answered to a permission/plan request to
// the Decision a UI would have made.
func decisionFor(resp map[string]any) agent.Decision {
	oc := obj(obj(resp, "result"), "outcome")
	switch str(oc, "outcome") {
	case "accepted":
		return agent.Decision{Behavior: agent.BehaviorAllow}
	case "rejected", "cancelled":
		return agent.Decision{Behavior: agent.BehaviorDeny}
	}
	switch str(oc, "optionId") {
	case "allow-always":
		return agent.Decision{Behavior: agent.BehaviorAllowAlways}
	case "reject-once":
		return agent.Decision{Behavior: agent.BehaviorDeny}
	}
	return agent.Decision{Behavior: agent.BehaviorAllow}
}

type replayResult struct {
	events []agent.Event
	got    []map[string]any
	want   []map[string]any
	full   bool // handshake completed in the fixture
}

func replay(t *testing.T, name string) replayResult {
	t.Helper()
	lines := loadFixture(t, filepath.Join("testdata", name+".jsonl"))

	opts := agent.StartOpts{RunID: "run"}
	full, prompted := false, false
	for _, l := range lines {
		if l.Dir != "out" {
			continue
		}
		p := obj(l.Msg, "params")
		switch str(l.Msg, "method") {
		case "session/new":
			opts.Cwd, full = str(p, "cwd"), true
		case "session/load":
			opts.Cwd, opts.ResumeID, full = str(p, "cwd"), str(p, "sessionId"), true
		case "session/set_model":
			if !prompted {
				opts.Model = str(p, "modelId")
			}
		case "session/set_mode":
			if !prompted {
				opts.Mode = str(p, "modeId")
			}
		case "session/prompt":
			prompted = true
		}
	}

	buf := &syncBuf{}
	s := acp.NewSession(buf, opts, acp.Config{
		ProviderID: "cursor", AuthMethodID: "cursor_login",
		Now: func() time.Time { return time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) },
	})
	seq := &agent.Sequencer{RunID: "run", Now: func() time.Time { return time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) }}
	var events []agent.Event
	add := func(evs []agent.Event) { events = append(events, seq.Stamp(evs)...) }

	var want []map[string]any
	agentReqMethod := map[string]string{} // agent request id -> method
	idMap := map[string]string{}          // fixture request id -> our id
	skipped := map[string]bool{}          // fixture ids of requests we do not send
	startDone := make(chan error, 1)
	inHandshake := true

	lastWrittenID := func() string {
		ls := buf.lines()
		var m map[string]any
		_ = json.Unmarshal([]byte(ls[len(ls)-1]), &m)
		return idStr(m)
	}

	for i, l := range lines {
		m := l.Msg
		method := str(m, "method")
		if l.Dir == "out" {
			if method == "" { // our response to an agent request
				want = append(want, m)
				continue
			}
			if inHandshake && !handshakeMethods[method] {
				inHandshake = false
			}
			if inHandshake {
				want = append(want, m)
				if method == "initialize" {
					go func() { startDone <- s.Start(context.Background()) }()
				}
				continue
			}
			p := obj(m, "params")
			var err error
			issued := true
			switch method {
			case "session/prompt":
				var evs []agent.Event
				text := str(obj(m, "params")["prompt"].([]any)[0].(map[string]any), "text")
				evs, err = s.SendUser(agent.UserMessage{Text: text})
				add(evs)
			case "session/cancel":
				err = s.Interrupt()
			case "session/set_model":
				err = s.SetModel(str(p, "modelId"))
			case "session/set_mode":
				err = s.SetMode(str(p, "modeId"))
			default: // session/list, session/set_config_option: no API
				skipped[idStr(m)] = true
				issued = false
			}
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			if issued {
				want = append(want, m)
				if m["id"] != nil {
					idMap[idStr(m)] = lastWrittenID()
				}
			}
			continue
		}

		// dir == "in"
		switch {
		case method == "": // response to one of our requests
			if skipped[idStr(m)] {
				continue
			}
			buf.waitLines(t, len(want))
			if our, ok := idMap[idStr(m)]; ok {
				n, _ := strconv.Atoi(our)
				m["id"] = float64(n)
			}
			b, _ := json.Marshal(m)
			evs, err := s.Feed(b)
			if err != nil {
				t.Fatalf("feed: %v\n%s", err, b)
			}
			add(evs)
		default:
			evs, err := s.Feed(l.raw)
			if err != nil {
				t.Fatalf("feed: %v\n%s", err, l.raw)
			}
			add(evs)
			if m["id"] == nil {
				continue
			}
			agentReqMethod[idStr(m)] = method
			if method != "session/request_permission" && method != "cursor/create_plan" {
				continue
			}
			var resp map[string]any
			for _, f := range lines[i+1:] {
				if f.Dir == "out" && str(f.Msg, "method") == "" && idStr(f.Msg) == idStr(m) {
					resp = f.Msg
					break
				}
			}
			if resp == nil {
				continue
			}
			rev, err := s.RespondPermission(idStr(m), decisionFor(resp))
			if err != nil {
				t.Fatal(err)
			}
			add(rev)
		}
	}

	if full {
		select {
		case err := <-startDone:
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Start did not complete")
		}
	} else {
		buf.waitLines(t, len(want)+1) // authenticate follows initialize
		add(s.Closed(0, ""))
		if err := <-startDone; err == nil {
			t.Fatal("Start should fail when the process closes mid-handshake")
		}
	}

	norm := func(m map[string]any, fixture bool) map[string]any {
		m = cloneMap(m)
		if m["method"] != nil && m["id"] != nil {
			delete(m, "id") // our request ids are renumbered
		}
		if str(m, "method") == "initialize" {
			delete(obj(m, "params"), "clientInfo")
		}
		if fixture && m["method"] == nil {
			switch agentReqMethod[idStr(m)] {
			case "cursor/update_todos": // the spike answered -32601; we reply {}
				delete(m, "error")
				m["result"] = map[string]any{}
			case "session/request_permission", "cursor/create_plan":
				if oc := obj(obj(m, "result"), "outcome"); str(oc, "outcome") == "cancelled" {
					m["result"] = map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "reject-once"}}
				}
			}
		}
		return m
	}
	var gotN, wantN []map[string]any
	for _, l := range buf.lines() {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad json written %q: %v", l, err)
		}
		gotN = append(gotN, norm(m, false))
	}
	for _, m := range want {
		wantN = append(wantN, norm(m, true))
	}
	return replayResult{events: events, got: gotN, want: wantN, full: full}
}

func cloneMap(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func golden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s differs from golden (run with -update to accept)", path)
	}
}

func TestReplayFixtures(t *testing.T) {
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			r := replay(t, name)

			got := r.got
			if !r.full { // truncated fixture: we legitimately went one step further
				got = got[:len(r.want)]
			}
			if len(got) != len(r.want) {
				t.Fatalf("wrote %d lines, fixture has %d", len(got), len(r.want))
			}
			for i := range got {
				if !reflect.DeepEqual(got[i], r.want[i]) {
					gj, _ := json.Marshal(got[i])
					wj, _ := json.Marshal(r.want[i])
					t.Errorf("written line %d differs\n got: %s\nwant: %s", i, gj, wj)
				}
			}

			var eb bytes.Buffer
			for i, e := range r.events {
				if e.Seq != uint64(i+1) {
					t.Fatalf("seq gap at %d: %d", i, e.Seq)
				}
				if e.Kind == agent.KindUsage {
					t.Errorf("ACP must not emit usage")
				}
				b, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				eb.Write(b)
				eb.WriteByte('\n')
			}
			golden(t, filepath.Join("testdata", "golden", name+".events.jsonl"), eb.Bytes())

			b := transcript.NewBuilder()
			var reduced []transcript.Block
			for _, e := range r.events {
				for _, p := range b.Apply(e) {
					reduced = transcript.Reduce(reduced, p)
				}
			}
			snap := b.Snapshot()
			rj, _ := json.Marshal(reduced)
			sj, _ := json.Marshal(snap)
			if len(reduced) == 0 && len(snap) == 0 {
				rj = sj
			}
			if string(rj) != string(sj) {
				t.Errorf("patches do not reproduce snapshot\npatches: %s\nsnapshot: %s", rj, sj)
			}
			bj, err := json.MarshalIndent(snap, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden(t, filepath.Join("testdata", "golden", name+".blocks.json"), append(bj, '\n'))
		})
	}
}
