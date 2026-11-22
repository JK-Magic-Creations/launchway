package config

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/starters"
)

// ProjectPipelineConfig runs a library's template as a project's pipeline.
type ProjectPipelineConfig struct {
	Library  string `yaml:"library"`
	Version  string `yaml:"version"` // a tag, branch or full commit id
	Template string `yaml:"template"`
	// Detect runs the flight plan that fits what the repository holds (go.mod:
	// go-service, and so on) where it has no pipeline file; library, version
	// and template are then left out. With still gives values to its parameters.
	Detect bool              `yaml:"detect"`
	With   map[string]string `yaml:"with"`
	On     ProjectPipelineOn `yaml:"on"`
	// Supersede: a new run of a push or pull request cancels the older runs
	// of the same ref that are still going (see the pipeline key).
	Supersede bool `yaml:"supersede"`
	// Lane makes runs of the project take turns (see the pipeline key lane);
	// the same fields, checked by the pipeline parser.
	Lane *ProjectLaneConfig `yaml:"lane"`
	// Tools that read YAML 1.1 (Helm values among them) turn a bare `on`
	// key into the boolean true and write it back as "true". Take that
	// spelling for what it was meant to be rather than refusing to start.
	OnAsTrue *ProjectPipelineOn `yaml:"true"`
}

// ProjectDefaultPipelineConfig is what one project says about the default
// pipeline of its unit or organisation.
type ProjectDefaultPipelineConfig struct {
	// Enabled is true unless it says false: the project then runs no
	// default pipeline (a repository file still runs).
	Enabled *bool `yaml:"enabled"`
	// With replaces the default's values of these template parameters.
	With map[string]string `yaml:"with"`
}

// ProjectLaneConfig is a configured pipeline's lane: the pipeline key lane,
// with the same fields.
type ProjectLaneConfig struct {
	Name     string   `yaml:"name"`
	Per      []string `yaml:"per"`
	WhenBusy string   `yaml:"when_busy"`
	Queue    string   `yaml:"queue"`
}

// ProjectPipelineOn says which events start a configured pipeline.
type ProjectPipelineOn struct {
	Push *struct {
		Branches []string `yaml:"branches"`
		Tags     []string `yaml:"tags"`
	} `yaml:"push"`
	// PullRequest, written pull_request: {} (or with no value), starts runs
	// for pull requests. It takes no settings.
	PullRequest yaml.Node `yaml:"pull_request"`
}

// source is the lane as the source package keeps it, or nil for none.
func (l *ProjectLaneConfig) source() *source.ProjectLane {
	if l == nil {
		return nil
	}
	return &source.ProjectLane{Name: l.Name, Per: l.Per, WhenBusy: l.WhenBusy, Queue: l.Queue}
}

// sourcePipeline is the project's pipeline as the source package keeps it,
// or nil for none.
func (p ProjectConfig) sourcePipeline() (*source.ProjectPipeline, error) {
	pp := p.Pipeline
	if pp == nil {
		return nil, nil
	}
	version := pp.Version
	if version == "" && pp.Library == starters.Name {
		version = starters.Version // the starters that ship with Launchway have one version
	}
	sp := &source.ProjectPipeline{Library: pp.Library, Version: version, Template: pp.Template, With: pp.With,
		Supersede: pp.Supersede, Default: p.PipelineFrom, Detect: pp.Detect}
	sp.Lane = pp.Lane.source()
	if t := pp.On.Push; t != nil {
		sp.On.Push = &pipeline.PushTrigger{Branches: t.Branches, Tags: t.Tags}
	}
	prs, err := pp.On.pullRequests()
	if err != nil {
		return nil, fmt.Errorf("flight %s: %w", p.Name, err)
	}
	if prs {
		sp.On.PullRequest = &pipeline.PullRequestTrigger{}
	}
	return sp, nil
}
