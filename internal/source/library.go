package source

// Shared libraries. A library is a Git repository of templates and the
// scripts they use, listed in the server's configuration under a name.
// Pipelines name a library and a version (a tag, branch or commit); when a
// run is created, the version is resolved to a commit with a mirror of the
// library, kept like a project's, and the pipeline is expanded from the
// templates at that commit. The run records the commit, so it, its resumes,
// and every runner that works on it use exactly those files.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
	"github.com/jk-magic-creations/launchway/internal/starters"
)

// Library is a repository of templates that pipelines may use. Only the
// libraries in the server's configuration can be used: a pipeline names
// one, never an address, so a pipeline cannot make the server fetch from
// anywhere with its credentials.
type Library struct {
	Name string // what pipelines call it in repo:
	URL  string // anything git clone accepts; credentials as for projects
}

// ProjectPipeline is a project's pipeline when its repository has no
// .launchway/pipeline.yaml: a template of a library, so that one shared
// pipeline can apply to many repositories without a file in each. A file
// in the repository takes its place.
type ProjectPipeline struct {
	Library  string            `json:"library"`
	Version  string            `json:"version"`
	Template string            `json:"template"`
	With     map[string]string `json:"with,omitempty"`
	On       pipeline.Triggers `json:"on"`
	// Supersede is the pipeline's supersede: a new run of a push or pull
	// request cancels the older ones of the same ref still going.
	Supersede bool `json:"supersede,omitempty"`
	// Default says which level's default pipeline this is ("organisation" or
	// "unit <name>"), or is empty when the project configured it itself.
	Default string `json:"default,omitempty"`
	// Lane is the pipeline's lane (schema 24), as written in the
	// configuration; the pipeline parser checks it.
	Lane *ProjectLane `json:"lane,omitempty"`
	// Detect says the project has no flight plan named but runs the one that
	// fits what the repository holds (go.mod: go-service, and so on), found
	// at each commit that has no pipeline file. Library and Template are then
	// empty.
	Detect bool `json:"detect,omitempty"`

	// detected is what Detect found at the commit; set on the copy
	// configuredAt writes text from.
	detected *starters.Detection
}

// ProjectLane is the lane of a configured pipeline: the pipeline key lane.
type ProjectLane struct {
	Name     string   `json:"name,omitempty" yaml:"name,omitempty"`
	Per      []string `json:"per,omitempty" yaml:"per,omitempty,flow"`
	WhenBusy string   `json:"when_busy,omitempty" yaml:"when_busy,omitempty"`
	Queue    string   `json:"queue,omitempty" yaml:"queue,omitempty"`
}

// MaxOverrides bounds the library overrides one start may ask for.
const MaxOverrides = pipeline.MaxLibraries

// addLibraries checks the libraries and prepares their mirrors. It runs
// before the projects are checked, since their pipelines may name them.
func (m *Manager) addLibraries(cat *catalog, libs []Library, old *catalog) error {
	cat.libraries = map[string]*project{}
	for _, l := range libs {
		if !pipeline.ValidLibraryName(l.Name) {
			return fmt.Errorf("library name %q: use lower-case letters, digits and hyphens, starting with a letter (at most 40)", l.Name)
		}
		if l.Name == starters.Name {
			return fmt.Errorf("library name %q is reserved for the starters that ship with Launchway; use another name (a pipeline can still name its own library %s)", l.Name, starters.Name)
		}
		if cat.libraries[l.Name] != nil {
			return fmt.Errorf("library %q is listed twice", l.Name)
		}
		if l.URL == "" {
			return fmt.Errorf("library %q has no repo", l.Name)
		}
		lib := &project{Project: Project{Name: l.Name, URL: l.URL, DefaultBranch: "main"},
			dir: filepath.Join(m.dataDir, "libraries", l.Name+".git"), mirror: &mirror{}, hosted: m.hostedLocal}
		if old != nil {
			if was := old.libraries[l.Name]; was != nil && was.URL == l.URL {
				lib.mirror = was.mirror
			}
		}
		cat.libraries[l.Name] = lib
		cat.libOrder = append(cat.libOrder, l.Name)
	}
	return nil
}

// checkProjectPipeline checks a project's configured pipeline, so that a
// mistake in the configuration stops the server at once.
func checkProjectPipeline(cat *catalog, p *Project) error {
	pp := p.Pipeline
	switch {
	case pp.Detect:
		if pp.Library != "" || pp.Template != "" {
			return fmt.Errorf("project %s: pipeline.detect chooses the flight plan itself, so it takes no library or template", p.Name)
		}
		return nil
	case pp.Library == starters.Name:
		if _, ok := starters.Template(pp.Template); !ok {
			return fmt.Errorf("project %s: pipeline.template %q is not one of the starters (%s)", p.Name, pp.Template, strings.Join(starters.Templates(), ", "))
		}
	case cat.libraries[pp.Library] == nil:
		return fmt.Errorf("project %s: pipeline.library %q is not one of the libraries (%s)", p.Name, pp.Library, cat.libraryNames())
	case !pipeline.ValidVersion(pp.Version):
		return fmt.Errorf("project %s: pipeline.version %q is not a tag, branch or full commit id", p.Name, pp.Version)
	case !pipeline.ValidTemplateName(pp.Template):
		return fmt.Errorf("project %s: pipeline.template %q is not a template name", p.Name, pp.Template)
	}
	text, err := configuredText(p.Name, pp)
	if err == nil {
		_, err = pipeline.Parse([]byte(text))
	}
	if err != nil {
		return fmt.Errorf("project %s: pipeline: %w", p.Name, err)
	}
	return nil
}

func (cat *catalog) libraryNames() string {
	if len(cat.libOrder) == 0 {
		return "none are configured"
	}
	return strings.Join(cat.libOrder, ", ")
}

// configuredText writes a project's configured pipeline as the pipeline
// file it amounts to. Runs keep this text, and the editor starts from it.
func configuredText(project string, pp *ProjectPipeline) (string, error) {
	type push struct {
		Branches []string `yaml:"branches,omitempty,flow"`
		Tags     []string `yaml:"tags,omitempty,flow"`
	}
	type on struct {
		Push        *push     `yaml:"push,omitempty"`
		PullRequest *struct{} `yaml:"pull_request,omitempty"`
	}
	type lib struct {
		Repo    string `yaml:"repo"`
		Version string `yaml:"version"`
	}
	type use struct {
		Uses string            `yaml:"uses"`
		With map[string]string `yaml:"with,omitempty"`
	}
	doc := struct {
		Pipeline  string         `yaml:"pipeline"`
		On        *on            `yaml:"on,omitempty"`
		Supersede bool           `yaml:"supersede,omitempty"`
		Lane      *ProjectLane   `yaml:"lane,omitempty"`
		Libraries map[string]lib `yaml:"libraries"`
		Steps     map[string]use `yaml:"steps"`
	}{
		Pipeline:  project,
		Supersede: pp.Supersede,
		Lane:      pp.Lane,
		Libraries: map[string]lib{pp.Library: {Repo: pp.Library, Version: pp.Version}},
		Steps:     map[string]use{pp.Template: {Uses: pp.Library + "/" + pp.Template, With: pp.With}},
	}
	if t := pp.On.Push; t != nil {
		doc.On = &on{Push: &push{Branches: t.Branches, Tags: t.Tags}}
	}
	if pp.On.PullRequest != nil {
		if doc.On == nil {
			doc.On = &on{}
		}
		doc.On.PullRequest = &struct{}{}
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	head := "# The repository has no .launchway/pipeline.yaml, so this project runs the\n" +
		"# pipeline configured for it on the server. A file in the repository takes\n" +
		"# its place.\n"
	if d := pp.detected; d != nil {
		head = "# No pipeline file: using " + starters.Name + "/" + d.Flight + " because " + d.Because + ".\n" +
			"# Save this text as .launchway/pipeline.yaml to own it and change it; a file\n" +
			"# in the repository always takes this one's place.\n"
	} else if pp.Default != "" {
		head = "# The repository has no .launchway/pipeline.yaml, so this project runs the\n" +
			"# flight plan of its " + levelWord(pp.Default) + ", set on the server. A file in the\n" +
			"# repository takes its place.\n"
	}
	return head + string(out), nil
}

// pipelineSource says where a commit's pipeline came from.
type pipelineSource int

const (
	fromFile   pipelineSource = iota // the repository's .launchway/pipeline.yaml
	fromServer                       // the project's configured pipeline
)

// pipelineText returns the pipeline a commit runs: the repository's file,
// or else the project's configured pipeline. ErrNoPipeline means neither.
func (p *project) pipelineText(ctx context.Context, sha string) (string, pipelineSource, error) {
	_, text, err := p.fileAt(ctx, sha)
	if errors.Is(err, ErrNoPipeline) && p.Pipeline != nil {
		text, _, err = p.configuredAt(ctx, sha)
		return text, fromServer, err
	}
	return text, fromFile, err
}

// note says, for a push decision, where the pipeline came from, when the
// project has a configured pipeline that could have been used.
func (p *project) note(from pipelineSource) string {
	switch {
	case p.Pipeline == nil:
		return ""
	case from == fromServer && p.Pipeline.Detect:
		return "(the repository has no .launchway/pipeline.yaml, so the flight plan that fits what it holds ran: " + p.Pipeline.what() + ")"
	case from == fromServer && p.Pipeline.Default != "":
		return fmt.Sprintf("(the repository has no .launchway/pipeline.yaml, so the flight plan of the %s ran: %s)",
			levelWord(p.Pipeline.Default), p.Pipeline.what())
	case from == fromServer:
		return fmt.Sprintf("(the repository has no .launchway/pipeline.yaml, so the pipeline configured on the server ran: %s)", p.Pipeline.what())
	case p.Pipeline.Default != "":
		return "(the repository's own .launchway/pipeline.yaml ran, in place of the flight plan of the " + levelWord(p.Pipeline.Default) + ")"
	default:
		return "(the repository's own .launchway/pipeline.yaml ran, in place of the pipeline configured on the server)"
	}
}

// parsedAt reads and checks the pipeline a commit runs, without expanding
// its templates. Problems are pipeline.Problems.
func (p *project) parsedAt(ctx context.Context, sha string) (*pipeline.Pipeline, pipelineSource, error) {
	text, from, err := p.pipelineText(ctx, sha)
	if err != nil {
		return nil, from, err
	}
	pl, err := pipeline.Parse([]byte(text))
	if err != nil {
		return nil, from, err
	}
	if from == fromServer {
		pl.Origin = &pipeline.Origin{Kind: pipeline.OriginServer, Text: text}
		if p.Pipeline != nil {
			pl.Origin.Default = p.Pipeline.Default
			if p.Pipeline.Detect {
				if _, d, _ := p.configuredAt(ctx, sha); d != nil {
					pl.Origin.Detected = &pipeline.Detected{Flight: starters.Name + "/" + d.Flight, Because: d.Because}
				}
			}
		}
	}
	return pl, from, nil
}

// CheckOverrides checks library overrides as a caller gave them, before
// anything is fetched.
func CheckOverrides(overrides map[string]string) error {
	if len(overrides) > MaxOverrides {
		return fmt.Errorf("%w: at most %d library overrides", engine.ErrInvalid, MaxOverrides)
	}
	for name, v := range overrides {
		if !pipeline.ValidLibraryName(name) {
			return fmt.Errorf("%w: %q is not a library name", engine.ErrInvalid, clip(name))
		}
		if !pipeline.ValidVersion(v) {
			return fmt.Errorf("%w: the override for %s, %q, is not a tag, branch or full commit id", engine.ErrInvalid, name, clip(v))
		}
	}
	return nil
}

func clip(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// Expand resolves the libraries a pipeline names, with any overrides, to
// commits, and expands its templates from them. It returns the expanded
// pipeline and where a runner fetches each library it used. Problems with
// the pipeline, its libraries and its templates are pipeline.Problems.
//
// Each library is fetched first, so that a run gets what a tag or branch
// points at now, even if it moved a moment ago.
func (m *Manager) Expand(ctx context.Context, pl *pipeline.Pipeline, overrides map[string]string) (*pipeline.Pipeline, []engine.LibrarySource, error) {
	return m.expand(ctx, pl, overrides, 0)
}

// expand is Expand that reuses a fetch of a library made within fresh.
func (m *Manager) expand(ctx context.Context, pl *pipeline.Pipeline, overrides map[string]string, fresh time.Duration) (*pipeline.Pipeline, []engine.LibrarySource, error) {
	out, libs, _, err := m.expandVars(ctx, pl, overrides, fresh, nil)
	return out, libs, err
}

// expandVars is expand with the variables in effect for the project, which
// template parameters may take; it also returns the ones taken.
func (m *Manager) expandVars(ctx context.Context, pl *pipeline.Pipeline, overrides map[string]string, fresh time.Duration, vars pipeline.Variables) (*pipeline.Pipeline, []engine.LibrarySource, []pipeline.BoundVariable, error) {
	if err := CheckOverrides(overrides); err != nil {
		return nil, nil, nil, err
	}
	for name := range overrides {
		if pl.Library(name) == nil {
			return nil, nil, nil, fmt.Errorf("%w: the pipeline uses no library called %s, so it cannot be overridden", engine.ErrInvalid, name)
		}
	}
	if len(pl.Libraries) == 0 {
		return pl, nil, nil, nil
	}
	var problems pipeline.Problems
	mirrors := map[string]*project{}
	for _, l := range pl.Libraries {
		at := "libraries." + l.Name
		if isBuiltin(l) {
			if l.Override = overrides[l.Name]; l.Override != "" {
				problems = append(problems, builtinProblem(l.Line, at+".version", "the library launchway is part of Launchway and has one version, so it cannot be overridden; to use other templates, name your own library"))
				continue
			}
			l.Commit, l.RefKind = starters.Commit(), "builtin"
			continue
		}
		lib := m.c().libraries[l.Repo]
		if lib == nil {
			problems = append(problems, pipeline.Problem{Line: l.Line, Path: at + ".repo",
				Message: fmt.Sprintf("%q is not a library this server allows (%s); an administrator lists libraries in the server's configuration", l.Repo, m.c().libraryNames())})
			continue
		}
		l.Override = overrides[l.Name]
		sha, kind, err := lib.resolveVersion(ctx, l.Wanted(), fresh)
		switch {
		case errors.Is(err, engine.ErrNotFound):
			what := "version " + l.Version
			if l.Override != "" {
				what = "override " + l.Override
			}
			problems = append(problems, pipeline.Problem{Line: l.Line, Path: at + ".version",
				Message: fmt.Sprintf("the library %s (%s) has no tag, branch or commit called %q, the %s", l.Name, l.Repo, l.Wanted(), what)})
			continue
		case err != nil:
			problems = append(problems, pipeline.Problem{Line: l.Line, Path: at,
				Message: fmt.Sprintf("reading the library %s (%s): %s", l.Name, l.Repo, redact.URLs(err.Error()))})
			continue
		}
		l.Commit, l.RefKind = sha, kind
		mirrors[l.Name] = lib
	}
	if len(problems) > 0 {
		return nil, nil, nil, problems
	}
	out, used, err := pipeline.ExpandWith(pl, func(l *pipeline.Library, name string) ([]byte, error) {
		if isBuiltin(l) {
			return builtinTemplate(name)
		}
		return mirrors[l.Name].templateAt(ctx, l.Commit, name)
	}, vars)
	if err != nil {
		return nil, nil, nil, plainBuiltin(err)
	}
	var libs []engine.LibrarySource
	for _, l := range out.Libraries {
		if isBuiltin(l) {
			libs = append(libs, engine.LibrarySource{Name: l.Name, URL: starters.URL, Commit: l.Commit})
			continue
		}
		libs = append(libs, engine.LibrarySource{Name: l.Name, URL: mirrors[l.Name].URL, Commit: l.Commit})
	}
	return out, libs, used, nil
}

// Check reads pipeline text as a run would: it parses it and, if it uses
// templates, expands them from the libraries' current versions, with any
// overrides. Nothing is started. It is for checking text as it is typed, so
// a library fetched in the last few seconds is not fetched again.
func (m *Manager) Check(ctx context.Context, text string, overrides map[string]string) (*pipeline.Pipeline, error) {
	pl, err := pipeline.Parse([]byte(text))
	if err != nil {
		return nil, err
	}
	if pl, err = m.preview(pl); err != nil {
		return nil, err
	}
	pl, _, err = m.expand(ctx, pl, overrides, checkFresh)
	return pl, err
}

// checkFresh is how long Check reuses a fetch of a library.
const checkFresh = 3 * time.Second

// resolveVersion finds the commit a library's version points at, fetching
// the mirror unless it was fetched within fresh. A tag wins over a branch of
// the same name, and a branch over a commit ID; a commit is accepted only
// as a full ID that the repository has.
func (lib *project) resolveVersion(ctx context.Context, version string, fresh time.Duration) (sha, kind string, err error) {
	if !pipeline.ValidVersion(version) {
		return "", "", fmt.Errorf("%w: %q is not a version", engine.ErrNotFound, version)
	}
	lib.mu.Lock()
	defer lib.mu.Unlock()
	if err := lib.fetch(ctx, fresh); err != nil {
		return "", "", err
	}
	refs, err := lib.refs(ctx)
	if err != nil {
		return "", "", err
	}
	for _, want := range []string{"tag", "branch"} {
		for _, r := range refs {
			if r.Kind == want && r.Name == version {
				return r.SHA, want, nil
			}
		}
	}
	if pipeline.IsCommitID(version) {
		if _, err := gitOut(ctx, lib.dir, "cat-file", "-e", version+"^{commit}"); err == nil {
			return version, "commit", nil
		}
	}
	return "", "", fmt.Errorf("%w: no tag, branch or commit %q", engine.ErrNotFound, version)
}

// templateAt reads a template from the library's mirror at a commit.
func (lib *project) templateAt(ctx context.Context, sha, name string) ([]byte, error) {
	if !pipeline.IsCommitID(sha) || !pipeline.ValidTemplateName(name) {
		return nil, pipeline.ErrNoTemplate
	}
	lib.mu.Lock()
	defer lib.mu.Unlock()
	data, err := gitOut(ctx, lib.dir, "show", sha+":"+pipeline.TemplatePath(name))
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "exists on disk, but not in") {
			return nil, pipeline.ErrNoTemplate
		}
		return nil, errors.New(redact.URLs(err.Error()))
	}
	return []byte(data), nil
}

// LibraryStatus is a library as shown in the UI.
type LibraryStatus struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	LastFetch int64  `json:"last_fetch"`
	Error     string `json:"error,omitempty"`
	// Builtin marks the library that ships with Launchway: it needs no
	// registration and has one version, the server's own.
	Builtin bool `json:"builtin,omitempty"`
}

// LibraryTemplates is a library's templates at one version, for someone
// choosing one to use.
type LibraryTemplates struct {
	Library string `json:"library"`
	// Version is the version read: as asked, else the newest tag, else the
	// default branch.
	Version string `json:"version"`
	Commit  string `json:"commit"`
	// Versions are the library's tags, newest first, then its branches: at
	// most maxLibraryVersions.
	Versions  []string                `json:"versions"`
	Templates []pipeline.TemplateInfo `json:"templates"`
	// Truncated says the library has more templates than are listed.
	Truncated bool `json:"truncated,omitempty"`
}

const (
	maxLibraryVersions  = 20
	maxLibraryTemplates = 100
)

// LibraryTemplates lists the templates of a library at a version, with
// their parameters and outputs. An unknown library is ErrNotFound.
func (m *Manager) LibraryTemplates(ctx context.Context, name, version string) (*LibraryTemplates, error) {
	if name == starters.Name {
		return builtinTemplates(), nil
	}
	lib := m.c().libraries[name]
	if lib == nil {
		return nil, fmt.Errorf("%w: library %q (the libraries are: %s)", engine.ErrNotFound, name, m.c().libraryNames())
	}
	out := &LibraryTemplates{Library: name, Versions: []string{}, Templates: []pipeline.TemplateInfo{}}
	lib.mu.Lock()
	err := lib.fetch(ctx, checkFresh)
	var refs []Ref
	if err == nil {
		refs, err = lib.refs(ctx)
	}
	lib.mu.Unlock()
	if err != nil {
		return nil, errors.New(redact.URLs(err.Error()))
	}
	var tags, branches []string
	for _, r := range refs {
		if r.Kind == "tag" {
			tags = append(tags, r.Name)
		} else {
			branches = append(branches, r.Name)
		}
	}
	out.Versions = append(out.Versions, tags...)
	out.Versions = append(out.Versions, branches...)
	if len(out.Versions) > maxLibraryVersions {
		out.Versions = out.Versions[:maxLibraryVersions]
	}
	if version == "" {
		switch {
		case len(tags) > 0:
			version = tags[0]
		case len(branches) > 0:
			version = branches[0]
		default:
			return out, nil // an empty repository
		}
	}
	sha, _, err := lib.resolveVersion(ctx, version, checkFresh)
	if err != nil {
		return nil, err
	}
	out.Version, out.Commit = version, sha
	lib.mu.Lock()
	list, err := gitOut(ctx, lib.dir, "ls-tree", "--name-only", sha, strings.TrimSuffix(pipeline.TemplatePath("x"), "x.yaml"))
	lib.mu.Unlock()
	if err != nil {
		return nil, errors.New(redact.URLs(err.Error()))
	}
	for _, path := range strings.Split(strings.TrimSpace(list), "\n") {
		file := filepath.Base(path)
		tname, ok := strings.CutSuffix(file, ".yaml")
		if !ok || !pipeline.ValidTemplateName(tname) {
			continue
		}
		if len(out.Templates) == maxLibraryTemplates {
			out.Truncated = true
			break
		}
		data, err := lib.templateAt(ctx, sha, tname)
		if err != nil {
			continue
		}
		out.Templates = append(out.Templates, pipeline.DescribeTemplate(data, tname))
	}
	return out, nil
}

// Libraries lists the libraries pipelines may use.
func (m *Manager) Libraries() []LibraryStatus {
	cat := m.c()
	out := make([]LibraryStatus, 0, len(cat.libOrder)+1)
	out = append(out, LibraryStatus{Name: starters.Name, URL: starters.URL, Builtin: true})
	for _, name := range cat.libOrder {
		l := cat.libraries[name]
		l.stateMu.Lock()
		var last int64
		if !l.lastFetch.IsZero() {
			last = l.lastFetch.UnixMilli()
		}
		out = append(out, LibraryStatus{Name: name, URL: redact.URLs(l.URL), LastFetch: last, Error: redact.URLs(l.lastErr)})
		l.stateMu.Unlock()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// levelWord is a level as people read it in a note: the American spelling. A
// default pipeline is called a flight plan wherever people see it; the
// configuration key and the API keep default_pipeline.
func levelWord(level string) string {
	return strings.Replace(level, "organisation", "organization", 1)
}

// what names the configured pipeline for a message.
func (pp *ProjectPipeline) what() string {
	if pp.Detect {
		return "the flight plan found by looking at the repository"
	}
	return fmt.Sprintf("%s@%s, template %s", pp.Library, pp.Version, pp.Template)
}

// configuredAt is the text of the project's configured pipeline at a commit.
// With detect on, it looks at the commit's files and chooses a flight plan;
// ErrNoPipeline means nothing fits, as for a project with no pipeline.
func (p *project) configuredAt(ctx context.Context, sha string) (string, *starters.Detection, error) {
	pp := p.Pipeline
	if !pp.Detect {
		text, err := configuredText(p.Name, pp)
		return text, nil, err
	}
	d, err := p.detectAt(ctx, sha)
	if err != nil {
		return "", nil, err
	}
	if d == nil {
		return "", nil, ErrNoPipeline
	}
	c := *pp
	c.Detect, c.detected = false, d
	c.Library, c.Version, c.Template = starters.Name, starters.Version, d.Flight
	c.With = map[string]string{}
	for k, v := range d.With {
		c.With[k] = v
	}
	for k, v := range pp.With {
		c.With[k] = v
	}
	if len(c.With) == 0 {
		c.With = nil
	}
	if c.On.Push == nil && c.On.PullRequest == nil {
		branch := p.DefaultBranch
		if branch == "" {
			branch = "main"
		}
		c.On.Push = &pipeline.PushTrigger{Branches: []string{branch}}
		c.On.PullRequest = &pipeline.PullRequestTrigger{}
		c.Supersede = true
	}
	text, err := configuredText(p.Name, &c)
	return text, d, err
}

// detectAt looks at the files of a commit and chooses a flight plan.
func (p *project) detectAt(ctx context.Context, sha string) (*starters.Detection, error) {
	out, err := gitOut(ctx, p.dir, "ls-tree", "-r", "--name-only", "--full-name", sha)
	if err != nil {
		return nil, err
	}
	files := strings.Split(strings.TrimSpace(out), "\n")
	if len(files) > 50000 {
		files = files[:50000]
	}
	read := func(name string) string {
		data, err := gitOut(ctx, p.dir, "show", sha+":"+name)
		if err != nil || len(data) > 1<<20 {
			return ""
		}
		return data
	}
	return starters.Detect(files, read), nil
}
