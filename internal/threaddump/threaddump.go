package threaddump

import (
	"regexp"
	"sort"
	"strings"
)

// Dump is the JVM thread dump returned by jattach.
type Dump struct {
	Raw     string   // the dump text as the JVM produced it, starting at its timestamp line
	Threads []string // Java thread state of every thread, "" for VM threads without a Java stack

	Deadlock bool // the JVM reported "Found one Java-level deadlock"
}

// stampRe matches the bare timestamp line a JVM prints before "Full thread dump".
var stampRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}`)

// Parse extracts the last "Full thread dump" from jattach output; nil if none.
func Parse(out []byte) *Dump {
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")

	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "Full thread dump") {
			start = i
		}
	}
	if start < 0 {
		return nil
	}

	// The JVM prints its own timestamp on the line before the header. Other lines there
	// ("Connected to remote JVM", "Response code") are not part of the dump.
	from := start
	if start > 0 && stampRe.MatchString(lines[start-1]) {
		from = start - 1
	}
	d := &Dump{Raw: strings.Join(lines[from:], "\n")}

	var state string
	inThread, inSMR := false, false
	flush := func() {
		if inThread {
			d.Threads = append(d.Threads, state)
		}
		inThread = false
	}
	for _, l := range lines[start+1:] {
		if inSMR && !isSMRLine(l) {
			inSMR = false // truncated or odd SMR block: do not swallow the threads after it
		}
		switch {
		case inSMR:
			inSMR = !strings.HasPrefix(l, "}")
			continue
		case strings.HasPrefix(l, "Threads class SMR info:"):
			flush()
			inSMR = true
			continue
		// After the thread list come the JNI refs, a deadlock report (its own lines start
		// with a quote like thread headers) and the heap summary: nothing to count there.
		case strings.HasPrefix(l, "JNI global refs"), strings.HasPrefix(l, "Found "), l == "Heap":
			flush()
			d.Deadlock = strings.Contains(d.Raw, "\nFound one Java-level deadlock")
			return d
		case strings.HasPrefix(l, "\""):
			flush()
			inThread = true
			state = ""
			if m := vmThreadTail.FindStringSubmatch(l); m != nil {
				state = "(" + m[1] + ")" // VM thread; overwritten by the Java state line if present
			}
		case inThread && strings.TrimSpace(l) == "":
			flush()
		case inThread:
			if s, ok := strings.CutPrefix(strings.TrimSpace(l), "java.lang.Thread.State: "); ok {
				state = s
			}
		}
	}
	flush()
	return d
}

var vmThreadTail = regexp.MustCompile(`nid=0x[0-9a-f]+ (.+?)\s*(\[0x[0-9a-f]+\])?\s*$`)

// StateCounts counts threads by Java state (first word); VM threads are skipped.
func (d *Dump) StateCounts() map[string]int {
	counts := map[string]int{}
	for _, s := range d.Threads {
		if s == "" || strings.HasPrefix(s, "(") {
			continue
		}
		counts[strings.Fields(s)[0]]++
	}
	return counts
}

// SortedStates returns the states ordered by count descending, then by name.
func SortedStates(counts map[string]int) []string {
	states := make([]string, 0, len(counts))
	for s := range counts {
		states = append(states, s)
	}
	sort.Slice(states, func(i, j int) bool {
		if counts[states[i]] != counts[states[j]] {
			return counts[states[i]] > counts[states[j]]
		}
		return states[i] < states[j]
	})
	return states
}

// isSMRLine reports whether l can belong to the "Threads class SMR info" address list.
func isSMRLine(l string) bool {
	return strings.HasPrefix(l, "}") || strings.HasPrefix(l, "_java_thread_list") ||
		strings.HasPrefix(l, "0x") || strings.TrimSpace(l) == ""
}
