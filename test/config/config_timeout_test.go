package config_test

import (
	"kube-dump/internal/config"
	"strings"
	"testing"
	"time"
)

func TestConfigTimeoutDefaults(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, ""), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KubectlTimeout != time.Minute || cfg.HeapTimeout != 15*time.Minute || cfg.CopyIdleTimeout != time.Minute {
		t.Errorf("unexpected defaults: %v %v %v", cfg.KubectlTimeout, cfg.HeapTimeout, cfg.CopyIdleTimeout)
	}
}

func TestConfigTimeoutValues(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, "kubectl_timeout: 30s\nheap_timeout: 1h\ncopy_idle_timeout: 2m\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KubectlTimeout != 30*time.Second || cfg.HeapTimeout != time.Hour || cfg.CopyIdleTimeout != 2*time.Minute {
		t.Errorf("got %v %v %v", cfg.KubectlTimeout, cfg.HeapTimeout, cfg.CopyIdleTimeout)
	}
}

func TestConfigTimeoutRejectsTooSmall(t *testing.T) {
	for _, extra := range []string{"kubectl_timeout: 0s\n", "heap_timeout: 500ms\n", "copy_idle_timeout: 0s\n"} {
		_, err := config.Load(writeConfig(t, extra), "")
		if err == nil || !strings.Contains(err.Error(), "at least 1s") {
			t.Errorf("%q: got %v", extra, err)
		}
	}
}
