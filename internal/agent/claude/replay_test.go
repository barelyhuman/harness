package claude_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/agent/claude"
	"github.com/sid/harness/internal/transcript"
)

var update = flag.Bool("update", false, "rewrite golden files")

func readLines(t *testing.T, path string) [][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) > 0 {
			out = append(out, append([]byte(nil), sc.Bytes()...))
		}
	}
	return out
}

type stdinFixture struct {
	users    []agent.UserMessage
	ctlReqs  []map[string]any // in order, supported subtypes only
	resps    map[string]map[string]any
	expected []map[string]any // lines the session should write, in order
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func obj(m map[string]any, k string) map[string]any {
	o, _ := m[k].(map[string]any)
	return o
}

func loadStdin(t *testing.T, path string) stdinFixture {
	t.Helper()
	fx := stdinFixture{resps: map[string]map[string]any{}}
	for _, l := range readLines(t, path) {
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatal(err)
		}
		switch str(m, "type") {
		case "user":
			c := obj(m, "message")["content"]
			fx.users = append(fx.users, agent.UserMessage{Text: c.(string)})
			fx.expected = append(fx.expected, m)
		case "control_request":
			sub := str(obj(m, "request"), "subtype")
			switch sub {
			case "interrupt", "set_model", "list_models":
				fx.ctlReqs = append(fx.ctlReqs, m)
				fx.expected = append(fx.expected, m)
			case "set_permission_mode":
				fx.ctlReqs = append(fx.ctlReqs, m)
				if str(obj(m, "request"), "mode") != "bogus" { // refused locally
					fx.expected = append(fx.expected, m)
				}
			}
		case "control_response":
			r := obj(m, "response")
			fx.resps[str(r, "request_id")] = m
			fx.expected = append(fx.expected, m)
		}
	}
	return fx
}

func norm(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("bad json written %q: %v", line, err)
	}
	return m
}

func decisionFor(m map[string]any) agent.Decision {
	body := obj(obj(m, "response"), "response")
	d := agent.Decision{Behavior: agent.BehaviorAllow}
	switch str(body, "behavior") {
	case "deny":
		d.Behavior, d.Message = agent.BehaviorDeny, str(body, "message")
	default:
		if _, ok := body["updatedPermissions"]; ok {
			d.Behavior = agent.BehaviorAllowAlways
		}
		if in, ok := body["updatedInput"]; ok {
			d.UpdatedInput, _ = json.Marshal(in)
		}
	}
	return d
}

// replay drives a Session from a recorded scenario, playing the part of the
// host/UI at the points where the fixture shows we acted.
func replay(t *testing.T, name string) []agent.Event {
	t.Helper()
	dir := "testdata"
	out := readLines(t, filepath.Join(dir, name+".jsonl"))
	fx := loadStdin(t, filepath.Join(dir, name+".stdin.jsonl"))

	var buf bytes.Buffer
	cur := ""
	s := claude.NewSession(&buf, agent.StartOpts{Model: "haiku"}, claude.WithRequestIDs(func() string { return cur }))
	seq := &agent.Sequencer{RunID: "run", Now: func() time.Time { return time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) }}

	var events []agent.Event
	add := func(evs []agent.Event) { events = append(events, seq.Stamp(evs)...) }
	nextUser, nextCtl := 0, 0

	sendCtl := func(m map[string]any) {
		r := obj(m, "request")
		cur = str(m, "request_id")
		var err error
		switch str(r, "subtype") {
		case "interrupt":
			err = s.Interrupt()
		case "set_model":
			err = s.SetModel(str(r, "model"))
		case "set_permission_mode":
			err = s.SetMode(str(r, "mode"))
			if str(r, "mode") == "bogus" {
				if err == nil {
					t.Fatal("bogus mode accepted")
				}
				return
			}
		case "list_models":
			err = s.ListModels()
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, l := range out {
		var peek struct {
			Type      string `json:"type"`
			IsReplay  bool   `json:"isReplay"`
			RequestID string `json:"request_id"`
			Response  struct {
				RequestID string `json:"request_id"`
			} `json:"response"`
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		}
		_ = json.Unmarshal(l, &peek)
		if peek.Type == "user" && peek.IsReplay {
			if c, ok := peek.Message.Content.(string); ok && !strings.HasPrefix(c, "<local-command") {
				if nextUser >= len(fx.users) {
					t.Fatalf("echo for unsent user message %q", c)
				}
				if fx.users[nextUser].Text != c {
					t.Fatalf("echo %q != sent %q", c, fx.users[nextUser].Text)
				}
				evs, err := s.SendUser(fx.users[nextUser])
				if err != nil {
					t.Fatal(err)
				}
				add(evs)
				nextUser++
			}
		}
		if peek.Type == "control_response" {
			id := peek.Response.RequestID
			for i := nextCtl; i < len(fx.ctlReqs); i++ {
				if str(fx.ctlReqs[i], "request_id") == id {
					for ; nextCtl <= i; nextCtl++ {
						sendCtl(fx.ctlReqs[nextCtl])
					}
					break
				}
			}
		}
		evs, err := s.Feed(l)
		if err != nil {
			t.Fatalf("feed: %v\n%s", err, l)
		}
		add(evs)
		for _, e := range evs {
			var reqID string
			switch e.Kind {
			case agent.KindPermissionRequest:
				reqID = agent.MustDecode[agent.PermissionRequest](e).ReqID
			case agent.KindPlanProposal:
				reqID = agent.MustDecode[agent.PlanProposal](e).ReqID
			default:
				continue
			}
			resp, ok := fx.resps[reqID]
			if !ok {
				continue
			}
			rev, err := s.RespondPermission(reqID, decisionFor(resp))
			if err != nil {
				t.Fatal(err)
			}
			add(rev)
		}
	}

	// Compare everything we wrote with the recorded stdin.
	var got []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l != "" {
			got = append(got, norm(t, l))
		}
	}
	want := fx.expected
	// A user turn the CLI never echoed (bogus resume: it exited) is not sent.
	for len(want) > len(got) && str(want[len(want)-1], "type") == "user" {
		want = want[:len(want)-1]
	}
	if len(got) != len(want) {
		t.Fatalf("wrote %d lines, fixture has %d:\n%s", len(got), len(want), buf.String())
	}
	for i := range got {
		g, w := got[i], want[i]
		// Fixtures that omitted updatedInput exercise the optional path; we
		// always echo it, so drop it before comparing.
		if body := obj(obj(w, "response"), "response"); body != nil {
			if _, has := body["updatedInput"]; !has {
				delete(obj(obj(g, "response"), "response"), "updatedInput")
			}
		}
		if !reflect.DeepEqual(g, w) {
			gj, _ := json.Marshal(g)
			wj, _ := json.Marshal(w)
			t.Errorf("stdin line %d differs\n got: %s\nwant: %s", i, gj, wj)
		}
	}
	return events
}

func scenarios(t *testing.T) []string {
	m, err := filepath.Glob("testdata/*.stdin.jsonl")
	if err != nil || len(m) == 0 {
		t.Fatal("no fixtures", err)
	}
	var names []string
	for _, p := range m {
		names = append(names, strings.TrimSuffix(filepath.Base(p), ".stdin.jsonl"))
	}
	return names
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
	for _, name := range scenarios(t) {
		t.Run(name, func(t *testing.T) {
			events := replay(t, name)

			var eb bytes.Buffer
			for i, e := range events {
				if e.Seq != uint64(i+1) {
					t.Fatalf("seq gap at %d: %d", i, e.Seq)
				}
				b, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				eb.Write(b)
				eb.WriteByte('\n')
			}
			golden(t, filepath.Join("testdata", "golden", name+".events.jsonl"), eb.Bytes())

			// Build the transcript; the patch stream must reproduce Snapshot.
			b := transcript.NewBuilder()
			var reduced []transcript.Block
			for _, e := range events {
				for _, p := range b.Apply(e) {
					reduced = transcript.Reduce(reduced, p)
				}
			}
			snap := b.Snapshot()
			rj, _ := json.Marshal(reduced)
			sj, _ := json.Marshal(snap)
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
