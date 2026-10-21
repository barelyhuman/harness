package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sid/harness/internal/git"
)

func TestBuildSCM(t *testing.T) {
	st := git.Status{
		Branch: "main", Ahead: 1,
		Staged: []git.FileChange{
			{Path: "src/b.go", Code: "A"},
			{Path: "README.md", Code: "M"},
			{Path: "new/name.go", OrigPath: "old.go", Code: "R"},
		},
		Unstaged:   []git.FileChange{{Path: "src/b.go", Code: "M"}, {Path: "gone.txt", Code: "D"}},
		Untracked:  []git.FileChange{{Path: "a/z.txt", Code: "U"}},
		Conflicted: []git.FileChange{{Path: "c.go", Code: "U"}},
	}
	got := buildSCM("/r", st)
	if len(got.Groups) != 3 || got.Groups[0].ID != "merge" || got.Groups[1].ID != "staged" || got.Groups[2].ID != "changes" {
		t.Fatalf("groups: %+v", got.Groups)
	}
	if got.Count != 7 || !got.Dirty || got.Branch != "main" || got.Ahead != 1 {
		t.Fatalf("header: %+v", got)
	}
	if r := got.Groups[0].Rows[0]; r.Badge != "!" || r.Class != "scm-conflict" {
		t.Fatalf("conflict row: %+v", r)
	}
	staged := got.Groups[1].Rows
	if staged[0].Path != "new/name.go" { // sorted case-insensitively: new/ < README < src/
		t.Fatalf("order: %+v", staged)
	}
	if r := staged[0]; r.Badge != "R" || r.Class != "scm-renamed" || r.Name != "name.go" || r.Dir != "new" || r.OrigPath != "old.go" {
		t.Fatalf("rename row: %+v", r)
	}
	ch := got.Groups[2].Rows // a/z.txt (untracked merged), gone.txt, src/b.go
	if len(ch) != 3 || ch[0].Path != "a/z.txt" || ch[0].Badge != "U" || ch[0].Class != "scm-untracked" {
		t.Fatalf("changes: %+v", ch)
	}
	if ch[1].Badge != "D" || ch[1].Class != "scm-deleted" || ch[1].Dir != "" || ch[1].Name != "gone.txt" {
		t.Fatalf("deleted row: %+v", ch[1])
	}
	if ch[2].Class != "scm-modified" {
		t.Fatalf("modified row: %+v", ch[2])
	}
}

func TestBuildSCMClean(t *testing.T) {
	got := buildSCM("/r", git.Status{})
	if got.Dirty || got.Count != 0 || len(got.Groups) != 2 || got.Groups[0].Rows == nil {
		t.Fatalf("%+v", got)
	}
}

func TestGitServiceStatusAndStage(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi\n"), 0o644)
	g := &GitService{hub: newScopeHub(nil, nil)}
	st, err := g.Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Groups) != 2 || st.Groups[1].Count != 1 || st.Groups[1].Rows[0].Badge != "U" {
		t.Fatalf("%+v", st)
	}
	if err := g.StageFiles(dir, []string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	st, _ = g.Status(dir)
	if st.Groups[0].Count != 1 || st.Groups[0].Rows[0].Badge != "A" || st.Groups[1].Count != 0 {
		t.Fatalf("%+v", st)
	}
}
