package collect

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Cleanups for what a run leaves inside the pods (the helper binary, a heap dump file).
// They run on Ctrl+C; the normal path removes the files itself and unregisters them.
var (
	cleanupMu sync.Mutex
	cleanups  = map[int]func(){}
	cleanupID int
)

// OnExit registers fn to run if the process is interrupted; the returned func unregisters it.
func OnExit(fn func()) func() {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	cleanupID++
	id := cleanupID
	cleanups[id] = fn
	return func() {
		cleanupMu.Lock()
		delete(cleanups, id)
		cleanupMu.Unlock()
	}
}

// RunCleanups runs and forgets every registered cleanup.
func RunCleanups() {
	cleanupMu.Lock()
	fns := make([]func(), 0, len(cleanups))
	for _, f := range cleanups {
		fns = append(fns, f)
	}
	cleanups = map[int]func(){}
	cleanupMu.Unlock()
	for _, f := range fns {
		f()
	}
}

// HandleInterrupt cleans the pods up on Ctrl+C / SIGTERM and exits with 130.
func HandleInterrupt() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		fmt.Fprintln(os.Stderr, "\ninterrupted — cleaning up the pods…")
		RunCleanups()
		os.Exit(130)
	}()
}
