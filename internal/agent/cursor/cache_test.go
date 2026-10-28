package cursor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sid/harness/internal/agent"
)

func TestModelsFromCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor-models.json")
	ms := []agent.Model{{ID: "custom[]", Provider: ProviderID, Name: "Custom"}}
	b, err := json.Marshal(ms)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	p := &Provider{CachePath: path}
	got, err := p.Models(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "custom[]" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSaveModelsAtomic(t *testing.T) {
	dir := t.TempDir()
	p := &Provider{CachePath: filepath.Join(dir, "m.json")}
	p.saveModels([]agent.Model{{ID: "a[]", Name: "A", Provider: ProviderID}})
	b, err := os.ReadFile(p.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	var got []agent.Model
	if json.Unmarshal(b, &got) != nil || len(got) != 1 || got[0].ID != "a[]" {
		t.Fatalf("%s", b)
	}
}

func TestParseVersionMultiline(t *testing.T) {
	if v := parseVersion("Cursor Agent 1.2.3-beta\nmore"); v != "1.2.3-beta" {
		t.Fatalf("%q", v)
	}
}
