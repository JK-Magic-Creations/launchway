package source

import (
	"context"
	"errors"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// A pipeline may use the custom steps of its own repository, `uses:
// ./steps/<name>`, which are read from the repository at the commit the run is
// for. The callers that know the commit say so through the context.

type selfKey struct{}

// selfRepo is the repository and commit a pipeline's own steps are read from.
type selfRepo struct {
	project, sha string
}

// WithRepository tells expansion which Flight's repository, at which commit,
// the steps of `uses: ./steps/<name>` are read from.
func WithRepository(ctx context.Context, project, sha string) context.Context {
	return context.WithValue(ctx, selfKey{}, selfRepo{project, sha})
}

func pipelineUsesSelf(p *pipeline.Pipeline) bool {
	for _, s := range p.Steps {
		if _, _, self := pipeline.SplitUses(s.Uses); self && s.IsUse() {
			return true
		}
	}
	return false
}

// selfSource is what expansion reads a pipeline's own steps with, or nil when
// the caller did not say which commit (the steps cannot then be read, and
// the pipeline says so where it uses one).
func (m *Manager) selfSource(ctx context.Context) *pipeline.SelfSource {
	r, ok := ctx.Value(selfKey{}).(selfRepo)
	if !ok || !pipeline.IsCommitID(r.sha) {
		return nil
	}
	p := m.c().projects[r.project]
	if p == nil {
		return nil
	}
	return &pipeline.SelfSource{
		Library: &pipeline.Library{Name: pipeline.SelfLibrary, Version: r.sha, Commit: r.sha, RefKind: "commit"},
		Read: func(path string) ([]byte, error) {
			// No lock: callers that start runs hold the project's, and reading a commit that is already
			// there does not need it.
			data, err := gitOut(ctx, p.dir, "show", r.sha+":"+path)
			if err != nil {
				if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "exists on disk, but not in") {
					return nil, pipeline.ErrNoTemplate
				}
				return nil, errors.New(redact.URLs(err.Error()))
			}
			return []byte(data), nil
		},
	}
}
