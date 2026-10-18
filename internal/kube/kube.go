// Package kube runs kubectl (with time limits) and answers the few questions the tool asks
// the cluster: which services and which running pods exist.
package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultTimeout is used when a limit is not set (zero).
const DefaultTimeout = time.Minute

// Client runs kubectl against one context and namespace.
type Client struct {
	Context   string
	Namespace string
	Timeout   time.Duration // limit for one call; see KubectlT for another one
}

// Kubectl runs kubectl with the context, namespace and the default timeout.
func (c *Client) Kubectl(args ...string) ([]byte, error) {
	return c.KubectlT(c.Timeout, nil, args...)
}

// KubectlIn is Kubectl with data fed to its stdin (for `exec -i`).
func (c *Client) KubectlIn(in io.Reader, args ...string) ([]byte, error) {
	return c.KubectlT(c.Timeout, in, args...)
}

// KubectlT is Kubectl with an explicit time limit.
func (c *Client) KubectlT(timeout time.Duration, in io.Reader, args ...string) ([]byte, error) {
	return Run(in, timeout, append([]string{"--context", c.Context, "-n", c.Namespace}, args...)...)
}

// Run executes kubectl and returns its stdout. The call is killed after timeout, so a hung
// API server or a stuck exec stream cannot block the tool forever.
func Run(in io.Reader, timeout time.Duration, args ...string) ([]byte, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.WaitDelay = 5 * time.Second // do not wait for pipes a killed kubectl left open
	cmd.Stdin = in
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("timed out after %v", timeout)
		}
		return stdout.Bytes(), fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Stream runs kubectl and streams its stdout to w (no buffering in memory). It is killed
// when no data arrives for idle; the caller resumes from the offset it has.
func (c *Client) Stream(w io.Writer, idle time.Duration, args ...string) error {
	if idle <= 0 {
		idle = DefaultTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--context", c.Context, "-n", c.Namespace}, args...)...)
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	aw := &activityWriter{w: w}
	aw.touch()
	defer aw.close() // a copier goroutine kubectl left behind must not write to w later
	cmd.Stdout = aw
	cmd.Stderr = &stderr

	done := make(chan struct{})
	defer close(done)
	var idled atomic.Bool
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if time.Since(aw.last()) > idle {
					idled.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	if err := cmd.Run(); err != nil {
		if idled.Load() {
			err = fmt.Errorf("no data for %v", idle)
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// activityWriter remembers when data was last written through it and refuses writes once
// closed, so that after Stream returns nothing can still change the destination.
type activityWriter struct {
	w      io.Writer
	at     atomic.Int64 // unix nanoseconds
	mu     sync.Mutex
	closed bool
}

func (a *activityWriter) touch()          { a.at.Store(time.Now().UnixNano()) }
func (a *activityWriter) last() time.Time { return time.Unix(0, a.at.Load()) }

func (a *activityWriter) close() {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
}

func (a *activityWriter) Write(b []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return 0, io.ErrClosedPipe
	}
	a.touch()
	return a.w.Write(b)
}

// ResolveEnv checks that the context exists in the standard kubeconfig (~/.kube/config or
// $KUBECONFIG) and returns its name; an empty env means the current context.
func ResolveEnv(env string) (string, error) {
	if env == "" {
		out, err := Run(nil, time.Minute, "config", "current-context")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	out, err := Run(nil, time.Minute, "config", "get-contexts", "-o", "name")
	if err != nil {
		return "", err
	}
	names := strings.Fields(string(out))
	for _, n := range names {
		if n == env {
			return env, nil
		}
	}
	return "", fmt.Errorf("unknown env %q, contexts in kubeconfig: %s", env, strings.Join(names, ", "))
}

// ListServices returns the names of the services of the namespace, sorted.
func (c *Client) ListServices() ([]string, error) {
	out, err := c.Kubectl("get", "services", "-o", "name")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Fields(string(out)) {
		names = append(names, strings.TrimPrefix(line, "service/"))
	}
	sort.Strings(names)
	return names, nil
}

// ModulePattern turns a module name into a glob; a value that already contains glob
// characters is used as is.
func ModulePattern(module string) string {
	if strings.ContainsAny(module, "*?[") {
		return module
	}
	return module + "-*"
}

// MatchServices returns the services matching the module pattern.
func MatchServices(all []string, module string) []string {
	pattern := ModulePattern(module)
	var matched []string
	for _, name := range all {
		if ok, _ := path.Match(pattern, name); ok {
			matched = append(matched, name)
		}
	}
	return matched
}

// FindPods resolves the service's label selector and returns its running pods.
func (c *Client) FindPods(service string) ([]string, error) {
	out, err := c.Kubectl("get", "service", service, "-o", "json")
	if err != nil {
		return nil, err
	}
	var svc struct {
		Spec struct {
			Selector map[string]string `json:"selector"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &svc); err != nil {
		return nil, fmt.Errorf("parse service: %w", err)
	}
	if len(svc.Spec.Selector) == 0 {
		return nil, fmt.Errorf("service %s has no selector", service)
	}

	var parts []string
	for k, v := range svc.Spec.Selector {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)

	out, err = c.Kubectl("get", "pods", "-l", strings.Join(parts, ","), "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string  `json:"name"`
				DeletionTimestamp *string `json:"deletionTimestamp"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("parse pods: %w", err)
	}

	var pods []string
	for _, p := range list.Items {
		// A terminating pod is still "Running" but is going away: do not dump it.
		if p.Status.Phase == "Running" && p.Metadata.DeletionTimestamp == nil {
			pods = append(pods, p.Metadata.Name)
		}
	}
	sort.Strings(pods)
	return pods, nil
}
