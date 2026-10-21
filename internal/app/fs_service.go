package app

import (
	"path/filepath"
	"strings"

	"github.com/sid/harness/internal/fs"
)

// FileContent is the result of FSService.Read.
type FileContent struct {
	Content  string `json:"content"`
	Etag     string `json:"etag"`
	Language string `json:"language"` // Monaco language id
	Binary   bool   `json:"binary"`   // content is empty when true
}

// FSService: file operations + scope watching. Watching pushes
// `fs/<scopeKey>` {paths:[abs...]} and `scm/<scopeKey>` (see GitService).
type FSService struct {
	hub *scopeHub
}

// Read returns text content, an etag and a Monaco language id. Files that are
// not valid UTF-8 or contain NUL are reported Binary with empty content.
func (s *FSService) Read(path string) (FileContent, error) {
	b, etag, err := fs.Read(fs.Expand(path))
	if err != nil {
		return FileContent{}, wrapErr(err)
	}
	fc := FileContent{Etag: etag, Language: LanguageFor(path)}
	if isBinary(b) {
		fc.Binary = true
		return fc, nil
	}
	fc.Content = string(b)
	return fc, nil
}

// Write saves content atomically. A non-empty ifEtag that no longer matches
// fails with code "conflict" (error.etag = current etag). Returns the new etag.
func (s *FSService) Write(path, content, ifEtag string) (string, error) {
	etag, err := fs.Write(fs.Expand(path), []byte(content), ifEtag)
	return etag, wrapErr(err)
}

func (s *FSService) Create(path string, isDir bool) error {
	return wrapErr(fs.Create(fs.Expand(path), isDir))
}

func (s *FSService) Rename(from, to string) error {
	return wrapErr(fs.Rename(fs.Expand(from), fs.Expand(to)))
}

// Delete moves to the Trash.
func (s *FSService) Delete(path string) error { return wrapErr(fs.Delete(fs.Expand(path))) }

// WatchScope starts (or restarts) watching root and opens the `fs/<scopeKey>`
// and `scm/<scopeKey>` channels. An initial scm state is pushed.
func (s *FSService) WatchScope(scopeKey, root string) error {
	return wrapErr(s.hub.watch(scopeKey, root))
}

func (s *FSService) UnwatchScope(scopeKey string) { s.hub.unwatch(scopeKey) }

func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	for _, c := range b[:n] {
		if c == 0 {
			return true
		}
	}
	return false
}

var langByExt = map[string]string{
	".go": "go", ".ts": "typescript", ".tsx": "typescript", ".mts": "typescript", ".cts": "typescript",
	".js": "javascript", ".jsx": "javascript", ".mjs": "javascript", ".cjs": "javascript",
	".json": "json", ".jsonc": "json", ".md": "markdown", ".markdown": "markdown",
	".html": "html", ".htm": "html", ".css": "css", ".scss": "scss", ".less": "less",
	".py": "python", ".rs": "rust", ".java": "java", ".kt": "kotlin", ".swift": "swift",
	".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".hpp": "cpp", ".m": "objective-c",
	".cs": "csharp", ".rb": "ruby", ".php": "php", ".lua": "lua", ".sql": "sql",
	".sh": "shell", ".bash": "shell", ".zsh": "shell", ".yml": "yaml", ".yaml": "yaml",
	".toml": "ini", ".ini": "ini", ".xml": "xml", ".svg": "xml", ".plist": "xml",
	".vue": "html", ".svelte": "html", ".graphql": "graphql", ".gql": "graphql",
	".dockerfile": "dockerfile", ".tf": "hcl", ".proto": "protobuf", ".txt": "plaintext",
}

var langByName = map[string]string{
	"dockerfile": "dockerfile", "makefile": "makefile", "go.mod": "go", "go.sum": "plaintext",
	".gitignore": "plaintext", ".zshrc": "shell", ".bashrc": "shell", ".zprofile": "shell",
}

// LanguageFor guesses a Monaco language id from the file name.
func LanguageFor(path string) string {
	base := strings.ToLower(filepath.Base(path))
	if l, ok := langByName[base]; ok {
		return l
	}
	if l, ok := langByExt[filepath.Ext(base)]; ok {
		return l
	}
	return "plaintext"
}
