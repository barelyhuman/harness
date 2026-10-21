// Package fs implements file-system operations for the project picker and
// editor: listing, etag-guarded reads/writes, trash, git badges and watching.
package fs

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Entry is one directory entry.
type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	IsRepo  bool   `json:"isRepo"`
	Symlink bool   `json:"symlink"`
}

// ListOpts filters ListDir.
type ListOpts struct {
	DirsOnly bool
	Hidden   bool
}

// Expand resolves a leading ~ to the home directory.
func Expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

// ListDir lists a directory, dirs first, then case-insensitive by name.
func ListDir(path string, opts ListOpts) ([]Entry, error) {
	path = Expand(path)
	des, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		name := de.Name()
		if !opts.Hidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(path, name)
		e := Entry{Name: name, Path: full}
		if de.Type()&os.ModeSymlink != 0 {
			e.Symlink = true
			if st, err := os.Stat(full); err == nil {
				e.IsDir = st.IsDir()
			}
		} else {
			e.IsDir = de.IsDir()
		}
		if opts.DirsOnly && !e.IsDir {
			continue
		}
		if e.IsDir {
			_, err := os.Stat(filepath.Join(full, ".git"))
			e.IsRepo = err == nil
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Badge is the git summary shown on repo rows.
type Badge struct {
	Branch    string `json:"branch"`
	Worktrees int    `json:"worktrees"`
}

// RepoBadges computes badges for paths with 8 workers, returning whatever
// completed within budget.
func RepoBadges(paths []string, budget time.Duration) map[string]Badge {
	var mu sync.Mutex
	out := map[string]Badge{}
	jobs := make(chan string)
	deadline := time.After(budget)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				b, ok := badge(Expand(p), budget)
				if !ok {
					continue
				}
				mu.Lock()
				out[p] = b
				mu.Unlock()
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, p := range paths {
			select {
			case jobs <- p:
			case <-deadline:
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-deadline:
	}
	mu.Lock()
	defer mu.Unlock()
	res := make(map[string]Badge, len(out))
	for k, v := range out {
		res[k] = v
	}
	return res
}

func badge(p string, budget time.Duration) (Badge, bool) {
	br, err := exec.Command("git", "-C", p, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return Badge{}, false
	}
	b := Badge{Branch: strings.TrimSpace(string(br)), Worktrees: 1}
	if wt, err := exec.Command("git", "-C", p, "worktree", "list", "--porcelain").Output(); err == nil {
		n := 0
		for _, l := range strings.Split(string(wt), "\n") {
			if strings.HasPrefix(l, "worktree ") {
				n++
			}
		}
		if n > 0 {
			b.Worktrees = n
		}
	}
	return b, true
}

// ErrConflict is returned by Write when the etag no longer matches.
type ErrConflict struct {
	Path    string
	Current string
}

func (e *ErrConflict) Error() string { return fmt.Sprintf("fs: %s changed on disk", e.Path) }

// IsConflict reports whether err is an *ErrConflict.
func IsConflict(err error) bool { var c *ErrConflict; return errors.As(err, &c) }

func etagOf(fi os.FileInfo) string {
	var ino uint64
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		ino = uint64(st.Ino)
	}
	h := sha1.Sum([]byte(fmt.Sprintf("%d:%d:%d", fi.Size(), fi.ModTime().UnixNano(), ino)))
	return hex.EncodeToString(h[:8])
}

// Stat returns file info (following symlinks).
func Stat(path string) (os.FileInfo, error) { return os.Stat(Expand(path)) }

// Read returns the file contents and its etag.
func Read(path string) ([]byte, string, error) {
	path = Expand(path)
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if fi.IsDir() {
		return nil, "", fmt.Errorf("fs: %s is a directory", path)
	}
	data := make([]byte, 0, fi.Size())
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		data = append(data, buf[:n]...)
		if err != nil {
			break
		}
	}
	return data, etagOf(fi), nil
}

// Write atomically replaces path (temp file + rename), preserving mode. If
// ifEtag is non-empty and the file exists with a different etag, it returns
// *ErrConflict. A missing file with a non-empty ifEtag is also a conflict.
// It returns the new etag.
func Write(path string, data []byte, ifEtag string) (string, error) {
	path = Expand(path)
	mode := os.FileMode(0o644)
	fi, err := os.Stat(path)
	switch {
	case err == nil:
		if ifEtag != "" && etagOf(fi) != ifEtag {
			return "", &ErrConflict{Path: path, Current: etagOf(fi)}
		}
		mode = fi.Mode().Perm()
	case os.IsNotExist(err):
		if ifEtag != "" {
			return "", &ErrConflict{Path: path}
		}
	default:
		return "", err
	}
	// Write through symlinks to the real target.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".harness-*.tmp")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return "", err
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return "", err
	}
	nfi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return etagOf(nfi), nil
}

// Create makes an empty file or a directory; it fails if path exists.
func Create(path string, isDir bool) error {
	path = Expand(path)
	if isDir {
		if _, err := os.Lstat(path); err == nil {
			return os.ErrExist
		}
		return os.MkdirAll(path, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// Rename moves from to to, refusing to overwrite.
func Rename(from, to string) error {
	from, to = Expand(from), Expand(to)
	if _, err := os.Lstat(to); err == nil {
		return os.ErrExist
	}
	return os.Rename(from, to)
}

// trashFn is overridable in tests.
var trashFn = trash

// Delete moves path to the macOS Trash. It never falls back to RemoveAll.
func Delete(path string) error {
	path = Expand(path)
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	return trashFn(abs)
}

func trash(abs string) error {
	if p := "/usr/bin/trash"; fileExists(p) {
		if out, err := exec.Command(p, abs).CombinedOutput(); err != nil {
			return fmt.Errorf("fs: trash: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(abs)
	script := `tell application "Finder" to delete POSIX file "` + esc + `"`
	if out, err := exec.Command("osascript", "-e", script).CombinedOutput(); err != nil {
		return fmt.Errorf("fs: could not move to Trash: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func fileExists(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }
