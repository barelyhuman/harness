package transcript

import (
	"encoding/json"
	"strings"
)

const maxTitle = 80

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func relPath(p, cwd string) string {
	if cwd != "" {
		c := strings.TrimSuffix(cwd, "/") + "/"
		if strings.HasPrefix(p, c) {
			return strings.TrimPrefix(p, c)
		}
		// macOS: /tmp and /var are symlinks into /private.
		if strings.HasPrefix(c, "/private/") && strings.HasPrefix(p, c[len("/private"):]) {
			return strings.TrimPrefix(p, c[len("/private"):])
		}
	}
	return p
}

type toolFacts struct {
	Kind    string
	Title   string
	Path    string
	Command string
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// describeTool derives the display facts for a tool call.
func describeTool(name string, input json.RawMessage, cwd string) toolFacts {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	f := toolFacts{Kind: KindOther, Title: name}
	withPath := func(kind string, keys ...string) {
		f.Kind = kind
		if p := str(in, keys...); p != "" {
			f.Path = relPath(p, cwd)
			f.Title = name + " " + f.Path
		}
	}
	switch name {
	case "Bash":
		f.Kind = KindBash
		f.Command = str(in, "command")
		if f.Command != "" {
			f.Title = "Bash: " + clip(f.Command, maxTitle)
		}
	case "Edit", "MultiEdit":
		withPath(KindEdit, "file_path")
	case "Write":
		withPath(KindWrite, "file_path")
	case "NotebookEdit":
		withPath(KindEdit, "notebook_path")
	case "Read":
		withPath(KindRead, "file_path")
	case "LS":
		withPath(KindRead, "path")
	case "Grep":
		f.Kind = KindSearch
		if p := str(in, "pattern"); p != "" {
			f.Title = "Grep \"" + clip(p, 60) + "\""
			if d := str(in, "path"); d != "" {
				f.Title += " in " + relPath(d, cwd)
			}
		}
	case "Glob":
		f.Kind = KindSearch
		if p := str(in, "pattern"); p != "" {
			f.Title = "Glob " + clip(p, 60)
		}
	case "ToolSearch":
		f.Kind = KindSearch
		if q := str(in, "query"); q != "" {
			f.Title = "ToolSearch " + clip(q, 60)
		}
	case "WebFetch":
		f.Kind = KindWeb
		if u := str(in, "url"); u != "" {
			f.Title = "Fetch " + clip(u, 70)
		}
	case "WebSearch":
		f.Kind = KindWeb
		if q := str(in, "query"); q != "" {
			f.Title = "Search \"" + clip(q, 60) + "\""
		}
	case "Task", "Agent":
		f.Kind = KindTask
		if d := str(in, "description", "subagent_type"); d != "" {
			f.Title = "Task: " + clip(d, 70)
		}
	default:
		if strings.HasPrefix(name, "mcp__") {
			f.Kind = KindMCP
			parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
			if len(parts) == 2 {
				f.Title = strings.ReplaceAll(parts[0], "_", " ") + ": " + parts[1]
			}
		}
	}
	return f
}
