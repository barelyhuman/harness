package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	d, _ := filepath.EvalSymlinks(t.TempDir())
	sh(t, d, "init", "-q", "-b", "main")
	sh(t, d, "config", "user.name", "t")
	sh(t, d, "config", "user.email", "t@t")
	write(t, d, "README.md", "hi\n")
	sh(t, d, "add", "-A")
	sh(t, d, "commit", "-q", "-m", "init")
	return d
}

func lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString("line " + string(rune('a'+i%26)) + "\n")
	}
	return b.String()
}

func TestStatusParsing(t *testing.T) {
	d := newRepo(t)
	write(t, d, "old name.txt", "some content\nmore\nlines here\n")
	sh(t, d, "add", "-A")
	sh(t, d, "commit", "-q", "-m", "add")
	sh(t, d, "mv", "old name.txt", "new name.txt")
	write(t, d, "README.md", "hi\nchanged\n")
	write(t, d, "dir/un tracked.txt", "x\n")
	write(t, d, "staged.txt", "s\n")
	sh(t, d, "add", "staged.txt")
	write(t, d, "staged.txt", "s\nmore\n")

	st, err := StatusOf(d)
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "main" {
		t.Errorf("branch %q", st.Branch)
	}
	has := func(l []FileChange, path, code, orig string) bool {
		for _, f := range l {
			if f.Path == path && f.Code == code && f.OrigPath == orig {
				return true
			}
		}
		return false
	}
	if !has(st.Staged, "new name.txt", "R", "old name.txt") {
		t.Errorf("rename missing: %+v", st.Staged)
	}
	if !has(st.Staged, "staged.txt", "A", "") || !has(st.Unstaged, "staged.txt", "M", "") {
		t.Errorf("staged.txt: %+v %+v", st.Staged, st.Unstaged)
	}
	if !has(st.Unstaged, "README.md", "M", "") {
		t.Errorf("unstaged: %+v", st.Unstaged)
	}
	if !has(st.Untracked, "dir/un tracked.txt", "U", "") {
		t.Errorf("untracked: %+v", st.Untracked)
	}
	if !st.Dirty() {
		t.Error("expected dirty")
	}
}

func TestStatusUnmergedAndAheadBehind(t *testing.T) {
	st, err := ParseStatus([]byte("# branch.head feat\x00# branch.upstream origin/feat\x00# branch.ab +2 -3\x00u UU N... 100644 100644 100644 100644 a b c con flict.txt\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Ahead != 2 || st.Behind != 3 || st.Upstream != "origin/feat" || len(st.Conflicted) != 1 || st.Conflicted[0].Path != "con flict.txt" {
		t.Errorf("%+v", st)
	}
}

func setupMultiHunk(t *testing.T) (string, string) {
	d := newRepo(t)
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		b.WriteString("line " + strings.Repeat("x", i) + "\n")
	}
	orig := b.String()
	write(t, d, "my file.txt", orig)
	sh(t, d, "add", "-A")
	sh(t, d, "commit", "-q", "-m", "file")
	ls := strings.Split(orig, "\n")
	ls[1] = "CHANGED 2"
	ls[14] = "CHANGED 15"
	ls[27] = "CHANGED 28"
	write(t, d, "my file.txt", strings.Join(ls, "\n"))
	return d, "my file.txt"
}

func TestHunkStageUnstageRevert(t *testing.T) {
	d, p := setupMultiHunk(t)
	hs, err := Hunks(d, p, false)
	if err != nil || len(hs) != 3 {
		t.Fatalf("hunks %v %v", hs, err)
	}
	if hs[0].ID == hs[1].ID || len(hs[0].ID) == 0 {
		t.Fatal("ids not distinct")
	}
	if err := StageHunk(d, p, hs[1].ID); err != nil {
		t.Fatal(err)
	}
	cached := sh(t, d, "diff", "--cached", "-U0")
	if !strings.Contains(cached, "CHANGED 15") || strings.Contains(cached, "CHANGED 2\n") || strings.Contains(cached, "CHANGED 28") {
		t.Errorf("cached diff wrong:\n%s", cached)
	}
	staged, _ := Hunks(d, p, true)
	if len(staged) != 1 {
		t.Fatalf("staged hunks %d", len(staged))
	}
	// Unstage it again.
	if err := UnstageHunk(d, p, staged[0].ID); err != nil {
		t.Fatal(err)
	}
	if c := sh(t, d, "diff", "--cached"); c != "" {
		t.Errorf("expected empty cached diff:\n%s", c)
	}
	// Revert the first hunk.
	hs, _ = Hunks(d, p, false)
	if len(hs) != 3 {
		t.Fatalf("hunks %d", len(hs))
	}
	if err := RevertHunk(d, p, hs[0].ID); err != nil {
		t.Fatal(err)
	}
	wt := sh(t, d, "diff", "-U0")
	if strings.Contains(wt, "CHANGED 2\n") || !strings.Contains(wt, "CHANGED 15") || !strings.Contains(wt, "CHANGED 28") {
		t.Errorf("worktree diff wrong:\n%s", wt)
	}
}

func TestStaleHunk(t *testing.T) {
	d, p := setupMultiHunk(t)
	hs, _ := Hunks(d, p, false)
	if err := StageHunk(d, p, hs[0].ID); err != nil {
		t.Fatal(err)
	}
	// Staging again with the same id: content is gone from the unstaged diff.
	if err := StageHunk(d, p, hs[0].ID); err != ErrStaleHunk {
		t.Errorf("want ErrStaleHunk, got %v", err)
	}
	if err := RevertHunk(d, p, "deadbeef"); err != ErrStaleHunk {
		t.Errorf("want ErrStaleHunk, got %v", err)
	}
}

func TestUntrackedHunk(t *testing.T) {
	d := newRepo(t)
	write(t, d, "new file.txt", "a\nb\nc\n")
	hs, err := Hunks(d, "new file.txt", false)
	if err != nil || len(hs) != 1 || hs[0].NewLines != 3 {
		t.Fatalf("%v %v", hs, err)
	}
	if err := StageHunk(d, "new file.txt", hs[0].ID); err != nil {
		t.Fatal(err)
	}
	if out := sh(t, d, "diff", "--cached", "--name-status"); !strings.HasPrefix(out, "A\tnew file.txt") {
		t.Errorf("got %q", out)
	}
	// Revert an untracked file's hunk deletes it.
	write(t, d, "other.txt", "x\ny\n")
	hs, _ = Hunks(d, "other.txt", false)
	if err := RevertHunk(d, "other.txt", hs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "other.txt")); !os.IsNotExist(err) {
		t.Errorf("file should be gone: %v", err)
	}
}

func TestFileOpsAndShow(t *testing.T) {
	d := newRepo(t)
	write(t, d, "README.md", "changed\n")
	write(t, d, "u.txt", "u\n")
	if err := StageFiles(d, []string{"README.md"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := ShowFile(d, ":0", "README.md"); s != "changed\n" {
		t.Errorf("index: %q", s)
	}
	if s, _ := ShowFile(d, "HEAD", "README.md"); s != "hi\n" {
		t.Errorf("head: %q", s)
	}
	if s, err := ShowFile(d, "HEAD", "u.txt"); s != "" || err != nil {
		t.Errorf("missing: %q %v", s, err)
	}
	if err := UnstageFiles(d, []string{"README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := DiscardFiles(d, []string{"README.md", "u.txt"}); err != nil {
		t.Fatal(err)
	}
	st, _ := StatusOf(d)
	if st.Dirty() {
		t.Errorf("still dirty: %+v", st)
	}
	if err := DiscardFiles(d, []string{"../escape"}); err == nil {
		// not untracked-in-repo, so git restore should fail
		t.Error("expected error for escaping path")
	}
	write(t, d, "x.txt", "x\n")
	StageFiles(d, []string{"x.txt"})
	if err := Commit(d, "add x\n\nbody", false); err != nil {
		t.Fatal(err)
	}
	if err := Commit(d, "add x v2", true); err != nil {
		t.Fatal(err)
	}
	if out := sh(t, d, "log", "-1", "--format=%s"); strings.TrimSpace(out) != "add x v2" {
		t.Errorf("amend: %q", out)
	}
}

func TestWorktrees(t *testing.T) {
	d := newRepo(t)
	wt := filepath.Join(filepath.Dir(d), filepath.Base(d)+"-wt")
	if err := WorktreeAdd(d, wt, "harness/a", "main"); err != nil {
		t.Fatal(err)
	}
	l, err := WorktreeList(d)
	if err != nil || len(l) != 2 {
		t.Fatalf("%v %v", l, err)
	}
	found := false
	for _, w := range l {
		if w.Branch == "harness/a" {
			found = true
		}
	}
	if !found {
		t.Errorf("%+v", l)
	}
	if b, _ := CurrentBranch(wt); b != "harness/a" {
		t.Errorf("branch %q", b)
	}
	if r, _ := RepoRoot(wt); r == "" {
		t.Error("no root")
	}
	if err := WorktreeRemove(d, wt, false); err != nil {
		t.Fatal(err)
	}
	if err := BranchDelete(d, "harness/a", true); err != nil {
		t.Fatal(err)
	}
	if err := BranchDelete(d, "nope", true); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("want stderr in error: %v", err)
	}
}
