package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sid/harness/internal/workspace"
)

// Plan spec statuses written to the spec file's front matter.
const (
	SpecProposed = "proposed"
	SpecApproved = "approved"
	SpecRejected = "rejected"
)

// specsDirAbs resolves a specs dir against the project root.
func specsDirAbs(projectPath, dir string) string {
	if dir == "" {
		dir = DefaultSpecsDir
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(projectPath, dir)
}

// specFileName is <YYYY-MM-DD>-<slug(title)>-<id[:6]>.md.
func specFileName(sess workspace.Session, now time.Time) string {
	id := sess.ID
	if len(id) > 6 {
		id = id[:6]
	}
	return fmt.Sprintf("%s-%s-%s.md", now.Format("2006-01-02"), workspace.Slugify(sess.Title), id)
}

func renderSpec(sess workspace.Session, status, plan string, now time.Time) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %q\n", sess.Title)
	fmt.Fprintf(&b, "session: %s\n", sess.ID)
	fmt.Fprintf(&b, "model: %s\n", sess.Model)
	fmt.Fprintf(&b, "status: %s\n", status)
	fmt.Fprintf(&b, "updated: %s\n", now.Format(time.RFC3339))
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(plan))
	b.WriteString("\n")
	return b.String()
}

func writeAtomic(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".spec-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// writeSpec writes (or overwrites) the session's plan spec with status
// "proposed". The path is chosen once per session and stored on the row so
// every revision of the plan updates the same file. Returns the path.
func writeSpec(ss *workspace.Sessions, ps *workspace.Projects, roles workspace.AgentRoles, sessionID, plan string) (string, error) {
	if strings.TrimSpace(plan) == "" {
		return "", nil
	}
	sess, err := ss.Get(sessionID)
	if err != nil {
		return "", err
	}
	path := sess.SpecPath
	now := time.Now()
	if path == "" {
		p, err := ps.Get(sess.ProjectID)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(p.Path); err != nil {
			return "", err
		}
		path = filepath.Join(specsDirAbs(p.Path, roles.SpecsDir), specFileName(sess, now))
		if err := ss.SetSpecPath(sessionID, path); err != nil {
			return "", err
		}
	}
	return path, writeAtomic(path, renderSpec(sess, SpecProposed, plan, now))
}

var specStatusRe = regexp.MustCompile(`(?m)^status: .*$`)

// setSpecStatus rewrites the status line of a spec file written by writeSpec.
func setSpecStatus(path, status string) error {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	loc := specStatusRe.FindIndex(b)
	if loc == nil {
		return nil
	}
	out := string(b[:loc[0]]) + "status: " + status + string(b[loc[1]:])
	return writeAtomic(path, out)
}

// specsPrompt is appended to the system prompt when the project has specs.
func specsPrompt(dir string, explorer bool) string {
	s := "Project specs and previously approved plans live in " + dir +
		". Read the ones relevant to the task before planning or making changes, and keep your work consistent with the decisions recorded there."
	if explorer {
		s += " Delegate broad codebase searches to the `explorer` subagent to keep your own context small."
	}
	return s
}

const explorerPrompt = "You are a fast, read-only codebase explorer. Search and read the code to answer the question you are given. " +
	"Never modify files or run commands that change state. Answer concisely, citing file:line references for every claim."

const explorerDesc = "Fast read-only codebase explorer. Use proactively to search and understand code before planning or editing."

var explorerTools = []string{"Read", "Grep", "Glob", "Bash"}
