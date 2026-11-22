package main

import (
	"errors"
	"os"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// expandRunLibraries expands the templates of a pipeline for a run on this machine. The starters that ship with
// Launchway need no server, and any other library is read from the directory given for it with --library. The
// cleanup removes the repositories made from those directories.
func expandRunLibraries(p *pipeline.Pipeline, path string, dirs map[string]string) (*pipeline.Pipeline, []engine.LibrarySource, func(), error) {
	none := func() {}
	if p.Expanded() {
		if len(dirs) > 0 {
			return nil, nil, none, errors.New("--library: the pipeline uses no templates")
		}
		return p, nil, none, nil
	}
	work, err := os.MkdirTemp("", "launchway-libs-")
	if err != nil {
		return nil, nil, none, err
	}
	cleanup := func() { os.RemoveAll(work) }
	out, libs, err := source.ExpandLocal(p, dirs, work)
	if err != nil {
		cleanup()
		return nil, nil, none, explainProblems(path, err)
	}
	return out, libs, cleanup, nil
}
