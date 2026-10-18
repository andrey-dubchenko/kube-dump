package threaddump_test

import (
	"strings"
	"testing"

	"kube-dump/internal/threaddump"
)

func TestParseDumpTakesLastAndCounts(t *testing.T) {
	out := "Connected to remote JVM\nResponse code = 0\n" +
		"2026-10-17 03:00:00\nFull thread dump OLD:\n\n\"old\" #1 nid=0x1 runnable\n   java.lang.Thread.State: BLOCKED (on object monitor)\n\n" +
		"2026-10-17 03:04:08\nFull thread dump NEW:\n\n" +
		"Threads class SMR info:\n_java_thread_list=0x1, length=2, elements={\n0x1, 0x2\n}\n\n" +
		"\"main\" #1 prio=5 tid=0x1 nid=0x1 runnable\n   java.lang.Thread.State: RUNNABLE\n\tat a.B.c(B.java:1)\n\n   Locked ownable synchronizers:\n\t- None\n\n" +
		"\"w\" #2 prio=5 tid=0x2 nid=0x2 waiting on condition\n   java.lang.Thread.State: WAITING (parking)\n\tat x.Y(Y.java:2)\n\n" +
		"\"VM Thread\" os_prio=0 tid=0x3 nid=0x3 runnable\n\n" +
		"JNI global refs: 1, weak refs: 0\n"
	d := threaddump.Parse([]byte(out))
	if d == nil {
		t.Fatal("dump not found")
	}
	if strings.Contains(d.Raw, "OLD") || strings.Contains(d.Raw, "Response code") {
		t.Errorf("raw has text from outside the last dump:\n%s", d.Raw)
	}
	if !strings.HasPrefix(d.Raw, "2026-10-17 03:04:08\nFull thread dump NEW") {
		t.Errorf("raw should start at the JVM timestamp line:\n%s", d.Raw)
	}
	counts := d.StateCounts()
	if len(d.Threads) != 3 || counts["RUNNABLE"] != 1 || counts["WAITING"] != 1 || len(counts) != 2 {
		t.Errorf("threads=%v counts=%v", d.Threads, counts)
	}
}

func TestParseDumpDeadlockReportIsNotThreads(t *testing.T) {
	out := "Full thread dump X:\n\n\"a\" #1 nid=0x1 waiting for monitor entry\n   java.lang.Thread.State: BLOCKED (on object monitor)\n\n" +
		"Found one Java-level deadlock:\n=============================\n\"a\":\n  waiting to lock monitor 0x1\n\n" +
		"Found 1 deadlock.\n\nHeap\n garbage-first heap\n"
	d := threaddump.Parse([]byte(out))
	if d == nil || len(d.Threads) != 1 {
		t.Fatalf("deadlock report must not create threads: %+v", d)
	}
	if !d.Deadlock {
		t.Error("Deadlock flag not set")
	}
	if !strings.Contains(d.Raw, "Found 1 deadlock.") || !strings.Contains(d.Raw, "garbage-first") {
		t.Errorf("raw must keep the whole dump:\n%s", d.Raw)
	}
}

func TestParseDumpNone(t *testing.T) {
	if threaddump.Parse([]byte("Error: no JVM\n")) != nil {
		t.Fatal("expected nil")
	}
}

func TestSortedStates(t *testing.T) {
	got := threaddump.SortedStates(map[string]int{"B": 1, "A": 1, "C": 5})
	if strings.Join(got, ",") != "C,A,B" {
		t.Errorf("got %v", got)
	}
}

func TestParseDumpTruncatedSMRBlockDoesNotHideThreads(t *testing.T) {
	out := "Full thread dump X:\n\nThreads class SMR info:\n_java_thread_list=0x1, length=1, elements={\n0x1, 0x2\n\n" +
		"\"main\" #1 nid=0x1 runnable\n   java.lang.Thread.State: RUNNABLE\n\n"
	d := threaddump.Parse([]byte(out))
	if d == nil || len(d.Threads) != 1 {
		t.Fatalf("unexpected: %+v", d)
	}
}
