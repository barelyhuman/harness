package app

import (
	"errors"
	"os"

	"github.com/sid/harness/internal/fs"
	"github.com/sid/harness/internal/git"
	"github.com/sid/harness/internal/host"
	"github.com/sid/harness/internal/pty"
	"github.com/sid/harness/internal/termlayout"
	"github.com/sid/harness/internal/workspace"
)

// Error codes carried in AppError.Code. The frontend switches on these.
const (
	CodeDirty        = "dirty"         // uncommitted changes; Files lists them
	CodeConflict     = "conflict"      // etag mismatch; Etag is the current etag
	CodeNotFound     = "not_found"     // project/session/pty/pane/tab missing
	CodeArchived     = "archived"      // session is archived
	CodeNotConfirmed = "not_confirmed" // destructive op needs confirm=true
	CodeStaleHunk    = "stale_hunk"    // hunk id no longer matches the diff
	CodeGit          = "git"           // git/gh command failed; Message has stderr
	CodeBadArg       = "bad_arg"
	CodeInternal     = "internal"
)

// AppError is the JSON-friendly error every binding returns on failure. The
// frontend receives it as the rejection's `cause`:
// {code, message, files?, etag?}.
type AppError struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Files   []string `json:"files,omitempty"`
	Etag    string   `json:"etag,omitempty"`
}

func (e *AppError) Error() string { return e.Message }

// wrapErr converts package errors into *AppError (nil stays nil).
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	var ae *AppError
	if errors.As(err, &ae) {
		return ae
	}
	out := &AppError{Code: CodeInternal, Message: err.Error()}
	var dirty *workspace.ErrDirty
	var conflict *fs.ErrConflict
	var gerr *git.Error
	switch {
	case errors.As(err, &dirty):
		out.Code, out.Files = CodeDirty, dirty.Files
	case errors.As(err, &conflict):
		out.Code, out.Etag = CodeConflict, conflict.Current
	case errors.Is(err, workspace.ErrNotFound), errors.Is(err, os.ErrNotExist), errors.Is(err, pty.ErrNotFound),
		errors.Is(err, termlayout.ErrNoTab), errors.Is(err, termlayout.ErrNoPane):
		out.Code = CodeNotFound
	case errors.Is(err, host.ErrHostGone):
		out.Code, out.Message = CodeNotFound, "no live agent run"
	case errors.Is(err, workspace.ErrArchived):
		out.Code = CodeArchived
	case errors.Is(err, workspace.ErrNotConfirmed):
		out.Code = CodeNotConfirmed
	case errors.Is(err, git.ErrStaleHunk):
		out.Code = CodeStaleHunk
	case errors.Is(err, termlayout.ErrBadArg):
		out.Code = CodeBadArg
	case errors.As(err, &gerr):
		out.Code = CodeGit
		if gerr.Stderr != "" {
			out.Message = gerr.Stderr
		}
	}
	return out
}

func badArg(msg string) error { return &AppError{Code: CodeBadArg, Message: msg} }

var errNoRun = &AppError{Code: CodeNotFound, Message: "no live agent run"}
