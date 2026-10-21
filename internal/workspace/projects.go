// Package workspace manages projects and agent sessions (with their git
// worktrees) on top of the store database.
package workspace

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sid/harness/internal/git"
)

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func nowMs() int64 { return time.Now().UnixMilli() }

// ErrNotFound is returned when a project or session does not exist.
var ErrNotFound = errors.New("workspace: not found")

// Project is a registered git repository.
type Project struct {
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	Name       string   `json:"name"`
	LastOpened int64    `json:"lastOpened"`
	SetupCmds  []string `json:"setupCmds"`
}

// Projects manages the projects table.
type Projects struct{ db *sql.DB }

func NewProjects(db *sql.DB) *Projects { return &Projects{db: db} }

const projCols = "id, path, name, last_opened, setup_cmds"

type scanner interface{ Scan(...any) error }

func scanProject(r scanner) (Project, error) {
	var p Project
	var cmds string
	if err := r.Scan(&p.ID, &p.Path, &p.Name, &p.LastOpened, &cmds); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, ErrNotFound
		}
		return p, err
	}
	p.SetupCmds = []string{}
	if cmds != "" {
		if err := json.Unmarshal([]byte(cmds), &p.SetupCmds); err != nil {
			return p, fmt.Errorf("workspace: bad setup_cmds: %w", err)
		}
	}
	return p, nil
}

// Add registers the repo containing path (resolving its root). Adding an
// existing repo returns the existing project.
func (ps *Projects) Add(path string) (Project, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Project{}, err
	}
	root, err := git.RepoRoot(abs)
	if err != nil {
		return Project{}, fmt.Errorf("workspace: %s is not a git repository: %w", path, err)
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	row := ps.db.QueryRow("SELECT "+projCols+" FROM projects WHERE path = ?", root)
	if p, err := scanProject(row); err == nil {
		return p, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Project{}, err
	}
	id := newID()
	if _, err := ps.db.Exec("INSERT INTO projects (id, path, name, last_opened) VALUES (?,?,?,?)",
		id, root, filepath.Base(root), nowMs()); err != nil {
		return Project{}, err
	}
	return ps.Get(id)
}

func (ps *Projects) Get(id string) (Project, error) {
	return scanProject(ps.db.QueryRow("SELECT "+projCols+" FROM projects WHERE id = ?", id))
}

// List returns projects, most recently opened first.
func (ps *Projects) List() ([]Project, error) {
	rows, err := ps.db.Query("SELECT " + projCols + " FROM projects ORDER BY last_opened DESC, name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Touch sets last_opened to now.
func (ps *Projects) Touch(id string) error {
	return mustAffect(ps.db.Exec("UPDATE projects SET last_opened = ? WHERE id = ?", nowMs(), id))
}

// SetSetupCmds replaces the project's setup commands.
func (ps *Projects) SetSetupCmds(id string, cmds []string) error {
	if cmds == nil {
		cmds = []string{}
	}
	b, _ := json.Marshal(cmds)
	return mustAffect(ps.db.Exec("UPDATE projects SET setup_cmds = ? WHERE id = ?", string(b), id))
}

func mustAffect(r sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
