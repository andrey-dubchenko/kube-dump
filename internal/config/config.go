// Package config loads and validates configuration.yml.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Mode says what to collect from a pod.
type Mode int

const (
	ModeThread Mode = iota
	ModeHeap
	ModeBoth
)

// WantsThread reports whether a thread dump is taken.
func (m Mode) WantsThread() bool { return m != ModeHeap }

// WantsHeap reports whether a heap dump is taken.
func (m Mode) WantsHeap() bool { return m != ModeThread }

// ParseMode accepts thread, heap or both (or t, h, b); empty means thread.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(s) {
	case "", "thread", "t":
		return ModeThread, nil
	case "heap", "h":
		return ModeHeap, nil
	case "both", "b":
		return ModeBoth, nil
	}
	return ModeThread, fmt.Errorf("mode must be thread, heap or both, got %q", s)
}

// Config is the content of configuration.yml.
type Config struct {
	Namespace string `yaml:"namespace"`
	Service   string `yaml:"service"`
	Module    string `yaml:"module"` // services <module>-* are offered for interactive choice
	Env       string `yaml:"env"`    // kubeconfig context name
	Container string `yaml:"container"`
	Jattach   string `yaml:"jattach"` // path to a local jattach binary (static, linux)
	Output    string `yaml:"output"`

	ModeName     string `yaml:"mode"`           // thread | heap | both; used when `service` is set
	Pod          string `yaml:"pod"`            // only this pod; "*" = every pod; "" = every pod (thread dumps)
	HeapConfirm  bool   `yaml:"heap_confirm"`   // ask before a heap dump
	HeapLiveOnly bool   `yaml:"heap_live_only"` // GC.heap_dump without -all: live objects only
	HeapSummary  bool   `yaml:"heap_summary"`   // write a class histogram next to the heap dump
	HeapTop      int    `yaml:"heap_top"`       // rows per section of that summary
	RemoteDir    string `yaml:"remote_dir"`     // directory inside the pod for jattach and the heap file

	KubectlTimeout  time.Duration `yaml:"kubectl_timeout"`   // limit for one kubectl call (get, exec, upload)
	HeapTimeout     time.Duration `yaml:"heap_timeout"`      // limit for the JVM to write the heap dump
	CopyIdleTimeout time.Duration `yaml:"copy_idle_timeout"` // no data from the pod for this long = restart the copy

	Mode Mode `yaml:"-"` // ModeName parsed (and overridable by -mode)
}

// remoteDirRe keeps the directory safe to put into a `sh -c` command inside the pod.
var remoteDirRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)

// Defaults returns the configuration with every optional parameter at its default.
func Defaults() *Config {
	return &Config{
		Output: "dumps", ModeName: "thread", HeapConfirm: true, HeapLiveOnly: true, HeapSummary: true, HeapTop: 30,
		RemoteDir:      "/tmp",
		KubectlTimeout: time.Minute, HeapTimeout: 15 * time.Minute, CopyIdleTimeout: time.Minute,
	}
}

// Load reads and validates the file; envName, when set, overrides env.
func Load(file, envName string) (*Config, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := Defaults()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Jattach == "" {
		return nil, fmt.Errorf("config: jattach is required: path to a linux jattach binary (https://github.com/jattach/jattach)")
	}
	if cfg.Jattach, err = LocateFile(cfg.Jattach); err != nil {
		return nil, fmt.Errorf("config: jattach: %w", err)
	}
	if cfg.Mode, err = ParseMode(cfg.ModeName); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	for name, d := range map[string]time.Duration{"kubectl_timeout": cfg.KubectlTimeout, "heap_timeout": cfg.HeapTimeout, "copy_idle_timeout": cfg.CopyIdleTimeout} {
		if d < time.Second {
			return nil, fmt.Errorf("config: %s must be a duration of at least 1s like 60s or 15m, got %v", name, d)
		}
	}
	if cfg.HeapTop < 1 {
		return nil, fmt.Errorf("config: heap_top must be at least 1, got %d", cfg.HeapTop)
	}
	if !remoteDirRe.MatchString(cfg.RemoteDir) {
		return nil, fmt.Errorf("config: remote_dir must be an absolute path of letters, digits, . _ - /, got %q", cfg.RemoteDir)
	}
	if envName != "" {
		cfg.Env = envName
	}
	if cfg.Namespace == "" {
		return nil, fmt.Errorf("config: namespace is required")
	}
	return cfg, nil
}

// LocateFile returns the path of an existing file. A relative path is looked up in the
// working directory first and then next to the executable, so the tool works when it is
// started from another directory.
func LocateFile(p string) (string, error) {
	_, err := os.Stat(p)
	if err == nil || filepath.IsAbs(p) {
		return p, err
	}
	if exe, exeErr := os.Executable(); exeErr == nil {
		alt := filepath.Join(filepath.Dir(exe), p)
		if _, altErr := os.Stat(alt); altErr == nil {
			return alt, nil
		}
	}
	return p, err
}
