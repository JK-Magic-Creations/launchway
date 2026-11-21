package main

import (
	"fmt"
	"io"
	"strings"
)

// printRunStages prints a run's stages, each step with its state.
func printRunStages(out io.Writer, r *runView) {
	state := map[string]string{}
	for _, s := range r.StepList {
		state[s.ID] = s.Status
	}
	printStages(out, r.Levels, state)
}

// printStages says a run's shape in words: the stages (steps grouped by how far they are from the start,
// from `needs`), with the steps of a stage that run at the same time named together. It is the text form of
// the run page's pipeline graph.
func printStages(out io.Writer, levels [][]string, status map[string]string) {
	if len(levels) == 0 {
		return
	}
	fmt.Fprintln(out, "Stages (the steps of a stage can run at the same time):")
	for i, level := range levels {
		names := make([]string, 0, len(level))
		for _, id := range level {
			if s := status[id]; s != "" && s != "pending" {
				id += " (" + s + ")"
			}
			names = append(names, id)
		}
		together := ""
		if len(level) > 1 {
			together = " (runs together)"
		}
		fmt.Fprintf(out, "  stage %d%s: %s\n", i+1, together, strings.Join(names, ", "))
	}
	fmt.Fprintln(out)
}
