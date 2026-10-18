package collect_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"kube-dump/internal/collect"
	"kube-dump/internal/config"
	"kube-dump/internal/kube"
)

const fakeDump = `2026-10-17 14:19:15
Full thread dump OpenJDK 64-Bit Server VM (11.0.32.1+1 mixed mode, sharing):

"main" #1 prio=5 os_prio=0 cpu=1.00ms elapsed=9.00s tid=0x1 nid=0x1 runnable  [0x0]
   java.lang.Thread.State: RUNNABLE
	at com.acme.A.run(A.java:1)

"worker" #2 prio=5 os_prio=0 cpu=1.00ms elapsed=9.00s tid=0x2 nid=0x2 waiting on condition  [0x0]
   java.lang.Thread.State: WAITING (parking)
	at jdk.internal.misc.Unsafe.park(Native Method)

JNI global refs: 1, weak refs: 0
`

// heapBytes is the content of the fake heap dump file inside every fake pod.
func heapBytes() []byte {
	b := make([]byte, 5000)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// TestMain lets the test binary double as a fake kubectl that behaves like a cluster with a
// service "svc" whose pods are pod-a and pod-b (plus a Pending and a terminating one). Every
// call is appended to KD_FAKE_DIR/log so the tests can see what was run inside the pods.
func TestMain(m *testing.M) {
	dir := os.Getenv("KD_FAKE_DIR")
	if dir == "" {
		os.Exit(m.Run())
	}
	os.Exit(fakeKubectl(dir, os.Args[1:]))
}

func fakeKubectl(dir string, args []string) int {
	if len(args) >= 4 && args[0] == "--context" {
		args = args[4:] // --context X -n Y
	}
	line := strings.Join(args, " ")
	if f, err := os.OpenFile(filepath.Join(dir, "log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, line)
		f.Close()
	}
	remote := func(pod string) string { return filepath.Join(dir, "remote-"+pod+".hprof") }
	pod := ""
	if len(args) > 1 && args[0] == "exec" {
		pod = args[1]
		if pod == "-i" {
			pod = args[2]
		}
	}
	switch {
	case strings.HasPrefix(line, "get service svc"):
		fmt.Print(`{"spec":{"selector":{"app":"x"}}}`)
	case strings.HasPrefix(line, "get pods"):
		fmt.Print(`{"items":[
			{"metadata":{"name":"pod-b"},"status":{"phase":"Running"}},
			{"metadata":{"name":"pod-a"},"status":{"phase":"Running"}},
			{"metadata":{"name":"pod-c"},"status":{"phase":"Pending"}},
			{"metadata":{"name":"pod-d","deletionTimestamp":"2026-10-17T00:00:00Z"},"status":{"phase":"Running"}}]}`)
	case strings.HasPrefix(line, "exec -i "):
		io.Copy(io.Discard, os.Stdin) // the uploaded jattach binary
	case strings.Contains(line, "jcmd Thread.print -l"):
		fmt.Print(fakeDump)
	case strings.Contains(line, "jcmd GC.heap_info"):
		fmt.Printf(" garbage-first heap   total 524288K, used %sK [0x1, 0x2)\n", os.Getenv("KD_FAKE_USED"))
	case strings.Contains(line, "jcmd GC.heap_dump"):
		if os.Getenv("KD_FAKE_DUMP_FAILS") != "" {
			fmt.Print("Unable to open socket file")
			return 1
		}
		os.WriteFile(remote(pod), heapBytes(), 0o644)
	case strings.Contains(line, "wc -c <"):
		data, err := os.ReadFile(remote(pod))
		if err != nil {
			return 1
		}
		fmt.Println(len(data))
	case strings.HasPrefix(line, "exec "+pod+" -- cat "):
		data, _ := os.ReadFile(remote(pod))
		marker := filepath.Join(dir, "cat-done-"+pod)
		if _, err := os.Stat(marker); err != nil && os.Getenv("KD_FAKE_BREAK") != "" {
			os.WriteFile(marker, nil, 0o644)
			os.Stdout.Write(data[:2000]) // the stream breaks off half way
			return 1
		}
		os.Stdout.Write(data)
	case strings.Contains(line, " -- tail -c +"):
		fields := strings.Fields(line)
		n, _ := strconv.Atoi(strings.TrimPrefix(fields[5], "+"))
		data, _ := os.ReadFile(remote(pod))
		os.Stdout.Write(data[n-1:])
	case strings.Contains(line, " -- df -Pk"):
		fmt.Printf("Filesystem 1024-blocks Used Available Capacity Mounted on\noverlay 100000 1 %s 1%% /tmp\n", os.Getenv("KD_FAKE_FREE"))
	case strings.Contains(line, " -- rm -f "):
		if strings.HasSuffix(line, ".hprof") {
			os.Remove(remote(pod))
		}
	}
	return 0
}

// cluster installs the fake kubectl and returns a Collector writing into a temp directory.
func cluster(t *testing.T, env map[string]string) (*collect.Collector, string, string) {
	t.Helper()
	bin := t.TempDir()
	name := "kubectl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(bin, name), os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	dst.Close()

	fake := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KD_FAKE_DIR", fake)
	t.Setenv("KD_FAKE_USED", "1024")
	t.Setenv("KD_FAKE_FREE", "5000000")
	for k, v := range env {
		t.Setenv(k, v)
	}

	jattach := filepath.Join(fake, "jattach")
	os.WriteFile(jattach, []byte("fake-jattach-binary"), 0o755)
	cfg := config.Defaults()
	cfg.Namespace, cfg.Env, cfg.Jattach, cfg.Output = "ns", "TEST", jattach, filepath.Join(t.TempDir(), "dumps")
	cfg.HeapSummary = false
	cfg.KubectlTimeout, cfg.HeapTimeout, cfg.CopyIdleTimeout = 30*time.Second, 30*time.Second, 10*time.Second
	kc := &kube.Client{Context: cfg.Env, Namespace: cfg.Namespace, Timeout: cfg.KubectlTimeout}
	return &collect.Collector{Cfg: cfg, Kube: kc, Confirm: func(string) bool { return true }}, filepath.Join(cfg.Output, "TEST", "svc"), fake
}

func readLog(t *testing.T, fake string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(fake, "log"))
	return string(b)
}

func files(t *testing.T, dir, prefix string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, prefix+"*"))
	return m
}

func TestThreadDumpsOfRunningPodsOnly(t *testing.T) {
	c, dir, fake := cluster(t, nil)
	if failed := c.Services([]string{"svc"}, collect.Options{Mode: config.ModeThread}); failed != 0 {
		t.Fatalf("failed = %d", failed)
	}
	for _, pod := range []string{"pod-a", "pod-b"} {
		got := files(t, dir, "thread_"+pod+"_")
		if len(got) != 1 || !strings.HasSuffix(got[0], ".dump") {
			t.Fatalf("%s: dump files %v", pod, got)
		}
		data, _ := os.ReadFile(got[0])
		if !strings.HasPrefix(string(data), "2026-10-17 14:19:15\nFull thread dump") {
			t.Errorf("%s: dump content: %.60q", pod, data)
		}
	}
	log := readLog(t, fake)
	for _, skipped := range []string{"pod-c", "pod-d"} {
		if strings.Contains(log, "exec "+skipped) || strings.Contains(log, "exec -i "+skipped) {
			t.Errorf("%s must not be touched:\n%s", skipped, log)
		}
	}
	// the helper binary is uploaded to and removed from every pod, and no heap command runs
	for _, pod := range []string{"pod-a", "pod-b"} {
		if !strings.Contains(log, "exec -i "+pod+" -- sh -c cat > /tmp/.kube-dump-jattach") ||
			!strings.Contains(log, "exec "+pod+" -- rm -f /tmp/.kube-dump-jattach") {
			t.Errorf("%s: upload/removal of jattach missing:\n%s", pod, log)
		}
	}
	if strings.Contains(log, "GC.heap") {
		t.Errorf("a thread dump run must not touch the heap:\n%s", log)
	}
}

func TestHeapDumpOfSeveralPodsNeedsAnExplicitChoice(t *testing.T) {
	c, dir, fake := cluster(t, nil)
	if failed := c.Services([]string{"svc"}, collect.Options{Mode: config.ModeHeap}); failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if strings.Contains(readLog(t, fake), "exec") || len(files(t, dir, "")) != 0 {
		t.Error("nothing may be run or saved without an explicit pod")
	}
	if failed := c.Services([]string{"svc"}, collect.Options{Mode: config.ModeHeap, Pod: "pod-z"}); failed != 1 {
		t.Errorf("an unknown pod must fail, failed = %d", failed)
	}
}

func TestHeapDumpCopiesFileResumesAndCleansUp(t *testing.T) {
	c, dir, fake := cluster(t, map[string]string{"KD_FAKE_BREAK": "1"})
	if failed := c.Services([]string{"svc"}, collect.Options{Mode: config.ModeBoth, Pod: "pod-a"}); failed != 0 {
		t.Fatalf("failed = %d\n%s", failed, readLog(t, fake))
	}
	heap := files(t, dir, "heap_pod-a_")
	if len(heap) != 1 || !strings.HasSuffix(heap[0], ".hprof") {
		t.Fatalf("heap files: %v", heap)
	}
	data, _ := os.ReadFile(heap[0])
	if string(data) != string(heapBytes()) {
		t.Errorf("the copy differs from the dump in the pod (%d bytes, want %d)", len(data), len(heapBytes()))
	}
	if len(files(t, dir, "thread_pod-a_")) != 1 {
		t.Error("mode both must also save the thread dump")
	}
	log := readLog(t, fake)
	if !strings.Contains(log, "tail -c +2001 ") {
		t.Errorf("the broken copy must resume at byte 2001:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(fake, "remote-pod-a.hprof")); err == nil {
		t.Error("the heap file must be removed from the pod")
	}
	if strings.Contains(log, "pod-b") {
		t.Errorf("only pod-a was requested:\n%s", log)
	}
}

func TestHeapDumpDeclinedLeavesNothing(t *testing.T) {
	c, dir, fake := cluster(t, nil)
	c.Confirm = func(string) bool { return false }
	if failed := c.Services([]string{"svc"}, collect.Options{Mode: config.ModeHeap, Pod: "pod-a"}); failed != 0 {
		t.Errorf("declining is not a failure, failed = %d", failed)
	}
	if strings.Contains(readLog(t, fake), "GC.heap_dump") || len(files(t, dir, "")) != 0 {
		t.Error("a declined heap dump must not run or save anything")
	}
	// -yes skips the question
	c.Confirm = func(string) bool { t.Error("asked although yes was given"); return false }
	if failed := c.Services([]string{"svc"}, collect.Options{Mode: config.ModeHeap, Pod: "pod-a", Yes: true}); failed != 0 {
		t.Errorf("failed = %d", failed)
	}
	if len(files(t, dir, "heap_pod-a_")) != 1 {
		t.Error("with yes the heap dump must be taken")
	}
}

func TestHeapDumpRefusedWhenThePodHasNoRoom(t *testing.T) {
	c, dir, fake := cluster(t, map[string]string{"KD_FAKE_USED": "900000", "KD_FAKE_FREE": "950000"})
	if failed := c.Services([]string{"svc"}, collect.Options{Mode: config.ModeHeap, Pod: "pod-a", Yes: true}); failed != 1 {
		t.Errorf("failed = %d, want 1 (the file needs the used heap plus 10%%)", failed)
	}
	if strings.Contains(readLog(t, fake), "GC.heap_dump") || len(files(t, dir, "")) != 0 {
		t.Error("nothing may be dumped when the pod has no room")
	}
}

func TestFailedHeapDumpStillRemovesFiles(t *testing.T) {
	c, dir, fake := cluster(t, map[string]string{"KD_FAKE_DUMP_FAILS": "1"})
	if failed := c.Services([]string{"svc"}, collect.Options{Mode: config.ModeHeap, Pod: "pod-a", Yes: true}); failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	log := readLog(t, fake)
	if !strings.Contains(log, "exec pod-a -- rm -f /tmp/.kube-dump-jattach") || !strings.Contains(log, "rm -f /tmp/.kube-dump-heap.hprof") {
		t.Errorf("the helper and the heap file must be removed after a failure:\n%s", log)
	}
	if len(files(t, dir, "")) != 0 {
		t.Error("a failed run must leave no files or directories")
	}
}
