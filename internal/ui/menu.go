// Package ui is the interactive part: arrow-key menus in a terminal and numbered prompts
// when stdin is not a terminal.
package ui

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	"kube-dump/internal/config"
	"kube-dump/internal/kube"
)

// Action is what the user chose in the service menu.
type Action int

const (
	ActionDump Action = iota
	ActionChangeModule
	ActionExit
	ActionInterrupted // Ctrl+C in the form: return to the menu, don't kill the process
)

const (
	optAll    = "\x00all"
	optModule = "\x00module"
	optExit   = "\x00exit"
	optBack   = "\x00back"
)

// stdin is the line reader for the numbered (non-TUI) menu. It is created lazily on first
// use so that it never buffers bytes that the TUI (keys.go) is about to read from the raw
// console — both consume os.Stdin and a shared bufio buffer would silently steal characters.
var stdin *bufio.Reader

// IsTTY reports whether stdin is a terminal (arrow-key menus work).
func IsTTY() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// Ask reads a line from stdin; ok=false on EOF.
func Ask(prompt string) (string, bool) {
	fmt.Print(prompt)
	if stdin == nil {
		stdin = bufio.NewReader(os.Stdin)
	}
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		fmt.Println()
		return "", false
	}
	return strings.TrimSpace(line), true
}

// ChooseServices shows an arrow-key list (Enter selects, "/" filters) in a terminal, or
// falls back to a numbered menu when stdin is not a terminal.
func ChooseServices(services []string) ([]string, Action, error) {
	if !IsTTY() {
		chosen, action := chooseServicesNumbered(services)
		return chosen, action, nil
	}

	options := make([]huh.Option[string], 0, len(services)+3)
	for _, s := range services {
		options = append(options, huh.NewOption(s, s))
	}
	if len(services) > 1 {
		options = append(options, huh.NewOption(fmt.Sprintf("[all %d services]", len(services)), optAll))
	}
	options = append(options,
		huh.NewOption("[change module]", optModule),
		huh.NewOption("[exit]", optExit),
	)

	var choice string
	sel := huh.NewSelect[string]().
		Title("Select service").
		Description("↑/↓ move · Enter select · / filter · Ctrl+C exit").
		Options(options...).
		Value(&choice)
	// Without an explicit height huh sizes the list to show every option at once, so
	// nothing scrolls. Limit it only when the list cannot fit the terminal.
	if _, rows, err := term.GetSize(int(os.Stdout.Fd())); err == nil && len(options)+4 > rows {
		sel.Height(max(rows-1, 5))
	}
	err := runForm(sel)
	if errors.Is(err, huh.ErrUserAborted) {
		// Ctrl+C in the TUI form. This is an interruption of the *form*, not of the
		// process: return to the service list instead of exiting the whole tool.
		return nil, ActionInterrupted, nil
	}
	if err != nil {
		return nil, ActionExit, err
	}

	switch choice {
	case optExit:
		return nil, ActionExit, nil
	case optModule:
		return nil, ActionChangeModule, nil
	case optAll:
		fmt.Printf("Services: all %d\n\n", len(services))
		return services, ActionDump, nil
	}
	fmt.Printf("Service: %s\n\n", choice)
	return []string{choice}, ActionDump, nil
}

// chooseServicesNumbered reads a selection like "2", "1,3", "a" (all), "m" (module) or "q" (quit).
func chooseServicesNumbered(services []string) ([]string, Action) {
	for i, s := range services {
		fmt.Printf("  %2d) %s\n", i+1, s)
	}
	for {
		answer, ok := Ask(fmt.Sprintf("Select service [1-%d, comma-separated, a = all, m = change module, q = quit]: ", len(services)))
		switch {
		case !ok || strings.EqualFold(answer, "q"):
			return nil, ActionExit
		case strings.EqualFold(answer, "m"):
			return nil, ActionChangeModule
		case strings.EqualFold(answer, "a"):
			fmt.Println()
			return services, ActionDump
		}
		var chosen []string
		seen := map[int]bool{}
		valid := answer != ""
		for _, part := range strings.Split(answer, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || n < 1 || n > len(services) {
				valid = false
				break
			}
			if !seen[n] {
				seen[n] = true
				chosen = append(chosen, services[n-1])
			}
		}
		if valid {
			fmt.Println()
			return chosen, ActionDump
		}
		fmt.Println("Invalid selection")
	}
}

// AskModule asks for a module name; ok=false means the user wants to exit.
func AskModule() (string, bool) {
	if IsTTY() {
		var module string
		err := runForm(huh.NewInput().
			Title("Module name").
			Description("services <module>-* will be listed · glob like my-*-api works · Ctrl+C exit").
			Value(&module))
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(module), true
	}
	for {
		module, ok := Ask("Module name: ")
		if !ok {
			return "", false
		}
		if module != "" {
			return module, true
		}
	}
}

// ChooseMode asks what to collect from the chosen service; ok=false means back to the list.
func ChooseMode() (config.Mode, bool) {
	if !IsTTY() {
		for {
			a, ok := Ask("Collect [t = thread dump, h = heap dump, b = both, q = back]: ")
			if !ok {
				return config.ModeThread, false
			}
			switch strings.ToLower(a) {
			case "q":
				return config.ModeThread, false
			case "t", "h", "b":
				m, _ := config.ParseMode(a)
				return m, true
			}
			fmt.Println("Invalid selection")
		}
	}

	var choice string
	err := runForm(huh.NewSelect[string]().
		Title("What to collect").
		Description("↑/↓ move · Enter select · Ctrl+C back").
		Options(
			huh.NewOption("Thread dump", "thread"),
			huh.NewOption("Heap dump (.hprof: pauses the JVM, may contain secrets)", "heap"),
			huh.NewOption("Both", "both"),
			huh.NewOption("[back]", optBack),
		).
		Value(&choice))
	if err != nil || choice == optBack {
		return config.ModeThread, false
	}
	m, _ := config.ParseMode(choice)
	return m, true
}

// ChoosePod asks which pod to take a heap dump of: a pod name, "*" for all of them;
// ok=false means back. A single pod is taken without asking.
func ChoosePod(pods []string) (string, bool) {
	if len(pods) == 1 {
		return pods[0], true
	}
	if !IsTTY() {
		for i, p := range pods {
			fmt.Printf("  %2d) %s\n", i+1, p)
		}
		for {
			a, ok := Ask(fmt.Sprintf("Pod [1-%d, a = all pods, q = back]: ", len(pods)))
			switch {
			case !ok || strings.EqualFold(a, "q"):
				return "", false
			case strings.EqualFold(a, "a"):
				return "*", true
			}
			if n, err := strconv.Atoi(a); err == nil && n >= 1 && n <= len(pods) {
				return pods[n-1], true
			}
			fmt.Println("Invalid selection")
		}
	}

	options := make([]huh.Option[string], 0, len(pods)+2)
	for _, p := range pods {
		options = append(options, huh.NewOption(p, p))
	}
	options = append(options,
		huh.NewOption(fmt.Sprintf("[all %d pods, one after another]", len(pods)), "*"),
		huh.NewOption("[back]", optBack),
	)
	var choice string
	err := runForm(huh.NewSelect[string]().
		Title("Pod for the heap dump").
		Description("↑/↓ move · Enter select · Ctrl+C back").
		Options(options...).
		Value(&choice))
	if err != nil || choice == optBack {
		return "", false
	}
	return choice, true
}

// Confirm asks a yes/no question; anything but an explicit yes is no.
func Confirm(msg string) bool {
	if !IsTTY() {
		a, _ := Ask(msg + " [y/N]: ")
		return strings.EqualFold(a, "y") || strings.EqualFold(a, "yes")
	}
	var ok bool
	err := runForm(huh.NewConfirm().Title(msg).Affirmative("Yes").Negative("No").Value(&ok))
	return err == nil && ok
}

// NoServices tells that the module pattern matched nothing.
func NoServices(module string, c *kube.Client) {
	fmt.Printf("No services matching %s in %s/%s\n", kube.ModulePattern(module), c.Context, c.Namespace)
}
