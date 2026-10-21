package agent

import (
	"context"
	"strings"
	"sync"
)

// Model is a selectable model. Key() is unique across providers.
type Model struct {
	ID          string   `json:"id"` // provider-local id, what the CLI accepts
	Provider    string   `json:"provider"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Resolved    string   `json:"resolved,omitempty"`
	Efforts     []string `json:"efforts,omitempty"`
}

// Key is "<provider>/<id>".
func (m Model) Key() string { return m.Provider + "/" + m.ID }

// ModelProvider returns the provider part of a model key.
func ModelProvider(key string) string {
	if i := strings.IndexByte(key, '/'); i >= 0 {
		return key[:i]
	}
	return ""
}

// ModelLocalID returns the provider-local id part of a model key.
func ModelLocalID(key string) string {
	if i := strings.IndexByte(key, '/'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// ModelRegistry merges provider model lists with the user's favourites.
// Safe for concurrent use.
type ModelRegistry struct {
	mu        sync.RWMutex
	order     []string // provider registration order
	byProv    map[string][]Model
	favorites []string
}

// NewModelRegistry returns an empty registry.
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{byProv: map[string][]Model{}}
}

// SetProviderModels replaces one provider's models.
func (r *ModelRegistry) SetProviderModels(provider string, ms []Model) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byProv[provider]; !ok {
		r.order = append(r.order, provider)
	}
	out := make([]Model, len(ms))
	for i, m := range ms {
		m.Provider = provider
		out[i] = m
	}
	r.byProv[provider] = out
}

// Refresh queries each provider; providers that fail keep their old list and
// the first error is returned after all were tried.
func (r *ModelRegistry) Refresh(ctx context.Context, providers ...Provider) error {
	var first error
	for _, p := range providers {
		ms, err := p.Models(ctx)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		r.SetProviderModels(p.ID(), ms)
	}
	return first
}

// SetFavourites sets the favourite model keys, in cycle order.
func (r *ModelRegistry) SetFavourites(keys []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.favorites = append([]string(nil), keys...)
}

// All returns every model, grouped by provider in registration order.
func (r *ModelRegistry) All() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.allLocked()
}

func (r *ModelRegistry) allLocked() []Model {
	var out []Model
	for _, p := range r.order {
		out = append(out, r.byProv[p]...)
	}
	return out
}

// Lookup finds a model by key.
func (r *ModelRegistry) Lookup(key string) (Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lookupLocked(key)
}

func (r *ModelRegistry) lookupLocked(key string) (Model, bool) {
	for _, m := range r.byProv[ModelProvider(key)] {
		if m.Key() == key {
			return m, true
		}
	}
	return Model{}, false
}

// Favourites returns the known favourite models in favourite order. Keys that
// no provider offers are skipped.
func (r *ModelRegistry) Favourites() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.favouritesLocked()
}

func (r *ModelRegistry) favouritesLocked() []Model {
	var out []Model
	seen := map[string]bool{}
	for _, k := range r.favorites {
		if seen[k] {
			continue
		}
		if m, ok := r.lookupLocked(k); ok {
			seen[k] = true
			out = append(out, m)
		}
	}
	return out
}

// Cycle returns the next (dir > 0) or previous (dir < 0) favourite after
// current, across providers, wrapping around. With no known favourites it
// cycles through all models. If current is not in the list it returns the
// first (dir > 0) or last entry. ok is false when there is nothing to cycle.
func (r *ModelRegistry) Cycle(current string, dir int) (Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := r.favouritesLocked()
	if len(list) == 0 {
		list = r.allLocked()
	}
	if len(list) == 0 {
		return Model{}, false
	}
	idx := -1
	for i, m := range list {
		if m.Key() == current {
			idx = i
			break
		}
	}
	n := len(list)
	switch {
	case idx < 0 && dir >= 0:
		return list[0], true
	case idx < 0:
		return list[n-1], true
	case dir >= 0:
		return list[(idx+1)%n], true
	default:
		return list[(idx-1+n)%n], true
	}
}

// SameProvider reports whether two model keys belong to the same provider
// (so a switch can happen live instead of forking a new session).
func (r *ModelRegistry) SameProvider(a, b string) bool {
	pa, pb := ModelProvider(a), ModelProvider(b)
	return pa != "" && pa == pb
}

// Providers returns provider ids in registration order.
func (r *ModelRegistry) Providers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]string(nil), r.order...)
	return out
}
