package acp

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sid/harness/internal/agent"
)

// maxOutput caps a tool result's output text (same cap as the Claude adapter).
const maxOutput = 100_000

type location struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

type planEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status"`
}

// update is the union of all session/update payloads.
type update struct {
	SessionUpdate     string          `json:"sessionUpdate"`
	ToolCallID        string          `json:"toolCallId"`
	Title             string          `json:"title"`
	Kind              string          `json:"kind"`
	Status            string          `json:"status"`
	RawInput          json.RawMessage `json:"rawInput"`
	RawOutput         json.RawMessage `json:"rawOutput"`
	Locations         []location      `json:"locations"`
	Content           json.RawMessage `json:"content"`
	Entries           []planEntry     `json:"entries"`
	CurrentModeID     string          `json:"currentModeId"`
	AvailableCommands []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"availableCommands"`
}

// toolName maps an ACP tool kind (or Cursor's rawInput._toolName) to the
// Claude-style tool names the transcript builder knows how to title.
// updateTodos and createPlan map to TodoWrite and ExitPlanMode so the builder
// hides those cards: their data arrives as todo_update and plan_proposal.
func toolName(kind string, raw map[string]any) string {
	if n, _ := raw["_toolName"].(string); n != "" {
		switch n {
		case "updateTodos":
			return "TodoWrite"
		case "createPlan":
			return "ExitPlanMode"
		}
		if kind == "other" || kind == "" {
			return n
		}
	}
	switch kind {
	case "execute":
		return "Bash"
	case "edit":
		return "Edit"
	case "read":
		return "Read"
	case "search":
		return "Grep"
	case "fetch":
		return "WebFetch"
	case "delete":
		return "Delete"
	case "move":
		return "Move"
	case "think":
		return "Think"
	case "switch_mode":
		return "SwitchMode"
	}
	return "Tool"
}

func rawString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// titleCode returns the text of a "`code`" title, or "".
func titleCode(title string) string {
	t := strings.TrimSpace(title)
	if len(t) >= 2 && t[0] == '`' && t[len(t)-1] == '`' {
		return t[1 : len(t)-1]
	}
	if i := strings.Index(t, "`"); i >= 0 {
		if j := strings.LastIndex(t, "`"); j > i {
			return t[i+1 : j]
		}
	}
	return ""
}

// toolInput builds the Claude-shaped input JSON for a tool.
func toolInput(name, title, cwd string, raw map[string]any, locs []location) json.RawMessage {
	path := rawString(raw, "path", "file_path")
	if path == "" && len(locs) > 0 {
		path = locs[0].Path
	}
	var in map[string]any
	switch name {
	case "Bash":
		cmd := rawString(raw, "command")
		if cmd == "" {
			cmd = titleCode(title)
		}
		in = map[string]any{"command": cmd}
	case "Read", "Edit", "Delete", "Move":
		if path == "" {
			path = titleCode(title)
		}
		if path == "" {
			return mustJSON(map[string]any{})
		}
		in = map[string]any{"file_path": path}
	case "Grep":
		pat := rawString(raw, "pattern", "query")
		if pat == "" {
			pat = titleCode(title)
		}
		in = map[string]any{}
		if pat != "" {
			in["pattern"] = pat
		}
		if p := rawString(raw, "path"); p != "" && strings.TrimSuffix(p, "/") != strings.TrimSuffix(cwd, "/") {
			in["path"] = p
		}
	case "WebFetch":
		in = map[string]any{"url": rawString(raw, "url")}
	default:
		in = map[string]any{}
		for k, v := range raw {
			in[k] = v
		}
	}
	return mustJSON(in)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func truncate(s string) (string, bool) {
	if len(s) <= maxOutput {
		return s, false
	}
	s = s[:maxOutput]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s, true
}

// toolContent is one element of a tool_call_update content array.
type toolContent struct {
	Type    string `json:"type"`
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
	Content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func parseToolContent(raw json.RawMessage) []toolContent {
	var cs []toolContent
	if len(raw) == 0 || json.Unmarshal(raw, &cs) != nil {
		return nil
	}
	return cs
}

// ---- diffs ----

// StripDiffMarkers removes the mangled unified-diff header lines Cursor embeds
// in new-file and deleted-file diff content ("-- /dev/null" in oldText and
// "++ b/<path>" as the first line of newText, or the mirror image for a
// deletion). isNew / isDelete report which case was detected.
func StripDiffMarkers(oldText, newText string) (o, n string, isNew, isDelete bool) {
	first := func(s string) (string, string) {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			return s[:i], s[i+1:]
		}
		return s, ""
	}
	isNull := func(l, sign string) bool {
		l = strings.TrimSpace(l)
		return l == sign+sign+" /dev/null" || l == sign+sign+sign+" /dev/null"
	}
	hasHdr := func(l, sign, side string) bool {
		return strings.HasPrefix(l, strings.Repeat(sign, 2)+" "+side+"/") ||
			strings.HasPrefix(l, strings.Repeat(sign, 3)+" "+side+"/")
	}
	ol, orest := first(oldText)
	nl, nrest := first(newText)
	switch {
	case isNull(ol, "-"):
		if hasHdr(nl, "+", "b") {
			return "", nrest, true, false
		}
		return "", newText, true, false
	case isNull(nl, "+"):
		if hasHdr(ol, "-", "a") {
			return orest, "", false, true
		}
		return oldText, "", false, true
	case hasHdr(ol, "-", "a") && hasHdr(nl, "+", "b"):
		return orest, nrest, false, false
	}
	return oldText, newText, false, false
}

func diffLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// UnifiedDiff renders a minimal single-hunk unified diff (common prefix and
// suffix trimmed, 3 lines of context) between two file texts. It returns ""
// when the texts are equal.
func UnifiedDiff(path, oldText, newText string) string {
	a, b := diffLines(oldText), diffLines(newText)
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	q := 0
	for q < len(a)-p && q < len(b)-p && a[len(a)-1-q] == b[len(b)-1-q] {
		q++
	}
	if p == len(a) && p == len(b) {
		return ""
	}
	cb, ca := min(3, p), min(3, q)
	oc, nc := cb+(len(a)-p-q)+ca, cb+(len(b)-p-q)+ca
	os, ns := p-cb+1, p-cb+1
	if oc == 0 {
		os--
	}
	if nc == 0 {
		ns--
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n", path, path, os, oc, ns, nc)
	for _, l := range a[p-cb : p] {
		sb.WriteString(" " + l + "\n")
	}
	for _, l := range a[p : len(a)-q] {
		sb.WriteString("-" + l + "\n")
	}
	for _, l := range b[p : len(b)-q] {
		sb.WriteString("+" + l + "\n")
	}
	for _, l := range a[len(a)-q : len(a)-q+ca] {
		sb.WriteString(" " + l + "\n")
	}
	return sb.String()
}

func deleteDiff(path, content string) string {
	ls := diffLines(content)
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n+++ /dev/null\n", path)
	if len(ls) > 0 {
		fmt.Fprintf(&sb, "@@ -1,%d +0,0 @@\n", len(ls))
		for _, l := range ls {
			sb.WriteString("-" + l + "\n")
		}
	}
	return sb.String()
}

// relPath makes p relative to cwd (also across the macOS /private symlink).
func relPath(p, cwd string) string {
	if cwd == "" {
		return strings.TrimPrefix(p, "/")
	}
	c := strings.TrimSuffix(cwd, "/") + "/"
	switch {
	case strings.HasPrefix(p, c):
		return strings.TrimPrefix(p, c)
	case strings.HasPrefix(c, "/private/") && strings.HasPrefix(p, c[len("/private"):]):
		return strings.TrimPrefix(p, c[len("/private"):])
	case strings.HasPrefix(p, "/private/") && strings.HasPrefix(p[len("/private"):], c):
		return strings.TrimPrefix(p[len("/private"):], c)
	}
	return strings.TrimPrefix(p, "/")
}

// DiffFromContent converts an ACP diff content item into a unified diff,
// normalising Cursor's new-file markers.
func DiffFromContent(c toolContent, cwd string) string {
	o, n, isNew, isDel := StripDiffMarkers(c.OldText, c.NewText)
	p := relPath(c.Path, cwd)
	switch {
	case isNew:
		return agent.DiffNewFile(p, n)
	case isDel:
		return deleteDiff(p, o)
	}
	return UnifiedDiff(p, o, n)
}

// ---- todos ----

type todoEntry struct {
	ID      string
	Content string
	Status  string
}

// normStatus maps Cursor's TODO_STATUS_* and ACP statuses to
// pending | in_progress | completed | cancelled.
func normStatus(s string) string {
	s = strings.ToLower(strings.TrimPrefix(s, "TODO_STATUS_"))
	s = strings.ToLower(s)
	switch s {
	case "in_progress", "inprogress", "active", "running":
		return "in_progress"
	case "completed", "complete", "done":
		return "completed"
	case "cancelled", "canceled":
		return "cancelled"
	}
	return "pending"
}

func todoItems(es []todoEntry) []agent.TodoItem {
	out := make([]agent.TodoItem, 0, len(es))
	for _, e := range es {
		out = append(out, agent.TodoItem{Content: e.Content, Status: e.Status})
	}
	return out
}
