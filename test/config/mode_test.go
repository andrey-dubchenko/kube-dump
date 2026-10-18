package config_test

import (
	"testing"

	"kube-dump/internal/config"
)

func TestParseMode(t *testing.T) {
	cases := map[string]config.Mode{
		"": config.ModeThread, "thread": config.ModeThread, "T": config.ModeThread,
		"heap": config.ModeHeap, "h": config.ModeHeap,
		"both": config.ModeBoth, "B": config.ModeBoth,
	}
	for in, want := range cases {
		got, err := config.ParseMode(in)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := config.ParseMode("everything"); err == nil {
		t.Error("expected an error for an unknown mode")
	}
}

func TestModeWants(t *testing.T) {
	if !config.ModeThread.WantsThread() || config.ModeThread.WantsHeap() {
		t.Error("thread mode")
	}
	if config.ModeHeap.WantsThread() || !config.ModeHeap.WantsHeap() {
		t.Error("heap mode")
	}
	if !config.ModeBoth.WantsThread() || !config.ModeBoth.WantsHeap() {
		t.Error("both mode")
	}
}
