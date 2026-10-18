package config_test

import (
	"kube-dump/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The example config must stay loadable and must not contain real names.
func TestExampleConfigLoads(t *testing.T) {
	data, err := os.ReadFile("../../configuration.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	jattach := filepath.Join(dir, "jattach")
	if err := os.WriteFile(jattach, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(data), "jattach: jattach", "jattach: "+filepath.ToSlash(jattach), 1)
	cfgPath := filepath.Join(dir, "configuration.yml")
	if err := os.WriteFile(cfgPath, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Namespace != "my-namespace" || cfg.Env != "my-context" || cfg.Module != "my-module" {
		t.Errorf("unexpected example values: %+v", cfg)
	}
	for _, real := range []string{"proezd", "ecp", "olrecon", "UAT", "TEST"} {
		if strings.Contains(string(data), real) {
			t.Errorf("the example config mentions %q", real)
		}
	}
}
