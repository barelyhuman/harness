package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Handoff sizing. Token counts are estimated at 4 characters per token.
const (
	charsPerToken = 4
	// HandoffMaxHunkTokens caps the diff hunks section.
	HandoffMaxHunkTokens = 8000
	// HandoffMaxTurns is how many of the most recent user/assistant messages
	// are considered (each is further trimmed to fit the budget).
	HandoffMaxTurns = 12

	maxFirstPromptChars = 8000
	maxTurnChars        = 6000
	maxListedFiles      = 40
)

// HandoffClosing is the last line of every handoff.
const HandoffClosing = "You are continuing this task in the same worktree."

type handoffTurn struct {
	role string // "User" | "Assistant"
	text string
}

type handoffFile struct {
	path string
	edit bool
}

// BuildHandoff renders a deterministic markdown summary of a source session,
// to be sent as the first user message of a session on another provider in
// the same worktree. events are the source session's normalized events;
// diffStat is `git diff --stat` and diffHunks the unified diff of the
// worktree. budget is the maximum size in tokens (4 characters per token);
// budget <= 0 means unlimited apart from the 8k-token cap on hunks. When the
// budget is tight the oldest turns are dropped first, then trimmed.
func BuildHandoff(events []Event, diffStat, diffHunks string, budget int) string {
	firstPrompt, turns, todos, files := summarizeEvents(events)

	// Fixed sections.
	var head strings.Builder
	head.WriteString("# Handoff: continuing a task from another agent session\n\n")
	if firstPrompt != "" {
		head.WriteString("## Original request\n\n")
		head.WriteString(trimMiddle(firstPrompt, maxFirstPromptChars))
		head.WriteString("\n\n")
	}
	var tail strings.Builder
	if len(todos) > 0 {
		tail.WriteString("## Todo list\n\n")
		for _, t := range todos {
			box := "[ ]"
			switch t.Status {
			case "completed":
				box = "[x]"
			case "in_progress":
				box = "[~]"
			}
			tail.WriteString("- " + box + " " + oneLine(t.Content) + "\n")
		}
		tail.WriteString("\n")
	}
	if len(files) > 0 {
		tail.WriteString("## Files touched\n\n")
		writeFiles := func(label string, edit bool) {
			n := 0
			var b strings.Builder
			for _, f := range files {
				if f.edit != edit {
					continue
				}
				if n < maxListedFiles {
					b.WriteString("- " + f.path + "\n")
				}
				n++
			}
			if n == 0 {
				return
			}
			tail.WriteString(label + ":\n" + b.String())
			if n > maxListedFiles {
				fmt.Fprintf(&tail, "- ... and %d more\n", n-maxListedFiles)
			}
		}
		writeFiles("Edited", true)
		writeFiles("Read", false)
		tail.WriteString("\n")
	}
	if s := strings.TrimSpace(diffStat); s != "" {
		tail.WriteString("## Current changes (git diff --stat)\n\n" + fence(s, "") + "\n\n")
	}
	closing := HandoffClosing + "\n"

	limit := -1 // chars
	if budget > 0 {
		limit = budget * charsPerToken
	}
	remaining := func(used int) int {
		if limit < 0 {
			return 1 << 30
		}
		return max(limit-used, 0)
	}
	used := head.Len() + tail.Len() + len(closing)

	// Diff hunks: at most 8k tokens and at most 40% of what is left.
	var hunks string
	if h := strings.TrimSpace(diffHunks); h != "" {
		hc := HandoffMaxHunkTokens * charsPerToken
		if limit >= 0 {
			hc = min(hc, remaining(used)*2/5)
		}
		const wrap = len("## Diff hunks\n\n```diff\n\n```\n\n") + len("\n... (diff truncated)")
		if hc > wrap {
			body, cut := cutLines(h, hc-wrap)
			if cut {
				body += "\n... (diff truncated)"
			}
			hunks = "## Diff hunks\n\n" + fence(body, "diff") + "\n\n"
		}
	}
	used += len(hunks)

	// Recent turns: newest first until the budget runs out.
	others := turns
	if len(others) > 0 && firstPrompt != "" && others[0].role == "User" && others[0].text == firstPrompt {
		others = others[1:] // already shown as the original request
	}
	recent := others
	if len(recent) > HandoffMaxTurns {
		recent = recent[len(recent)-HandoffMaxTurns:]
	}
	const turnsHeader = "## Recent conversation\n\n"
	room := remaining(used) - len(turnsHeader) - 64 // slack for the omitted note
	var picked []string                             // newest first
	for i := len(recent) - 1; i >= 0; i-- {
		prefix := "**" + recent[i].role + ":** "
		avail := room - len(prefix) - 2
		if avail < 80 {
			break
		}
		entry := prefix + trimMiddle(recent[i].text, min(maxTurnChars, avail)) + "\n\n"
		picked = append(picked, entry)
		room -= len(entry)
	}
	var conv strings.Builder
	if len(picked) > 0 {
		conv.WriteString(turnsHeader)
		if n := len(others) - len(picked); n > 0 {
			fmt.Fprintf(&conv, "(%d earlier messages omitted)\n\n", n)
		}
		for i := len(picked) - 1; i >= 0; i-- {
			conv.WriteString(picked[i])
		}
	}

	return head.String() + conv.String() + tail.String() + hunks + closing
}

// summarizeEvents folds the event stream into the pieces a handoff needs.
func summarizeEvents(events []Event) (firstPrompt string, turns []handoffTurn, todos []TodoItem, files []handoffFile) {
	type block struct{ text string }
	var (
		order  []string
		blocks = map[string]*block{}
	)
	flush := func() {
		var parts []string
		for _, k := range order {
			if t := strings.TrimSpace(blocks[k].text); t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			turns = append(turns, handoffTurn{"Assistant", strings.Join(parts, "\n\n")})
		}
		order, blocks = nil, map[string]*block{}
	}
	type touch struct {
		path string
		edit bool
	}
	pending := map[string]touch{} // tool id -> touch
	seen := map[string]int{}      // path -> index in files
	record := func(path string, edit bool) {
		if i, ok := seen[path]; ok {
			files[i].edit = files[i].edit || edit
			return
		}
		seen[path] = len(files)
		files = append(files, handoffFile{path, edit})
	}
	for _, e := range events {
		switch e.Kind {
		case KindUserMessage:
			flush()
			text := strings.TrimSpace(MustDecode[UserMessageData](e).Text)
			if text == "" {
				continue
			}
			if firstPrompt == "" {
				firstPrompt = text
			}
			turns = append(turns, handoffTurn{"User", text})
		case KindTextDelta, KindMessageDone:
			if e.ParentToolID != "" {
				continue // subagent chatter
			}
			var key, text string
			final := false
			if e.Kind == KindTextDelta {
				d := MustDecode[BlockDelta](e)
				key, text = fmt.Sprintf("%s:%d", d.MessageID, d.Index), d.Text
			} else {
				d := MustDecode[MessageDone](e)
				if d.BlockType != "text" {
					continue
				}
				key, text, final = fmt.Sprintf("%s:%d", d.MessageID, d.Index), d.Text, true
			}
			b := blocks[key]
			if b == nil {
				b = &block{}
				blocks[key] = b
				order = append(order, key)
			}
			if final {
				b.text = text
			} else {
				b.text += text
			}
		case KindToolInputDone, KindToolStart:
			var id, name string
			var in json.RawMessage
			if e.Kind == KindToolStart {
				d := MustDecode[ToolStart](e)
				id, name, in = d.ID, d.Name, d.Input
			} else {
				d := MustDecode[ToolInputDone](e)
				id, name, in = d.ID, d.Name, d.Input
			}
			edit := false
			switch name {
			case "Edit", "MultiEdit", "Write", "NotebookEdit", "Delete", "Move":
				edit = true
			case "Read":
			default:
				continue
			}
			var m map[string]any
			if json.Unmarshal(in, &m) != nil {
				continue
			}
			for _, k := range []string{"file_path", "notebook_path", "path"} {
				if p, _ := m[k].(string); p != "" {
					pending[id] = touch{p, edit}
					break
				}
			}
		case KindToolResult:
			r := MustDecode[ToolResult](e)
			if t, ok := pending[r.ID]; ok {
				delete(pending, r.ID)
				if r.OK && !r.Denied {
					record(t.path, t.edit)
				}
			}
		case KindTodoUpdate:
			todos = MustDecode[TodoUpdate](e).Todos
		}
	}
	flush()
	// Tools that never reported a result still count (interrupted runs).
	for _, e := range events {
		if e.Kind != KindToolInputDone {
			continue
		}
		if t, ok := pending[MustDecode[ToolInputDone](e).ID]; ok {
			record(t.path, t.edit)
			delete(pending, MustDecode[ToolInputDone](e).ID)
		}
	}
	return
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// fence wraps s in a code fence longer than any backtick run inside it.
func fence(s, lang string) string {
	n, run := 3, 0
	for _, r := range s {
		if r == '`' {
			run++
			n = max(n, run+1)
		} else {
			run = 0
		}
	}
	f := strings.Repeat("`", n)
	return f + lang + "\n" + strings.TrimRight(s, "\n") + "\n" + f
}

// trimMiddle shortens s to at most n bytes, keeping the start and the end.
func trimMiddle(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	const marker = "\n...[trimmed]...\n"
	if n <= len(marker)+8 {
		return cutRunes(s, max(n, 0))
	}
	keep := n - len(marker)
	head, tail := keep*2/3, keep-keep*2/3
	return cutRunes(s, head) + marker + cutTail(s, tail)
}

func cutRunes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func cutTail(s string, n int) string {
	if n >= len(s) {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

// cutLines keeps whole leading lines of s within n bytes.
func cutLines(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	cut := strings.LastIndexByte(s[:n], '\n')
	if cut <= 0 {
		return cutRunes(s, n), true
	}
	return s[:cut], true
}
