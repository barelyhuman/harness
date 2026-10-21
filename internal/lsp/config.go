// Package lsp manages language-server processes (lazy, refcounted, idle
// reaped) and proxies their JSON-RPC to frontend clients over bus channels
// named lsp/<serverKey>.
package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ServerConfig describes one language server.
type ServerConfig struct {
	ID          string          `json:"id"`
	Languages   []string        `json:"languages"` // monaco language ids
	Command     string          `json:"command"`
	Args        []string        `json:"args,omitempty"`
	InitOptions json.RawMessage `json:"initOptions,omitempty"`
	RootMarkers []string        `json:"rootMarkers,omitempty"`
}

// DefaultServers returns the built-in server table.
func DefaultServers() []ServerConfig {
	return []ServerConfig{
		{ID: "gopls", Languages: []string{"go"}, Command: "gopls", RootMarkers: []string{"go.work", "go.mod"}},
		{ID: "typescript-language-server", Languages: []string{"typescript", "javascript", "typescriptreact", "javascriptreact"},
			Command: "typescript-language-server", Args: []string{"--stdio"}, RootMarkers: []string{"tsconfig.json", "package.json"}},
		{ID: "rust-analyzer", Languages: []string{"rust"}, Command: "rust-analyzer", RootMarkers: []string{"Cargo.toml"}},
		{ID: "pyright", Languages: []string{"python"}, Command: "pyright-langserver", Args: []string{"--stdio"},
			RootMarkers: []string{"pyproject.toml", "setup.py", "requirements.txt"}},
		{ID: "vscode-json-language-server", Languages: []string{"json"}, Command: "vscode-json-language-server", Args: []string{"--stdio"}},
	}
}

// MergeConfigs overlays overrides on base. An override with a known ID
// replaces the non-zero fields; an unknown ID is appended.
func MergeConfigs(base, overrides []ServerConfig) []ServerConfig {
	out := make([]ServerConfig, len(base))
	copy(out, base)
	for _, o := range overrides {
		if o.ID == "" {
			continue
		}
		found := false
		for i := range out {
			if out[i].ID != o.ID {
				continue
			}
			found = true
			if len(o.Languages) > 0 {
				out[i].Languages = o.Languages
			}
			if o.Command != "" {
				out[i].Command = o.Command
			}
			if o.Args != nil {
				out[i].Args = o.Args
			}
			if len(o.InitOptions) > 0 {
				out[i].InitOptions = o.InitOptions
			}
			if len(o.RootMarkers) > 0 {
				out[i].RootMarkers = o.RootMarkers
			}
		}
		if !found {
			out = append(out, o)
		}
	}
	return out
}

// ErrNotInstalled is returned when a server binary cannot be found.
type ErrNotInstalled struct {
	ID   string
	Hint string
}

func (e *ErrNotInstalled) Error() string {
	return fmt.Sprintf("lsp: %s is not installed (%s)", e.ID, e.Hint)
}

// ErrNoServer is returned when no server handles a language.
var ErrNoServer = errors.New("lsp: no server for language")

var installHints = map[string]string{
	"gopls":                       "go install golang.org/x/tools/gopls@latest",
	"typescript-language-server":  "npm install -g typescript-language-server typescript",
	"rust-analyzer":               "rustup component add rust-analyzer",
	"pyright":                     "npm install -g pyright",
	"vscode-json-language-server": "npm install -g vscode-langservers-extracted",
}

func hintFor(cfg ServerConfig) string {
	if h, ok := installHints[cfg.ID]; ok {
		return h
	}
	return "install " + cfg.Command + " and make sure it is on PATH"
}

// resolveBinary finds cfg.Command on PATH or in well-known bin dirs.
func resolveBinary(cfg ServerConfig) (string, error) {
	if p, err := exec.LookPath(cfg.Command); err == nil {
		return p, nil
	}
	if filepath.IsAbs(cfg.Command) {
		return "", &ErrNotInstalled{ID: cfg.ID, Hint: hintFor(cfg)}
	}
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "go", "bin"), filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, ".npm-global", "bin"), filepath.Join(home, ".local", "bin"))
	}
	if gb := os.Getenv("GOBIN"); gb != "" {
		dirs = append(dirs, gb)
	}
	if p := os.Getenv("NPM_CONFIG_PREFIX"); p != "" {
		dirs = append(dirs, filepath.Join(p, "bin"))
	}
	dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	if p := npmGlobalBin(); p != "" {
		dirs = append(dirs, p)
	}
	for _, d := range dirs {
		p := filepath.Join(d, cfg.Command)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", &ErrNotInstalled{ID: cfg.ID, Hint: hintFor(cfg)}
}

func npmGlobalBin() string {
	npm, err := exec.LookPath("npm")
	if err != nil {
		return ""
	}
	cmd := exec.Command(npm, "prefix", "-g")
	done := make(chan []byte, 1)
	go func() { b, _ := cmd.Output(); done <- b }()
	select {
	case b := <-done:
		if s := strings.TrimSpace(string(b)); s != "" {
			return filepath.Join(s, "bin")
		}
	case <-time.After(3 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}
	return ""
}

var extLang = map[string]string{
	".go": "go", ".ts": "typescript", ".mts": "typescript", ".cts": "typescript", ".tsx": "typescriptreact",
	".js": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".jsx": "javascriptreact",
	".rs": "rust", ".py": "python", ".json": "json",
}

// LanguageForPath maps a file path to a monaco language id ("" if unknown).
func LanguageForPath(path string) string { return extLang[strings.ToLower(filepath.Ext(path))] }
