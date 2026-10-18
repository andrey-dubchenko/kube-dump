package collect

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kube-dump/internal/hprof"
	"kube-dump/internal/size"
)

// copyAttempts is how many times the copy of the heap dump out of the pod is (re)started.
const copyAttempts = 5

var heapUsedRe = regexp.MustCompile(`used\s+(\d+)([KMG])`)

// ParseHeapUsedKB sums the "used" figures of the heap lines of `GC.heap_info` (G1 prints one
// line, Parallel/Serial one per generation; Metaspace and class space are not heap) and
// returns KiB, or -1 when nothing could be read.
func ParseHeapUsedKB(out []byte) int64 {
	total, found := int64(0), false
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Metaspace") || strings.Contains(line, "class space") {
			continue
		}
		m := heapUsedRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		switch m[2] {
		case "M":
			v *= 1024
		case "G":
			v *= 1024 * 1024
		}
		total += v
		found = true
	}
	if !found {
		return -1
	}
	return total
}

// heapDump asks the JVM for a heap dump into the pod, streams the file to
// <dir>/heap_<pod>_<YYYYMMDD>_<HHMMSS>.hprof and removes it from the pod. The JVM is paused
// while it writes, and the directory may be small, so the free space is checked and the user
// confirms first (unless yes). It returns the files written.
func (s *session) heapDump(dir string, yes bool) ([]string, error) {
	cfg, pod := s.c.Cfg, s.pod
	heapRemote := path.Join(cfg.RemoteDir, ".kube-dump-heap.hprof") // the JVM writes here before the copy
	usedKB, freeKB := int64(-1), int64(-1)
	if out, err := s.run("jcmd", "GC.heap_info"); err == nil {
		usedKB = ParseHeapUsedKB(out)
	}
	if out, err := s.kubectl("df", "-Pk", cfg.RemoteDir); err == nil {
		freeKB = ParseDfAvail(string(out))
	}
	fmt.Printf("[%s] heap dump: heap used %s, free in the pod %s: %s\n", pod, size.KiB(usedKB), cfg.RemoteDir, size.KiB(freeKB))
	if need := usedKB + usedKB/10; usedKB >= 0 && freeKB >= 0 && freeKB < need { // the file is about as big as the used heap
		return nil, fmt.Errorf("not enough space in the pod %s: %s free, about %s needed", cfg.RemoteDir, size.KiB(freeKB), size.KiB(need))
	}
	if !yes && s.c.Confirm != nil && !s.c.Confirm(fmt.Sprintf("[%s] The JVM is paused while the heap is dumped (seconds to minutes) and the file may contain secrets and personal data. Take the heap dump?", pod)) {
		return nil, errSkipped
	}

	rmHeap := func() { s.kubectl("rm", "-f", heapRemote) }
	rmHeap()               // the JVM refuses to overwrite an existing file
	defer OnExit(rmHeap)() // also on Ctrl+C: a multi-GB file must not stay in the pod
	defer rmHeap()

	fmt.Printf("[%s] dumping the heap inside the pod…\n", pod)
	dumpCmd := "GC.heap_dump " + heapRemote
	if !cfg.HeapLiveOnly {
		dumpCmd = "GC.heap_dump -all " + heapRemote // unreachable objects too: bigger, no full GC
	}
	if out, err := s.runT(cfg.HeapTimeout, "jcmd", dumpCmd); err != nil {
		return nil, fmt.Errorf("heap dump failed: %s", failReason(out, err))
	}
	out, err := s.kubectl("sh", "-c", "wc -c < "+heapRemote)
	if err != nil {
		return nil, fmt.Errorf("cannot stat the heap dump in the pod: %s", failReason(out, err))
	}
	total, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || total <= 0 {
		return nil, fmt.Errorf("the JVM produced no heap dump file (size %q)", strings.TrimSpace(string(out)))
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, "heap_"+pod+"_"+time.Now().Format("20060102_150405")+".hprof")
	f, err := os.Create(file)
	if err != nil {
		return nil, err
	}
	// A kubectl exec stream of hundreds of megabytes can break off. Resume from the
	// current offset (tail -c +N) instead of starting over, and verify the final size.
	var off int64
	var copyErr error
	for attempt := 1; attempt <= copyAttempts && off < total; attempt++ {
		cmd := []string{"cat", heapRemote}
		if off > 0 {
			cmd = []string{"tail", "-c", "+" + strconv.FormatInt(off+1, 10), heapRemote}
		}
		pw := &Progress{W: f, Total: total, N: off, Pod: pod}
		copyErr = s.c.Kube.Stream(pw, cfg.CopyIdleTimeout, append(append(s.execArgs(pod), "--"), cmd...)...)
		pw.Finish()
		off = pw.N
		if off >= total {
			copyErr = nil
			break
		}
		if attempt < copyAttempts {
			fmt.Printf("[%s] copy interrupted at %s of %s (%v), resuming…\n", pod, size.KiB(off/1024), size.KiB(total/1024), firstLine(fmt.Sprint(copyErr)))
			time.Sleep(2 * time.Second)
		}
	}
	if cerr := f.Close(); copyErr == nil && cerr != nil {
		copyErr = cerr
	}
	if copyErr == nil && off != total {
		copyErr = fmt.Errorf("copied %d of %d bytes", off, total)
	}
	if copyErr != nil {
		os.Remove(file)
		return nil, fmt.Errorf("copying the heap dump failed after %d attempts at %s of %s: %v", copyAttempts, size.KiB(off/1024), size.KiB(total/1024), copyErr)
	}

	files := []string{file}
	if cfg.HeapSummary {
		fmt.Printf("[%s] analyzing the heap dump locally…\n", pod)
		if err := SummarizeHeapFile(file, cfg.HeapTop, false); err != nil {
			fmt.Printf("[%s] WARNING: heap summary failed: %v\n", pod, err)
		} else {
			files = append(files, SummaryPath(file))
		}
	}
	return files, nil
}

// Progress counts the bytes written and prints the download progress about once a second.
type Progress struct {
	W     io.Writer
	Total int64
	N     int64 // bytes written so far, including the offset a resumed copy started at
	Pod   string
	last  time.Time
}

func (p *Progress) Write(b []byte) (int, error) {
	n, err := p.W.Write(b)
	p.N += int64(n)
	if time.Since(p.last) >= time.Second {
		p.last = time.Now()
		p.print("\r")
	}
	return n, err
}

func (p *Progress) print(end string) {
	fmt.Printf("%s[%s] copied %s of %s", end, p.Pod, size.KiB(p.N/1024), size.KiB(p.Total/1024))
}

// Finish prints the final progress line.
func (p *Progress) Finish() {
	p.print("\r")
	fmt.Println()
}

// ParseDfAvail returns the available KB from `df -Pk` output, or -1.
func ParseDfAvail(out string) int64 {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	f := strings.Fields(lines[len(lines)-1])
	if len(lines) < 2 || len(f) < 4 {
		return -1
	}
	v, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil {
		return -1
	}
	return v
}

// SummaryPath is where the histogram of a heap dump file is written.
func SummaryPath(hprofFile string) string {
	return strings.TrimSuffix(hprofFile, filepath.Ext(hprofFile)) + ".summary.txt"
}

// SummarizeHeapFile reads a heap dump, writes its class histogram next to it and prints it
// (all of it when full, otherwise only the top 10 by size).
func SummarizeHeapFile(file string, top int, full bool) error {
	start := time.Now()
	sum, err := hprof.AnalyzeFile(file, top)
	if err != nil {
		return err
	}
	if err := os.WriteFile(SummaryPath(file), []byte(sum.Format(top)), 0o644); err != nil {
		return err
	}
	if full {
		fmt.Print(sum.Format(top))
	} else {
		fmt.Print(sum.FormatBrief(10))
	}
	fmt.Printf("\nsummary written to %s (%v)\n", SummaryPath(file), time.Since(start).Round(time.Millisecond))
	return nil
}
