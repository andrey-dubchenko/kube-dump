package config_test

import (
	"kube-dump/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes configuration.yml with the given extra lines and a fake jattach file.
func writeConfig(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	jattach := filepath.Join(dir, "jattach")
	if err := os.WriteFile(jattach, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "configuration.yml")
	body := "namespace: ns\njattach: " + filepath.ToSlash(jattach) + "\n" + extra
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestConfigDefaults(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, ""), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != config.ModeThread || cfg.Pod != "" || !cfg.HeapConfirm || !cfg.HeapLiveOnly || cfg.RemoteDir != "/tmp" || cfg.Output != "dumps" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestConfigHeapOptions(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, "mode: both\npod: \"*\"\nheap_confirm: false\nheap_live_only: false\nremote_dir: /var/tmp\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != config.ModeBoth || cfg.Pod != "*" || cfg.HeapConfirm || cfg.HeapLiveOnly || cfg.RemoteDir != "/var/tmp" {
		t.Errorf("unexpected: %+v", cfg)
	}
}

func TestConfigRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"mode: everything\n":         "mode must be",
		"remote_dir: tmp\n":          "remote_dir",
		"remote_dir: \"/tmp; rm\"\n": "remote_dir",
		"remote_dir: \"/a b\"\n":     "remote_dir",
	}
	for extra, want := range cases {
		_, err := config.Load(writeConfig(t, extra), "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want it to mention %q", extra, err, want)
		}
	}
}

func TestConfigNeedsJattach(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yml")
	os.WriteFile(cfg, []byte("namespace: ns\n"), 0o644)
	if _, err := config.Load(cfg, ""); err == nil || !strings.Contains(err.Error(), "jattach") {
		t.Errorf("got %v", err)
	}
}

func TestConfigHeapSummary(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, ""), "")
	if err != nil || !cfg.HeapSummary || cfg.HeapTop != 30 {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	cfg, err = config.Load(writeConfig(t, "heap_summary: false\nheap_top: 5\n"), "")
	if err != nil || cfg.HeapSummary || cfg.HeapTop != 5 {
		t.Fatalf("values: %+v %v", cfg, err)
	}
	if _, err := config.Load(writeConfig(t, "heap_top: 0\n"), ""); err == nil {
		t.Error("heap_top 0 must be rejected")
	}
}
