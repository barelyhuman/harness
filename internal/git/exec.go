// Package git is a stateless wrapper around the system git and gh binaries.
// Every function takes the repo or worktree directory to operate in.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Error is returned when a git or gh command fails. It carries stderr.
type Error struct {
	Cmd    string
	Dir    string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s (in %s): %v: %s", e.Cmd, e.Dir, e.Err, strings.TrimSpace(e.Stderr))
}
func (e *Error) Unwrap() error { return e.Err }

const (
	defaultTimeout = 60 * time.Second
	networkTimeout = 5 * time.Minute
)

func lookBin(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"} {
		p := d + "/" + name
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return name
}

func run(ctx context.Context, bin, dir string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, lookBin(bin), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), &Error{Cmd: bin + " " + strings.Join(args, " "), Dir: dir, Stderr: errb.String(), Err: err}
	}
	return out.Bytes(), nil
}

func runGit(dir string, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return run(ctx, "git", dir, stdin, args...)
}

func isExit(err error, code int) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == code
}

// RepoRoot returns the top-level directory of the repo containing dir.
func RepoRoot(dir string) (string, error) {
	out, err := runGit(dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CurrentBranch returns the checked-out branch name, or "HEAD" if detached.
func CurrentBranch(dir string) (string, error) {
	out, err := runGit(dir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// BranchExists reports whether refs/heads/<branch> exists.
func BranchExists(repo, branch string) bool {
	_, err := runGit(repo, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}
