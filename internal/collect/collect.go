// Package collect takes thread and heap dumps of the pods of a service through jattach, so
// that nothing is printed to the container stdout (and nothing reaches log collectors).
package collect

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"kube-dump/internal/config"
	"kube-dump/internal/kube"
	"kube-dump/internal/threaddump"
)

// Collector collects dumps with one configuration through one kubectl client.
type Collector struct {
	Cfg     *config.Config
	Kube    *kube.Client
	Confirm func(msg string) bool // asked before a heap dump; nil means "yes"
}

// Options say what to collect and from which pods.
type Options struct {
	Mode config.Mode
	Pod  string // "" = every pod; a pod name; "*" = every pod, stated explicitly
	Yes  bool   // do not ask before a heap dump
}

// errSkipped marks a step the user declined; it is not a failure.
var errSkipped = errors.New("skipped")

// session is a jattach binary uploaded into one pod.
type session struct {
	c   *Collector
	pod string
	ctr []string // -c <container> when set
	bin string   // where the helper binary lives inside the pod
	off func()   // unregisters the Ctrl+C cleanup
}

// open streams the user's jattach binary into the pod (cfg.RemoteDir).
func (c *Collector) open(pod string) (*session, error) {
	f, err := os.Open(c.Cfg.Jattach)
	if err != nil {
		return nil, fmt.Errorf("jattach: %w", err)
	}
	defer f.Close()

	s := &session{c: c, pod: pod, bin: path.Join(c.Cfg.RemoteDir, ".kube-dump-jattach")}
	if c.Cfg.Container != "" {
		s.ctr = []string{"-c", c.Cfg.Container}
	}
	s.off = OnExit(func() { s.kubectl("rm", "-f", s.bin) })
	up := append(s.execArgs("-i", pod), "--", "sh", "-c", "cat > "+s.bin+" && chmod 700 "+s.bin)
	if _, err := c.Kube.KubectlIn(f, up...); err != nil {
		s.close() // also removes a partial upload
		return nil, fmt.Errorf("jattach upload failed: %s", firstLine(err.Error()))
	}
	return s, nil
}

func (s *session) execArgs(extra ...string) []string {
	return append(append([]string{"exec"}, extra...), s.ctr...)
}

// kubectl runs a command inside the pod.
func (s *session) kubectl(args ...string) ([]byte, error) {
	return s.c.Kube.Kubectl(append(append(s.execArgs(s.pod), "--"), args...)...)
}

// run runs jattach against PID 1 of the container.
func (s *session) run(args ...string) ([]byte, error) {
	return s.runT(s.c.Cfg.KubectlTimeout, args...)
}

// runT is run with an explicit time limit (a heap dump takes far longer than a thread dump).
func (s *session) runT(timeout time.Duration, args ...string) ([]byte, error) {
	full := append(append(s.execArgs(s.pod), "--", s.bin, "1"), args...)
	return s.c.Kube.KubectlT(timeout, nil, full...)
}

// close removes the helper binary from the pod.
func (s *session) close() {
	s.off()
	s.kubectl("rm", "-f", s.bin)
}

// threadDump takes `jcmd Thread.print -l` (threads, locks, deadlock report).
func (s *session) threadDump() (*threaddump.Dump, error) {
	out, err := s.run("jcmd", "Thread.print -l")
	if err != nil {
		return nil, fmt.Errorf("jattach failed: %s", failReason(out, err))
	}
	dump := threaddump.Parse(out)
	if dump == nil || len(dump.Threads) == 0 {
		return nil, fmt.Errorf("jattach returned no thread dump: %s", firstLine(strings.TrimSpace(string(out))))
	}
	return dump, nil
}

// failReason is the most useful one-line explanation of a failed kubectl/jattach call.
func failReason(out []byte, err error) string {
	reason := strings.TrimSpace(string(out))
	if reason == "" {
		reason = err.Error()
	}
	return firstLine(reason)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Services collects from every service and returns the number of failures.
func (c *Collector) Services(services []string, opts Options) int {
	failed := 0
	for _, svc := range services {
		failed += c.service(svc, opts)
	}
	return failed
}

// service collects from the running pods of the service into <output>/<env>/<service>/
// and returns the number of failures.
func (c *Collector) service(service string, opts Options) int {
	pods, err := c.Kube.FindPods(service)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] ERROR: %v\n\n", service, err)
		return 1
	}
	if len(pods) == 0 {
		fmt.Fprintf(os.Stderr, "[%s] ERROR: no running pods\n\n", service)
		return 1
	}
	if opts.Pod != "" && opts.Pod != "*" {
		if !slices.Contains(pods, opts.Pod) {
			fmt.Fprintf(os.Stderr, "[%s] ERROR: %s is not a running pod of the service: %s\n\n", service, opts.Pod, strings.Join(pods, ", "))
			return 1
		}
		pods = []string{opts.Pod}
	}
	if opts.Mode.WantsHeap() && len(pods) > 1 && opts.Pod != "*" {
		fmt.Fprintf(os.Stderr, "[%s] ERROR: %d pods; heap dumps are never started for all pods implicitly, pass -pod <name> or -pod '*'\n\n", service, len(pods))
		return 1
	}
	fmt.Printf("=== Env %s, service %s/%s: %d pod(s): %s\n\n", c.Cfg.Env, c.Cfg.Namespace, service, len(pods), strings.Join(pods, ", "))

	dir := filepath.Join(c.Cfg.Output, c.Cfg.Env, service)
	total := map[string]int{}
	failed := 0
	for _, pod := range pods {
		res, err := c.pod(pod, dir, opts)
		for _, f := range res.files {
			fmt.Printf("[%s] saved %s\n", pod, f)
		}
		if res.counts != nil {
			printCounts(res.counts)
			for k, v := range res.counts {
				total[k] += v
			}
		}
		switch {
		case errors.Is(err, errSkipped):
			fmt.Printf("[%s] heap dump skipped\n\n", pod)
		case err != nil:
			fmt.Fprintf(os.Stderr, "[%s] ERROR: %v\n\n", pod, err)
			failed++
		}
	}

	if len(pods) > 1 && len(total) > 0 {
		fmt.Printf("[%s TOTAL]\n", service)
		printCounts(total)
	}
	return failed
}

// podResult is what was saved for one pod, also when a later step failed.
type podResult struct {
	files  []string
	counts map[string]int // thread states; nil when no thread dump was taken
}

// pod collects a thread dump and/or a heap dump of the pod through one jattach upload.
// Directories are created on the first saved file, so a failed run leaves nothing behind.
func (c *Collector) pod(pod, dir string, opts Options) (podResult, error) {
	var res podResult
	s, err := c.open(pod)
	if err != nil {
		return res, err
	}
	defer s.close()

	if opts.Mode.WantsThread() {
		dump, err := s.threadDump()
		if err != nil {
			return res, err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return res, err
		}
		// thread_<pod>_<YYYYMMDD>_<HHMMSS>.dump
		file := filepath.Join(dir, "thread_"+pod+"_"+time.Now().Format("20060102_150405")+".dump")
		if err := os.WriteFile(file, []byte(dump.Raw), 0o644); err != nil {
			return res, err
		}
		res.files = append(res.files, file)
		res.counts = dump.StateCounts()
		if dump.Deadlock {
			fmt.Printf("[%s] WARNING: the JVM reports a Java-level DEADLOCK — details at the end of %s\n", pod, filepath.Base(file))
		}
	}
	if opts.Mode.WantsHeap() {
		files, err := s.heapDump(dir, opts.Yes)
		if err != nil {
			return res, err
		}
		res.files = append(res.files, files...)
	}
	return res, nil
}

// printCounts prints state counts sorted by count descending.
func printCounts(counts map[string]int) {
	if len(counts) == 0 {
		fmt.Println("  no thread states found (is it a JVM?)")
		fmt.Println()
		return
	}
	sum := 0
	for _, c := range counts {
		sum += c
	}
	fmt.Printf("  %6s  %s\n", "Count", "State")
	for _, s := range threaddump.SortedStates(counts) {
		fmt.Printf("  %6d  %s\n", counts[s], s)
	}
	fmt.Printf("  %6d  (total)\n\n", sum)
}
