package agent

import (
	"encoding/json"
	"testing"
)

func TestDiffStat(t *testing.T) {
	add, rem := DiffStat("--- a\n+++ b\n-old\n+new\n+more\n")
	if add != 2 || rem != 1 {
		t.Fatalf("add=%d rem=%d", add, rem)
	}
}

func TestDiffNewFileEmpty(t *testing.T) {
	got := DiffNewFile("foo.go", "")
	if got != "--- /dev/null\n+++ b/foo.go\n" {
		t.Fatalf("%q", got)
	}
}

func TestDiffReplace(t *testing.T) {
	got := DiffReplace("x.go", "a\nb", "c")
	if add, rem := DiffStat(got); add != 1 || rem != 2 {
		t.Fatalf("stat add=%d rem=%d\n%s", add, rem, got)
	}
}

func TestDiffFromStructuredPatch(t *testing.T) {
	raw := json.RawMessage(`[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":2,"lines":["-old","+new","+line"]}]`)
	got, ok := DiffFromStructuredPatch("f.go", raw)
	if !ok || got == "" {
		t.Fatal(ok, got)
	}
}

func TestDiffFromToolInput(t *testing.T) {
	cases := []struct {
		tool string
		in   string
		want bool
	}{
		{"Edit", `{"file_path":"a.go","old_string":"x","new_string":"y"}`, true},
		{"Write", `{"file_path":"b.go","content":"z"}`, true},
		{"Bash", `{"command":"ls"}`, false},
		{"Edit", `{"bad":true}`, false},
	}
	for _, c := range cases {
		got := DiffFromToolInput(c.tool, json.RawMessage(c.in))
		if (got != "") != c.want {
			t.Fatalf("%s: %q want nonempty=%v", c.tool, got, c.want)
		}
	}
}

func TestNewEventBadPayload(t *testing.T) {
	// chan cannot be marshaled to JSON; NewEvent should emit KindError.
	e := NewEvent(KindTextDelta, "", make(chan int))
	if e.Kind != KindError {
		t.Fatalf("kind %s", e.Kind)
	}
}

func TestDecodeErrors(t *testing.T) {
	e := NewEvent(KindTextDelta, "", BlockDelta{Text: "hi"})
	if _, err := Decode[int](e); err == nil {
		t.Fatal("expected decode error")
	}
	if v := MustDecode[int](e); v != 0 {
		t.Fatal(v)
	}
}
