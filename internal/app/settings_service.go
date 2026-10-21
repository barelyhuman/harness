package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/sid/harness/internal/fs"
	"github.com/sid/harness/internal/stream"
)

const (
	defaultSettings    = "// Harness settings (JSON with comments).\n{\n}\n"
	defaultKeybindings = "// Place your key bindings here to override the defaults.\n// Format: [{ \"key\": \"cmd+k cmd+s\", \"command\": \"workbench.action.x\", \"when\": \"editorFocus\" }]\n// Prefix a command with \"-\" to remove a default binding.\n[\n]\n"
)

// SettingsResult is the GetSettings return value and the `settings` channel payload.
type SettingsResult struct {
	Values map[string]any `json:"values"`
	Error  string         `json:"error"` // parse error ("" when fine); values is {} then
	Path   string         `json:"path"`
}

// Keybinding is one user entry (VS Code keybindings.json format).
type Keybinding struct {
	Key     string `json:"key"`
	Command string `json:"command"` // without the leading "-"
	When    string `json:"when"`
	Args    any    `json:"args"`
	Remove  bool   `json:"remove"` // command was "-cmd": removes a default binding
}

// KeybindingConflict lists distinct commands bound to the same key+when.
type KeybindingConflict struct {
	Key      string   `json:"key"`
	When     string   `json:"when"`
	Commands []string `json:"commands"`
}

// KeybindingsResult is the GetKeybindings return value and the `keybindings`
// channel payload. Invalid entries are skipped and described in Errors; the
// frontend layers Bindings over its defaults (Remove entries delete defaults).
type KeybindingsResult struct {
	Bindings  []Keybinding         `json:"bindings"`
	Errors    []string             `json:"errors"`
	Conflicts []KeybindingConflict `json:"conflicts"`
	Path      string               `json:"path"`
}

// SettingsService reads and hot-reloads ~/.config/harness/{settings,keybindings}.json.
type SettingsService struct {
	dir        string
	settingsCh *stream.Channel
	keysCh     *stream.Channel
	watcher    *fs.Watcher
}

const settingsScope = "config:harness"

func newSettingsService(bus *stream.Bus, w *fs.Watcher, dir string) (*SettingsService, error) {
	s := &SettingsService{dir: dir, watcher: w}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s.ensureFile("settings.json", defaultSettings)
	s.ensureFile("keybindings.json", defaultKeybindings)
	if bus != nil {
		s.settingsCh = bus.Open("settings")
		s.keysCh = bus.Open("keybindings")
	}
	err := w.Watch(settingsScope, dir, func(paths []string) {
		for _, p := range paths {
			switch filepath.Base(p) {
			case "settings.json":
				if s.settingsCh != nil {
					_ = s.settingsCh.SendJSON(s.GetSettings())
				}
			case "keybindings.json":
				if s.keysCh != nil {
					_ = s.keysCh.SendJSON(s.GetKeybindings())
				}
			}
		}
	})
	return s, err
}

func (s *SettingsService) ensureFile(name, content string) {
	p := filepath.Join(s.dir, name)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		_ = os.WriteFile(p, []byte(content), 0o644)
	}
}

// GetSettings returns the parsed settings.json. Channel `settings` pushes the
// same SettingsResult on every change of the file.
func (s *SettingsService) GetSettings() SettingsResult {
	p := filepath.Join(s.dir, "settings.json")
	res := SettingsResult{Values: map[string]any{}, Path: p}
	b, err := os.ReadFile(p)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if err := json.Unmarshal(stripJSONC(b), &res.Values); err != nil {
		res.Error = err.Error()
		res.Values = map[string]any{}
	}
	if res.Values == nil {
		res.Values = map[string]any{}
	}
	return res
}

// GetKeybindings returns the validated user keybindings with conflicts.
// Channel `keybindings` pushes the same KeybindingsResult on every change.
func (s *SettingsService) GetKeybindings() KeybindingsResult {
	p := filepath.Join(s.dir, "keybindings.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return KeybindingsResult{Bindings: []Keybinding{}, Errors: []string{err.Error()}, Conflicts: []KeybindingConflict{}, Path: p}
	}
	r := parseKeybindings(b)
	r.Path = p
	return r
}

// OpenSettingsFile returns the absolute path of "settings" or "keybindings"
// (created with defaults if missing) for the frontend to open in an editor tab.
func (s *SettingsService) OpenSettingsFile(which string) (string, error) {
	switch which {
	case "settings":
		s.ensureFile("settings.json", defaultSettings)
		return filepath.Join(s.dir, "settings.json"), nil
	case "keybindings":
		s.ensureFile("keybindings.json", defaultKeybindings)
		return filepath.Join(s.dir, "keybindings.json"), nil
	}
	return "", badArg(`which must be "settings" or "keybindings"`)
}

func normKey(k string) string { return strings.Join(strings.Fields(strings.ToLower(k)), " ") }

func parseKeybindings(b []byte) KeybindingsResult {
	res := KeybindingsResult{Bindings: []Keybinding{}, Errors: []string{}, Conflicts: []KeybindingConflict{}}
	var raw []map[string]any
	if err := json.Unmarshal(stripJSONC(b), &raw); err != nil {
		res.Errors = append(res.Errors, "keybindings.json must be a JSON array of objects: "+err.Error())
		return res
	}
	type slot struct{ key, when string }
	cmds := map[slot][]string{}
	var order []slot
	for i, e := range raw {
		key, _ := e["key"].(string)
		cmd, _ := e["command"].(string)
		when, _ := e["when"].(string)
		if strings.TrimSpace(key) == "" || strings.TrimSpace(cmd) == "" {
			res.Errors = append(res.Errors, "entry "+itoa(i)+": \"key\" and \"command\" must be non-empty strings")
			continue
		}
		kb := Keybinding{Key: normKey(key), Command: cmd, When: strings.TrimSpace(when), Args: e["args"]}
		if strings.HasPrefix(cmd, "-") {
			kb.Remove, kb.Command = true, cmd[1:]
		}
		res.Bindings = append(res.Bindings, kb)
		if kb.Remove {
			continue
		}
		sl := slot{kb.Key, kb.When}
		if _, ok := cmds[sl]; !ok {
			order = append(order, sl)
		}
		dup := false
		for _, c := range cmds[sl] {
			dup = dup || c == kb.Command
		}
		if !dup {
			cmds[sl] = append(cmds[sl], kb.Command)
		}
	}
	for _, sl := range order {
		if len(cmds[sl]) > 1 {
			res.Conflicts = append(res.Conflicts, KeybindingConflict{Key: sl.key, When: sl.when, Commands: cmds[sl]})
		}
	}
	return res
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// stripJSONC removes // and /* */ comments and trailing commas.
func stripJSONC(in []byte) []byte {
	out := make([]byte, 0, len(in))
	inStr := false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inStr {
			out = append(out, c)
			if c == '\\' && i+1 < len(in) {
				i++
				out = append(out, in[i])
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(in) && in[i+1] == '/':
			for i < len(in) && in[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(in) && in[i+1] == '*':
			i += 2
			for i+1 < len(in) && !(in[i] == '*' && in[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	// trailing commas: a ',' followed (ignoring space) by ] or }
	res := make([]byte, 0, len(out))
	inStr = false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inStr {
			res = append(res, c)
			if c == '\\' && i+1 < len(out) {
				i++
				res = append(res, out[i])
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
		}
		if c == ',' {
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\n' || out[j] == '\t' || out[j] == '\r') {
				j++
			}
			if j < len(out) && (out[j] == ']' || out[j] == '}') {
				continue
			}
		}
		res = append(res, c)
	}
	return res
}
