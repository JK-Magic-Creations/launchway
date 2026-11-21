package source

import (
	"context"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Standing orders reach a run here: when a run is created from a commit, the
// server asks the Inspector (internal/standards/service) for the Inspection
// that the repository's effective Standing orders call for, and the steps
// it returns are added to the run's pipeline. The repository changes
// nothing: the pipeline file is as it was, and the run says which steps the
// server added and why.

// InspectRequest is what the Inspector needs to know about a run.
type InspectRequest struct {
	Project string
	// GitDir is the project's mirror, where the commit is.
	GitDir string
	SHA    string
	// DefaultBranch is the project's default branch; OnDefaultBranch says
	// the run is for it (a push or a run by hand or schedule on that branch).
	DefaultBranch   string
	OnDefaultBranch bool
	Event           string
	// Since is the commit the previous run of this ref had, where one is
	// known, so that the secret scan reads only what is new.
	Since string
	// Restricted: the run is for a pull request from an automation account
	// or a fork. The Inspection needs no secret, so it runs all the same.
	Restricted bool
}

// Inspector adds the Inspection to a run's pipeline. It returns the
// pipeline to run (the same one when there is nothing to add) and a note
// for the run's record.
type Inspector interface {
	Inspect(ctx context.Context, req InspectRequest, p *pipeline.Pipeline) (*pipeline.Pipeline, string, error)
}

type inspectorHolder struct{ i Inspector }

// SetInspector says who adds the Inspection to runs. Without it, runs are
// made exactly as the repositories' pipelines say.
func (m *Manager) SetInspector(i Inspector) { m.inspector.Store(&inspectorHolder{i}) }

func (m *Manager) inspect(ctx context.Context, nr *engine.NewRun) {
	h := m.inspector.Load()
	if h == nil || h.i == nil || nr.Draft != nil || nr.Pipeline == nil || nr.Trigger.SHA == "" {
		return
	}
	switch nr.Trigger.Event {
	case pipeline.EventPush, pipeline.EventPullRequest, pipeline.EventManual, pipeline.EventSchedule, pipeline.EventWebhook:
	default:
		return
	}
	p := m.c().projects[nr.Project]
	if p == nil {
		return
	}
	req := InspectRequest{Project: nr.Project, GitDir: p.dir, SHA: nr.Trigger.SHA, DefaultBranch: p.DefaultBranch, Event: nr.Trigger.Event,
		OnDefaultBranch: nr.Trigger.Event != pipeline.EventPullRequest && nr.Trigger.Branch != "" && nr.Trigger.Branch == p.DefaultBranch,
		Restricted:      nr.Restricted != nil}
	if nr.Push != nil && strings.Trim(nr.Push.Old, "0") != "" {
		req.Since = nr.Push.Old
	}
	q, note, err := h.i.Inspect(ctx, req, nr.Pipeline)
	if err != nil {
		m.log.Error("the Inspection could not be added to a run", "project", nr.Project, "sha", nr.Trigger.SHA, "err", err)
		nr.Note = strings.TrimSpace(nr.Note + " The Inspection could not be added: " + err.Error())
		return
	}
	nr.Pipeline = q
	if note != "" {
		nr.Note = strings.TrimSpace(nr.Note + " " + note)
	}
}

// ProjectGit says where a project's commits are and which branch is its
// default, for the pages that show what Standing orders see in it.
func (m *Manager) ProjectGit(name string) (gitDir, defaultBranch string, ok bool) {
	p := m.c().projects[name]
	if p == nil {
		return "", "", false
	}
	return p.dir, p.DefaultBranch, true
}
