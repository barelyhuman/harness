package app

import "github.com/sid/harness/internal/git"

// GitService: SCM state + mutations. Every mutating call refreshes and pushes
// the SCM view-model on `scm/<scopeKey>` for each watched scope with that root
// (see FSService.WatchScope). The same view-model is pushed (debounced 150ms)
// whenever fs changes arrive for the scope.
type GitService struct {
	hub *scopeHub
}

// Status returns the render-ready SCM view-model (see SCMState).
func (s *GitService) Status(root string) (SCMState, error) {
	st, err := scmState(cleanRoot(root))
	return st, wrapErr(err)
}

// Refresh forces a push of the SCM state for a watched scope.
func (s *GitService) Refresh(scopeKey string) { s.hub.refreshKey(scopeKey) }

// Hunks lists the -U0 hunks of path (staged: index vs HEAD, else worktree vs index).
func (s *GitService) Hunks(root, path string, staged bool) ([]git.Hunk, error) {
	h, err := git.Hunks(cleanRoot(root), path, staged)
	if h == nil {
		h = []git.Hunk{}
	}
	return h, wrapErr(err)
}

func (s *GitService) mutate(root string, err error) error {
	s.hub.refreshRoot(root)
	return wrapErr(err)
}

func (s *GitService) StageHunk(root, path, hunkID string) error {
	return s.mutate(root, git.StageHunk(cleanRoot(root), path, hunkID))
}

func (s *GitService) UnstageHunk(root, path, hunkID string) error {
	return s.mutate(root, git.UnstageHunk(cleanRoot(root), path, hunkID))
}

func (s *GitService) RevertHunk(root, path, hunkID string) error {
	return s.mutate(root, git.RevertHunk(cleanRoot(root), path, hunkID))
}

func (s *GitService) StageFiles(root string, paths []string) error {
	return s.mutate(root, git.StageFiles(cleanRoot(root), paths))
}

func (s *GitService) UnstageFiles(root string, paths []string) error {
	return s.mutate(root, git.UnstageFiles(cleanRoot(root), paths))
}

// DiscardFiles reverts tracked files and deletes untracked ones.
func (s *GitService) DiscardFiles(root string, paths []string) error {
	return s.mutate(root, git.DiscardFiles(cleanRoot(root), paths))
}

// Commit commits the index; amend with an empty msg keeps the message.
func (s *GitService) Commit(root, msg string, amend bool) error {
	return s.mutate(root, git.Commit(cleanRoot(root), msg, amend))
}

// ShowFile returns the file at rev ("HEAD", ":0" for the index); "" if absent.
func (s *GitService) ShowFile(root, rev, path string) (string, error) {
	c, err := git.ShowFile(cleanRoot(root), rev, path)
	return c, wrapErr(err)
}
