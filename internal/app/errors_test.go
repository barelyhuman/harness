package app

import (
	"errors"
	"os"
	"testing"

	"github.com/sid/harness/internal/fs"
	"github.com/sid/harness/internal/git"
	"github.com/sid/harness/internal/host"
	"github.com/sid/harness/internal/workspace"
)

func TestWrapErrMapping(t *testing.T) {
	cases := []struct {
		in   error
		code string
	}{
		{nil, ""},
		{&workspace.ErrDirty{Files: []string{"a"}}, CodeDirty},
		{&fs.ErrConflict{Current: "e1"}, CodeConflict},
		{workspace.ErrNotFound, CodeNotFound},
		{host.ErrHostGone, CodeNotFound},
		{workspace.ErrArchived, CodeArchived},
		{workspace.ErrNotConfirmed, CodeNotConfirmed},
		{git.ErrStaleHunk, CodeStaleHunk},
		{&git.Error{Stderr: "fatal: nope"}, CodeGit},
		{os.ErrNotExist, CodeNotFound},
		{errors.New("other"), CodeInternal},
	}
	for _, c := range cases {
		err := wrapErr(c.in)
		if c.in == nil {
			if err != nil {
				t.Fatal("want nil")
			}
			continue
		}
		ae, ok := err.(*AppError)
		if !ok || ae.Code != c.code {
			t.Fatalf("%v -> %v", c.in, err)
		}
	}
	if ae := wrapErr(&AppError{Code: CodeBadArg, Message: "x"}); ae.(*AppError).Code != CodeBadArg {
		t.Fatal(ae)
	}
}
