package workspace

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sid/harness/internal/git"
)

const (
	ScopeMain     = "main"
	ScopeWorktree = "worktree"
)

// ErrNotConfirmed is returned by Delete when confirm is false.
var ErrNotConfirmed = errors.New("workspace: delete requires confirmation")

// ErrArchived is returned when an operation needs a live worktree.
var ErrArchived = errors.New("workspace: session is archived")

// ErrDirty is returned by RaisePR when there are uncommitted changes.
type ErrDirty struct{ Files []string }

func (e *ErrDirty) Error() string {
	return fmt.Sprintf("workspace: uncommitted changes in %d file(s): %s", len(e.Files), strings.Join(e.Files, ", "))
}

// Session is a row of the sessions table.
type Session struct {
	ID                string `json:"id"`
	ProjectID         string `json:"projectId"`
	Title             string `json:"title"`
	Provider          string `json:"provider"`
	ProviderSessionID string `json:"providerSessionId"`
	Model             string `json:"model"`
	Mode              string `json:"mode"`
	Effort            string `json:"effort"`
	ScopeKind         string `json:"scopeKind"`
	WorktreePath      string `json:"worktreePath"`
	Branch            string `json:"branch"`
	BaseRef           string `json:"baseRef"`
	ForkedFrom        string `json:"forkedFrom"`
	Status            string `json:"status"`
	PRURL             string `json:"prUrl"`
	PRState           string `json:"prState"`
	Created           int64  `json:"created"`
	Updated           int64  `json:"updated"`
	ArchivedAt        *int64 `json:"archivedAt"`
}

// CreateOpts are the inputs for Sessions.Create.
type CreateOpts struct {
	Title      string
	Provider   string
	Model      string
	Mode       string
	Effort     string
	ScopeKind  string // main (default) or worktree
	BaseRef    string // defaults to the repo's current branch
	ForkedFrom string
}

// Scope identifies where a session's file and git operations run.
type Scope struct {
	ProjectID string `json:"projectId"`
	Kind      string `json:"kind"`
	SessionID string `json:"sessionId"`
	Root      string `json:"root"`
}

// Sessions manages sessions and their worktrees.
type Sessions struct {
	db       *sql.DB
	projects *Projects
	mu       sync.Mutex // serialises worktree path/branch allocation
}

func NewSessions(db *sql.DB) *Sessions {
	return &Sessions{db: db, projects: NewProjects(db)}
}

const sessCols = `id, project_id, title, provider, provider_session_id, model, mode, effort, scope_kind,
 worktree_path, branch, base_ref, forked_from, status, pr_url, pr_state, created, updated, archived_at`

func scanSession(r scanner) (Session, error) {
	var s Session
	var arch sql.NullInt64
	err := r.Scan(&s.ID, &s.ProjectID, &s.Title, &s.Provider, &s.ProviderSessionID, &s.Model, &s.Mode, &s.Effort,
		&s.ScopeKind, &s.WorktreePath, &s.Branch, &s.BaseRef, &s.ForkedFrom, &s.Status, &s.PRURL, &s.PRState,
		&s.Created, &s.Updated, &arch)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if arch.Valid {
		v := arch.Int64
		s.ArchivedAt = &v
	}
	return s, err
}

// Slugify lowercases s, maps runs of non [a-z0-9] to single dashes and
// truncates to 40 chars. An empty result becomes "session".
func Slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		} else {
			dash = true
		}
	}
	out := b.String()
	if len(out) > 40 {
		out = strings.TrimRight(out[:40], "-")
	}
	if out == "" {
		out = "session"
	}
	return out
}

func (ss *Sessions) Get(id string) (Session, error) {
	return scanSession(ss.db.QueryRow("SELECT "+sessCols+" FROM sessions WHERE id = ?", id))
}

// List returns a project's sessions, newest activity first.
func (ss *Sessions) List(projectID string, includeArchived bool) ([]Session, error) {
	q := "SELECT " + sessCols + " FROM sessions WHERE project_id = ?"
	if !includeArchived {
		q += " AND archived_at IS NULL"
	}
	rows, err := ss.db.Query(q+" ORDER BY updated DESC, created DESC", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Create inserts a session; for worktree scope it also creates the worktree
// at <repo parent>/<repo name>-worktrees/<slug> on branch harness/<slug>.
func (ss *Sessions) Create(projectID string, o CreateOpts) (Session, error) {
	p, err := ss.projects.Get(projectID)
	if err != nil {
		return Session{}, err
	}
	if o.ScopeKind == "" {
		o.ScopeKind = ScopeMain
	}
	if o.ScopeKind != ScopeMain && o.ScopeKind != ScopeWorktree {
		return Session{}, fmt.Errorf("workspace: bad scope kind %q", o.ScopeKind)
	}
	s := Session{
		ID: newID(), ProjectID: projectID, Title: o.Title, Provider: o.Provider, Model: o.Model, Mode: o.Mode,
		Effort: o.Effort, ScopeKind: o.ScopeKind, BaseRef: o.BaseRef, ForkedFrom: o.ForkedFrom, Status: "idle",
	}
	if s.Provider == "" {
		s.Provider = "claude"
	}
	if o.ScopeKind == ScopeWorktree {
		ss.mu.Lock()
		defer ss.mu.Unlock()
		if s.BaseRef == "" {
			if s.BaseRef, err = git.CurrentBranch(p.Path); err != nil {
				return Session{}, err
			}
		}
		slug := Slugify(o.Title)
		wtRoot := filepath.Join(filepath.Dir(p.Path), filepath.Base(p.Path)+"-worktrees")
		dir, branch := ss.allocate(p.Path, wtRoot, slug)
		if err := os.MkdirAll(wtRoot, 0o755); err != nil {
			return Session{}, err
		}
		if err := git.WorktreeAdd(p.Path, dir, branch, s.BaseRef); err != nil {
			return Session{}, err
		}
		s.WorktreePath, s.Branch = dir, branch
	}
	s.Created = nowMs()
	s.Updated = s.Created
	_, err = ss.db.Exec(`INSERT INTO sessions (id, project_id, title, provider, model, mode, effort, scope_kind,
 worktree_path, branch, base_ref, forked_from, status, created, updated) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.ProjectID, s.Title, s.Provider, s.Model, s.Mode, s.Effort, s.ScopeKind, s.WorktreePath, s.Branch,
		s.BaseRef, s.ForkedFrom, s.Status, s.Created, s.Updated)
	if err != nil {
		if s.WorktreePath != "" { // roll back
			git.WorktreeRemove(p.Path, s.WorktreePath, true)
			git.BranchDelete(p.Path, s.Branch, true)
		}
		return Session{}, err
	}
	return s, nil
}

// allocate finds a free <slug>[-N] where neither the dir, the branch nor a
// session row uses it.
func (ss *Sessions) allocate(repo, wtRoot, slug string) (dir, branch string) {
	for n := 1; ; n++ {
		name := slug
		if n > 1 {
			name = fmt.Sprintf("%s-%d", slug, n)
		}
		dir, branch = filepath.Join(wtRoot, name), "harness/"+name
		if _, err := os.Lstat(dir); err == nil {
			continue
		}
		if git.BranchExists(repo, branch) {
			continue
		}
		var c int
		ss.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE branch = ? OR worktree_path = ?", branch, dir).Scan(&c)
		if c > 0 {
			continue
		}
		return
	}
}

func (ss *Sessions) set(id, col string, v any) error {
	return mustAffect(ss.db.Exec("UPDATE sessions SET "+col+" = ?, updated = ? WHERE id = ?", v, nowMs(), id))
}

func (ss *Sessions) Rename(id, title string) error { return ss.set(id, "title", title) }
func (ss *Sessions) SetProviderSessionID(id, psid string) error {
	return ss.set(id, "provider_session_id", psid)
}
func (ss *Sessions) SetStatus(id, status string) error { return ss.set(id, "status", status) }

// SetPR stores the PR url and state.
func (ss *Sessions) SetPR(id, url, state string) error {
	return mustAffect(ss.db.Exec("UPDATE sessions SET pr_url = ?, pr_state = ?, updated = ? WHERE id = ?",
		url, state, nowMs(), id))
}

func (ss *Sessions) load(id string) (Session, Project, error) {
	s, err := ss.Get(id)
	if err != nil {
		return s, Project{}, err
	}
	p, err := ss.projects.Get(s.ProjectID)
	return s, p, err
}

// sharedWorktree reports whether another live session uses the same worktree,
// as a cross-provider fork does.
func (ss *Sessions) sharedWorktree(s Session) (bool, error) {
	if s.ScopeKind != ScopeWorktree || s.WorktreePath == "" {
		return false, nil
	}
	var n int
	err := ss.db.QueryRow(`SELECT COUNT(*) FROM sessions
		WHERE worktree_path = ? AND id != ? AND archived_at IS NULL`, s.WorktreePath, s.ID).Scan(&n)
	return n > 0, err
}

// Archive removes the worktree (keeping the branch) and sets archived_at.
// Without force, a worktree with uncommitted changes makes it fail. A worktree
// still used by another live session is left in place.
func (ss *Sessions) Archive(id string, force bool) error {
	s, p, err := ss.load(id)
	if err != nil {
		return err
	}
	if s.ArchivedAt != nil {
		return nil
	}
	shared, err := ss.sharedWorktree(s)
	if err != nil {
		return err
	}
	if s.ScopeKind == ScopeWorktree && !shared {
		if _, err := os.Stat(s.WorktreePath); err == nil {
			if err := git.WorktreeRemove(p.Path, s.WorktreePath, force); err != nil {
				return err
			}
		} else {
			git.WorktreePrune(p.Path)
		}
	}
	now := nowMs()
	_, err = ss.db.Exec("UPDATE sessions SET archived_at = ?, updated = ? WHERE id = ?", now, now, id)
	return err
}

// Restore re-adds the worktree from the kept branch and clears archived_at.
func (ss *Sessions) Restore(id string) (Session, error) {
	s, p, err := ss.load(id)
	if err != nil {
		return s, err
	}
	if s.ArchivedAt == nil {
		return s, nil
	}
	shared, err := ss.sharedWorktree(s)
	if err != nil {
		return s, err
	}
	if s.ScopeKind == ScopeWorktree && !shared {
		ss.mu.Lock()
		defer ss.mu.Unlock()
		dir := s.WorktreePath
		if _, err := os.Lstat(dir); err == nil {
			for n := 2; ; n++ {
				cand := fmt.Sprintf("%s-%d", s.WorktreePath, n)
				if _, err := os.Lstat(cand); err != nil {
					dir = cand
					break
				}
			}
		}
		git.WorktreePrune(p.Path)
		if err := git.WorktreeAdd(p.Path, dir, s.Branch, ""); err != nil {
			return s, err
		}
		s.WorktreePath = dir
	}
	s.Updated = nowMs()
	s.ArchivedAt = nil
	_, err = ss.db.Exec("UPDATE sessions SET archived_at = NULL, worktree_path = ?, updated = ? WHERE id = ?",
		s.WorktreePath, s.Updated, id)
	return s, err
}

// Delete removes the worktree, force-deletes the branch and drops the row.
// A worktree still used by another live session is kept, along with its branch.
func (ss *Sessions) Delete(id string, confirm bool) error {
	if !confirm {
		return ErrNotConfirmed
	}
	s, p, err := ss.load(id)
	if err != nil {
		return err
	}
	shared, err := ss.sharedWorktree(s)
	if err != nil {
		return err
	}
	if s.ScopeKind == ScopeWorktree && !shared {
		if _, err := os.Stat(s.WorktreePath); err == nil {
			if err := git.WorktreeRemove(p.Path, s.WorktreePath, true); err != nil {
				return err
			}
		}
		git.WorktreePrune(p.Path)
		if s.Branch != "" && git.BranchExists(p.Path, s.Branch) {
			if err := git.BranchDelete(p.Path, s.Branch, true); err != nil {
				return err
			}
		}
	}
	_, err = ss.db.Exec("DELETE FROM sessions WHERE id = ?", id)
	return err
}

// Scope returns where the session's operations run.
func (ss *Sessions) Scope(id string) (Scope, error) {
	s, p, err := ss.load(id)
	if err != nil {
		return Scope{}, err
	}
	sc := Scope{ProjectID: s.ProjectID, Kind: s.ScopeKind, SessionID: s.ID, Root: p.Path}
	if s.ScopeKind == ScopeWorktree {
		if s.ArchivedAt != nil {
			return sc, ErrArchived
		}
		sc.Root = s.WorktreePath
	}
	return sc, nil
}

// RaisePR pushes the session branch and opens a PR. It fails with *ErrDirty
// when there are uncommitted changes. It stores and returns the PR URL.
func (ss *Sessions) RaisePR(id, title, body string) (string, error) {
	s, err := ss.Get(id)
	if err != nil {
		return "", err
	}
	sc, err := ss.Scope(id)
	if err != nil {
		return "", err
	}
	st, err := git.StatusOf(sc.Root)
	if err != nil {
		return "", err
	}
	if st.Dirty() {
		return "", &ErrDirty{Files: st.Paths()}
	}
	branch := s.Branch
	if branch == "" {
		branch = st.Branch
	}
	if err := git.Push(sc.Root, branch); err != nil {
		return "", err
	}
	url, err := git.PRCreate(sc.Root, title, body, s.BaseRef)
	if err != nil {
		return "", err
	}
	if err := ss.SetPR(id, url, "OPEN"); err != nil {
		return url, err
	}
	return url, nil
}

// RefreshPR polls gh for the PR state and stores it.
func (ss *Sessions) RefreshPR(id string) (string, error) {
	s, err := ss.Get(id)
	if err != nil {
		return "", err
	}
	if s.PRURL == "" {
		return "", nil
	}
	sc, err := ss.Scope(id)
	if err != nil {
		return "", err
	}
	state, err := git.PRState(sc.Root, s.PRURL)
	if err != nil {
		return "", err
	}
	return state, ss.SetPR(id, s.PRURL, state)
}
