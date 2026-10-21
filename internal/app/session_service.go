package app

import (
	"database/sql"
	"log"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/git"
	"github.com/sid/harness/internal/workspace"
)

// CreateSessionOpts are the inputs for CreateSession.
type CreateSessionOpts struct {
	Title      string `json:"title"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Mode       string `json:"mode"`
	Effort     string `json:"effort"`
	ScopeKind  string `json:"scopeKind"` // "main" (default) | "worktree"
	BaseRef    string `json:"baseRef"`   // default: repo's current branch
	ForkedFrom string `json:"forkedFrom"`
}

// ScopeInfo is workspace.Scope plus the stable bus/ui-state scope key:
// "session:<id>" for sessions, "main:<projectId>" for main checkouts.
type ScopeInfo struct {
	Key       string `json:"key"`
	ProjectID string `json:"projectId"`
	Kind      string `json:"kind"` // main | worktree
	SessionID string `json:"sessionId"`
	Root      string `json:"root"`
}

// SessionService: session metadata + worktrees, and the agent-run bindings
// (see session_agent.go).
type SessionService struct {
	sessions *workspace.Sessions
	projects *workspace.Projects
	db       *sql.DB
	sup      *supervisor
	models   *agent.ModelRegistry
	settings *SettingsService
	term     *TermService
}

func (s *SessionService) push() {
	if s.sup != nil {
		s.sup.schedulePush()
	}
}

func (s *SessionService) ListSessions(projectID string, includeArchived bool) ([]workspace.Session, error) {
	l, err := s.sessions.List(projectID, includeArchived)
	if l == nil {
		l = []workspace.Session{}
	}
	return l, wrapErr(err)
}

// CreateSession inserts the session and, for scopeKind "worktree", creates the
// git worktree + branch. The project's setup commands (if any) start in a
// "setup" terminal tab of the session scope; their failure never blocks the
// session (the tab shows it). The agent host launches on the first Send.
func (s *SessionService) CreateSession(projectID string, o CreateSessionOpts) (workspace.Session, error) {
	sess, err := s.sessions.Create(projectID, workspace.CreateOpts{
		Title: o.Title, Provider: o.Provider, Model: o.Model, Mode: o.Mode, Effort: o.Effort,
		ScopeKind: o.ScopeKind, BaseRef: o.BaseRef, ForkedFrom: o.ForkedFrom,
	})
	if err != nil {
		return sess, wrapErr(err)
	}
	s.runSetup(sess)
	s.push()
	return sess, nil
}

func (s *SessionService) runSetup(sess workspace.Session) {
	if s.term == nil {
		return
	}
	p, err := s.projects.Get(sess.ProjectID)
	if err != nil || len(p.SetupCmds) == 0 {
		return
	}
	sc, err := s.sessions.Scope(sess.ID)
	if err != nil {
		return
	}
	if _, err := s.term.RunSetup(SessionScopeKey(sess.ID), sc.Root, p.SetupCmds); err != nil {
		log.Printf("harness: setup terminal for %s: %v", sess.ID, err)
	}
}

func (s *SessionService) GetSession(id string) (workspace.Session, error) {
	sess, err := s.sessions.Get(id)
	return sess, wrapErr(err)
}

func (s *SessionService) RenameSession(id, title string) error {
	err := s.sessions.Rename(id, title)
	s.push()
	return wrapErr(err)
}

// ArchiveSession removes the worktree (keeps the branch). Without force a
// dirty worktree fails with code "dirty" (files listed when known).
func (s *SessionService) ArchiveSession(id string, force bool) error {
	if !force {
		if sess, err := s.sessions.Get(id); err == nil && sess.ScopeKind == workspace.ScopeWorktree &&
			sess.ArchivedAt == nil {
			if st, err := git.StatusOf(sess.WorktreePath); err == nil && st.Dirty() {
				return &AppError{Code: CodeDirty, Message: "worktree has uncommitted changes", Files: st.Paths()}
			}
		}
	}
	err := s.sessions.Archive(id, force)
	if err == nil && s.sup != nil {
		s.sup.release(id)
	}
	s.push()
	return wrapErr(err)
}

func (s *SessionService) RestoreSession(id string) (workspace.Session, error) {
	sess, err := s.sessions.Restore(id)
	s.push()
	return sess, wrapErr(err)
}

// DeleteSession removes worktree + branch + row. confirm must be true, else
// code "not_confirmed".
func (s *SessionService) DeleteSession(id string, confirm bool) error {
	err := s.sessions.Delete(id, confirm)
	if err == nil && s.sup != nil {
		s.sup.release(id)
	}
	s.push()
	return wrapErr(err)
}

// RaisePR pushes the branch and opens a PR; returns the URL. Code "dirty" with
// files when there are uncommitted changes.
func (s *SessionService) RaisePR(id, title, body string) (string, error) {
	u, err := s.sessions.RaisePR(id, title, body)
	s.push()
	return u, wrapErr(err)
}

// RefreshPR polls gh for the PR state and returns the PR state string.
func (s *SessionService) RefreshPR(id string) (string, error) {
	st, err := s.sessions.RefreshPR(id)
	s.push()
	return st, wrapErr(err)
}

// Scope returns where the session's operations run.
func (s *SessionService) Scope(sessionID string) (ScopeInfo, error) {
	sc, err := s.sessions.Scope(sessionID)
	if err != nil {
		return ScopeInfo{}, wrapErr(err)
	}
	return ScopeInfo{Key: SessionScopeKey(sessionID), ProjectID: sc.ProjectID, Kind: sc.Kind,
		SessionID: sc.SessionID, Root: sc.Root}, nil
}

// MainScope is the scope of a project's main checkout (no session).
func (s *SessionService) MainScope(projectID string) (ScopeInfo, error) {
	p, err := s.projects.Get(projectID)
	if err != nil {
		return ScopeInfo{}, wrapErr(err)
	}
	return ScopeInfo{Key: MainScopeKey(projectID), ProjectID: projectID, Kind: workspace.ScopeMain, Root: p.Path}, nil
}

// SessionScopeKey / MainScopeKey build the scope keys used for bus channels
// (fs/, scm/, termlayout/) and ui_state.
func SessionScopeKey(sessionID string) string { return "session:" + sessionID }
func MainScopeKey(projectID string) string    { return "main:" + projectID }
