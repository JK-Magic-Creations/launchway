package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// libraryFlag collects --library NAME=DIRECTORY.
type libraryFlag []string

func (l *libraryFlag) String() string     { return strings.Join(*l, ",") }
func (l *libraryFlag) Set(v string) error { *l = append(*l, v); return nil }

// validateOffline checks a pipeline file and expands the templates it uses
// from library directories, with no server: the same parse, binding and
// expansion a server runs when it checks a pipeline, reading each library
// from the directory given for it. It prints the problems as the server
// reports them, and with resolved the resolved pipeline, as JSON.
func validateOffline(path string, libs []string, resolved bool, facts pipeline.ProjectFacts, vars pipeline.Variables) error {
	dirs, err := pipeline.ParseLibraryFlags(libs)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	p, err := source.ExpandOffline(data, dirs, pipeline.BindOptions{}, pipeline.WithProjectFacts(vars, facts))
	if err != nil {
		var ps pipeline.Problems
		if !errors.As(err, &ps) {
			return err
		}
		for _, pr := range ps {
			loc := path
			if pr.File != "" {
				loc = pr.Library + ":" + pr.File
			}
			if pr.Line > 0 {
				loc = fmt.Sprintf("%s:%d", loc, pr.Line)
			}
			where := ""
			if pr.Path != "" {
				where = pr.Path + ": "
			}
			fmt.Fprintf(os.Stderr, "%s: %s%s\n", loc, where, pr.Message)
		}
		return exitErr(1)
	}
	if resolved {
		b, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("%s: pipeline %q is valid, %d step(s) in %d level(s), templates expanded from %d library directories\n", path, p.Name, len(p.Steps), len(p.Levels()), len(dirs))
	for _, w := range pipeline.OutputWarnings(p) {
		fmt.Fprintf(os.Stderr, "%s:%d: warning: %s: %s\n", path, w.Line, w.Path, w.Message)
	}
	return nil
}
