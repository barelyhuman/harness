package app

import (
	"strings"
	"testing"
)

func TestLoginEnvHasPath(t *testing.T) {
	env := LoginEnv()
	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			found = true
		}
	}
	if !found {
		t.Fatal("no PATH in login env")
	}
}
