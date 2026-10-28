package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sid/harness/internal/agent"
)

func TestMainRequiresSpec(t *testing.T) {
	if err := Main([]string{"harness", "--agent-host", "--run", "r1"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestMainBadSpecJSON(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(sp, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Main([]string{"harness", "--agent-host", "--spec", sp}); err == nil {
		t.Fatal("expected error")
	}
}

func TestMainRunMismatch(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "spec.json")
	spec := Spec{RunID: "a", DataDir: dir, Provider: "fake", StartOpts: agent.StartOpts{Cwd: dir}}
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Main([]string{"harness", "--agent-host", "--run", "b", "--spec", sp}); err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestReadLogNoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(runsDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	p := logPath(dir, "r")
	line := `{"seq":1,"runId":"r","kind":"text_delta","data":{"text":"x"}}`
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	evs, err := ReadLog(dir, "r", 1)
	if err != nil || len(evs) != 1 || evs[0].Seq != 1 {
		t.Fatalf("evs=%v err=%v", evs, err)
	}
}
