package app

import (
	"database/sql"
	"os"
	"time"

	"github.com/sid/harness/internal/fs"
	"github.com/sid/harness/internal/fuzzy"
	"github.com/sid/harness/internal/workspace"
)

// FuzzyMatch is a ranked candidate: Index into the input slice, Score, and the
// matched rune Positions for highlighting.
type FuzzyMatch struct {
	Index     int   `json:"index"`
	Score     int   `json:"score"`
	Positions []int `json:"positions"`
}

func toFuzzy(ms []fuzzy.Match) []FuzzyMatch {
	out := make([]FuzzyMatch, len(ms))
	for i, m := range ms {
		out[i] = FuzzyMatch{Index: m.Index, Score: m.Score, Positions: m.Positions}
		if out[i].Positions == nil {
			out[i].Positions = []int{}
		}
	}
	return out
}

// ProjectService: projects, the directory picker and fuzzy helpers.
type ProjectService struct {
	projects *workspace.Projects
	db       *sql.DB
}

func (s *ProjectService) ListProjects() ([]workspace.Project, error) {
	ps, err := s.projects.List()
	if ps == nil {
		ps = []workspace.Project{}
	}
	return ps, wrapErr(err)
}

// AddProject registers the repo containing path (existing repos are returned).
func (s *ProjectService) AddProject(path string) (workspace.Project, error) {
	p, err := s.projects.Add(fs.Expand(path))
	return p, wrapErr(err)
}

// RemoveProject unregisters a project. Its session rows go with it (FK
// cascade); no files, branches or worktrees on disk are touched.
func (s *ProjectService) RemoveProject(id string) error {
	res, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return wrapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return wrapErr(workspace.ErrNotFound)
	}
	return nil
}

func (s *ProjectService) TouchProject(id string) error { return wrapErr(s.projects.Touch(id)) }

func (s *ProjectService) SetSetupCmds(id string, cmds []string) error {
	if cmds == nil {
		cmds = []string{}
	}
	return wrapErr(s.projects.SetSetupCmds(id, cmds))
}

// ListDir lists a directory for the picker (dirs first; ~ is expanded).
func (s *ProjectService) ListDir(path string, dirsOnly, hidden bool) ([]fs.Entry, error) {
	es, err := fs.ListDir(path, fs.ListOpts{DirsOnly: dirsOnly, Hidden: hidden})
	if es == nil {
		es = []fs.Entry{}
	}
	return es, wrapErr(err)
}

// RepoBadges returns branch + worktree count for the repos among paths,
// within a 200ms budget (missing keys = not a repo or not ready).
func (s *ProjectService) RepoBadges(paths []string) map[string]fs.Badge {
	return fs.RepoBadges(paths, 200*time.Millisecond)
}

// Home returns the user's home directory.
func (s *ProjectService) Home() string {
	h, _ := os.UserHomeDir()
	return h
}

// FilterEntries is the picker's fuzzy filter over entry names; matches are
// best first, and Index refers to names.
func (s *ProjectService) FilterEntries(query string, names []string) []FuzzyMatch {
	return toFuzzy(fuzzy.Rank(query, names, 0))
}

// Fuzzy ranks candidates (limit <= 0: unlimited). Used by the palette.
func (s *ProjectService) Fuzzy(query string, candidates []string, limit int) []FuzzyMatch {
	return toFuzzy(fuzzy.Rank(query, candidates, limit))
}
