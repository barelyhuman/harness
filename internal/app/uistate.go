package app

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// uiStore is the ui_state table (scope_key -> json).
type uiStore struct{ db *sql.DB }

func (u *uiStore) get(key string) (string, bool, error) {
	var j string
	err := u.db.QueryRow(`SELECT json FROM ui_state WHERE scope_key = ?`, key).Scan(&j)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return j, err == nil, err
}

func (u *uiStore) set(key, j string) error {
	_, err := u.db.Exec(`INSERT INTO ui_state(scope_key, json) VALUES(?, ?)
		ON CONFLICT(scope_key) DO UPDATE SET json = excluded.json`, key, j)
	return err
}

// UIStateService persists small per-key JSON blobs (open tabs, pane sizes,
// sidebar widths...) in the ui_state table. Keys are free-form strings, e.g.
// "tabs:<scopeKey>", "layout". Terminal layouts use "term:<scopeKey>" and are
// managed by TermService; don't write those.
type UIStateService struct{ ui *uiStore }

// Get returns the stored JSON text, or "" if the key was never set.
func (s *UIStateService) Get(key string) (string, error) {
	j, _, err := s.ui.get(key)
	return j, wrapErr(err)
}

// Set stores json (must be valid JSON).
func (s *UIStateService) Set(key, j string) error {
	if !json.Valid([]byte(j)) {
		return badArg("invalid json")
	}
	return wrapErr(s.ui.set(key, j))
}
