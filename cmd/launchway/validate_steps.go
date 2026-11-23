package main

// What `launchway validate` says about the custom steps a pipeline uses: what each may do, and, for a step
// of a library whose directory is a Git checkout (--library NAME=DIRECTORY), the newer versions of the library
// and what they change in the step's inputs, outputs and permissions.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/stepkit"
)

func gitLines(dir string, args ...string) []string {
	out, err := gitexec.Output(context.Background(), gitexec.Spec{Dir: dir, Args: args})
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// manifestIn reads a step's manifest from a Git checkout at a version ("" reads the files as they are).
func manifestIn(dir, ref, name string) *stepkit.Manifest {
	var data []byte
	var err error
	if ref == "" {
		data, err = os.ReadFile(filepath.Join(dir, filepath.FromSlash(stepkit.ManifestPath(name)))) //nolint:gosec // G703: a library directory the person gave on the command line
	} else {
		var out string
		out, err = gitexec.Output(context.Background(), gitexec.Spec{Dir: dir, Args: []string{"show", ref + ":" + stepkit.ManifestPath(name)}})
		data = []byte(out)
	}
	if err != nil {
		return nil
	}
	m, probs := stepkit.ParseManifest(data)
	if len(probs) > 0 {
		return nil
	}
	return m
}

// printStepNotices prints, for each custom step of the library directories given, the newer versions and what
// they change. It prints nothing for a library that is not a Git checkout, or has no newer tag.
func printStepNotices(p *pipeline.Pipeline, dirs map[string]string) {
	done := map[string]bool{}
	for _, s := range p.Steps {
		c := s.Custom
		if c == nil || c.Library == pipeline.SelfLibrary || done[c.Library+"/"+c.Name] {
			continue
		}
		done[c.Library+"/"+c.Name] = true
		dir, ok := dirs[c.Library]
		if !ok {
			continue
		}
		lib := p.Library(c.Library)
		tags := gitLines(dir, "tag", "--sort=-v:refname")
		if lib == nil || len(tags) == 0 {
			continue
		}
		pinned := manifestIn(dir, lib.Version, c.Name)
		if pinned == nil {
			continue
		}
		var newer []string
		for _, t := range tags {
			if t == lib.Version {
				break
			}
			newer = append(newer, t)
		}
		if len(newer) == 0 {
			continue
		}
		newest := manifestIn(dir, newer[0], c.Name)
		if newest == nil {
			fmt.Printf("  update: library %s has %s (you use %s); it has no step %s any more\n", c.Library, newer[0], lib.Version, c.Name)
			continue
		}
		changes := stepkit.Diff(pinned, newest)
		fmt.Printf("  update: library %s has %s (you use %s)", c.Library, newer[0], lib.Version)
		switch {
		case len(changes) == 0:
			fmt.Printf(": the step %s is unchanged\n", c.Name)
		default:
			fmt.Println(":")
			for _, ch := range changes {
				mark := "    "
				if ch.Widening {
					mark = "    ASKS FOR MORE: "
				}
				fmt.Println(mark + ch.What)
			}
		}
	}
}
