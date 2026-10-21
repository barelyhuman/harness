package app

import (
	"errors"

	"github.com/sid/harness/internal/lsp"
)

// LSPService exposes the language-server manager. After Acquire the frontend
// opens bus channel `lsp/<key>`; status changes are pushed on `lsp`.
type LSPService struct {
	m *lsp.Manager
}

// Acquire starts (or reuses) the server for languageId in scopeRoot and
// returns its key. A missing binary fails with code not_found and the install
// hint as message.
func (s *LSPService) Acquire(scopeRoot, languageId string) (string, error) {
	key, err := s.m.Acquire(cleanRoot(scopeRoot), languageId)
	if err != nil {
		var ni *lsp.ErrNotInstalled
		switch {
		case errors.As(err, &ni):
			return "", &AppError{Code: CodeNotFound, Message: ni.Hint}
		case errors.Is(err, lsp.ErrNoServer):
			return "", &AppError{Code: CodeNotFound, Message: "no language server for " + languageId}
		}
		return "", wrapErr(err)
	}
	return key, nil
}
