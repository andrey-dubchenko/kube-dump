package kube_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"kube-dump/internal/kube"
)

// TestMain lets the test binary double as a fake kubectl: the tests copy it to a directory
// that is put first in PATH, and KD_FAKE_KUBECTL selects what it does.
func TestMain(m *testing.M) {
	switch os.Getenv("KD_FAKE_KUBECTL") {
	case "":
		os.Exit(m.Run())
	case "ok":
		os.Stdout.WriteString("hello")
	case "sleep":
		time.Sleep(60 * time.Second)
	case "stall": // some data, then silence
		os.Stdout.WriteString("abc")
		time.Sleep(60 * time.Second)
	}
	os.Exit(0)
}

// fakeKubectl installs the fake in PATH and returns a function that sets its mode.
func fakeKubectl(t *testing.T) func(mode string) {
	t.Helper()
	dir := t.TempDir()
	name := "kubectl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	dst.Close()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func(mode string) { t.Setenv("KD_FAKE_KUBECTL", mode) }
}

func TestRunReturnsOutput(t *testing.T) {
	fakeKubectl(t)("ok")
	out, err := kube.Run(nil, 10*time.Second, "get", "x")
	if err != nil || string(out) != "hello" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestRunTimesOut(t *testing.T) {
	fakeKubectl(t)("sleep")
	start := time.Now()
	_, err := kube.Run(nil, time.Second, "get", "x")
	if err == nil || !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("took %v, the call was not killed", d)
	}
}

func TestStreamIdleTimeout(t *testing.T) {
	fakeKubectl(t)("stall")
	var buf bytes.Buffer
	c := &kube.Client{Context: "e", Namespace: "n"}
	start := time.Now()
	err := c.Stream(&buf, 2*time.Second, "exec", "p")
	if err == nil || !strings.Contains(err.Error(), "no data for 2s") {
		t.Fatalf("err = %v", err)
	}
	if buf.String() != "abc" {
		t.Errorf("data received before the stall: %q", buf.String())
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("took %v, the stream was not killed", d)
	}
}

func TestStreamCompletes(t *testing.T) {
	fakeKubectl(t)("ok")
	var buf bytes.Buffer
	c := &kube.Client{Context: "e", Namespace: "n"}
	if err := c.Stream(&buf, 5*time.Second, "exec", "p"); err != nil || buf.String() != "hello" {
		t.Fatalf("err=%v data=%q", err, buf.String())
	}
}

func TestModulePatternAndMatchServices(t *testing.T) {
	if kube.ModulePattern("my-module") != "my-module-*" || kube.ModulePattern("my-*-api") != "my-*-api" {
		t.Error("ModulePattern")
	}
	all := []string{"my-module-api", "my-module-bo", "my-other-api", "unrelated"}
	if got := kube.MatchServices(all, "my-module"); !reflect.DeepEqual(got, []string{"my-module-api", "my-module-bo"}) {
		t.Errorf("module: %v", got)
	}
	if got := kube.MatchServices(all, "my-*-api"); !reflect.DeepEqual(got, []string{"my-module-api", "my-other-api"}) {
		t.Errorf("glob: %v", got)
	}
	if got := kube.MatchServices(all, "nothing"); got != nil {
		t.Errorf("no match must be empty: %v", got)
	}
}
