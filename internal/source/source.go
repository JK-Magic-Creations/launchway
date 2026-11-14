// Package source watches Git repositories, reads pipeline files from them,
// and starts runs when pushes match a pipeline's trigger.
//
// It works with any Git host by polling a local mirror. Every decision about
// a push is recorded, so "why did my push not start a run?" has an answer.
//
// When several servers share a database, each keeps its own mirror and polls
// every project. The engine lets exactly one of them act on each push.
package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// PipelinePaths are where a repository's pipeline file is looked for.
var PipelinePaths = []string{".launchway/pipeline.yaml", ".launchway/pipeline.yml"}

var ErrNoPipeline = errors.New("the commit has no .launchway/pipeline.yaml")

// Project is one repository Launchway watches.
type Project struct {
	Name          string
	URL           string // anything git clone accepts, including a local path
	DefaultBranch string
	Poll          time.Duration
	Push          *PushSettings // nil: pipelines cannot be saved to this repository
}

// Ref is a branch or tag and the commit it points at.
type Ref struct {
	Name        string `json:"name"` // main, v1.2.0
	Kind        string `json:"kind"` // branch or tag
	SHA         string `json:"sha"`
	Subject     string `json:"subject"`
	Author      string `json:"author"`
	CommittedAt int64  `json:"committed_at"`
}

// FullRef returns refs/heads/... or refs/tags/....
func (r Ref) FullRef() string {
	if r.Kind == "tag" {
		return "refs/tags/" + r.Name
	}
	return "refs/heads/" + r.Name
}

// Decision records what was done about one change on a ref. Decisions are
// kept in the database, so every server shows the same ones.
type Decision = engine.PushDecision

// ProjectStatus is a project as shown in the UI.
type ProjectStatus struct {
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	DefaultBranch string     `json:"default_branch"`
	PollSeconds   int        `json:"poll_seconds"`
	LastFetch     int64      `json:"last_fetch"`
	Error         string     `json:"error,omitempty"`
	Decisions     []Decision `json:"decisions"`
	// Push is how the editor may save to the repository; nil if it may not.
	Push *PushSettings `json:"push"`
}

type project struct {
	Project
	dir       string
	mu        sync.Mutex // one git operation at a time per mirror
	stateMu   sync.Mutex
	lastFetch time.Time // of this server's mirror
	lastErr   string
}

// Manager owns every project.
type Manager struct {
	eng      *engine.Engine
	log      *slog.Logger
	dataDir  string
	order    []string
	projects map[string]*project
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// NewManager checks the projects and prepares their mirror directories.
func NewManager(eng *engine.Engine, dataDir string, projects []Project, log *slog.Logger) (*Manager, error) {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{eng: eng, log: log, dataDir: dataDir, projects: map[string]*project{}}
	for _, p := range projects {
		if !nameRe.MatchString(p.Name) {
			return nil, fmt.Errorf("project name %q: use lower-case letters, digits and hyphens", p.Name)
		}
		if m.projects[p.Name] != nil {
			return nil, fmt.Errorf("project %q is listed twice", p.Name)
		}
		if p.URL == "" {
			return nil, fmt.Errorf("project %q has no repo", p.Name)
		}
		if p.DefaultBranch == "" {
			p.DefaultBranch = "main"
		}
		if p.Poll == 0 {
			p.Poll = 30 * time.Second
		}
		if p.Poll < 5*time.Second {
			p.Poll = 5 * time.Second
		}
		if p.Push != nil {
			push := *p.Push
			if err := push.check(); err != nil {
				return nil, fmt.Errorf("project %s: %w", p.Name, err)
			}
			p.Push = &push
		}
		m.projects[p.Name] = &project{Project: p, dir: filepath.Join(dataDir, "repos", p.Name+".git")}
		m.order = append(m.order, p.Name)
	}
	removeStaleSaves(dataDir, time.Hour)
	return m, nil
}

// Start polls every project until ctx ends.
func (m *Manager) Start(ctx context.Context) {
	for _, name := range m.order {
		go m.watch(ctx, m.projects[name])
	}
}

func (m *Manager) watch(ctx context.Context, p *project) {
	for {
		if err := m.poll(ctx, p); err != nil && ctx.Err() == nil {
			m.log.Warn("polling a project", "project", p.Name, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.Poll):
		}
	}
}

// List returns every project's status.
func (m *Manager) List() []ProjectStatus {
	out := make([]ProjectStatus, 0, len(m.order))
	for _, name := range m.order {
		out = append(out, m.status(m.projects[name]))
	}
	return out
}

// Get returns one project's status.
func (m *Manager) Get(name string) (ProjectStatus, error) {
	p := m.projects[name]
	if p == nil {
		return ProjectStatus{}, fmt.Errorf("%w: project %s", engine.ErrNotFound, name)
	}
	return m.status(p), nil
}

func (m *Manager) status(p *project) ProjectStatus {
	d, err := m.eng.PushDecisions(p.Name)
	if err != nil {
		m.log.Warn("reading push decisions", "project", p.Name, "err", err)
		d = []Decision{}
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	var last int64
	if !p.lastFetch.IsZero() {
		last = p.lastFetch.UnixMilli()
	}
	return ProjectStatus{Name: p.Name, URL: redact.URLs(p.URL), DefaultBranch: p.DefaultBranch, PollSeconds: int(p.Poll / time.Second),
		LastFetch: last, Error: redact.URLs(p.lastErr), Decisions: d, Push: m.Push(p.Name)}
}

func (p *project) setErr(err error) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if err != nil {
		p.lastErr = err.Error()
	} else {
		p.lastErr = ""
		p.lastFetch = time.Now()
	}
}

// Refs fetches and lists a project's branches and tags, newest first.
func (m *Manager) Refs(ctx context.Context, name string) ([]Ref, error) {
	p := m.projects[name]
	if p == nil {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.fetch(ctx, 3*time.Second); err != nil {
		return nil, err
	}
	return p.refs(ctx)
}

// StartRun starts a run by hand on a branch or tag.
func (m *Manager) StartRun(ctx context.Context, name, ref, actor string) (*engine.RunSummary, error) {
	p := m.projects[name]
	if p == nil {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.fetch(ctx, 3*time.Second); err != nil {
		return nil, err
	}
	refs, err := p.refs(ctx)
	if err != nil {
		return nil, err
	}
	if ref == "" {
		ref = p.DefaultBranch
	}
	for _, r := range refs {
		if r.Name == ref || r.FullRef() == ref {
			pl, err := p.pipelineAt(ctx, r.SHA)
			if err != nil {
				return nil, fmt.Errorf("%w: %s at %s: %v", engine.ErrConflict, r.Name, short(r.SHA), err)
			}
			return m.eng.CreateRun(engine.NewRun{Project: name, Pipeline: pl, Trigger: trigger(r, "manual", actor),
				Source: engine.Source{Kind: "git", URL: p.URL}})
		}
	}
	return nil, fmt.Errorf("%w: no branch or tag called %q", engine.ErrNotFound, ref)
}

// poll fetches the mirror and starts runs for pushes that match.
func (m *Manager) poll(ctx context.Context, p *project) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Taken before fetching: whatever the fetch shows is at least this new.
	seenAt := time.Now().UnixMilli()
	if err := p.fetch(ctx, 0); err != nil {
		return err
	}
	refs, err := p.refs(ctx)
	if err != nil {
		return err
	}
	seen, err := m.eng.SeenRefs(p.Name)
	if err != nil {
		return err
	}
	// The first time a project is seen, record where everything is without
	// starting runs for history.
	if len(seen) == 0 {
		all := make(map[string]string, len(refs))
		for _, r := range refs {
			all[r.FullRef()] = r.SHA
		}
		return m.eng.SeedRefs(p.Name, all, seenAt)
	}
	for _, r := range refs {
		old, known := seen[r.FullRef()]
		if known && old == r.SHA {
			continue
		}
		change := engine.RefChange{Ref: r.FullRef(), Old: old, New: r.SHA, SeenAt: seenAt}
		d := Decision{Ref: r.FullRef(), SHA: r.SHA}
		pl, err := p.pipelineAt(ctx, r.SHA)
		switch {
		case errors.Is(err, ErrNoPipeline):
			d.Outcome, d.Detail = "ignored", "no .launchway/pipeline.yaml at this commit"
		case err != nil:
			d.Outcome, d.Detail = "error", "the pipeline file has problems:\n"+err.Error()
		case !pl.On.MatchPush(branchOf(r), tagOf(r)):
			d.Outcome, d.Detail = "ignored", "the pipeline's push trigger does not match "+r.FullRef()
		default:
			// Claims the change, creates the run and records the decision
			// in one transaction.
			run, err := m.eng.CreateRun(engine.NewRun{Project: p.Name, Pipeline: pl, Trigger: trigger(r, "push", r.Author),
				Source: engine.Source{Kind: "git", URL: p.URL}, Push: &change})
			switch {
			case errors.Is(err, engine.ErrAlreadyClaimed):
				continue // another server handled it
			case err != nil:
				d.Outcome, d.Detail = "error", err.Error()
			default:
				m.log.Info("push started a run", "project", p.Name, "ref", r.FullRef(), "run", run.Number)
				continue
			}
		}
		if _, err := m.eng.RecordPush(p.Name, change, d); err != nil {
			return err
		}
	}
	return nil
}

func trigger(r Ref, event, actor string) engine.Trigger {
	return engine.Trigger{Event: event, Ref: r.FullRef(), Branch: branchOf(r), Tag: tagOf(r), SHA: r.SHA,
		Actor: actor, Message: r.Subject, Author: r.Author}
}

func branchOf(r Ref) string {
	if r.Kind == "branch" {
		return r.Name
	}
	return ""
}

func tagOf(r Ref) string {
	if r.Kind == "tag" {
		return r.Name
	}
	return ""
}

// fetch brings the mirror up to date unless it was fetched within fresh.
func (p *project) fetch(ctx context.Context, fresh time.Duration) error {
	p.stateMu.Lock()
	recent := fresh > 0 && time.Since(p.lastFetch) < fresh && p.lastErr == ""
	p.stateMu.Unlock()
	if recent {
		return nil
	}
	var err error
	if _, statErr := os.Stat(filepath.Join(p.dir, "HEAD")); statErr != nil {
		_ = os.RemoveAll(p.dir)
		if err = os.MkdirAll(filepath.Dir(p.dir), 0o755); err == nil {
			_, err = gitOut(ctx, "", "clone", "--mirror", "--quiet", p.URL, p.dir)
		}
	} else {
		// The configured address may have changed since the mirror was made.
		if cur, _ := gitOut(ctx, p.dir, "remote", "get-url", "origin"); strings.TrimSpace(cur) != p.URL {
			_, err = gitOut(ctx, p.dir, "remote", "set-url", "origin", p.URL)
		}
		if err == nil {
			_, err = gitOut(ctx, p.dir, "remote", "update", "--prune")
		}
	}
	if err != nil {
		err = fmt.Errorf("fetching %s: %w", p.URL, err)
	}
	p.setErr(err)
	return err
}

func (p *project) refs(ctx context.Context) ([]Ref, error) {
	out, err := gitOut(ctx, p.dir, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname)%00%(objectname)%00%(*objectname)%00%(committerdate:unix)%00%(*committerdate:unix)%00%(authorname)%00%(*authorname)%00%(subject)%00%(*subject)",
		"refs/heads", "refs/tags")
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 9 {
			continue
		}
		r := Ref{SHA: f[1], Author: f[5], Subject: f[7]}
		ts := f[3]
		if f[2] != "" { // an annotated tag: describe the commit it points at
			r.SHA, ts, r.Author, r.Subject = f[2], f[4], f[6], f[8]
		}
		r.CommittedAt, _ = strconv.ParseInt(ts, 10, 64)
		r.CommittedAt *= 1000
		switch {
		case strings.HasPrefix(f[0], "refs/heads/"):
			r.Kind, r.Name = "branch", strings.TrimPrefix(f[0], "refs/heads/")
		case strings.HasPrefix(f[0], "refs/tags/"):
			r.Kind, r.Name = "tag", strings.TrimPrefix(f[0], "refs/tags/")
		default:
			continue
		}
		refs = append(refs, r)
	}
	// The default branch first, then other branches newest first, then tags.
	rank := func(r Ref) int {
		switch {
		case r.Kind == "branch" && r.Name == p.DefaultBranch:
			return 0
		case r.Kind == "branch":
			return 1
		}
		return 2
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if a, b := rank(refs[i]), rank(refs[j]); a != b {
			return a < b
		}
		return refs[i].CommittedAt > refs[j].CommittedAt
	})
	return refs, nil
}

func (p *project) pipelineAt(ctx context.Context, sha string) (*pipeline.Pipeline, error) {
	for _, path := range PipelinePaths {
		data, err := gitOut(ctx, p.dir, "show", sha+":"+path)
		if err != nil {
			if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "exists on disk, but not in") {
				continue
			}
			return nil, err
		}
		return pipeline.Parse([]byte(data))
	}
	return nil, ErrNoPipeline
}

// ReadPipeline returns the pipeline file at a commit, for showing in the UI.
func (m *Manager) ReadPipeline(ctx context.Context, name, sha string) (string, error) {
	p := m.projects[name]
	if p == nil {
		return "", fmt.Errorf("%w: project %s", engine.ErrNotFound, name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, path := range PipelinePaths {
		if data, err := gitOut(ctx, p.dir, "show", sha+":"+path); err == nil {
			return data, nil
		}
	}
	return "", ErrNoPipeline
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitRun(ctx, dir, nil, "", args...)
	if err != nil {
		return "", err
	}
	return out, nil
}

// gitRun runs git in dir with extra environment and stdin. It returns what
// git wrote to stdout, even when it fails; the error is what it wrote to
// stderr.
func gitRun(ctx context.Context, dir string, env []string, stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"), env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), errors.New(msg)
	}
	return stdout.String(), nil
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
