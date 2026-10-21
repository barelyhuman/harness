package app

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sid/harness/internal/fuzzy"
	"github.com/sid/harness/internal/search"
	"github.com/sid/harness/internal/stream"
)

// QuickOpenResult is a ranked file path with matched rune positions.
type QuickOpenResult struct {
	Path      string `json:"path"` // root-relative, slash separated
	Score     int    `json:"score"`
	Positions []int  `json:"positions"`
}

// GrepOptions configure Grep.
type GrepOptions struct {
	Regex         bool   `json:"regex"`
	CaseSensitive bool   `json:"caseSensitive"`
	Glob          string `json:"glob"`
}

// grepStartDelay is how long a grep stream waits for the client to attach
// (it signals by sending any JSON frame on the channel) before streaming
// anyway. Server channels have no replay, so early results would be lost.
const grepStartDelay = time.Second

// SearchService: quick open + find in files.
type SearchService struct {
	bus *stream.Bus

	mu    sync.Mutex
	greps map[string]context.CancelFunc
	seq   atomic.Int64
}

// QuickOpen ranks the root's (rg --files, cached) paths against query.
func (s *SearchService) QuickOpen(root, query string, limit int) ([]QuickOpenResult, error) {
	files, err := search.Files(context.Background(), cleanRoot(root))
	if err != nil {
		return nil, wrapErr(err)
	}
	ms := fuzzy.Rank(query, files, limit)
	out := make([]QuickOpenResult, len(ms))
	for i, m := range ms {
		pos := m.Positions
		if pos == nil {
			pos = []int{}
		}
		out[i] = QuickOpenResult{Path: files[m.Index], Score: m.Score, Positions: pos}
	}
	return out, nil
}

// Grep starts a streaming search and returns the streamID. Results go out on
// bus channel `search/<streamID>` as JSON frames:
//
//	{path, matches:[{line,column,text,submatches:[{start,end}]}]}   per file
//	{error:"..."}                                                   on failure
//	{done:true}                                                     last frame
//
// The server closes the channel after done. Streaming starts when the client
// sends any JSON frame on the channel (e.g. {}) or after 1s.
func (s *SearchService) Grep(root, query string, opts GrepOptions) (string, error) {
	if query == "" {
		return "", badArg("empty query")
	}
	id := strconv.FormatInt(s.seq.Add(1), 10)
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.greps[id] = cancel
	s.mu.Unlock()
	ch := s.bus.Open("search/" + id)
	start := make(chan struct{}, 1)
	ch.OnJSON(func(json.RawMessage) {
		select {
		case start <- struct{}{}:
		default:
		}
	})
	ch.OnClose(cancel)
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.greps, id)
			s.mu.Unlock()
			cancel()
		}()
		select {
		case <-start:
		case <-time.After(grepStartDelay):
		case <-ctx.Done():
			ch.Close()
			return
		}
		for r := range search.Grep(ctx, cleanRoot(root), query,
			search.GrepOpts{Regex: opts.Regex, CaseSensitive: opts.CaseSensitive, Glob: opts.Glob}) {
			if r.Err != nil {
				_ = ch.SendJSON(map[string]any{"error": r.Err.Error()})
				continue
			}
			if ctx.Err() != nil {
				break
			}
			_ = ch.SendJSON(r)
		}
		if ctx.Err() == nil {
			_ = ch.SendJSON(map[string]any{"done": true})
		}
		ch.Close()
	}()
	return id, nil
}

// CancelGrep stops a running search; its channel is closed without `done`.
func (s *SearchService) CancelGrep(streamID string) {
	s.mu.Lock()
	cancel := s.greps[streamID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
