// Package cursor implements the agent.Provider for the Cursor CLI, which
// speaks ACP over stdio (`agent acp`). The protocol is handled by the generic
// internal/agent/acp client; the authoritative Cursor notes live in
// docs/spikes/cursor-acp.md.
package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/agent/acp"
)

// ProviderID is the id of the Cursor provider.
const ProviderID = "cursor"

// Provider is the Cursor provider. The zero value is usable.
type Provider struct {
	// Binary overrides the CLI path (default: `agent`, then `cursor-agent`
	// on PATH, then ~/.local/bin).
	Binary string
	// CachePath overrides where the last session/new model list is cached
	// (default: <user cache dir>/harness/cursor-models.json).
	CachePath string

	mu sync.Mutex
}

var _ agent.Provider = (*Provider)(nil)

// ID implements agent.Provider.
func (p *Provider) ID() string { return ProviderID }

func (p *Provider) binary() (string, error) {
	if p.Binary != "" {
		return p.Binary, nil
	}
	for _, n := range []string{"agent", "cursor-agent"} {
		if path, err := exec.LookPath(n); err == nil {
			return path, nil
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, n := range []string{"agent", "cursor-agent"} {
			c := filepath.Join(home, ".local", "bin", n)
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c, nil
			}
		}
	}
	return "", errors.New("cursor CLI (agent / cursor-agent) not found")
}

// Detect resolves the CLI and runs `--version`.
func (p *Provider) Detect(ctx context.Context) (agent.Install, error) {
	bin, err := p.binary()
	if err != nil {
		return agent.Install{Found: false, Detail: err.Error()}, nil
	}
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return agent.Install{Found: false, Path: bin, Detail: err.Error()}, nil
	}
	return agent.Install{Found: true, Path: bin, Version: parseVersion(string(out))}, nil
}

func parseVersion(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if f := strings.Fields(s); len(f) > 0 {
		return f[len(f)-1]
	}
	return ""
}

// Command is `agent acp`, run in the session cwd.
func (p *Provider) Command(o agent.StartOpts) (agent.Cmd, error) {
	bin := o.Binary
	if bin == "" {
		var err error
		if bin, err = p.binary(); err != nil {
			return agent.Cmd{}, err
		}
	}
	return agent.Cmd{Path: bin, Args: []string{"acp"}, Env: o.Env, Dir: o.Cwd}, nil
}

// NewSession implements agent.Provider. The returned session also refreshes
// the on-disk model cache whenever session/new reports the model list.
func (p *Provider) NewSession(w io.Writer, o agent.StartOpts) agent.Session {
	return acp.NewSession(w, o, acp.Config{
		ProviderID:   ProviderID,
		AuthMethodID: "cursor_login",
		ClientName:   "harness",
		OnModels:     p.saveModels,
	})
}

// StaticModels is the fallback used before any session has reported models.
// Ids are Cursor's parameterised strings and must be passed verbatim to
// session/set_model.
func StaticModels() []agent.Model {
	mk := func(id, name string) agent.Model { return agent.Model{ID: id, Provider: ProviderID, Name: name} }
	return []agent.Model{
		mk("default[]", "Auto"),
		mk("composer-2.5[fast=true]", "composer-2.5"),
		mk("claude-sonnet-5[thinking=true,context=300k,effort=high]", "claude-sonnet-5"),
	}
}

func (p *Provider) cachePath() string {
	if p.CachePath != "" {
		return p.CachePath
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "harness", "cursor-models.json")
}

// Models returns the model list from the last session/new (cached on disk),
// or StaticModels. It never starts the CLI and never fails.
func (p *Provider) Models(ctx context.Context) ([]agent.Model, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if path := p.cachePath(); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var ms []agent.Model
			if json.Unmarshal(b, &ms) == nil && len(ms) > 0 {
				for i := range ms {
					ms[i].Provider = ProviderID
				}
				return ms, nil
			}
		}
	}
	return StaticModels(), nil
}

func (p *Provider) saveModels(ms []agent.Model) {
	p.mu.Lock()
	defer p.mu.Unlock()
	path := p.cachePath()
	if path == "" {
		return
	}
	b, err := json.Marshal(ms)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cursor-models-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
		_ = os.Remove(tmp.Name())
	}
}
