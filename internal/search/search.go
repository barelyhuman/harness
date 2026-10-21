// Package search provides file listing (quick open) and content search
// (find in files) backed by ripgrep with a pure-Go fallback for listing.
package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

var (
	mu    sync.Mutex
	cache = map[string][]string{}
)

// Invalidate drops the cached file list for root. paths (changed files) are
// accepted so callers can wire it to the watcher; any change that adds or
// removes a file invalidates, plain content writes to known files do not.
func Invalidate(root string, paths []string) {
	root = clean(root)
	mu.Lock()
	defer mu.Unlock()
	cached, ok := cache[root]
	if !ok {
		return
	}
	if len(paths) > 0 {
		known := make(map[string]struct{}, len(cached))
		for _, c := range cached {
			known[c] = struct{}{}
		}
		changed := false
		for _, p := range paths {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				continue
			}
			st, err := os.Lstat(p)
			_, wasKnown := known[filepath.ToSlash(rel)]
			if (err != nil) == wasKnown || (err == nil && st.IsDir()) { // created/removed/dir
				changed = true
				break
			}
		}
		if !changed {
			return
		}
	}
	delete(cache, root)
}

func clean(root string) string {
	if root == "~" || strings.HasPrefix(root, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(h, root[1:])
		}
	}
	if a, err := filepath.Abs(root); err == nil {
		root = a
	}
	return root
}

// Files returns root-relative slash paths of all non-ignored files.
func Files(ctx context.Context, root string) ([]string, error) {
	root = clean(root)
	mu.Lock()
	if c, ok := cache[root]; ok {
		out := append([]string(nil), c...)
		mu.Unlock()
		return out, nil
	}
	mu.Unlock()
	files, err := rgFiles(ctx, root)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		files, err = walkFiles(ctx, root)
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	mu.Lock()
	cache[root] = files
	mu.Unlock()
	return append([]string(nil), files...), nil
}

func rgFiles(ctx context.Context, root string) ([]string, error) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, rg, "--files", "--hidden", "-g", "!.git")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		// exit 1 = no files; anything else is a failure.
		if !(errors.As(err, &ee) && ee.ExitCode() == 1) {
			return nil, err
		}
	}
	files := []string{}
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimPrefix(l, "./")
		if l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

// walkFiles lists files honouring .gitignore files found while walking.
func walkFiles(ctx context.Context, root string) ([]string, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}
	pats := map[string][]gitignore.Pattern{}
	var matcher gitignore.Matcher
	rebuild := func() {
		dirs := make([]string, 0, len(pats))
		for d := range pats {
			dirs = append(dirs, d)
		}
		sort.Slice(dirs, func(i, j int) bool {
			return strings.Count(dirs[i], "/") < strings.Count(dirs[j], "/") || dirs[i] == "" && dirs[j] != ""
		})
		var all []gitignore.Pattern
		for _, d := range dirs {
			all = append(all, pats[d]...)
		}
		matcher = gitignore.NewMatcher(all)
	}
	load := func(rel, file string, domain []string) {
		b, err := os.ReadFile(file)
		if err != nil {
			return
		}
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimRight(l, "\r")
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			pats[rel] = append(pats[rel], gitignore.ParsePattern(l, domain))
		}
	}
	load("", filepath.Join(root, ".git", "info", "exclude"), nil)
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = ""
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if rel != "" && matcher != nil && matcher.Match(strings.Split(rel, "/"), true) {
				return filepath.SkipDir
			}
			var domain []string
			if rel != "" {
				domain = strings.Split(rel, "/")
			}
			before := len(pats[rel])
			load(rel, filepath.Join(p, ".gitignore"), domain)
			if len(pats[rel]) != before || matcher == nil {
				rebuild()
			}
			return nil
		}
		if !d.Type().IsRegular() && d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		if matcher != nil && matcher.Match(strings.Split(rel, "/"), false) {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}

// GrepOpts configures Grep.
type GrepOpts struct {
	Regex         bool
	CaseSensitive bool
	Glob          string // comma or space separated globs; "!x" excludes
}

// Range is a submatch byte range within a line.
type Range struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Match is one matching line.
type Match struct {
	Line       int     `json:"line"`
	Column     int     `json:"column"` // 1-based byte column of the first submatch
	Text       string  `json:"text"`
	Submatches []Range `json:"submatches"`
}

// Result groups the matches of one file. Err is set on a terminal error
// (delivered as a final Result with empty Path).
type Result struct {
	Path    string  `json:"path"`
	Matches []Match `json:"matches"`
	Err     error   `json:"-"`
}

// Grep streams results grouped by file; the channel closes when rg exits or
// ctx is cancelled.
func Grep(ctx context.Context, root, query string, opts GrepOpts) <-chan Result {
	out := make(chan Result, 16)
	root = clean(root)
	go func() {
		defer close(out)
		if query == "" {
			return
		}
		rg, err := exec.LookPath("rg")
		if err != nil {
			send(ctx, out, Result{Err: err})
			return
		}
		args := []string{"--json", "--hidden", "-g", "!.git"}
		if !opts.Regex {
			args = append(args, "--fixed-strings")
		}
		if opts.CaseSensitive {
			args = append(args, "--case-sensitive")
		} else {
			args = append(args, "--ignore-case")
		}
		for _, g := range strings.FieldsFunc(opts.Glob, func(r rune) bool { return r == ',' || r == ' ' }) {
			args = append(args, "-g", g)
		}
		args = append(args, "-e", query, "--", ".")
		cmd := exec.CommandContext(ctx, rg, args...)
		cmd.Dir = root
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			send(ctx, out, Result{Err: err})
			return
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			send(ctx, out, Result{Err: err})
			return
		}
		parseRG(ctx, stdout, root, out)
		err = cmd.Wait()
		var ee *exec.ExitError
		if err != nil && ctx.Err() == nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) {
			send(ctx, out, Result{Err: errors.New(strings.TrimSpace(stderr.String()))})
		}
	}()
	return out
}

func send(ctx context.Context, out chan<- Result, r Result) bool {
	select {
	case out <- r:
		return true
	case <-ctx.Done():
		return false
	}
}

type rgText struct {
	Text  string `json:"text"`
	Bytes string `json:"bytes"`
}

type rgMsg struct {
	Type string `json:"type"`
	Data struct {
		Path       rgText `json:"path"`
		Lines      rgText `json:"lines"`
		LineNumber int    `json:"line_number"`
		Submatches []struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"submatches"`
	} `json:"data"`
}

func parseRG(ctx context.Context, r interface{ Read([]byte) (int, error) }, root string, out chan<- Result) {
	sc := bufio.NewReaderSize(r, 256*1024)
	var cur *Result
	flush := func() bool {
		if cur != nil && len(cur.Matches) > 0 {
			ok := send(ctx, out, *cur)
			cur = nil
			return ok
		}
		cur = nil
		return true
	}
	for {
		line, err := sc.ReadBytes('\n')
		if len(line) > 0 {
			var m rgMsg
			if json.Unmarshal(line, &m) == nil {
				switch m.Type {
				case "begin":
					if !flush() {
						return
					}
					cur = &Result{Path: strings.TrimPrefix(m.Data.Path.Text, "./")}
				case "match":
					if cur == nil {
						cur = &Result{Path: strings.TrimPrefix(m.Data.Path.Text, "./")}
					}
					text := strings.TrimRight(m.Data.Lines.Text, "\r\n")
					mt := Match{Line: m.Data.LineNumber, Text: text}
					for _, s := range m.Data.Submatches {
						mt.Submatches = append(mt.Submatches, Range{s.Start, s.End})
					}
					if len(mt.Submatches) > 0 {
						mt.Column = mt.Submatches[0].Start + 1
					}
					cur.Matches = append(cur.Matches, mt)
				case "end":
					if !flush() {
						return
					}
				}
			}
		}
		if err != nil {
			flush()
			return
		}
	}
}
