package kube_test

import (
	"bytes"
	"testing"
	"time"

	"kube-dump/internal/kube"
)

// A limit that was never set (zero) must fall back to a default, not expire immediately.
func TestZeroTimeoutsMeanDefault(t *testing.T) {
	fakeKubectl(t)("ok")
	c := &kube.Client{Context: "e", Namespace: "n"}
	if out, err := c.Kubectl("get", "x"); err != nil || string(out) != "hello" {
		t.Errorf("Kubectl with Timeout 0: out=%q err=%v", out, err)
	}
	if out, err := kube.Run(nil, 0, "get", "x"); err != nil || string(out) != "hello" {
		t.Errorf("Run with timeout 0: out=%q err=%v", out, err)
	}
	var buf bytes.Buffer
	if err := c.Stream(&buf, 0, "exec", "p"); err != nil || buf.String() != "hello" {
		t.Errorf("Stream with idle 0: err=%v data=%q", err, buf.String())
	}
	if kube.DefaultTimeout < time.Second {
		t.Errorf("DefaultTimeout = %v", kube.DefaultTimeout)
	}
}
