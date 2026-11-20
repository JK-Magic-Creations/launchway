package main

import (
	"fmt"
	"strings"
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
