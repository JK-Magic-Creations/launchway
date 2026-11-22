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
	"context"
	"errors"
	"fmt"
	"github.com/jk-magic-creations/launchway/internal/metrics"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
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
	// Pipeline, if set, is the project's pipeline whenever its repository
	// has no pipeline file at a commit.
	Pipeline *ProjectPipeline
	// PullRequestSecrets lets the steps of pull-request runs read secrets.
	// Off by default: a pull request's code may come from anyone who can
	// open one.
	PullRequestSecrets bool
	// UntrustedAllowed turns the restricted mode of pull requests from
	// automation accounts off (pull_requests.untrusted: allow).
	UntrustedAllowed bool
	// AutomationAccounts name the accounts whose runs are automation (see
	// config.AutomationConfig).
	AutomationAccounts []string
	// SchedulesOff turns the project's schedules off: their firings are
	// recorded as skipped (see fireDue).
	SchedulesOff bool
	// Hook, if set, lets the project's Git host call a webhook that makes
	// the server poll the project at once.
	Hook *GitHook
	// Triggers says what callers of each of the pipeline's webhook
	// triggers must prove, by trigger name. A trigger without an entry
	// cannot be called.
	Triggers map[string]*TriggerAuth
	// PullRefs is where the project's Git host publishes the head of each
	// pull request as a ref. The forge provider of the project's connection
	// supplies it (Provider.ProposalRefs); the zero value means the shape
	// Launchway has always assumed, refs/pull/<n>/head.
	PullRefs forge.RefLayout
}

// Ref is a branch or tag and the commit it points at, or a pull request's
// head.
type Ref struct {
	Name        string `json:"name"` // main, v1.2.0, or a pull request's number
	Kind        string `json:"kind"` // branch, tag or pull_request
	SHA         string `json:"sha"`
	Subject     string `json:"subject"`
	Author      string `json:"author"`
	CommittedAt int64  `json:"committed_at"`
	// full is the ref as the repository names it, when it was read from one.
	full string
}

// FullRef returns refs/heads/..., refs/tags/..., or the pull request's ref as
// its host publishes it (refs/pull/<n>/head by default).
func (r Ref) FullRef() string {
	if r.full != "" {
		return r.full
	}
	switch r.Kind {
	case "tag":
		return "refs/tags/" + r.Name
	case kindPull:
		return forge.PullRefLayout.Prefix + "/" + r.Name + forge.PullRefLayout.Suffix
	}
	return "refs/heads/" + r.Name
}

// kindPull is the Kind of a pull request's head.
const kindPull = "pull_request"

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
	// Pipeline is the pipeline configured on the server for commits that
	// have no pipeline file; nil if there is none.
	Pipeline *ProjectPipeline `json:"pipeline,omitempty"`
}

type project struct {
	Project
	dir string
	// hosted says whether an address is one of this installation's own
	// repositories, which are then read from its storage (see hosted.go).
	hosted func() HostedLocal
	// outside gives the credential of a project kept on another Git host (outside.go).
	outside func() OutsideSource
	// The mirror outlives a change of the project's settings: the new
	// project shares it with the old one.
	*mirror
}

// mirror is a project's or library's local mirror.
type mirror struct {
	mu        sync.Mutex // one git operation at a time per mirror
	stateMu   sync.Mutex
	lastFetch time.Time // of this server's mirror
	lastErr   string
}

// catalog is the projects and libraries in force. It is never changed once
// made; Reconfigure makes a new one.
type catalog struct {
	order    []string
	projects map[string]*project
	// libraries are mirrors of the libraries pipelines may use, by name.
	libraries map[string]*project
	libOrder  []string
}

// Manager owns every project.
type Manager struct {
	eng     *engine.Engine
	log     *slog.Logger
	dataDir string
	cat     atomic.Pointer[catalog]
	// forge is the link to the forge's connection and repository rows (see
	// forgelink.go); empty until Start has recorded the projects.
	hosted forgeLink
	// pullNotes is what the outside host said about pull requests (outside.go), under forgeMu.
	pullNotes map[prKey]PullRequestInfo
	// hostedPtr reads this installation's own repositories from its storage (hosted.go).
	hostedPtr atomic.Pointer[hostedHolder]
	// outsidePtr reads repositories kept on outside Git hosts through their connections (outside.go).
	outsidePtr atomic.Pointer[outsideHolder]
	// trig is what schedules and webhooks keep (see triggers.go).
	trig     *triggerState
	trigOnce sync.Once
	// vars is where variables set at levels come from (see variables.go).
	vars atomic.Pointer[varHolder]
	// inspector adds the Inspection of Standing orders to runs (see standings.go).
	inspector atomic.Pointer[inspectorHolder]
	// forkGate says which pull requests come from forks (forkgate.go).
	forkGate atomic.Pointer[forkGateHolder]
	// RegistryFacts says whether this server's own registry can serve and which registry to use when it cannot,
	// for the starters' image steps (nil: not said). It is called when a run is made.
	RegistryFacts func() pipeline.RegistryFacts

	// Watching: started once by Start; Reconfigure starts and stops the
	// watchers of the projects it adds, changes and removes.
	watchMu  sync.Mutex
	watchCtx context.Context
	watching map[string]context.CancelFunc
	// forges are the projects' forge connections, and targets what they
	// said about pull-request heads (see forge.go).
	forgeMu    sync.Mutex
	forges     map[string]Forge
	targets    map[targetKey]string
	lastTarget map[targetPR]string
}

// c returns the projects and libraries in force.
func (m *Manager) c() *catalog { return m.cat.Load() }

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// NewManager checks the projects and prepares their mirror directories.
func NewManager(eng *engine.Engine, dataDir string, projects []Project, log *slog.Logger) (*Manager, error) {
	return NewManagerWithLibraries(eng, dataDir, projects, nil, log)
}

// NewManagerWithLibraries is NewManager for a server whose pipelines may use
// templates from the given libraries.
func NewManagerWithLibraries(eng *engine.Engine, dataDir string, projects []Project, libraries []Library, log *slog.Logger) (*Manager, error) {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{eng: eng, log: log, dataDir: dataDir, watching: map[string]context.CancelFunc{}}
	eng.SetDefaultBranch(m.defaultBranchOf) // which entries of the build caches are the default scope
	cat, err := m.build(projects, libraries, nil)
	if err != nil {
		return nil, err
	}
	m.cat.Store(cat)
	removeStaleSaves(dataDir, time.Hour)
	return m, nil
}

// CheckProjects checks projects and libraries as a server does before
// using them, without a server: the check a change to them must pass.
func CheckProjects(projects []Project, libraries []Library) error {
	m := &Manager{dataDir: "-"}
	_, err := m.build(projects, libraries, nil)
	return err
}

// build checks the projects and libraries and makes a catalog of them,
// sharing the mirrors of old (if given) for those that stay.
func (m *Manager) build(projects []Project, libraries []Library, old *catalog) (*catalog, error) {
	cat := &catalog{projects: map[string]*project{}}
	if err := m.addLibraries(cat, libraries, old); err != nil {
		return nil, err
	}
	for _, p := range projects {
		if !nameRe.MatchString(p.Name) {
			return nil, fmt.Errorf("project name %q: use lower-case letters, digits and hyphens", p.Name)
		}
		if cat.projects[p.Name] != nil {
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
		if p.Pipeline != nil {
			if err := checkProjectPipeline(cat, &p); err != nil {
				return nil, err
			}
		}
		np := &project{Project: p, dir: filepath.Join(m.dataDir, "repos", p.Name+".git"), mirror: &mirror{}, hosted: m.hostedLocal, outside: m.outsideSource}
		if old != nil {
			if was := old.projects[p.Name]; was != nil && was.URL == p.URL {
				np.mirror = was.mirror
			}
		}
		cat.projects[p.Name] = np
		cat.order = append(cat.order, p.Name)
	}
	return cat, nil
}

// Reconfigure replaces the projects and libraries while the server runs:
// projects added are watched at once, removed ones are no longer polled,
// and changed ones carry on with their new settings (and their mirror,
// unless their repository changed). Runs in progress are not touched.
func (m *Manager) Reconfigure(projects []Project, libraries []Library) error {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	old := m.c()
	cat, err := m.build(projects, libraries, old)
	if err != nil {
		return err
	}
	t := m.ts()
	t.mu.Lock()
	for _, name := range cat.order {
		if t.wake[name] == nil {
			t.wake[name] = make(chan struct{}, 1)
		}
	}
	t.mu.Unlock()
	m.cat.Store(cat)
	if m.watchCtx == nil {
		return nil // not started yet: Start watches what is there then
	}
	for name, stop := range m.watching {
		if np := cat.projects[name]; np == nil || !reflect.DeepEqual(old.projects[name].Project, np.Project) || old.projects[name].mirror != np.mirror {
			stop()
			delete(m.watching, name)
		}
	}
	for _, name := range cat.order {
		if m.watching[name] == nil {
			m.startWatch(m.watchCtx, cat.projects[name])
		}
	}
	return nil
}

// Start polls every project until ctx ends.
func (m *Manager) Start(ctx context.Context) {
	m.logGitVersion(ctx)
	m.syncForge(ctx)
	m.watchMu.Lock()
	m.watchCtx = ctx
	cat := m.c()
	for _, name := range cat.order {
		m.startWatch(ctx, cat.projects[name])
	}
	m.watchMu.Unlock()
	go m.runSchedules(ctx)
	go m.runWindows(ctx)
	go m.runResultLoop(ctx)
}

// startWatch starts polling one project; the caller holds watchMu.
func (m *Manager) startWatch(parent context.Context, p *project) {
	ctx, cancel := context.WithCancel(parent)
	m.watching[p.Name] = cancel
	go m.watch(ctx, p)
}

func (m *Manager) watch(ctx context.Context, p *project) {
	for {
		if err := m.poll(ctx, p); err != nil && ctx.Err() == nil {
			m.log.Warn("polling a project", "project", p.Name, "err", err)
		} else if err == nil {
			metrics.Worker("source_poll")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.Poll):
		case <-m.wakeChan(p.Name):
		}
	}
}

// List returns every project's status.
func (m *Manager) List() []ProjectStatus {
	cat := m.c()
	out := make([]ProjectStatus, 0, len(cat.order))
	for _, name := range cat.order {
		out = append(out, m.status(cat.projects[name]))
	}
	return out
}

// Get returns one project's status.
func (m *Manager) Get(name string) (ProjectStatus, error) {
	p := m.c().projects[name]
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
		LastFetch: last, Error: redact.URLs(p.lastErr), Decisions: d, Push: m.Push(p.Name), Pipeline: p.Pipeline}
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
	p := m.c().projects[name]
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

// StartRun starts a run by hand on a branch or tag. Libraries, if given,
// overrides the versions of the libraries the pipeline uses: library name →
// tag, branch or commit, to try a change to a library before releasing it.
func (m *Manager) StartRun(ctx context.Context, name, ref, actor string, libraries map[string]string) (*engine.RunSummary, error) {
	p := m.c().projects[name]
	if p == nil {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, name)
	}
	// The pipeline's inputs take their defaults (see StartRunWith).
	return m.StartRunWith(ctx, StartRequest{Project: name, Ref: ref, Actor: actor, Libraries: libraries})
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
	if m.eng.Triggers() {
		m.refreshSchedules(ctx, p, refs)
	}
	seen, err := m.eng.SeenRefs(p.Name)
	if err != nil {
		return err
	}
	var pulls []Ref
	if m.eng.PullRequests() {
		if pulls, err = p.pullRefs(ctx); err != nil {
			return err
		}
	}
	// The first time a project is seen, record where everything is without
	// starting runs for history.
	if len(seen) == 0 {
		all := make(map[string]string, len(refs)+len(pulls)+1)
		for _, r := range refs {
			all[r.FullRef()] = r.SHA
		}
		if m.eng.PullRequests() {
			for _, r := range pulls {
				all[r.FullRef()] = r.SHA
			}
			all[engine.PullRequestsSeeded] = ""
		}
		return m.eng.SeedRefs(p.Name, all, seenAt)
	}
	if err := m.pollPushes(ctx, p, refs, seen, seenAt); err != nil {
		return err
	}
	if m.eng.PullRequests() {
		return m.pollPullRequests(ctx, p, refs, pulls, seen, seenAt)
	}
	return nil
}

// pollPushes starts runs for the branches and tags that moved.
func (m *Manager) pollPushes(ctx context.Context, p *project, refs []Ref, seen map[string]string, seenAt int64) error {
	for _, r := range refs {
		old, known := seen[r.FullRef()]
		if known && old == r.SHA {
			continue
		}
		change := engine.RefChange{Ref: r.FullRef(), Old: old, New: r.SHA, SeenAt: seenAt}
		d := Decision{Ref: r.FullRef(), SHA: r.SHA}
		// The trigger is read before templates are expanded, so that a push
		// the pipeline ignores fetches no library.
		pl, from, err := p.parsedAt(ctx, r.SHA)
		var libs []engine.LibrarySource
		var vals map[string]string
		var rec *varRecord
		if err == nil && pl.On.MatchPush(branchOf(r), tagOf(r)) {
			pl, libs, vals, rec, err = m.prepare(WithRepository(ctx, p.Name, r.SHA), p.Name, pl, nil, nil)
		}
		cs, pathSkip := p.pushPathRule(ctx, pl, err, r, old)
		switch {
		case errors.Is(err, ErrNoPipeline):
			d.Outcome, d.Detail = "ignored", "no .launchway/pipeline.yaml at this commit"
		case err != nil:
			what := "the pipeline file has problems"
			if from == fromServer {
				what = "the pipeline configured on the server (" + p.Pipeline.what() + ") has problems"
			}
			d.Outcome, d.Detail = "error", what+":\n"+redact.URLs(err.Error())
		case !pl.On.MatchPush(branchOf(r), tagOf(r)):
			d.Outcome, d.Detail = "ignored", strings.TrimSpace("the pipeline's push trigger does not match "+r.FullRef()+" "+p.note(from))
		case pathSkip != "":
			d.Outcome, d.Detail = PathRule, pipeline.PathSkipPrefix+pathSkip
		case skipMarker(r.Subject) != "":
			d.Outcome, d.Detail = "ignored", "the commit message says "+skipMarker(r.Subject)+": no run for this commit (start one by hand if you want it)"
		default:
			// Claims the change, creates the run and records the decision
			// in one transaction.
			t := trigger(r, "push", r.Author)
			t.Inputs, t.Changed = vals, cs
			run, err := m.createRun(ctx, engine.NewRun{Project: p.Name, Pipeline: pl, Trigger: t,
				Source: engine.Source{Kind: "git", URL: p.URL, Libraries: libs}, Push: &change, Note: p.note(from)}, rec)
			switch {
			case errors.Is(err, engine.ErrAlreadyClaimed):
				continue // another server handled it
			case err != nil:
				d.Outcome, d.Detail = "error", err.Error()
			default:
				m.log.Info("push started a run", "project", p.Name, "ref", r.FullRef(), "run", run.Number)
				m.noteForge(ctx, p, change, "started", fmt.Sprintf("run %d", run.Number))
				continue
			}
		}
		if _, err := m.eng.RecordPush(p.Name, change, d); err != nil {
			return err
		}
		m.noteForge(ctx, p, change, d.Outcome, d.Detail)
	}
	return nil
}

// pollPullRequests starts runs for pull requests that were opened or whose
// head moved, as Git hosts publish them: refs/pull/<n>/head.
//
// The rule:
//
//   - The first time a server that knows pull requests looks at a project
//     (the project is new, or it was watched by an older version), every
//     pull request's head is recorded and nothing runs. Git hosts keep
//     these refs after a pull request is closed or merged, so otherwise
//     every old pull request would run at once.
//   - After that, a pull request whose head is new or has moved runs if
//     the pipeline at the tip of the project's default branch asks for
//     pull requests (on: { pull_request: {} }), and so does the pipeline
//     at the pull request's head, which is what runs. The default branch
//     decides because it has been reviewed: a pull request cannot turn
//     pull-request runs on for itself.
//   - The heads of pull requests that do not run are still recorded, so
//     turning pull requests on later does not run the old ones either.
//
// Like pushes, each change is acted on by one server, through the
// database.
func (m *Manager) pollPullRequests(ctx context.Context, p *project, refs, pulls []Ref, seen map[string]string, seenAt int64) error {
	if _, ok := seen[engine.PullRequestsSeeded]; !ok {
		all := make(map[string]string, len(pulls)+1)
		for _, r := range pulls {
			all[r.FullRef()] = r.SHA
		}
		all[engine.PullRequestsSeeded] = ""
		return m.eng.SeedRefs(p.Name, all, seenAt)
	}
	gateRead := false
	var gateOn bool
	var gateWhy string
	var gateFilter *pipeline.PullRequestTrigger
	for _, r := range pulls {
		old, known := seen[r.FullRef()]
		if known && old == r.SHA {
			continue
		}
		if !gateRead {
			gateOn, gateWhy, gateFilter = m.pullRequestsOn(ctx, p, refs)
			gateRead = true
		}
		change := engine.RefChange{Ref: r.FullRef(), Old: old, New: r.SHA, SeenAt: seenAt}
		d := Decision{Ref: r.FullRef(), SHA: r.SHA}
		n, _ := strconv.Atoi(r.Name)
		// A host that keeps the project's code says who opened the pull request and
		// whether it is from a fork; without its answer a pull request does not run.
		meta, fromHost, merr := m.pullMeta(ctx, p.Name, n, r.SHA)
		if merr != nil {
			if err := m.eng.RecordDecision(p.Name, Decision{Ref: r.FullRef(), SHA: r.SHA, Outcome: "waiting",
				Detail: fmt.Sprintf("waiting: pull request #%d is not run until the host says whether it is from a fork (%s)", n, clipLine(redact.URLs(merr.Error())))}); err != nil {
				return err
			}
			continue
		}
		// One restriction for both ways a pull request can be known to come from a fork: a hosted repository's
		// fork gate and an outside host's own flag. Either way the run takes the default branch's pipeline.
		fork := m.forkVerdict(ctx, p, n, r.SHA)
		if fromHost && meta.Fork && !fork.Fork {
			fork = ForkVerdict{Fork: true, Reason: "pull request from a fork"}
		}
		pipelineAt, restr := p.trustedPipelineSHA(refs, r, fork.Fork)
		if fork.Fork {
			pipelineAt, restr = p.forkPipelineSHA(refs, r, fork.Reason)
		}
		pl, from, err := p.parsedAt(ctx, pipelineAt)
		asks := err == nil && pl.On.MatchPullRequest()
		if !gateOn {
			if !asks {
				// Nobody asked for pull requests: just record the head.
				if _, err := m.eng.MoveRef(p.Name, change); err != nil {
					return err
				}
				continue
			}
			d.Outcome, d.Detail = "ignored", "the pull request's pipeline asks for pull requests, but "+gateWhy+
				"; pull requests run once the default branch's pipeline has on: { pull_request: {} }"
		} else {
			var libs []engine.LibrarySource
			var vals map[string]string
			var rec *varRecord
			if asks {
				pl, libs, vals, rec, err = m.prepare(WithRepository(ctx, p.Name, r.SHA), p.Name, pl, nil, nil)
			}
			switch {
			case errors.Is(err, ErrNoPipeline):
				d.Outcome, d.Detail = "ignored", "no .launchway/pipeline.yaml at the pull request's head"
			case err != nil:
				what := "the pipeline file at the pull request's head has problems"
				if from == fromServer {
					what = fmt.Sprintf("the pipeline configured on the server (%s@%s, template %s) has problems",
						p.Pipeline.Library, p.Pipeline.Version, p.Pipeline.Template)
				}
				d.Outcome, d.Detail = "error", what+":\n"+redact.URLs(err.Error())
			case !asks:
				d.Outcome, d.Detail = "ignored", strings.TrimSpace("the pipeline at the pull request's head does not ask for pull requests "+p.note(from))
			case fork.Wait != "":
				// Not claimed: the next poll looks at this head again, and it runs once approved.
				if err := m.eng.RecordDecision(p.Name, Decision{Ref: r.FullRef(), SHA: r.SHA, Outcome: "waiting", Detail: fork.Wait}); err != nil {
					return err
				}
				continue
			default:
				// The default branch's pipeline says whether to filter by the
				// branch the pull request targets; only the project's forge
				// can say which that is, and without it nothing runs.
				into := m.pullRequestInto(ctx, p, n, r.SHA, gateFilter)
				if into.Wait != "" {
					// Not claimed: the next poll looks at this head again, for
					// as long as it is the newest.
					if err := m.eng.RecordDecision(p.Name, Decision{Ref: r.FullRef(), SHA: r.SHA, Outcome: "waiting", Detail: into.Wait}); err != nil {
						return err
					}
					continue
				}
				if into.Reject != "" {
					d.Outcome, d.Detail = "ignored", into.Reject
					break
				}
				cs := p.pullChange(ctx, into.Target, r.SHA)
				if why := pathRule(pl.On.PullRequestPaths(), cs); why != "" {
					d.Outcome, d.Detail = PathRule, pipeline.PathSkipPrefix+why
					break
				}
				t := trigger(r, pipeline.EventPullRequest, r.Author)
				t.Changed = cs
				t.PullRequest = n
				t.Fork = fork.Fork
				if fromHost {
					t.PullRequestTitle, t.PullRequestAuthor, t.PullRequestURL = meta.Title, meta.Author, meta.URL
					t.PullRequestDraft = meta.Draft
				}
				t.PullRequestInto = into.Target
				t.Inputs = vals
				note := p.note(from)
				if into.Target != "" {
					note = strings.TrimSpace("into " + into.Target + "; " + into.Note + " " + note)
					note = strings.ReplaceAll(note, "; ;", ";")
				}
				run, err := m.createRun(ctx, engine.NewRun{Project: p.Name, Pipeline: pl, Trigger: t,
					Source: engine.Source{Kind: "git", URL: p.URL, Libraries: libs}, Push: &change, Note: note,
					PullRequestSecrets: p.PullRequestSecrets, Restricted: restr}, rec)
				switch {
				case errors.Is(err, engine.ErrAlreadyClaimed):
					continue // another server handled it
				case err != nil:
					d.Outcome, d.Detail = "error", err.Error()
				default:
					m.log.Info("pull request started a run", "project", p.Name, "pull_request", n, "run", run.Number)
					m.noteForge(ctx, p, change, "started", fmt.Sprintf("run %d", run.Number))
					continue
				}
			}
		}
		if _, err := m.eng.RecordPush(p.Name, change, d); err != nil {
			return err
		}
		m.noteForge(ctx, p, change, d.Outcome, d.Detail)
	}
	return nil
}

// pullRequestsOn reports whether the pipeline at the tip of the project's
// default branch asks for pull requests, and if not, says why in words.
//
// It also returns the default branch's on.pull_request, whose into says which
// target branches the project's pull requests may have.
func (m *Manager) pullRequestsOn(ctx context.Context, p *project, refs []Ref) (bool, string, *pipeline.PullRequestTrigger) {
	for _, r := range refs {
		if r.Kind != "branch" || r.Name != p.DefaultBranch {
			continue
		}
		pl, _, err := p.parsedAt(ctx, r.SHA)
		switch {
		case errors.Is(err, ErrNoPipeline):
			return false, "the default branch, " + p.DefaultBranch + ", has no pipeline", nil
		case err != nil:
			return false, "the pipeline on the default branch, " + p.DefaultBranch + ", has problems", nil
		case !pl.On.MatchPullRequest():
			return false, "the pipeline on the default branch, " + p.DefaultBranch + ", does not ask for them", nil
		}
		return true, "", pl.On.PullRequest
	}
	return false, "the default branch, " + p.DefaultBranch + ", does not exist", nil
}

func trigger(r Ref, event, actor string) engine.Trigger {
	return engine.Trigger{Event: event, Ref: r.FullRef(), Branch: branchOf(r), Tag: tagOf(r), SHA: r.SHA,
		CommitTime: engine.CommitTimeAt(r.CommittedAt / 1000),
		Actor:      actor, Message: r.Subject, Author: r.Author}
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
	if done, err := p.fetchHosted(ctx); done {
		p.setErr(err)
		return err
	}
	if done, err := p.fetchOutside(ctx); done {
		p.setErr(err)
		return err
	}
	env, err := remoteEnv(ctx, p.URL)
	if err != nil {
		p.setErr(err)
		return err
	}
	if _, statErr := os.Stat(filepath.Join(p.dir, "HEAD")); statErr != nil {
		_ = os.RemoveAll(p.dir)
		if err = os.MkdirAll(filepath.Dir(p.dir), 0o755); err == nil {
			_, err = gitRun(ctx, "", env, "", "clone", "--mirror", "--quiet", p.URL, p.dir)
			if err != nil {
				// A clone that was cut off (the time limit, a network that did not answer) leaves a
				// directory that has a HEAD but no remote and no refs; the next fetch would take it for a
				// mirror and never repair it. Take it away so that the next try clones again.
				_ = os.RemoveAll(p.dir)
			}
		}
	} else {
		// The configured address may have changed since the mirror was made.
		if cur, _ := gitOut(ctx, p.dir, "remote", "get-url", "origin"); strings.TrimSpace(cur) != p.URL {
			_, err = gitOut(ctx, p.dir, "remote", "set-url", "origin", p.URL)
		}
		if err == nil {
			_, err = gitRun(ctx, p.dir, env, "", "remote", "update", "--prune")
		}
	}
	if err != nil {
		err = fmt.Errorf("fetching %s: %w", p.URL, err)
	}
	p.setErr(err)
	return err
}

// refs lists the branches and tags: the default branch first, then other
// branches newest first, then tags.
func (p *project) refs(ctx context.Context) ([]Ref, error) {
	refs, err := p.listRefs(ctx, "refs/heads", "refs/tags")
	if err != nil {
		return nil, err
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

// layout is where the project's host publishes pull request heads.
func (p *project) layout() forge.RefLayout {
	if !p.PullRefs.IsZero() {
		return p.PullRefs
	}
	// A repository on an outside host publishes them the way that host does.
	if p.outside != nil {
		if o, ok := p.outside().(PullRefLayouter); ok {
			if l, ok := o.PullRefLayout(p.Name); ok && !l.IsZero() {
				return l
			}
		}
	}
	return forge.PullRefLayout
}

// pullRefs lists the heads of the repository's pull requests, as the host
// publishes them (refs/pull/<n>/head by default), oldest pull request first. The mirror
// fetches every ref, so they are there whenever the host has them.
func (p *project) pullRefs(ctx context.Context) ([]Ref, error) {
	refs, err := p.listRefs(ctx, p.layout().Prefix)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(refs, func(i, j int) bool {
		a, _ := strconv.Atoi(refs[i].Name)
		b, _ := strconv.Atoi(refs[j].Name)
		return a < b
	})
	return refs, nil
}

// listRefs lists the refs under the given prefixes, newest commit first.
func (p *project) listRefs(ctx context.Context, prefixes ...string) ([]Ref, error) {
	out, err := gitOut(ctx, p.dir, append([]string{"for-each-ref", "--sort=-committerdate",
		"--format=%(refname)%00%(objectname)%00%(*objectname)%00%(committerdate:unix)%00%(*committerdate:unix)%00%(authorname)%00%(*authorname)%00%(subject)%00%(*subject)"},
		prefixes...)...)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 9 {
			continue
		}
		r := Ref{SHA: f[1], Author: f[5], Subject: f[7], full: f[0]}
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
		case strings.HasPrefix(f[0], p.layout().Prefix+"/"):
			// Only heads: a host may also publish refs/pull/<n>/merge.
			n, ok := p.layout().Parse(f[0])
			if !ok {
				continue
			}
			r.Kind, r.Name = kindPull, strconv.Itoa(n)
		default:
			continue
		}
		refs = append(refs, r)
	}
	return refs, nil
}

// ReadPipeline returns the pipeline file at a commit, for showing in the UI.
func (m *Manager) ReadPipeline(ctx context.Context, name, sha string) (string, error) {
	p := m.c().projects[name]
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

// remoteEnv is the Git environment for talking to a remote: for a network
// address, only the protocols the egress policy allows, no redirects, and
// the checked address pinned (see internal/egress). A path on the server was
// allowed when it was configured (it can only come from the bootstrap file
// or from a server that allowed local paths), so it is not refused here.
func remoteEnv(ctx context.Context, addr string) ([]string, error) {
	t, err := egress.ParseRepo(addr)
	if err != nil {
		return nil, err
	}
	if t.Local {
		return nil, nil
	}
	env, err := egress.Default.CheckRepo(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", "not fetched", err)
	}
	return env, nil
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitRun(ctx, dir, nil, "", args...)
	if err != nil {
		return "", err
	}
	return out, nil
}

// gitRun runs git in dir with extra environment and stdin through the one
// hardened runner (internal/gitexec). It returns what git wrote to stdout,
// even when it fails; the error is what it wrote to stderr.
func gitRun(ctx context.Context, dir string, env []string, stdin string, args ...string) (string, error) {
	spec := gitexec.Spec{Dir: dir, Args: args, Env: env, Inherit: gitexec.Remote, Timeout: 2 * time.Minute}
	if stdin != "" {
		spec.Stdin = strings.NewReader(stdin)
	}
	res, err := gitexec.Run(ctx, spec)
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			msg := strings.TrimSpace(ee.Stderr)
			if msg == "" {
				msg = "exit status " + strconv.Itoa(ee.Code)
			}
			return res.Stdout, errors.New(redact.URLs(msg))
		}
		return res.Stdout, err
	}
	return res.Stdout, nil
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

// logGitVersion names the installed git once and says what it is too old for.
func (m *Manager) logGitVersion(ctx context.Context) {
	v, err := gitexec.Installed(ctx)
	if err != nil {
		m.log.Error("git is not usable; repositories cannot be watched", "error", err)
		return
	}
	m.log.Info("git found", "version", v.String())
	for _, missing := range v.Missing() {
		m.log.Warn("git is older than later features need", "version", v.String(), "detail", missing)
	}
}
