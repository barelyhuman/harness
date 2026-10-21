package app

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	loginEnvOnce sync.Once
	loginEnv     []string
)

var envLine = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// LoginEnv returns the environment of the user's login shell as KEY=VALUE
// strings. GUI apps on macOS start with a bare PATH, so `$SHELL -lic env` is
// resolved once (first call) and cached. On failure it falls back to
// os.Environ().
func LoginEnv() []string {
	loginEnvOnce.Do(func() { loginEnv = resolveLoginEnv() })
	return append([]string(nil), loginEnv...)
}

func resolveLoginEnv() []string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, "-lic", "env").Output()
	if err != nil && len(out) == 0 {
		return os.Environ()
	}
	var env []string
	for _, l := range strings.Split(string(out), "\n") {
		if envLine.MatchString(l) {
			env = append(env, l)
		}
	}
	if len(env) == 0 {
		return os.Environ()
	}
	return env
}

// ApplyLoginPath sets this process's PATH from the login environment so
// exec.LookPath finds user-installed CLIs (claude, gh, git...).
func ApplyLoginPath() {
	for _, kv := range LoginEnv() {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			os.Setenv("PATH", v)
			return
		}
	}
}
