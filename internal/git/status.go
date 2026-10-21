package git

import (
	"fmt"
	"strconv"
	"strings"
)

// FileChange is one changed path. Code is one of M A D R U C T.
type FileChange struct {
	Path     string `json:"path"`
	OrigPath string `json:"origPath,omitempty"`
	Code     string `json:"code"`
}

// Status is the parsed result of `git status --porcelain=v2`.
type Status struct {
	Branch     string       `json:"branch"`
	Upstream   string       `json:"upstream"`
	Ahead      int          `json:"ahead"`
	Behind     int          `json:"behind"`
	Staged     []FileChange `json:"staged"`
	Unstaged   []FileChange `json:"unstaged"`
	Untracked  []FileChange `json:"untracked"`
	Conflicted []FileChange `json:"conflicted"`
}

// Dirty reports whether any change exists.
func (s Status) Dirty() bool {
	return len(s.Staged)+len(s.Unstaged)+len(s.Untracked)+len(s.Conflicted) > 0
}

// Paths returns the unique changed paths.
func (s Status) Paths() []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range [][]FileChange{s.Staged, s.Unstaged, s.Untracked, s.Conflicted} {
		for _, f := range l {
			if !seen[f.Path] {
				seen[f.Path] = true
				out = append(out, f.Path)
			}
		}
	}
	return out
}

// StatusOf runs git status in dir. (Named StatusOf because Status is the
// result type.)
func StatusOf(dir string) (Status, error) {
	out, err := runGit(dir, nil, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	if err != nil {
		return Status{}, err
	}
	return ParseStatus(out)
}

// ParseStatus parses NUL-separated porcelain v2 output.
func ParseStatus(out []byte) (Status, error) {
	st := Status{Staged: []FileChange{}, Unstaged: []FileChange{}, Untracked: []FileChange{}, Conflicted: []FileChange{}}
	recs := strings.Split(string(out), "\x00")
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		if r == "" {
			continue
		}
		switch {
		case strings.HasPrefix(r, "# "):
			k, v, _ := strings.Cut(r[2:], " ")
			switch k {
			case "branch.head":
				st.Branch = v
			case "branch.upstream":
				st.Upstream = v
			case "branch.ab":
				f := strings.Fields(v)
				if len(f) == 2 {
					st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
					st.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
				}
			}
		case r[0] == '1':
			f := strings.SplitN(r, " ", 9)
			if len(f) < 9 {
				return st, fmt.Errorf("git status: bad entry %q", r)
			}
			addXY(&st, f[1], f[8], "")
		case r[0] == '2':
			f := strings.SplitN(r, " ", 10)
			if len(f) < 10 || i+1 >= len(recs) {
				return st, fmt.Errorf("git status: bad rename entry %q", r)
			}
			i++
			addXY(&st, f[1], f[9], recs[i])
		case r[0] == 'u':
			f := strings.SplitN(r, " ", 11)
			if len(f) < 11 {
				return st, fmt.Errorf("git status: bad unmerged entry %q", r)
			}
			st.Conflicted = append(st.Conflicted, FileChange{Path: f[10], Code: "U"})
		case r[0] == '?':
			st.Untracked = append(st.Untracked, FileChange{Path: r[2:], Code: "U"})
		case r[0] == '!':
		default:
			return st, fmt.Errorf("git status: unknown entry %q", r)
		}
	}
	return st, nil
}

func addXY(st *Status, xy, path, orig string) {
	if len(xy) < 2 {
		return
	}
	if x := xy[0]; x != '.' {
		st.Staged = append(st.Staged, FileChange{Path: path, OrigPath: orig, Code: string(x)})
	}
	if y := xy[1]; y != '.' {
		st.Unstaged = append(st.Unstaged, FileChange{Path: path, OrigPath: orig, Code: string(y)})
	}
}
