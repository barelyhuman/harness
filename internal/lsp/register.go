package lsp

import (
	"encoding/json"

	"github.com/sid/harness/internal/stream"
)

// Conn is the subset of *stream.Channel the proxy needs.
type Conn interface {
	SendJSON(v any) error
	OnData(func([]byte))
	OnJSON(func(json.RawMessage))
	OnClose(func())
	Close()
}

var _ Conn = (*stream.Channel)(nil)

// Register handles client-opened lsp/<serverKey> channels. The frontend must
// call Acquire (app binding) first to get the key. The handler never blocks.
func (m *Manager) Register(bus *stream.Bus) {
	bus.Handle("lsp/", func(ch *stream.Channel) {
		if err := m.Attach(splitKey(ch.Name()), ch); err != nil {
			ch.Close()
		}
	})
}
