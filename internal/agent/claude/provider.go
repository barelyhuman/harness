// Package claude implements the agent.Provider for the Claude Code CLI
// (stream-json over stdio). The authoritative protocol notes live in
// docs/spikes/claude-protocol.md.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sid/harness/internal/agent"
)

// ProviderID is the id of the Claude provider.
const ProviderID = "claude"

// Provider is the Claude Code provider. The zero value is usable.
type Provider struct {
	// Binary overrides the CLI path (default: PATH lookup, then
	// ~/.local/bin/claude).
	Binary string
	// ModelsTimeout bounds the live list_models probe (default 15s).
	ModelsTimeout time.Duration
}

var _ agent.Provider = (*Provider)(nil)

// ID implements agent.Provider.
func (p *Provider) ID() string { return ProviderID }

func (p *Provider) binary() (string, error) {
	if p.Binary != "" {
		return p.Binary, nil
	}
	if path, err := exec.LookPath("claude"); err == nil {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		c := filepath.Join(home, ".local", "bin", "claude")
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("claude CLI not found")
}

// Detect runs `claude --version`.
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
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// Command builds argv for the host. See the spike doc for the flag set.
func (p *Provider) Command(o agent.StartOpts) (agent.Cmd, error) {
	bin := o.Binary
	if bin == "" {
		var err error
		if bin, err = p.binary(); err != nil {
			return agent.Cmd{}, err
		}
	}
	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--replay-user-messages",
		"--permission-prompt-tool", "stdio",
	}
	mode := o.Mode
	if mode == "" {
		mode = "default"
	}
	args = append(args, "--permission-mode", mode)
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if o.Effort != "" {
		args = append(args, "--effort", o.Effort)
	}
	if o.ResumeID != "" {
		args = append(args, "--resume", o.ResumeID)
	}
	for _, m := range o.MCPConfig {
		args = append(args, "--mcp-config", m)
	}
	for _, d := range o.AddDirs {
		args = append(args, "--add-dir", d)
	}
	if len(o.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(o.AllowedTools, ","))
	}
	if len(o.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(o.DisallowedTools, ","))
	}
	if len(o.Settings) > 0 {
		merged, err := MergeSettings(o.Settings)
		if err != nil {
			return agent.Cmd{}, err
		}
		args = append(args, "--settings", merged)
	}
	var sys []string
	for _, s := range o.AppendSystemPrompt {
		if strings.TrimSpace(s) != "" {
			sys = append(sys, s)
		}
	}
	if len(sys) > 0 {
		args = append(args, "--append-system-prompt", strings.Join(sys, "\n\n"))
	}
	return agent.Cmd{Path: bin, Args: args, Env: o.Env, Dir: o.Cwd}, nil
}

// MergeSettings deep-merges JSON objects (later wins) into one JSON string.
func MergeSettings(parts []json.RawMessage) (string, error) {
	out := map[string]any{}
	for i, raw := range parts {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return "", fmt.Errorf("settings[%d]: %w", i, err)
		}
		deepMerge(out, m)
	}
	b, err := json.Marshal(out) // map keys are sorted: deterministic
	return string(b), err
}

func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMerge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

// NewSession implements agent.Provider.
func (p *Provider) NewSession(w io.Writer, o agent.StartOpts) agent.Session {
	return NewSession(w, o)
}

// StaticModels is the fallback model list used when list_models is not
// available.
func StaticModels() []agent.Model {
	efforts := []string{"low", "medium", "high", "xhigh", "max"}
	mk := func(id, name, desc string) agent.Model {
		return agent.Model{ID: id, Provider: ProviderID, Name: name, Description: desc, Efforts: efforts}
	}
	return []agent.Model{
		mk("default", "Default", "Recommended model"),
		mk("opus", "Opus", "Complex work"),
		mk("sonnet", "Sonnet", "Everyday tasks"),
		mk("haiku", "Haiku", "Fast and cheap"),
		mk("opus[1m]", "Opus (1M context)", "Opus with the 1M context window"),
		mk("sonnet[1m]", "Sonnet (1M context)", "Sonnet with the 1M context window"),
	}
}

// Models asks a short-lived CLI process for list_models and falls back to
// StaticModels on any failure. It never returns an error for a missing CLI.
func (p *Provider) Models(ctx context.Context) ([]agent.Model, error) {
	if ms, err := p.liveModels(ctx); err == nil && len(ms) > 0 {
		return ms, nil
	}
	return StaticModels(), nil
}

func (p *Provider) liveModels(ctx context.Context) ([]agent.Model, error) {
	bin, err := p.binary()
	if err != nil {
		return nil, err
	}
	timeout := p.ModelsTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--print", "--output-format", "stream-json",
		"--input-format", "stream-json", "--verbose")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	const reqID = "models-probe"
	if _, err := io.WriteString(stdin, EncodeControlRequest(reqID, "list_models", nil)); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		ms, ok, err := ParseModelsResponse(sc.Bytes(), reqID)
		if ok {
			return ms, err
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("no list_models response")
}

// ParseModelsResponse decodes a list_models control_response for reqID.
// ok reports whether the line was that response.
func ParseModelsResponse(line []byte, reqID string) (models []agent.Model, ok bool, err error) {
	var env struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Error     string `json:"error"`
			Response  struct {
				Models []modelInfo `json:"models"`
			} `json:"response"`
		} `json:"response"`
	}
	if json.Unmarshal(line, &env) != nil || env.Type != "control_response" || env.Response.RequestID != reqID {
		return nil, false, nil
	}
	if env.Response.Subtype != "success" {
		return nil, true, errors.New(env.Response.Error)
	}
	return convertModels(env.Response.Response.Models), true, nil
}

type modelInfo struct {
	Value                 string   `json:"value"`
	ResolvedModel         string   `json:"resolvedModel"`
	DisplayName           string   `json:"displayName"`
	Description           string   `json:"description"`
	SupportsEffort        bool     `json:"supportsEffort"`
	SupportedEffortLevels []string `json:"supportedEffortLevels"`
}

func convertModels(in []modelInfo) []agent.Model {
	out := make([]agent.Model, 0, len(in))
	for _, m := range in {
		if m.Value == "" {
			continue
		}
		am := agent.Model{ID: m.Value, Provider: ProviderID, Name: m.DisplayName, Description: m.Description, Resolved: m.ResolvedModel}
		if am.Name == "" {
			am.Name = m.Value
		}
		if m.SupportsEffort {
			am.Efforts = m.SupportedEffortLevels
		}
		out = append(out, am)
	}
	return out
}
