package app

import (
	"strings"

	"github.com/sid/harness/internal/termlayout"
)

// RunSetup runs the project setup commands in order in a "setup" terminal tab
// of scopeKey, rooted at cwd. The tab stays open as a login shell afterwards,
// so a failure is visible there; nothing waits for it (spec 12: errors pass
// through without blocking the session).
func (s *TermService) RunSetup(scopeKey, cwd string, cmds []string) (PaneRef, error) {
	s.ensure(scopeKey)
	script := strings.Join(cmds, " && ") +
		`; code=$?; echo; if [ $code -eq 0 ]; then echo "[setup finished]"; else echo "[setup failed: exit $code]"; fi; exec "${SHELL:-/bin/zsh}" -l`
	var ref PaneRef
	err := s.layouts.Update(scopeKey, func(l *termlayout.Layout) error {
		tab, pane := l.NewTab(termlayout.KindSetup)
		id, err := s.pty.SpawnCmd(scopeKey, cwd, 0, 0, "setup", s.env(), "/bin/sh", "-c", script)
		if err != nil {
			_, _ = l.Close(pane)
			return err
		}
		if err := l.AttachPty(pane, id); err != nil {
			_ = s.pty.Kill(id)
			return err
		}
		_ = l.RenameTab(tab, "setup")
		ref = PaneRef{TabID: tab, PaneID: pane, PtyID: id}
		return nil
	})
	return ref, wrapErr(err)
}
