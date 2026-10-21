package app

import (
	"path"
	"sort"
	"strings"

	"github.com/sid/harness/internal/git"
)

// SCMRow is one file row, ready to render.
type SCMRow struct {
	Path     string `json:"path"`               // repo-relative, slash separated
	OrigPath string `json:"origPath,omitempty"` // renames
	Name     string `json:"name"`               // base name
	Dir      string `json:"dir"`                // dimmed directory, "" for repo root
	Code     string `json:"code"`               // raw git code: M A D R C T U
	Badge    string `json:"badge"`              // letter shown in the row
	Class    string `json:"class"`              // css class: scm-modified, scm-added, ...
	Title    string `json:"title"`              // tooltip, e.g. "Modified"
}

// SCMGroup is a VS Code style resource group.
type SCMGroup struct {
	ID    string   `json:"id"` // "merge" | "staged" | "changes"
	Label string   `json:"label"`
	Count int      `json:"count"`
	Rows  []SCMRow `json:"rows"`
}

// SCMState is the view-model pushed on `scm/<scopeKey>` and returned by
// GitService.Status. Groups are always [merge?, staged, changes]; "merge" only
// appears when there are conflicts. Untracked files are merged into "changes"
// with badge U, as VS Code does.
type SCMState struct {
	Root     string     `json:"root"`
	Branch   string     `json:"branch"`
	Upstream string     `json:"upstream"`
	Ahead    int        `json:"ahead"`
	Behind   int        `json:"behind"`
	Count    int        `json:"count"` // total badge count: rows in all groups
	Dirty    bool       `json:"dirty"`
	Groups   []SCMGroup `json:"groups"`
}

type badgeInfo struct{ badge, class, title string }

var badgeFor = map[string]badgeInfo{
	"M": {"M", "scm-modified", "Modified"},
	"T": {"T", "scm-modified", "Type changed"},
	"A": {"A", "scm-added", "Added"},
	"D": {"D", "scm-deleted", "Deleted"},
	"R": {"R", "scm-renamed", "Renamed"},
	"C": {"C", "scm-renamed", "Copied"},
	"U": {"U", "scm-untracked", "Untracked"},
}

func scmRow(c git.FileChange, conflict bool) SCMRow {
	bi, ok := badgeFor[c.Code]
	if !ok {
		bi = badgeInfo{c.Code, "scm-modified", "Changed"}
	}
	if conflict {
		bi = badgeInfo{"!", "scm-conflict", "Conflict"}
	}
	dir := path.Dir(c.Path)
	if dir == "." {
		dir = ""
	}
	return SCMRow{Path: c.Path, OrigPath: c.OrigPath, Name: path.Base(c.Path), Dir: dir,
		Code: c.Code, Badge: bi.badge, Class: bi.class, Title: bi.title}
}

func scmRows(cs []git.FileChange, conflict bool) []SCMRow {
	rows := make([]SCMRow, 0, len(cs))
	for _, c := range cs {
		rows = append(rows, scmRow(c, conflict))
	}
	return rows
}

func sortRows(rows []SCMRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].Path) < strings.ToLower(rows[j].Path)
	})
}

// buildSCM shapes a git.Status into the render-ready view-model (pure).
func buildSCM(root string, st git.Status) SCMState {
	staged := scmRows(st.Staged, false)
	changes := append(scmRows(st.Unstaged, false), scmRows(st.Untracked, false)...)
	sortRows(staged)
	sortRows(changes)
	groups := []SCMGroup{}
	if len(st.Conflicted) > 0 {
		m := scmRows(st.Conflicted, true)
		sortRows(m)
		groups = append(groups, SCMGroup{ID: "merge", Label: "Merge Changes", Count: len(m), Rows: m})
	}
	groups = append(groups,
		SCMGroup{ID: "staged", Label: "Staged Changes", Count: len(staged), Rows: staged},
		SCMGroup{ID: "changes", Label: "Changes", Count: len(changes), Rows: changes},
	)
	n := 0
	for _, g := range groups {
		n += g.Count
	}
	return SCMState{Root: root, Branch: st.Branch, Upstream: st.Upstream, Ahead: st.Ahead, Behind: st.Behind,
		Count: n, Dirty: st.Dirty(), Groups: groups}
}

func scmState(root string) (SCMState, error) {
	st, err := git.StatusOf(root)
	if err != nil {
		return SCMState{}, err
	}
	return buildSCM(root, st), nil
}
