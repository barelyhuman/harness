package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DiffStat counts added and removed lines in a unified diff.
func DiffStat(diff string) (added, removed int) {
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+++") || strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// DiffNewFile renders content as an all-added unified diff.
func DiffNewFile(path, content string) string {
	ls := splitLines(content)
	var b strings.Builder
	fmt.Fprintf(&b, "--- /dev/null\n+++ b/%s\n", strings.TrimPrefix(path, "/"))
	if len(ls) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", len(ls))
	for _, l := range ls {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}

// DiffReplace renders a whole-block replacement (all old lines removed, all
// new lines added). It is a preview, not a minimal diff.
func DiffReplace(path, oldText, newText string) string {
	o, n := splitLines(oldText), splitLines(newText)
	var b strings.Builder
	p := strings.TrimPrefix(path, "/")
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n@@ -1,%d +1,%d @@\n", p, p, len(o), len(n))
	for _, l := range o {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range n {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}

type patchHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// DiffFromStructuredPatch converts the CLI's tool_use_result.structuredPatch
// into a unified diff. ok is false when the patch is empty or malformed.
func DiffFromStructuredPatch(path string, raw json.RawMessage) (string, bool) {
	var hunks []patchHunk
	if len(raw) == 0 || json.Unmarshal(raw, &hunks) != nil || len(hunks) == 0 {
		return "", false
	}
	var b strings.Builder
	p := strings.TrimPrefix(path, "/")
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", p, p)
	for _, h := range hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		for _, l := range h.Lines {
			b.WriteString(l + "\n")
		}
	}
	return b.String(), true
}

// DiffFromToolInput builds a preview diff from an Edit/MultiEdit/Write tool
// input, or "" for other tools.
func DiffFromToolInput(tool string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	switch tool {
	case "Edit":
		var in struct {
			FilePath  string `json:"file_path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
			return ""
		}
		return DiffReplace(in.FilePath, in.OldString, in.NewString)
	case "MultiEdit":
		var in struct {
			FilePath string `json:"file_path"`
			Edits    []struct {
				OldString string `json:"old_string"`
				NewString string `json:"new_string"`
			} `json:"edits"`
		}
		if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
			return ""
		}
		var parts []string
		for _, e := range in.Edits {
			parts = append(parts, DiffReplace(in.FilePath, e.OldString, e.NewString))
		}
		return strings.Join(parts, "")
	case "Write":
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		if json.Unmarshal(input, &in) != nil || in.FilePath == "" {
			return ""
		}
		return DiffNewFile(in.FilePath, in.Content)
	}
	return ""
}
