package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func testApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	tmp := t.TempDir()
	a, err := New(Options{DataDir: filepath.Join(tmp, "data"), ConfigDir: filepath.Join(tmp, "cfg"), SkipModelRefresh: true, SkipRecover: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Shutdown)
	return a
}

func TestFSReadWriteBinary(t *testing.T) {
	a := testApp(t)
	root := t.TempDir()
	txtPath := filepath.Join(root, "x.go")
	binPath := filepath.Join(root, "data.bin")
	os.WriteFile(txtPath, []byte("package main\n"), 0o644)
	os.WriteFile(binPath, []byte{0x00, 0x01, 'a'}, 0o644)
	fc, err := a.FSSvc.Read(txtPath)
	if err != nil || fc.Language != "go" || fc.Binary || fc.Content == "" {
		t.Fatalf("%+v %v", fc, err)
	}
	fc, err = a.FSSvc.Read(binPath)
	if err != nil || !fc.Binary || fc.Content != "" {
		t.Fatalf("%+v %v", fc, err)
	}
	got, err := a.FSSvc.Read(txtPath)
	if err != nil {
		t.Fatal(err)
	}
	etag, err := a.FSSvc.Write(txtPath, "package main\n// edit\n", got.Etag)
	if err != nil || etag == "" {
		t.Fatal(etag, err)
	}
	if err := a.FSSvc.Create(filepath.Join(root, "sub"), true); err != nil {
		t.Fatal(err)
	}
	if err := a.FSSvc.Rename(txtPath, filepath.Join(root, "sub", "y.go")); err != nil {
		t.Fatal(err)
	}
}

func TestProjectServiceCRUD(t *testing.T) {
	a := testApp(t)
	repo := filepath.Join(t.TempDir(), "r")
	os.MkdirAll(repo, 0o755)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := execGit(repo, args...); err != nil {
			t.Fatal(string(out))
		}
	}
	p, err := a.ProjectSvc.AddProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := a.ProjectSvc.ListProjects()
	if err != nil || len(ps) != 1 {
		t.Fatal(ps, err)
	}
	if err := a.ProjectSvc.TouchProject(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.ProjectSvc.SetSetupCmds(p.ID, []string{"make"}); err != nil {
		t.Fatal(err)
	}
	if err := a.ProjectSvc.RemoveProject("missing"); err == nil {
		t.Fatal("expected not found")
	}
	if err := a.ProjectSvc.RemoveProject(p.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLanguageForPaths(t *testing.T) {
	if LanguageFor("/a/Dockerfile") != "dockerfile" {
		t.Fatal()
	}
	if LanguageFor("/a/file.tsx") != "typescript" {
		t.Fatal()
	}
}

func execGit(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}
