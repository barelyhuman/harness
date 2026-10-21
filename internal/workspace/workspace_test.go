package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sid/harness/internal/store"
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

func setup(t *testing.T) (*Projects, *Sessions, Project) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(root, "myrepo")
	os.Mkdir(repo, 0o755)
	sh(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	sh(t, repo, "add", "-A")
	sh(t, repo, "commit", "-q", "-m", "init")
	st, err := store.Open(filepath.Join(root, "db", "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ps, ss := NewProjects(st.DB), NewSessions(st.DB)
	sub := filepath.Join(repo, "sub")
	os.Mkdir(sub, 0o755)
	p, err := ps.Add(sub) // resolves root from a subdir
	if err != nil {
		t.Fatal(err)
	}
	return ps, ss, p
}

func TestProjects(t *testing.T) {
	ps, _, p := setup(t)
	if filepath.Base(p.Path) != "myrepo" || p.Name != "myrepo" {
		t.Fatalf("%+v", p)
	}
	p2, _ := ps.Add(p.Path)
	if p2.ID != p.ID {
		t.Error("Add should be idempotent")
	}
	if err := ps.SetSetupCmds(p.ID, []string{"npm i", "make"}); err != nil {
		t.Fatal(err)
	}
	if err := ps.Touch(p.ID); err != nil {
		t.Fatal(err)
	}
	g, _ := ps.Get(p.ID)
	if len(g.SetupCmds) != 2 || g.SetupCmds[1] != "make" {
		t.Errorf("%+v", g)
	}
	if l, _ := ps.List(); len(l) != 1 {
		t.Errorf("list %d", len(l))
	}
	if _, err := ps.Add(t.TempDir()); err == nil {
		t.Error("non-repo should fail")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Fix the Bug!!":           "fix-the-bug",
		"  ":                      "session",
		"a/b_c  d":                "a-b-c-d",
		strings.Repeat("ab ", 30): "ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-a",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q)=%q want %q", in, got, want)
		}
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	_, ss, p := setup(t)
	a, err := ss.Create(p.ID, CreateOpts{Title: "Add Login", ScopeKind: ScopeWorktree})
	if err != nil {
		t.Fatal(err)
	}
	wantDir := a.WorktreePath
	branchRe := regexp.MustCompile(`^harness/add-login-[0-9a-f]{6}$`)
	if wantDir != filepath.Join(filepath.Dir(p.Path), "myrepo-worktrees", strings.TrimPrefix(a.Branch, "harness/")) ||
		!branchRe.MatchString(a.Branch) || a.BaseRef != "main" {
		t.Fatalf("%+v", a)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	b, err := ss.Create(p.ID, CreateOpts{Title: "add login", ScopeKind: ScopeWorktree})
	if err != nil {
		t.Fatal(err)
	}
	if !branchRe.MatchString(b.Branch) || b.Branch == a.Branch {
		t.Fatalf("collision: %+v", b)
	}
	// Pre-existing branch also forces a suffix.
	c, err := ss.Create(p.ID, CreateOpts{Title: "other", ScopeKind: ScopeWorktree})
	if err != nil || !regexp.MustCompile(`^harness/other-[0-9a-f]{6}$`).MatchString(c.Branch) {
		t.Fatalf("%+v %v", c, err)
	}
	sc, _ := ss.Scope(a.ID)
	if sc.Root != wantDir || sc.Kind != ScopeWorktree || sc.SessionID != a.ID || sc.ProjectID != p.ID {
		t.Errorf("%+v", sc)
	}

	// Archive keeps the branch, removes the dir.
	if err := ss.Archive(a.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wantDir); !os.IsNotExist(err) {
		t.Error("worktree should be gone")
	}
	sh(t, p.Path, "rev-parse", "--verify", a.Branch)
	if l, _ := ss.List(p.ID, false); len(l) != 2 {
		t.Errorf("active list %d", len(l))
	}
	if l, _ := ss.List(p.ID, true); len(l) != 3 {
		t.Errorf("full list %d", len(l))
	}
	if _, err := ss.Scope(a.ID); !errors.Is(err, ErrArchived) {
		t.Errorf("scope: %v", err)
	}
	// Restore.
	r, err := ss.Restore(a.ID)
	if err != nil || r.ArchivedAt != nil {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "a.txt")); err != nil {
		t.Error(err)
	}
	// Delete requires confirm.
	if err := ss.Delete(a.ID, false); !errors.Is(err, ErrNotConfirmed) {
		t.Errorf("%v", err)
	}
	if err := ss.Delete(a.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wantDir); !os.IsNotExist(err) {
		t.Error("dir should be gone")
	}
	if out := sh(t, p.Path, "branch", "--list", a.Branch); strings.TrimSpace(out) != "" {
		t.Errorf("branch remains: %q", out)
	}
	if _, err := ss.Get(a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("%v", err)
	}
	// Delete of an archived session whose worktree is gone.
	ss.Archive(b.ID, false)
	if err := ss.Delete(b.ID, true); err != nil {
		t.Fatal(err)
	}
	d, err := ss.Create(p.ID, CreateOpts{Title: "Add Login", ScopeKind: ScopeWorktree})
	if err != nil || !branchRe.MatchString(d.Branch) {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestSessionFieldsAndMain(t *testing.T) {
	_, ss, p := setup(t)
	s, err := ss.Create(p.ID, CreateOpts{Title: "main one", Provider: "cursor", Model: "m", ForkedFrom: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ScopeKind != ScopeMain || s.WorktreePath != "" {
		t.Fatalf("%+v", s)
	}
	ss.Rename(s.ID, "renamed")
	ss.SetProviderSessionID(s.ID, "psid")
	ss.SetStatus(s.ID, "running")
	ss.SetPR(s.ID, "http://pr/1", "OPEN")
	g, _ := ss.Get(s.ID)
	if g.Title != "renamed" || g.ProviderSessionID != "psid" || g.Status != "running" || g.PRURL != "http://pr/1" || g.ForkedFrom != "x" || g.Provider != "cursor" {
		t.Errorf("%+v", g)
	}
	sc, _ := ss.Scope(s.ID)
	if sc.Root != p.Path || sc.Kind != ScopeMain {
		t.Errorf("%+v", sc)
	}
	if err := ss.Archive(s.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.Restore(s.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRaisePRDirty(t *testing.T) {
	_, ss, p := setup(t)
	s, err := ss.Create(p.ID, CreateOpts{Title: "dirty", ScopeKind: ScopeWorktree})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(s.WorktreePath, "new.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(s.WorktreePath, "a.txt"), []byte("changed"), 0o644)
	_, err = ss.RaisePR(s.ID, "t", "b")
	var d *ErrDirty
	if !errors.As(err, &d) {
		t.Fatalf("want ErrDirty, got %v", err)
	}
	if len(d.Files) != 2 {
		t.Errorf("files %v", d.Files)
	}
	// Archive of a dirty worktree without force fails; with force succeeds.
	if err := ss.Archive(s.ID, false); err == nil {
		t.Error("expected archive to fail on dirty worktree")
	}
	if err := ss.Archive(s.ID, true); err != nil {
		t.Fatal(err)
	}
}

func TestSharedWorktreeFork(t *testing.T) {
	_, ss, p := setup(t)
	src, err := ss.Create(p.ID, CreateOpts{Title: "fork me", ScopeKind: ScopeWorktree})
	if err != nil {
		t.Fatal(err)
	}
	fork, err := ss.Create(p.ID, CreateOpts{Title: "fork", Provider: "cursor", ForkedFrom: src.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Cross-provider forks reuse the source worktree.
	if _, err := ss.db.Exec("UPDATE sessions SET scope_kind = ?, worktree_path = ?, branch = ? WHERE id = ?",
		ScopeWorktree, src.WorktreePath, src.Branch, fork.ID); err != nil {
		t.Fatal(err)
	}

	if err := ss.Archive(src.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src.WorktreePath); err != nil {
		t.Fatalf("archiving the source removed the worktree its fork uses: %v", err)
	}
	if err := ss.Delete(src.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src.WorktreePath); err != nil {
		t.Fatalf("deleting the source removed the shared worktree: %v", err)
	}

	// The last session using the worktree removes it.
	if err := ss.Archive(fork.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree should be removed once unused, stat err = %v", err)
	}
}
