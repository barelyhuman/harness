package git

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func pathArgs(base []string, paths []string) []string {
	return append(append(base, "--"), paths...)
}

// StageFiles stages whole files (including deletions and new files).
func StageFiles(dir string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := runGit(dir, nil, pathArgs([]string{"add", "-A"}, paths)...)
	return err
}

// UnstageFiles resets paths in the index to HEAD.
func UnstageFiles(dir string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if _, err := runGit(dir, nil, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		// No commits yet: remove from the index.
		_, err = runGit(dir, nil, pathArgs([]string{"rm", "--cached", "-r", "-q", "--ignore-unmatch"}, paths)...)
		return err
	}
	_, err := runGit(dir, nil, pathArgs([]string{"restore", "--staged"}, paths)...)
	return err
}

// DiscardFiles reverts tracked files to the index and deletes untracked
// files. Untracked deletions are confined to dir.
func DiscardFiles(dir string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	root, err := RepoRoot(dir)
	if err != nil {
		return err
	}
	var tracked, untracked []string
	for _, p := range paths {
		u, err := isUntracked(dir, p)
		if err != nil {
			return err
		}
		if u {
			untracked = append(untracked, p)
		} else {
			tracked = append(tracked, p)
		}
	}
	absDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	var targets []string
	for _, p := range untracked {
		full := filepath.Join(rootReal, p)
		parent, err := filepath.EvalSymlinks(filepath.Dir(full))
		if err != nil {
			return err
		}
		full = filepath.Join(parent, filepath.Base(full))
		rel, err := filepath.Rel(absDir, full)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("git: refusing to delete %q outside %s", p, dir)
		}
		targets = append(targets, full)
	}
	if len(tracked) > 0 {
		if _, err := runGit(dir, nil, pathArgs([]string{"restore"}, tracked)...); err != nil {
			return err
		}
	}
	for _, t := range targets {
		if err := os.RemoveAll(t); err != nil {
			return err
		}
	}
	return nil
}

// Commit commits the index. With amend and an empty msg the message is kept.
func Commit(dir, msg string, amend bool) error {
	args := []string{"commit"}
	var in []byte
	if amend {
		args = append(args, "--amend")
	}
	if amend && strings.TrimSpace(msg) == "" {
		args = append(args, "--no-edit")
	} else {
		args = append(args, "-F", "-")
		in = []byte(msg)
	}
	_, err := runGit(dir, in, args...)
	return err
}

// ShowFile returns the file content at rev ("HEAD" or ":0" for the index).
// A path absent at rev yields "" and no error (the left side of a new-file diff).
func ShowFile(dir, rev, path string) (string, error) {
	spec := rev + ":" + path
	if rev == ":0" || rev == "" {
		spec = ":0:" + path
	}
	out, err := runGit(dir, nil, "show", spec)
	if err != nil {
		if e, ok := err.(*Error); ok {
			s := e.Stderr
			if strings.Contains(s, "exists on disk, but not in") || strings.Contains(s, "does not exist in") ||
				strings.Contains(s, "not in the index") || strings.Contains(s, "unknown revision or path") && rev == "HEAD" {
				return "", nil
			}
		}
		return "", err
	}
	return string(out), nil
}

// Worktree is one entry of `git worktree list`.
type Worktree struct {
	Path     string `json:"path"`
	Head     string `json:"head"`
	Branch   string `json:"branch"` // short name, empty if detached
	Bare     bool   `json:"bare"`
	Detached bool   `json:"detached"`
	Locked   bool   `json:"locked"`
	Prunable bool   `json:"prunable"`
}

// WorktreeAdd creates a worktree at path. With a non-empty base it creates
// the new branch from base; with an empty base it checks out the existing branch.
func WorktreeAdd(repo, path, branch, base string) error {
	var args []string
	if base == "" {
		args = []string{"worktree", "add", path, branch}
	} else {
		args = []string{"worktree", "add", "-b", branch, path, base}
	}
	_, err := runGit(repo, nil, args...)
	return err
}

// WorktreeRemove removes the worktree at path.
func WorktreeRemove(repo, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	_, err := runGit(repo, nil, append(args, path)...)
	return err
}

// WorktreePrune drops administrative records of missing worktrees.
func WorktreePrune(repo string) error {
	_, err := runGit(repo, nil, "worktree", "prune")
	return err
}

// WorktreeList parses `git worktree list --porcelain`.
func WorktreeList(repo string) ([]Worktree, error) {
	out, err := runGit(repo, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var res []Worktree
	var cur *Worktree
	for _, l := range strings.Split(string(out), "\n") {
		if l == "" {
			cur = nil
			continue
		}
		k, v, _ := strings.Cut(l, " ")
		if k == "worktree" {
			res = append(res, Worktree{Path: v})
			cur = &res[len(res)-1]
			continue
		}
		if cur == nil {
			continue
		}
		switch k {
		case "HEAD":
			cur.Head = v
		case "branch":
			cur.Branch = strings.TrimPrefix(v, "refs/heads/")
		case "bare":
			cur.Bare = true
		case "detached":
			cur.Detached = true
		case "locked":
			cur.Locked = true
		case "prunable":
			cur.Prunable = true
		}
	}
	return res, nil
}

// BranchDelete deletes a local branch (-D when force).
func BranchDelete(repo, branch string, force bool) error {
	f := "-d"
	if force {
		f = "-D"
	}
	_, err := runGit(repo, nil, "branch", f, "--", branch)
	return err
}

// Push pushes branch to origin and sets upstream.
func Push(dir, branch string) error {
	ctx, cancel := context.WithTimeout(context.Background(), networkTimeout)
	defer cancel()
	_, err := run(ctx, "git", dir, nil, "push", "-u", "origin", branch)
	return err
}

// PRCreate runs `gh pr create` and returns the PR URL.
func PRCreate(dir, title, body, base string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), networkTimeout)
	defer cancel()
	args := []string{"pr", "create", "--title", title, "--body", body}
	if base != "" {
		args = append(args, "--base", base)
	}
	out, err := run(ctx, "gh", dir, nil, args...)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

// PRState returns the PR state (OPEN, CLOSED or MERGED).
func PRState(dir, url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	out, err := run(ctx, "gh", dir, nil, "pr", "view", url, "--json", "state,url")
	if err != nil {
		return "", err
	}
	var v struct{ State, URL string }
	if err := json.Unmarshal(out, &v); err != nil {
		return "", fmt.Errorf("gh pr view: %w", err)
	}
	return v.State, nil
}
