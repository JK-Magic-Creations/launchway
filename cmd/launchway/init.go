package main

// launchway init: propose the pipeline file for this repository by looking at
// what it holds, from the starters that ship with Launchway.

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/starters"
)

func cmdInit(args []string) error {
	fl := flag.NewFlagSet("init", flag.ContinueOnError)
	write := fl.Bool("write", false, "save the file as .launchway/pipeline.yaml (without it, the file is only printed)")
	force := fl.Bool("force", false, "replace a pipeline file that is already there")
	if _, err := parseArgs(fl, args); err != nil {
		return err
	}
	files, err := listRepository(".")
	if err != nil {
		return err
	}
	read := func(name string) string {
		b, err := os.ReadFile(name)
		if err != nil || len(b) > 1<<20 {
			return ""
		}
		return string(b)
	}
	for _, p := range source.PipelinePaths {
		if _, err := os.Stat(p); err == nil && !*force {
			fmt.Printf("%s already exists, so there is nothing to start from. Check it with: launchway validate\n", p)
			return nil
		}
	}
	d := starters.Detect(files, read)
	if d == nil {
		fmt.Println("Nothing here says what kind of Flight this is (no go.mod, pyproject.toml, package.json, pom.xml, build.gradle, Chart.yaml, Dockerfile or index.html at the top).")
		fmt.Println("Write a pipeline by hand: https://jk-magic-creations.github.io/launchway/tutorials/first-pipeline, or run it from a subdirectory.")
		return cliErr{1, ""}
	}
	name := filepath.Base(mustAbs("."))
	text := proposal(name, d)
	fmt.Printf("launchway/%s fits: %s.\n\n", d.Flight, d.Because)
	fmt.Print(text)
	for _, n := range d.Notes {
		fmt.Println("note:", n)
	}
	if !*write {
		fmt.Printf("\nThis is only printed. Save it with: launchway init --write\n")
		fmt.Printf("A repository that has no pipeline file at all can also get this one by itself, when its level of the installation allows it (default_pipeline: { detect: true }).\n")
		return nil
	}
	dest := source.PipelinePaths[0]
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(dest, []byte(text), 0o600); err != nil { // Git records only whether a file is executable
		return err
	}
	fmt.Printf("\nWrote %s. Check what it runs with: launchway validate --resolved\n", dest)
	return nil
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

// proposal is the pipeline file for a detected flight plan.
func proposal(project string, d *starters.Detection) string {
	name := strings.ToLower(project)
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, name)
	name = strings.Trim(name, "-")
	if name == "" {
		name = "my-project"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: made by launchway init because %s.\n# Every input of the flight plan has a default; see what it runs with: launchway validate --resolved\n", name, d.Because)
	fmt.Fprintf(&b, "pipeline: %s\non:\n  push: { branches: [main] }\n  pull_request: {}\nsupersede: true\nsteps:\n  ci:\n    uses: launchway/%s\n", name, d.Flight)
	if len(d.With) > 0 {
		keys := make([]string, 0, len(d.With))
		for k := range d.With {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			if v := d.With[k]; strings.HasPrefix(v, "[") {
				parts = append(parts, k+": "+v) // a list is written as a list
			} else {
				parts = append(parts, fmt.Sprintf("%s: %q", k, v))
			}
		}
		fmt.Fprintf(&b, "    with: { %s }\n", strings.Join(parts, ", "))
	}
	return b.String()
}

// listRepository lists the files of the directory, two levels deep, without
// the directories Git and package managers keep.
func listRepository(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		depth := strings.Count(rel, string(filepath.Separator))
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".venv", "target", "dist", "build":
				return filepath.SkipDir
			}
			if depth >= 2 {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out, err
}
