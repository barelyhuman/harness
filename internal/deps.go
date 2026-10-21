//go:build tools

// Package internal pins dependencies that later packages will import, so that
// go.mod never needs editing by feature work.
package internal

import (
	_ "github.com/coder/websocket"
	_ "github.com/creack/pty"
	_ "github.com/fsnotify/fsnotify"
	_ "github.com/go-git/go-git/v5/plumbing/format/gitignore"
	_ "modernc.org/sqlite"
)
