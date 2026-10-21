//go:build !darwin

package fs

import (
	"os"

	"github.com/fsnotify/fsnotify"
)

// fsnBackend adapts fsnotify (one watch per directory) to backend.
type fsnBackend struct {
	w   *fsnotify.Watcher
	evs chan event
	end chan struct{}
}

func newBackend(root string) (backend, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	b := &fsnBackend{w: fw, evs: make(chan event), end: make(chan struct{})}
	go b.pump()
	return b, nil
}

func (b *fsnBackend) pump() {
	defer close(b.evs)
	for ev := range b.w.Events {
		e := event{
			name:   ev.Name,
			create: ev.Has(fsnotify.Create),
			write:  ev.Has(fsnotify.Write),
			remove: ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename),
			chmod:  ev.Has(fsnotify.Chmod),
		}
		if e.create || e.write {
			if st, err := os.Lstat(ev.Name); err == nil {
				e.isDir = st.IsDir()
			}
		}
		select {
		case b.evs <- e:
		case <-b.end:
			return
		}
	}
}

func (b *fsnBackend) events() <-chan event    { return b.evs }
func (b *fsnBackend) errors() <-chan error    { return b.w.Errors }
func (b *fsnBackend) addDir(dir string) error { return b.w.Add(dir) }
func (b *fsnBackend) close() {
	close(b.end)
	b.w.Close()
}
