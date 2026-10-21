package git

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Hunk is one -U0 diff hunk. Lines keep their leading ' ', '+', '-' or '\'.
type Hunk struct {
	ID       string   `json:"id"`
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Header   string   `json:"header"`
	Lines    []string `json:"lines"`
}

// ErrStaleHunk means the hunk id no longer matches the current diff.
var ErrStaleHunk = errors.New("git: hunk not found (stale id)")

func isUntracked(dir, path string) (bool, error) {
	out, err := runGit(dir, nil, "ls-files", "-z", "--others", "--exclude-standard", "--", path)
	if err != nil {
		return false, err
	}
	return len(out) > 0, nil
}

// Hunks returns the hunks of path. staged selects index-vs-HEAD; otherwise
// working tree vs index (or vs /dev/null for untracked files).
func Hunks(dir, path string, staged bool) ([]Hunk, error) {
	hs, _, err := hunks(dir, path, staged)
	return hs, err
}

func hunks(dir, path string, staged bool) ([]Hunk, bool, error) {
	var out []byte
	var err error
	untracked := false
	if !staged {
		if untracked, err = isUntracked(dir, path); err != nil {
			return nil, false, err
		}
	}
	if untracked {
		out, err = runGit(dir, nil, "diff", "--no-index", "--no-color", "--no-ext-diff", "-U0", "--", "/dev/null", path)
		if err != nil && isExit(err, 1) {
			err = nil // --no-index exits 1 when files differ
		}
	} else if staged {
		out, err = runGit(dir, nil, "diff", "--no-color", "--no-ext-diff", "-U0", "--cached", "--", path)
	} else {
		out, err = runGit(dir, nil, "diff", "--no-color", "--no-ext-diff", "-U0", "--", path)
	}
	if err != nil {
		return nil, untracked, err
	}
	hs, err := ParseHunks(path, string(out))
	return hs, untracked, err
}

// ParseHunks parses unified diff text (single file) into hunks.
func ParseHunks(path, diff string) ([]Hunk, error) {
	hs := []Hunk{}
	var cur *Hunk
	flush := func() {
		if cur != nil {
			h := sha256.New()
			h.Write([]byte(path))
			h.Write([]byte{0})
			h.Write([]byte(cur.Header))
			for _, l := range cur.Lines {
				h.Write([]byte{0})
				h.Write([]byte(l))
			}
			cur.ID = hex.EncodeToString(h.Sum(nil))[:16]
			hs = append(hs, *cur)
			cur = nil
		}
	}
	lines := strings.Split(diff, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "@@ ") {
			flush()
			h, err := parseHunkHeader(l)
			if err != nil {
				return nil, err
			}
			cur = &h
			continue
		}
		if cur == nil {
			continue // file header
		}
		if l == "" || strings.ContainsRune("+-\\ ", rune(l[0])) {
			cur.Lines = append(cur.Lines, l)
		}
	}
	flush()
	return hs, nil
}

func parseHunkHeader(l string) (Hunk, error) {
	h := Hunk{Header: l}
	end := strings.Index(l[3:], " @@")
	if end < 0 {
		return h, fmt.Errorf("git: bad hunk header %q", l)
	}
	f := strings.Fields(l[3 : 3+end])
	if len(f) != 2 || f[0][0] != '-' || f[1][0] != '+' {
		return h, fmt.Errorf("git: bad hunk header %q", l)
	}
	var err error
	if h.OldStart, h.OldLines, err = parseRange(f[0][1:]); err != nil {
		return h, err
	}
	if h.NewStart, h.NewLines, err = parseRange(f[1][1:]); err != nil {
		return h, err
	}
	return h, nil
}

func parseRange(s string) (start, n int, err error) {
	a, b, has := strings.Cut(s, ",")
	if start, err = strconv.Atoi(a); err != nil {
		return
	}
	n = 1
	if has {
		n, err = strconv.Atoi(b)
	}
	return
}

func quotePath(p string) string {
	need := false
	for i := 0; i < len(p); i++ {
		if c := p[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			need = true
		}
	}
	if !need {
		return p
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&b, "\\%03o", c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// buildPatch builds a minimal single-hunk patch for path.
func buildPatch(path string, h Hunk, newFile bool, mode string) []byte {
	var b strings.Builder
	a, bb := quotePath("a/"+path), quotePath("b/"+path)
	tab := ""
	if strings.ContainsAny(path, " ") {
		tab = "\t"
	}
	fmt.Fprintf(&b, "diff --git %s %s\n", a, bb)
	if newFile {
		fmt.Fprintf(&b, "new file mode %s\n--- /dev/null\n", mode)
	} else {
		fmt.Fprintf(&b, "--- %s%s\n", a, tab)
	}
	fmt.Fprintf(&b, "+++ %s%s\n", bb, tab)
	b.WriteString(h.Header + "\n")
	for _, l := range h.Lines {
		b.WriteString(l + "\n")
	}
	return []byte(b.String())
}

func applyHunk(dir, path, id string, staged bool, args ...string) error {
	hs, untracked, err := hunks(dir, path, staged)
	if err != nil {
		return err
	}
	for _, h := range hs {
		if h.ID != id {
			continue
		}
		mode := "100644"
		if untracked {
			if st, err := os.Stat(filepath.Join(dir, path)); err == nil && st.Mode()&0o111 != 0 {
				mode = "100755"
			}
		}
		patch := buildPatch(path, h, untracked, mode)
		_, err := runGit(dir, patch, append([]string{"apply"}, args...)...)
		return err
	}
	return ErrStaleHunk
}

// StageHunk stages one hunk of path's unstaged changes.
func StageHunk(dir, path, hunkID string) error {
	return applyHunk(dir, path, hunkID, false, "--cached", "--unidiff-zero")
}

// UnstageHunk removes one hunk of path's staged changes from the index.
func UnstageHunk(dir, path, hunkID string) error {
	return applyHunk(dir, path, hunkID, true, "--cached", "-R", "--unidiff-zero")
}

// RevertHunk discards one unstaged hunk from the working tree.
func RevertHunk(dir, path, hunkID string) error {
	return applyHunk(dir, path, hunkID, false, "-R", "--unidiff-zero")
}
