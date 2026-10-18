package ui

import (
	"io"
	"os"
	"sync"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"
)

// Every huh form starts its own bubbletea program. On Windows each program reads the
// console with a blocking ReadConsoleInput that cannot be cancelled, so after a form
// closes its reader stays blocked, swallows the next keypress and drops it — the first
// character typed into the next form is lost.
//
// To avoid that, stdin is read by one goroutine for the whole process, and each form
// gets its own session on top of it. A finished session stops taking data, so
// keypresses go to the next form.

var (
	keysOnce sync.Once
	keys     chan []byte
)

// keyBuffer is a small ring for keypresses that arrive while no session is
// attached (e.g. the keypress that closes a form, or keys typed between forms).
// The shared reader never blocks: if the buffer is full it drops the chunk.
const keyBufSize = 64

func startKeyReader() {
	keysOnce.Do(func() {
		keys = make(chan []byte, keyBufSize)
		go func() {
			buf := make([]byte, 256)
			for {
				n, err := os.Stdin.Read(buf)
				if n > 0 {
					chunk := make([]byte, n)
					copy(chunk, buf[:n])
					select {
					case keys <- chunk:
					default: // buffer full, no session attached yet — drop
					}
				}
				if err != nil {
					close(keys)
					return
				}
			}
		}()
	})
}

// keySession is an io.Reader over the shared stdin reader, valid until closed.
// It is safe for concurrent use by multiple goroutines.
type keySession struct {
	mu      sync.Mutex
	done    chan struct{}
	closing bool
	pending []byte
	eof     bool
}

func (s *keySession) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		if s.eof || s.closing {
			return 0, io.EOF
		}
		select {
		case b, ok := <-keys:
			if !ok {
				s.eof = true
				return 0, io.EOF
			}
			s.pending = b
		case <-s.done:
			s.closing = true
			return 0, io.EOF
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

// runForm runs a huh form in raw console mode on a fresh input session.
func runForm(field huh.Field) error {
	// bubbletea only switches the console to raw mode for a real TTY input,
	// so do it here (raw + VT input: arrows arrive as escape sequences).
	fd := int(os.Stdin.Fd())
	if state, err := term.MakeRaw(fd); err == nil {
		defer term.Restore(fd, state)
	}

	// Start the shared reader only after the console is raw. On Windows a read that
	// is already pending when the mode changes keeps line-input semantics: it delivers
	// nothing until Enter, so the form looks dead. Starting the goroutine first made
	// this a scheduling race — it only showed up on some launches.
	startKeyReader()

	s := &keySession{done: make(chan struct{})}

	err := huh.NewForm(huh.NewGroup(field)).
		WithShowHelp(false).
		WithInput(s).
		Run()

	// Close the session first: a reader still blocked in Read holds s.mu, and
	// without this the Lock below would wait for (and swallow) the next keypress.
	close(s.done)

	// The form's close keypress (Enter / Esc / Ctrl+C) is often the *next*
	// chunk in the keys channel after Run returns. Drain it so it is not
	// swallowed as the first input of the next form.
	s.mu.Lock()
	if len(s.pending) == 0 && !s.eof {
		select {
		case <-keys: // discard the close keypress
		default:
		}
	}
	s.mu.Unlock()

	return err
}
