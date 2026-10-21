//go:build darwin

package fs

import (
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsevents"
)

// fseBackend uses one FSEvents stream for the whole tree: no per-directory
// file descriptors.
type fseBackend struct {
	es      *fsevents.EventStream
	evs     chan event
	errs    chan error
	end     chan struct{}
	once    sync.Once
	pumpEnd chan struct{}
}

func newBackend(root string) (backend, error) {
	start := fsevents.LatestEventID()
	es := &fsevents.EventStream{
		Paths:   []string{root},
		Flags:   fsevents.FileEvents | fsevents.NoDefer,
		Latency: 10 * time.Millisecond,
		Events:  make(chan []fsevents.Event, 64),
	}
	if err := es.Start(); err != nil {
		return nil, err
	}
	startT := time.Now() // changes before Watch returns are not reported
	b := &fseBackend{es: es, evs: make(chan event), errs: make(chan error), end: make(chan struct{}), pumpEnd: make(chan struct{})}
	go b.pump(start, startT)
	return b, nil
}

func (b *fseBackend) pump(start uint64, startT time.Time) {
	defer close(b.pumpEnd)
	for {
		select {
		case <-b.end:
			return
		case batch := <-b.es.Events:
			for _, fe := range batch {
				if fe.ID <= start { // history from before the stream began
					continue
				}
				if stale(fe.Path, startT) {
					continue
				}
				f := fe.Flags
				if bornBefore(fe.Path, startT) {
					f &^= fsevents.ItemCreated // creation replayed from history
				}
				if f&fsevents.ItemIsDir != 0 && f&(fsevents.ItemCreated|fsevents.ItemRemoved|fsevents.ItemRenamed) == 0 {
					continue // a directory's contents changed; the children report themselves
				}
				const content = fsevents.ItemCreated | fsevents.ItemRemoved | fsevents.ItemRenamed | fsevents.ItemModified
				e := event{
					name:   fe.Path,
					create: f&(fsevents.ItemCreated|fsevents.ItemRenamed) != 0,
					write:  f&fsevents.ItemModified != 0,
					remove: f&fsevents.ItemRemoved != 0,
					chmod:  f&content == 0,
					isDir:  f&fsevents.ItemIsDir != 0,
				}
				// Renames/coalesced events: trust the filesystem for existence.
				if e.create {
					if _, err := os.Lstat(fe.Path); err != nil {
						e.create = false
						e.remove = true
					}
				}
				select {
				case b.evs <- e:
				case <-b.end:
					return
				}
			}
		}
	}
}

func (b *fseBackend) events() <-chan event { return b.evs }
func (b *fseBackend) errors() <-chan error { return b.errs }
func (b *fseBackend) addDir(string) error  { return nil }

func (b *fseBackend) close() {
	b.once.Do(func() {
		close(b.end)
		<-b.pumpEnd
		// The cgo callback blocks sending on Events; drain while stopping.
		stopped := make(chan struct{})
		go func() {
			for {
				select {
				case <-b.es.Events:
				case <-stopped:
					return
				}
			}
		}()
		b.es.Stop()
		close(stopped)
	})
}

// stale reports whether an event shortly after stream creation describes a
// change that happened before it: FSEvents replays recent history that the
// event-ID cutoff does not always exclude.
func stale(path string, startT time.Time) bool {
	if time.Since(startT) > 2*time.Second {
		return false
	}
	st, err := os.Lstat(path)
	if err != nil {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return time.Unix(sys.Ctimespec.Unix()).Before(startT)
}

func bornBefore(path string, startT time.Time) bool {
	if time.Since(startT) > 2*time.Second {
		return false
	}
	st, err := os.Lstat(path)
	if err != nil {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && time.Unix(sys.Birthtimespec.Unix()).Before(startT)
}
