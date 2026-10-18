package collect_test

import (
	"bytes"
	"testing"

	"kube-dump/internal/collect"
)

func TestParseDfAvail(t *testing.T) {
	out := "Filesystem     1024-blocks    Used Available Capacity Mounted on\noverlay           61255492 9934468  48154784      18% /\n"
	if got := collect.ParseDfAvail(out); got != 48154784 {
		t.Errorf("got %d", got)
	}
	for _, bad := range []string{"", "df: not found", "Filesystem 1\n", "h\nx y z\n"} {
		if got := collect.ParseDfAvail(bad); got != -1 {
			t.Errorf("ParseDfAvail(%q) = %d, want -1", bad, got)
		}
	}
}

func TestParseHeapUsedKB(t *testing.T) {
	cases := map[string]struct {
		out  string
		want int64
	}{
		"g1":       {" garbage-first heap   total 524288K, used 123456K [0x1, 0x2)\n  region size 1024K, 15 young (15360K), 0 survivors (0K)\n Metaspace       used 99999K, capacity 100K\n  class space    used 5555K, capacity 1K\n", 123456},
		"parallel": {" PSYoungGen      total 76288K, used 5243K [0x1, 0x2)\n ParOldGen       total 175104K, used 20000K [0x3, 0x4)\n Metaspace       used 3000K, capacity 3100K\n", 25243},
		"zgc":      {" ZHeap           used 512M, capacity 1024M, max capacity 2048M\n Metaspace       used 100K\n", 512 * 1024},
		"gigabyte": {" ZHeap           used 2G, capacity 4G\n", 2 * 1024 * 1024},
		"none":     {"nothing useful here\n", -1},
		"empty":    {"", -1},
	}
	for name, c := range cases {
		if got := collect.ParseHeapUsedKB([]byte(c.out)); got != c.want {
			t.Errorf("%s: got %d, want %d", name, got, c.want)
		}
	}
}

func TestProgressCountsBytes(t *testing.T) {
	var buf bytes.Buffer
	p := &collect.Progress{W: &buf, Total: 10, Pod: "p"}
	if _, err := p.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	if p.N != 10 || buf.String() != "helloworld" {
		t.Errorf("n=%d data=%q", p.N, buf.String())
	}
}

func TestProgressContinuesFromOffset(t *testing.T) {
	var buf bytes.Buffer
	p := &collect.Progress{W: &buf, Total: 10, N: 6, Pod: "p"}
	p.Write([]byte("abcd"))
	if p.N != 10 {
		t.Errorf("a resumed copy must count from its offset, n=%d", p.N)
	}
}

func TestCleanupsRunOnceAndCanBeUnregistered(t *testing.T) {
	collect.RunCleanups() // start from an empty registry
	var a, b int
	offA := collect.OnExit(func() { a++ })
	collect.OnExit(func() { b++ })
	offA() // a normal run unregisters its cleanup
	collect.RunCleanups()
	if a != 0 || b != 1 {
		t.Fatalf("a=%d b=%d, want 0 and 1", a, b)
	}
	collect.RunCleanups()
	if b != 1 {
		t.Errorf("a cleanup must run only once, b=%d", b)
	}
}

func TestSummaryPath(t *testing.T) {
	if got := collect.SummaryPath("dir/heap_p_20261017_171530.hprof"); got != "dir/heap_p_20261017_171530.summary.txt" {
		t.Errorf("got %q", got)
	}
}
