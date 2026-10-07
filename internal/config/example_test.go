package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestExampleConfigStaysInSync decodes config.example.yaml with unknown-field
// checking, so a renamed or removed setting cannot silently rot the docs.
func TestExampleConfigStaysInSync(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	cfg := Default()
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		t.Fatalf("config.example.yaml does not match the config schema: %v", err)
	}

	applyDefaults(cfg)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config.example.yaml is not a valid configuration: %v", err)
	}

	// The example must not ship a placeholder password that could never work.
	if cfg.Panel.Admin.PasswordHash != "" {
		t.Fatalf("the example should leave password_hash empty so first start can generate one")
	}
	if len(cfg.Users) == 0 || len(cfg.Subscriptions) == 0 {
		t.Fatal("the example should document at least one subscription and one account")
	}
	for i := range cfg.Users {
		for j, target := range cfg.Users[i].Targets {
			if target.Sub == "" {
				continue
			}
			if cfg.FindSub(target.Sub) == nil {
				t.Fatalf("example account %s target %d references unknown subscription %q",
					cfg.Users[i].Name, j+1, target.Sub)
			}
		}
	}
}
