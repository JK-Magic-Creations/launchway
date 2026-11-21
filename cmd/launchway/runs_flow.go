package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// allowedNote says that a run went on past tolerated failures, naming the
// steps: "1 allowed failure: lint". It is empty for a run with none.
func allowedNote(n int, steps []string) string {
	if n == 0 {
		return ""
	}
	noun := "allowed failures"
	if n == 1 {
		noun = "allowed failure"
	}
	note := fmt.Sprintf("%d %s", n, noun)
	if len(steps) > 0 {
		note += ": " + strings.Join(steps, ", ")
	}
	return note
}

// printActions says what the caller may do with a run, and for what they may
// not, why, what to do instead and what would help: the server's list, as it
// gave it.
func printActions(out io.Writer, acts []engine.Action) {
	if len(acts) == 0 {
		return
	}
	fmt.Fprintln(out, "\nActions:")
	for _, a := range acts {
		if a.Available {
			line := "  can " + a.Name
			if a.Risk != "" {
				line += " (risky: " + a.Risk + ")"
			}
			fmt.Fprintln(out, line)
			continue
		}
		fmt.Fprintf(out, "  cannot %s: %s\n", a.Name, a.Reason)
		if a.Instead != "" {
			fmt.Fprintf(out, "    instead: %s\n", a.Instead)
		}
		if a.WouldHelp != "" {
			fmt.Fprintf(out, "    would help: %s\n", a.WouldHelp)
		}
	}
}
