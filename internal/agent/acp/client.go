package acp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Cursor never calls fs/* or terminal/* (docs/spikes/cursor-acp.md), but
// other ACP agents do, so the generic client answers them minimally: file
// access is confined to the session cwd, and terminals run a command with
// exec (no pty).

// within reports whether p (absolute or cwd-relative) resolves inside root,
// following symlinks of the deepest existing ancestor.
func within(root, p string) (string, bool) {
	if root == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		r = filepath.Clean(root)
	}
	// Resolve the deepest existing ancestor to defeat symlink escapes.
	dir, rest := p, ""
	for {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			p = filepath.Join(real, rest)
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
	rel, err := filepath.Rel(r, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

func (s *Session) handleFS(m *message) {
	var p struct {
		Path    string `json:"path"`
		Line    int    `json:"line"`
		Limit   int    `json:"limit"`
		Content string `json:"content"`
	}
	_ = json.Unmarshal(m.Params, &p)
	path, ok := within(s.cwd, p.Path)
	if !ok {
		_ = s.c.replyErr(m.ID, CodeInvalidParams, "path outside the session working directory: "+p.Path)
		return
	}
	switch m.Method {
	case "fs/read_text_file":
		b, err := os.ReadFile(path)
		if err != nil {
			_ = s.c.replyErr(m.ID, CodeInternal, err.Error())
			return
		}
		txt := string(b)
		if p.Line > 0 || p.Limit > 0 {
			ls := strings.SplitAfter(txt, "\n")
			start := max(p.Line-1, 0)
			if start > len(ls) {
				start = len(ls)
			}
			end := len(ls)
			if p.Limit > 0 && start+p.Limit < end {
				end = start + p.Limit
			}
			txt = strings.Join(ls[start:end], "")
		}
		_ = s.c.reply(m.ID, map[string]any{"content": txt})
	case "fs/write_text_file":
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			_ = s.c.replyErr(m.ID, CodeInternal, err.Error())
			return
		}
		if err := os.WriteFile(path, []byte(p.Content), 0o644); err != nil {
			_ = s.c.replyErr(m.ID, CodeInternal, err.Error())
			return
		}
		_ = s.c.reply(m.ID, map[string]any{})
	}
}

// ---- terminals ----

const termOutputLimit = 1 << 20

type term struct {
	cmd   *exec.Cmd
	mu    sync.Mutex
	buf   bytes.Buffer
	trunc bool
	done  chan struct{}
	code  *int
	sig   string
	limit int
}

func (t *term) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if room := t.limit - t.buf.Len(); room < len(p) {
		t.trunc = true
		if room > 0 {
			t.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return t.buf.Write(p)
}

func (t *term) exitStatus() map[string]any {
	select {
	case <-t.done:
	default:
		return nil
	}
	st := map[string]any{}
	if t.code != nil {
		st["exitCode"] = *t.code
	}
	if t.sig != "" {
		st["signal"] = t.sig
	}
	return st
}

type termTable struct {
	mu sync.Mutex
	n  int
	m  map[string]*term
}

func (tt *termTable) get(id string) *term {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return tt.m[id]
}

func (s *Session) handleTerminal(m *message) {
	var p struct {
		TerminalID      string                         `json:"terminalId"`
		Command         string                         `json:"command"`
		Args            []string                       `json:"args"`
		Cwd             string                         `json:"cwd"`
		Env             []struct{ Name, Value string } `json:"env"`
		OutputByteLimit int                            `json:"outputByteLimit"`
	}
	_ = json.Unmarshal(m.Params, &p)
	tt := &s.terms
	switch m.Method {
	case "terminal/create":
		dir := s.cwd
		if p.Cwd != "" {
			d, ok := within(s.cwd, p.Cwd)
			if !ok {
				_ = s.c.replyErr(m.ID, CodeInvalidParams, "cwd outside the session working directory")
				return
			}
			dir = d
		}
		if p.Command == "" {
			_ = s.c.replyErr(m.ID, CodeInvalidParams, "command required")
			return
		}
		t := &term{done: make(chan struct{}), limit: termOutputLimit}
		if p.OutputByteLimit > 0 {
			t.limit = p.OutputByteLimit
		}
		t.cmd = exec.Command(p.Command, p.Args...)
		t.cmd.Dir = dir
		t.cmd.Env = os.Environ()
		for _, e := range p.Env {
			t.cmd.Env = append(t.cmd.Env, e.Name+"="+e.Value)
		}
		t.cmd.Stdout, t.cmd.Stderr = t, t
		if err := t.cmd.Start(); err != nil {
			_ = s.c.replyErr(m.ID, CodeInternal, err.Error())
			return
		}
		go func() {
			err := t.cmd.Wait()
			t.mu.Lock()
			if ps := t.cmd.ProcessState; ps != nil {
				c := ps.ExitCode()
				if c >= 0 {
					t.code = &c
				} else if err != nil {
					t.sig = "killed"
				}
			}
			t.mu.Unlock()
			close(t.done)
		}()
		tt.mu.Lock()
		if tt.m == nil {
			tt.m = map[string]*term{}
		}
		tt.n++
		id := "term-" + strconv.Itoa(tt.n)
		tt.m[id] = t
		tt.mu.Unlock()
		_ = s.c.reply(m.ID, map[string]any{"terminalId": id})
		return
	}
	t := tt.get(p.TerminalID)
	if t == nil {
		_ = s.c.replyErr(m.ID, CodeInvalidParams, fmt.Sprintf("unknown terminal %q", p.TerminalID))
		return
	}
	switch m.Method {
	case "terminal/output":
		t.mu.Lock()
		res := map[string]any{"output": t.buf.String(), "truncated": t.trunc}
		t.mu.Unlock()
		if st := t.exitStatus(); st != nil {
			res["exitStatus"] = st
		}
		_ = s.c.reply(m.ID, res)
	case "terminal/wait_for_exit":
		// Blocks until exit: callers run this off the reader goroutine.
		<-t.done
		_ = s.c.reply(m.ID, t.exitStatus())
	case "terminal/kill":
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		_ = s.c.reply(m.ID, map[string]any{})
	case "terminal/release":
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		tt.mu.Lock()
		delete(tt.m, p.TerminalID)
		tt.mu.Unlock()
		_ = s.c.reply(m.ID, map[string]any{})
	default:
		_ = s.c.replyErr(m.ID, CodeMethodNotFound, "method not found: "+m.Method)
	}
}

// killTerminals kills every live terminal (process exit).
func (s *Session) killTerminals() {
	s.terms.mu.Lock()
	defer s.terms.mu.Unlock()
	for id, t := range s.terms.m {
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		delete(s.terms.m, id)
	}
}
