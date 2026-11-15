package argocd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/deploy/edit"
	"github.com/jk-magic-creations/launchway/internal/deploy/gitops"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/secrets"
)

// Agent does a deploy runner's actions and observations.
type Agent struct {
	Config  Config
	DataDir string // clones of the repositories are kept under it
	Secrets *secrets.Resolver
	// Connect makes a target's cluster client; default Connect. Tests give
	// fakes.
	Connect func(name string, t Target) (*Cluster, error)
	// SeeTimeout is how long an action waits for Argo CD to see a new
	// commit before asking it to sync anyway; default 60s. RunningTimeout is
	// how long it waits for a sync already running to end; default 2m.
	SeeTimeout, RunningTimeout time.Duration
	// WindowEvery is how often an action held by a sync window looks again;
	// default 30s.
	WindowEvery time.Duration

	mu       sync.Mutex
	clusters map[string]*Cluster
	repos    map[string]*sync.Mutex
}

// ErrUnavailable wraps failures to reach something the action needs (a
// secret store, a cluster): the action may be tried again.
var ErrUnavailable = errors.New("unavailable")

func (a *Agent) cluster(name string) (*Cluster, Target, error) {
	t, ok := a.Config.Targets[name]
	if !ok {
		return nil, t, fmt.Errorf("this runner does not serve target %s", name)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.clusters == nil {
		a.clusters = map[string]*Cluster{}
	}
	if c := a.clusters[name]; c != nil {
		return c, t, nil
	}
	connect := a.Connect
	if connect == nil {
		connect = func(_ string, t Target) (*Cluster, error) { return Connect(t) }
	}
	c, err := connect(name, t)
	if err != nil {
		return nil, t, err
	}
	a.clusters[name] = c
	return c, t, nil
}

// repo opens the working clone of a repository for a branch; unlock must be
// called when done with it.
func (a *Agent) repo(ctx context.Context, project, name, branch string, log func(string, ...any)) (*gitops.Repo, Repo, func(), error) {
	rc, ok := a.Config.Repos[name]
	if !ok {
		return nil, rc, nil, fmt.Errorf("repository %s is not in this deploy runner's configuration (deploy.repos)", name)
	}
	var auth *gitops.Auth
	if rc.Credentials != nil {
		v, err := a.Secrets.Lookup(ctx, project, *rc.Credentials)
		if err != nil {
			if secrets.IsUnavailable(err) {
				return nil, rc, nil, fmt.Errorf("%w: the credentials of repository %s (%s): %v", ErrUnavailable, name, rc.Credentials, err)
			}
			return nil, rc, nil, fmt.Errorf("the credentials of repository %s (%s): %v", name, rc.Credentials, err)
		}
		auth = &gitops.Auth{Username: rc.Username, Password: v}
	}
	sum := sha256.Sum256([]byte(rc.URL + "\x00" + branch))
	dir := filepath.Join(a.DataDir, "deploy-repos", name+"-"+hex.EncodeToString(sum[:6]))
	a.mu.Lock()
	if a.repos == nil {
		a.repos = map[string]*sync.Mutex{}
	}
	l := a.repos[dir]
	if l == nil {
		l = &sync.Mutex{}
		a.repos[dir] = l
	}
	a.mu.Unlock()
	l.Lock()
	r := &gitops.Repo{URL: rc.URL, Branch: branch, Dir: dir, Auth: auth,
		Author: gitops.Author{Name: rc.Author.Name, Email: rc.Author.Email}, Log: log}
	return r, rc, l.Unlock, nil
}

// check refuses what the runner's configuration does not allow.
func (a *Agent) check(project string, d *engine.DeployJob) (Target, error) {
	t, ok := a.Config.Targets[d.Target]
	if !ok {
		return t, fmt.Errorf("this runner does not serve target %s", d.Target)
	}
	if !t.AppAllowed(project, d.Application) {
		return t, fmt.Errorf("project %s may not deploy application %s on target %s (see deploy.targets.%s.applications in the runner's configuration)",
			project, d.Application, d.Target, d.Target)
	}
	rc, ok := a.Config.Repos[d.Repo]
	if !ok {
		return t, fmt.Errorf("repository %s is not in this deploy runner's configuration (deploy.repos)", d.Repo)
	}
	for _, c := range d.Changes {
		if !rc.FileAllowed(project, c.File) {
			return t, fmt.Errorf("project %s may not change %s in repository %s (see deploy.repos.%s.paths)", project, c.File, d.Repo, d.Repo)
		}
	}
	return t, nil
}

// Apply commits a deployment's changes (unless an earlier try did) and asks
// Argo CD to sync if it must.
func (a *Agent) Apply(ctx context.Context, job *engine.Job, log func(string, ...any)) (*engine.DeployFacts, error) {
	d := job.Deploy
	if _, err := a.check(job.Project, d); err != nil {
		return nil, err
	}
	repo, _, unlock, err := a.repo(ctx, job.Project, d.Repo, d.Branch, log)
	if err != nil {
		return nil, err
	}
	cs := gitops.ChangeSet{Deployment: d.Deployment, Message: d.Message}
	for _, c := range d.Changes {
		c := c
		cs.Files = append(cs.Files, gitops.FileEdit{Path: c.File, Edit: func(path string, data []byte) ([]byte, error) {
			if data == nil {
				return nil, fmt.Errorf("%s does not exist on branch %s", path, d.Branch)
			}
			if c.Image != "" {
				out, n, err := edit.Image(data, c.Image, c.Tag)
				if err == nil {
					log("%s: %d reference(s) to %s set to %s", path, n, c.Image, c.Tag)
				}
				return out, err
			}
			out, err := edit.Set(data, c.Set, c.Value)
			if err == nil {
				log("%s: %s set to %s", path, c.Set, c.Value)
			}
			return out, err
		}})
	}
	res, err := repo.Apply(ctx, cs)
	unlock()
	if err != nil {
		return nil, err
	}
	facts := &engine.DeployFacts{Repo: d.Repo, Branch: d.Branch, Commit: res.Commit, Changed: res.Changed, Existing: res.Existing, Files: res.Files}
	switch {
	case res.Existing:
		log("Commit %s of this deployment is already on %s (an earlier try made it)", short(res.Commit), d.Branch)
	case !res.Changed:
		log("Nothing to commit: %s already holds version %s at %s; it is synced and verified all the same", d.Branch, d.Version, short(res.Commit))
	}
	facts.Sync, err = a.requestSync(ctx, job, res.Commit, log)
	return facts, err
}

// Revert reverts a deployment's commit (unless an earlier try did) and asks
// Argo CD to sync if it must. A deployment that changed nothing in Git has
// nothing to revert.
func (a *Agent) Revert(ctx context.Context, job *engine.Job, log func(string, ...any)) (*engine.DeployFacts, error) {
	d := job.Deploy
	if _, err := a.check(job.Project, d); err != nil {
		return nil, err
	}
	abort := ""
	if d.AbortFirst && d.Rollout != nil {
		var err error
		if abort, err = a.abortFirst(ctx, d, log); err != nil {
			return nil, err
		}
	}
	repo, _, unlock, err := a.repo(ctx, job.Project, d.Repo, d.Branch, log)
	if err != nil {
		return nil, err
	}
	commit, err := repo.Find(ctx, d.Deployment)
	if err != nil {
		unlock()
		return nil, err
	}
	if commit == "" && d.Commit != "" {
		// Older than the history searched: trust what the server recorded.
		commit = d.Commit
	}
	facts := &engine.DeployFacts{Repo: d.Repo, Branch: d.Branch, Commit: commit, Abort: abort}
	if commit == "" {
		unlock()
		log("Deployment %s made no commit on %s, so there is nothing to revert", d.Deployment, d.Branch)
		return facts, nil
	}
	res, err := repo.Revert(ctx, d.Deployment, commit, d.Message)
	unlock()
	var conflict *gitops.ConflictError
	if errors.As(err, &conflict) {
		return facts, fmt.Errorf("the revert of %s does not apply to %s: later commits changed the same lines of %s. Git was left as it is; "+
			"the environment needs a person", short(commit), d.Branch, strings.Join(conflict.Files, ", "))
	}
	if err != nil {
		return facts, err
	}
	facts.Revert, facts.Reverted, facts.Existing = res.Commit, true, res.Existing
	if res.Existing {
		log("The revert %s is already on %s (an earlier try made it)", short(res.Commit), d.Branch)
	} else {
		log("Reverted %s with %s on %s", short(commit), short(res.Commit), d.Branch)
	}
	facts.Sync, err = a.requestSync(ctx, job, res.Commit, log)
	return facts, err
}

// requestSync asks Argo CD to look at the repository, and to sync if the
// step asks for it (always, or auto when the application does not sync by
// itself). It returns what it did, in words.
func (a *Agent) requestSync(ctx context.Context, job *engine.Job, commit string, log func(string, ...any)) (string, error) {
	d := job.Deploy
	cl, t, err := a.cluster(d.Target)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	app, err := cl.Application(ctx, d.Application)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if app == nil {
		return "", fmt.Errorf("there is no application %s in namespace %s", d.Application, cl.argoNS)
	}
	if t.Refresh() {
		if err := cl.Refresh(ctx, d.Application); err != nil {
			log("Could not ask Argo CD to refresh %s (%v); it will notice the commit at its next poll", d.Application, err)
		}
	}
	rc := a.Config.Repos[d.Repo]
	spec := deploy.Spec{Health: "application"}
	automated := deploy.ReduceApplication(app, spec, rc.ArgoCDURL, nil).Automated
	switch {
	case d.Sync.Request == "never":
		log("Not asking Argo CD to sync (sync.request: never)")
		return "not requested (sync.request: never)", nil
	case d.Sync.Request == "auto" && automated:
		log("Not asking Argo CD to sync: %s syncs by itself", d.Application)
		return "not needed: the application syncs by itself", nil
	}
	repo, _, unlock, err := a.repo(ctx, job.Project, d.Repo, d.Branch, func(string, ...any) {})
	if err != nil {
		return "", err
	}
	contains := func(rev string) (bool, error) { return repo.Contains(ctx, commit, rev) }
	defer unlock()

	// Wait, for a while, until Argo CD has seen the commit and any sync
	// already running has ended.
	see, running := a.SeeTimeout, a.RunningTimeout
	if see == 0 {
		see = time.Minute
	}
	if running == 0 {
		running = 2 * time.Minute
	}
	seeBy, runBy := time.Now().Add(see), time.Now().Add(running)
	waitedWindow := false
	for tries := 0; ; tries++ {
		app, err = cl.Application(ctx, d.Application)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		if app == nil {
			return "", fmt.Errorf("application %s disappeared", d.Application)
		}
		st := deploy.ReduceApplication(app, spec, rc.ArgoCDURL, contains)
		if st.SourceError != "" {
			return "", errors.New(st.SourceError)
		}
		seen := st.Contains != nil && *st.Contains
		busy := st.Operation != nil && (st.Operation.Phase == "Running" || st.Operation.Phase == "Terminating")
		if seen && st.Sync == "Synced" && len(d.Sync.Resources) == 0 && !busy {
			log("%s is already synced at %s", d.Application, short(st.Revision))
			return "not needed: already synced", nil
		}
		if (seen || time.Now().After(seeBy)) && (!busy || time.Now().After(runBy)) {
			// Never ask for a sync a window does not allow: Argo CD would hold
			// it, then end it as if done without applying anything once the
			// window closes.
			wv, err := cl.SyncWindow(ctx, app, true, time.Now())
			switch {
			case errors.Is(err, ErrNoProjectRight):
				return "", err
			case err != nil:
				return "", fmt.Errorf("%w: checking sync windows: %v", ErrUnavailable, err)
			case !wv.Allowed && d.Sync.Windows == "fail":
				return "", fmt.Errorf("Argo CD may not sync %s now: %s (the step says sync.windows: fail)", d.Application, wv.Reason)
			case !wv.Allowed:
				if !waitedWindow {
					log("Argo CD may not sync %s now: %s. Waiting until a window allows it (sync.windows: wait)", d.Application, wv.Reason)
					waitedWindow = true
				}
				select {
				case <-ctx.Done():
					return "", fmt.Errorf("no sync window allowed syncing %s before the step's deadline", d.Application)
				case <-time.After(a.windowEvery()):
				}
				continue
			}
			if busy {
				log("A sync of %s is still running; asking for this one after it", d.Application)
			}
			rev := commit
			if seen {
				rev = st.Revision
			}
			sync := map[string]any{"prune": d.Sync.Prune}
			if sources := listOf(mapOf(app, "spec"), "sources"); len(sources) > 0 {
				// One revision per source, in order: "" is a source's own
				// targetRevision. Argo CD ignores a shorter list (and has no
				// sourcePositions field to name one source).
				revs := make([]any, len(sources))
				for i := range revs {
					revs[i] = ""
				}
				revs[st.Source] = rev
				sync["revisions"] = revs
			} else {
				sync["revision"] = rev
			}
			if len(d.Sync.Resources) > 0 {
				var rs []any
				for _, r := range d.Sync.Resources {
					rs = append(rs, map[string]any{"group": r.Group, "kind": r.Kind, "name": r.Name, "namespace": r.Namespace})
				}
				sync["resources"] = rs
			}
			meta, _ := app["metadata"].(map[string]any)
			rv, _ := meta["resourceVersion"].(string)
			err = cl.Sync(ctx, d.Application, rv, sync, "Launchway deployment "+d.Deployment)
			switch {
			case err == nil:
				log("Asked Argo CD to sync %s at %s", d.Application, short(rev))
				return "requested at " + short(rev), nil
			case errors.Is(err, errConflict) && tries < 5:
				continue
			case errors.Is(err, ErrNoPatchRight):
				return "", fmt.Errorf("the step asks for a sync, but %v", err)
			default:
				return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Observe reads what a watched deployment's application, rollout and
// analysis runs are doing now, reduced to a snapshot. It never fails: what
// it could not read is in the snapshot's Error.
func (a *Agent) Observe(ctx context.Context, it engine.WatchItem) deploy.Snapshot {
	cl, t, err := a.cluster(it.Target)
	if err != nil {
		return deploy.Snapshot{Error: err.Error()}
	}
	if !t.AppAllowed(it.Project, it.Application) {
		return deploy.Snapshot{Error: fmt.Sprintf("this runner may not watch application %s for project %s", it.Application, it.Project)}
	}
	rc, ok := a.Config.Repos[it.Repo]
	if !ok {
		return deploy.Snapshot{Error: fmt.Sprintf("repository %s is not in this deploy runner's configuration", it.Repo)}
	}
	app, err := cl.Application(ctx, it.Application)
	if err != nil {
		return deploy.Snapshot{Error: err.Error()}
	}
	repo, _, unlock, err := a.repo(ctx, it.Project, it.Repo, it.Branch, func(string, ...any) {})
	if err != nil {
		return deploy.Snapshot{Error: err.Error()}
	}
	var containsErr error
	contains := func(rev string) (bool, error) {
		ok, err := repo.Contains(ctx, it.Spec.Commit, rev)
		if err != nil {
			containsErr = err
		}
		return ok, err
	}
	snap := deploy.Snapshot{App: deploy.ReduceApplication(app, it.Spec, rc.ArgoCDURL, contains)}
	unlock()
	// An application that syncs by itself shows nothing in its status while
	// a sync window holds it; say so from the windows themselves.
	if app != nil && snap.App.Automated {
		if wv, err := cl.SyncWindow(ctx, app, false, time.Now()); err != nil {
			snap.App.Notes = append(snap.App.Notes, "could not check sync windows: "+err.Error())
		} else if !wv.Allowed {
			snap.App.WindowBlocked = wv.Reason
		}
	}
	if containsErr != nil && snap.App.Contains == nil {
		snap.App.Notes = append(snap.App.Notes, "could not compare revisions: "+containsErr.Error())
	}
	if ro := it.Spec.Rollout; ro != nil {
		if !t.RolloutAllowed(ro.Namespace) {
			return deploy.Snapshot{Error: fmt.Sprintf("this runner may not watch rollouts in namespace %s (see deploy.targets.%s.rollout_namespaces)", ro.Namespace, it.Target)}
		}
		obj, err := cl.Rollout(ctx, ro.Namespace, ro.Name)
		if err != nil {
			return deploy.Snapshot{Error: err.Error()}
		}
		var wg *int64
		if obj != nil {
			if ref := mapOf(mapOf(obj, "spec"), "workloadRef"); ref != nil {
				if name, _ := ref["name"].(string); name != "" && ref["kind"] == "Deployment" {
					wg = cl.DeploymentGeneration(ctx, ro.Namespace, name)
				}
			}
		}
		snap.Rollout = deploy.ReduceRollout(obj, wg)
		if obj != nil {
			runs, err := cl.AnalysisRuns(ctx, ro.Namespace)
			if err != nil {
				return deploy.Snapshot{Error: err.Error()}
			}
			meta := mapOf(obj, "metadata")
			uid, _ := meta["uid"].(string)
			snap.Analysis = deploy.ReduceAnalysisRuns(runs, uid)
		}
	}
	return snap
}

// Watch makes changed fire when what an item watches changes.
func (a *Agent) Watch(ctx context.Context, it engine.WatchItem, changed chan<- struct{}) {
	cl, _, err := a.cluster(it.Target)
	if err != nil {
		return
	}
	ns, name := "", ""
	if ro := it.Spec.Rollout; ro != nil {
		ns, name = ro.Namespace, ro.Name
	}
	cl.WatchChanges(ctx, it.Application, ns, name, changed)
}

// windowEvery is how often an action waiting for a sync window looks again.
func (a *Agent) windowEvery() time.Duration {
	if a.WindowEvery > 0 {
		return a.WindowEvery
	}
	return 30 * time.Second
}

func mapOf(m map[string]any, k string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[k].(map[string]any)
	return v
}

func listOf(m map[string]any, k string) []any {
	if m == nil {
		return nil
	}
	v, _ := m[k].([]any)
	return v
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
