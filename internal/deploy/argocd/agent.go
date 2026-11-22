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
	// SeeEvery is how often an action that waits for Argo CD to see a new commit looks again; default 2s.
	SeeEvery time.Duration
	// WindowEvery is how often an action held by a sync window looks again;
	// default 30s.
	WindowEvery time.Duration
	// Pulls opens and follows pull requests, for deployments that write
	// back by pull request; PullEvery is how often it asks (default 5s).
	Pulls     Pulls
	PullEvery time.Duration
	// PairTimeout is how long a deployment that changes an application's own
	// manifest and its values waits for the cluster to show the new manifest
	// before it writes the values (default 8m); PairSettle is the pause it
	// makes instead when the cluster cannot be read (default 90s); PairEvery
	// is how often it looks (default 2s). See pairing.go.
	PairTimeout, PairSettle, PairEvery time.Duration
	// RefreshEvery is the pause before an application is asked a second time to re-read its sources while a
	// commit is not noticed (default 10s; it doubles up to three minutes). See refresh.go.
	RefreshEvery time.Duration
	refresh      refresher

	// KeepStaleOperations stops the runner from ending a sync operation that
	// was started with source revisions the application no longer points at
	// (the controller retries it with the same stale revisions, for as long
	// as it is left). Default: it ends it, for the application it is deploying
	// only, and says so. It needs the right to patch the application, which the
	// runner already has for refresh and sync.
	KeepStaleOperations bool
	// ComparisonGrace is how long a ComparisonError must last before the step
	// fails on it (default 20s).
	ComparisonGrace time.Duration
	// ChartSchema, when set, gives the values schema of a chart source at its
	// revision (nil: not known); default: from a configured Git repository.
	ChartSchema func(ctx context.Context, source map[string]any) ([]byte, error)

	mu       sync.Mutex
	seenCmp  map[string]time.Time
	cleared  map[string]string
	clusters map[string]*Cluster
	repos    map[string]chan struct{}
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
	c := a.clusters[name]
	a.mu.Unlock()
	if c != nil {
		return c, t, nil
	}
	// Connecting is done without the agent's lock: a target whose client takes
	// long to make must not hold up the others.
	connect := a.Connect
	if connect == nil {
		connect = func(_ string, t Target) (*Cluster, error) { return Connect(t) }
	}
	c, err := connect(name, t)
	if err != nil {
		return nil, t, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.clusters == nil {
		a.clusters = map[string]*Cluster{}
	}
	if have := a.clusters[name]; have != nil {
		return have, t, nil
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
	dir := a.repoDir(rc, name, branch)
	a.mu.Lock()
	if a.repos == nil {
		a.repos = map[string]chan struct{}{}
	}
	l := a.repos[dir]
	if l == nil {
		l = make(chan struct{}, 1)
		a.repos[dir] = l
	}
	a.mu.Unlock()
	// The clone is used by one step at a time. The wait ends with ctx: an
	// action or an observation that waits for a clone another one holds must
	// never wait for longer than its own deadline.
	select {
	case l <- struct{}{}:
	case <-ctx.Done():
		return nil, rc, nil, fmt.Errorf("waiting for the working copy of repository %s (another step holds it): %w", name, ctx.Err())
	}
	var once sync.Once
	unlock := func() { once.Do(func() { <-l }) }
	r := &gitops.Repo{URL: rc.URL, Branch: branch, Dir: dir, Auth: auth,
		Author: gitops.Author{Name: rc.Author.Name, Email: rc.Author.Email}, Log: log}
	return r, rc, unlock, nil
}

// repoDir is the directory of the working clone of a repository's branch.
func (a *Agent) repoDir(rc Repo, name, branch string) string {
	sum := sha256.Sum256([]byte(rc.URL + "\x00" + branch))
	return filepath.Join(a.DataDir, "deploy-repos", name+"-"+hex.EncodeToString(sum[:6]))
}

// repoWrite opens the clone for a deploy action that writes: with the step's
// own credential when the repository is hosted by Launchway (and the
// environment says how to write back), with the configuration's otherwise.
func (a *Agent) repoWrite(ctx context.Context, job *engine.Job, log func(string, ...any)) (*gitops.Repo, func(), error) {
	d := job.Deploy
	rc, ok := a.Config.Repos[d.Repo]
	switch {
	case !ok:
		return nil, nil, fmt.Errorf("repository %s is not in this deploy runner's configuration (deploy.repos)", d.Repo)
	case d.WriteBack != nil && d.WriteBack.Error != "":
		return nil, nil, errors.New(d.WriteBack.Error)
	case d.WriteBack != nil && !rc.Hosted:
		return nil, nil, fmt.Errorf("environment %s writes back to the hosted repository %s, but this runner's deploy.repos.%s is not marked hosted: true (its url must then be Launchway's address)", d.Environment, d.WriteBack.Repo, d.Repo)
	case d.WriteBack == nil && rc.Hosted:
		return nil, nil, fmt.Errorf("deploy.repos.%s is hosted by Launchway, but environment %s has no write_back: set it (mode direct or pull_request) so the step has a credential of its own", d.Repo, d.Environment)
	case d.WriteBack != nil && d.WriteBack.Token == "":
		return nil, nil, errors.New("the server gave this step no credential for the hosted repository")
	}
	repo, _, unlock, err := a.repo(ctx, job.Project, d.Repo, d.Branch, log)
	if err != nil {
		return nil, nil, err
	}
	if d.WriteBack != nil {
		repo.Auth = &gitops.Auth{Username: "launchway", Password: d.WriteBack.Token}
		repo.WorkBranch = d.WriteBack.WorkBranch
	}
	return repo, unlock, nil
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
	repo, unlock, err := a.repoWrite(ctx, job, log)
	if err != nil {
		return nil, err
	}
	pl, err := classify(ctx, repo, d)
	unlock()
	if err != nil {
		return nil, err
	}
	editFor := func(c engine.DeployChange) gitops.FileEdit { return fileEdit(d, c, log) }
	cs := gitops.ChangeSet{Deployment: d.Deployment, Message: d.Message, Flight: job.Project, Run: job.RunID, Environment: d.Environment}
	for _, c := range d.Changes {
		cs.Files = append(cs.Files, editFor(c))
	}
	var res gitops.ApplyResult
	var pins []pinned
	if len(pl.manifest) > 0 && len(pl.values) > 0 {
		mcs := gitops.ChangeSet{Flight: job.Project, Run: job.RunID, Environment: d.Environment, Deployment: d.Deployment + "-pin", Message: d.Message + "\n\n(first of two commits: the application's own manifest; the values follow once the cluster shows it)"}
		vcs := gitops.ChangeSet{Flight: job.Project, Run: job.RunID, Environment: d.Environment, Deployment: d.Deployment, Message: d.Message}
		for _, c := range pl.manifest {
			mcs.Files = append(mcs.Files, editFor(c))
		}
		for _, c := range pl.values {
			vcs.Files = append(vcs.Files, editFor(c))
		}
		res, pins, err = a.applySequenced(ctx, job, pl, mcs, vcs, log)
	} else {
		repo, unlock, err = a.repoWrite(ctx, job, log)
		if err != nil {
			return nil, err
		}
		res, err = repo.Apply(ctx, cs)
		unlock()
	}
	if err != nil {
		return nil, err
	}
	facts := &engine.DeployFacts{Repo: d.Repo, Branch: d.Branch, Commit: res.Commit, Changed: res.Changed, Existing: res.Existing, Files: res.Files}
	if d.WriteBack != nil && d.WriteBack.Mode == engine.WriteBackPullRequest && res.Changed && (res.OnWork || !res.Existing) {
		merged, err := a.landPull(ctx, job, log)
		if err != nil {
			return facts, err
		}
		facts.Commit, facts.Existing = merged, false
		res.Commit = merged
	}
	switch {
	case res.Existing:
		log("Commit %s of this deployment is already on %s (an earlier try made it)", short(res.Commit), d.Branch)
	case !res.Changed:
		log("Nothing to commit: %s already holds version %s at %s; it is synced and verified all the same", d.Branch, d.Version, short(res.Commit))
	}
	facts.Sync, err = a.requestSync(ctx, job, res.Commit, log)
	if err == nil && len(pins) > 0 {
		var note string
		if note, err = a.verifyPair(ctx, job, pins, res.Commit, log); err == nil {
			facts.Sync += note
		}
	}
	return facts, err
}

func fileEdit(d *engine.DeployJob, c engine.DeployChange, log func(string, ...any)) gitops.FileEdit {
	return gitops.FileEdit{Path: c.File, Edit: func(path string, data []byte) ([]byte, error) {
		if data == nil {
			return nil, fmt.Errorf("%s does not exist on branch %s", path, d.Branch)
		}
		if c.Image != "" && c.Digest != "" && c.Tag != "" {
			out, n, err := edit.ImageTagDigest(data, c.Image, c.Tag, c.Digest)
			if err == nil {
				log("%s: %d reference(s) to %s set to %s and pinned to %s", path, n, c.Image, c.Tag, c.Digest)
			}
			return out, err
		}
		if c.Image != "" && c.Digest != "" {
			out, n, err := edit.ImageDigest(data, c.Image, c.Digest)
			if err == nil {
				log("%s: %d reference(s) to %s pinned to %s", path, n, c.Image, c.Digest)
			}
			return out, err
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
	}}
}

// Revert reverts a deployment's commit (unless an earlier try did) and asks
// Argo CD to sync if it must. A deployment that changed nothing in Git has
// nothing to revert. The revert of a rollback run names several deployments
// (d.Reverts): they are reverted together, newest first, or not at all.
func (a *Agent) Revert(ctx context.Context, job *engine.Job, log func(string, ...any)) (*engine.DeployFacts, error) {
	d := job.Deploy
	if _, err := a.check(job.Project, d); err != nil {
		return nil, err
	}
	if len(d.Reverts) > 0 {
		return a.revertSeries(ctx, job, log)
	}
	abort := ""
	if d.AbortFirst && d.Rollout != nil {
		var err error
		if abort, err = a.abortFirst(ctx, d, log); err != nil {
			return nil, err
		}
	}
	repo, unlock, err := a.repoWrite(ctx, job, log)
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
	var valueFiles []string
	if commit != "" {
		valueFiles, _ = repo.CommitFiles(ctx, commit)
	}
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
	if d.WriteBack != nil && d.WriteBack.Mode == engine.WriteBackPullRequest && (res.OnWork || !res.Existing) {
		merged, err := a.landPull(ctx, job, log)
		if err != nil {
			return facts, err
		}
		res.Commit, res.Existing = merged, false
	}
	facts.Revert, facts.Reverted, facts.Existing = res.Commit, true, res.Existing
	if res.Existing {
		log("The revert %s is already on %s (an earlier try made it)", short(res.Commit), d.Branch)
	} else {
		log("Reverted %s with %s on %s", short(commit), short(res.Commit), d.Branch)
	}
	if d.WriteBack == nil || d.WriteBack.Mode != engine.WriteBackPullRequest {
		pinRevert, perr := a.revertPin(ctx, job, valueFiles, log)
		if perr != nil {
			return facts, perr
		}
		if pinRevert != "" {
			res.Commit, facts.Revert = pinRevert, pinRevert
		}
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
	// The controller caches what a branch points at, so a normal refresh can leave it on an older commit of the
	// repository for minutes. A deployment can be several commits a minute apart (the application's own manifest, then
	// its values), and a refresh asked after the first only would leave the last unseen: the application is asked (hard)
	// for the LAST commit, and asked again with a growing pause, up to a bound, while it still reports a revision that
	// does not hold that commit.
	ra := &reask{a: a, cl: cl, ok: t.asksRefresh(d.Sync), key: "sync:" + d.Deployment + ":" + commit, app: d.Application, commit: commit, log: log}
	defer a.refresh.forget(ra.key)
	ra.first(ctx)
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
		ra.again(ctx, seen)
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
		case <-time.After(a.seeEvery()):
		}
	}
}

// seeEvery is how often an action waiting for Argo CD to see a commit looks again.
func (a *Agent) seeEvery() time.Duration {
	if a.SeeEvery > 0 {
		return a.SeeEvery
	}
	return 2 * time.Second
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
	a.noticeCommit(ctx, cl, t, it, snap.App)
	a.afterReduce(ctx, cl, it, app, snap.App)
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
	if app != nil && snap.App.Found && snap.App.Health != "Healthy" && snap.App.Contains != nil && *snap.App.Contains {
		if ns, _ := mapOf(mapOf(app, "spec"), "destination")["namespace"].(string); ns != "" {
			snap.Evidence = cl.PodEvidence(ctx, ns, it.Application)
		}
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
			cl.addJobLogs(ctx, snap.Analysis)
		}
		snap.Rights = cl.Rights(ctx, ro.Namespace, ro.Name)
	}
	snap.Rights = a.withWriteRights(snap.Rights, it.Project, it.Repo)
	return snap
}

// afterReduce adds what the application's own status cannot say: the real
// leftovers, and what the controller is stuck on.
func (a *Agent) afterReduce(ctx context.Context, cl *Cluster, it engine.WatchItem, obj map[string]any, app *deploy.App) {
	if obj != nil && app.Found {
		a.realLeftovers(ctx, cl, it.Application, app)
	}
	if it.Kind != engine.WatchUnit {
		a.watchController(ctx, cl, it, obj, app)
	}
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

// revertSeries reverts the deployments of a rollback run, newest first, as
// one all-or-nothing change in Git, and asks Argo CD to sync. When one
// revert does not apply it changes nothing and says which deployment
// stands in the way and what to do.
func (a *Agent) revertSeries(ctx context.Context, job *engine.Job, log func(string, ...any)) (*engine.DeployFacts, error) {
	d := job.Deploy
	if d.WriteBack != nil && d.WriteBack.Mode == engine.WriteBackPullRequest {
		return nil, errors.New("rolling back several deployments at once is not done by pull request yet: use write_back mode direct for this environment, or revert the commits in a pull request by hand")
	}
	repo, unlock, err := a.repoWrite(ctx, job, log)
	if err != nil {
		return nil, err
	}
	var items []gitops.RevertItem
	for _, rv := range d.Reverts {
		commit, err := repo.Find(ctx, rv.Deployment)
		if err != nil {
			unlock()
			return nil, err
		}
		if commit == "" {
			commit = rv.Commit // older than the history searched: trust what the server recorded
		}
		if commit == "" {
			log("Deployment %s (%s) made no commit on %s, so there is nothing to revert for it", rv.Deployment, rv.Version, d.Branch)
			continue
		}
		items = append(items, gitops.RevertItem{Deployment: rv.Deployment, Commit: commit, Message: rv.Message})
	}
	facts := &engine.DeployFacts{Repo: d.Repo, Branch: d.Branch}
	if len(items) == 0 {
		unlock()
		log("None of the deployments after %s changed %s, so there is nothing to revert", d.Version, d.Branch)
		return facts, nil
	}
	log("Rolling back to %s: reverting %d deployment(s), newest first, in one change", d.Version, len(items))
	res, err := repo.RevertSeries(ctx, items)
	unlock()
	var conflict *gitops.ConflictError
	if errors.As(err, &conflict) {
		which := conflict.Deployment
		for _, rv := range d.Reverts {
			if rv.Deployment == conflict.Deployment {
				which = fmt.Sprintf("%s (run #%d, deployment %s)", rv.Version, rv.RunNumber, rv.Deployment)
			}
		}
		return facts, fmt.Errorf("rolling back to %s cannot be done cleanly: reverting %s does not apply to %s, because later commits changed the same lines of %s. "+
			"Nothing was changed in Git. To go ahead anyway, fix those files by hand in the GitOps repository, or deploy the version you want with a normal run",
			d.Version, which, d.Branch, strings.Join(conflict.Files, ", "))
	}
	if err != nil {
		return facts, err
	}
	facts.Revert, facts.Reverted, facts.Existing = res.Commits[len(res.Commits)-1], true, res.Existing
	facts.Commit = items[0].Commit
	if res.Existing {
		log("The reverts are already on %s (an earlier try made them)", d.Branch)
	} else {
		log("Reverted %d deployment(s) on %s; the last revert is %s", len(items), d.Branch, short(facts.Revert))
	}
	facts.Sync, err = a.requestSync(ctx, job, facts.Revert, log)
	return facts, err
}

// withWriteRights adds to a snapshot's rights whether this runner can write
// the repository a deployment's changes go to: a rollback is only offered
// when it can.
func (a *Agent) withWriteRights(r *deploy.Rights, project, repo string) *deploy.Rights {
	if r == nil {
		r = &deploy.Rights{}
	}
	ok, why := a.canWrite(project, repo)
	r.Write = &ok
	r.WriteWhy = why
	return r
}

// canWrite says whether the runner's configuration lets it commit to a
// repository for a project, and if not, what an administrator must change.
func (a *Agent) canWrite(project, name string) (bool, string) {
	rc, ok := a.Config.Repos[name]
	switch {
	case !ok:
		return false, fmt.Sprintf("this deploy runner has no GitOps repository %s in its configuration (deploy.repos), so it cannot revert changes there", name)
	case !rc.AllowsProject(project):
		return false, fmt.Sprintf("this deploy runner may not change repository %s for project %s (see deploy.repos.%s.paths in the runner's configuration)", name, project, name)
	case rc.Credentials == nil && (strings.HasPrefix(rc.URL, "https://") || strings.HasPrefix(rc.URL, "http://")):
		return false, fmt.Sprintf("this deploy runner has no credentials for repository %s (deploy.repos.%s.credentials), so it cannot push to it", name, name)
	}
	return true, ""
}

// watchController adds to the snapshot what a person needs to see why a
// deployment waits: a ComparisonError that lasts, and a sync operation stuck
// on stale source revisions, which it ends (and says so) unless told not to.
func (a *Agent) watchController(ctx context.Context, cl *Cluster, it engine.WatchItem, obj map[string]any, app *deploy.App) {
	if obj == nil || !app.Found {
		return
	}
	a.mu.Lock()
	if a.seenCmp == nil {
		a.seenCmp, a.cleared = map[string]time.Time{}, map[string]string{}
	}
	key := it.Key
	if app.ComparisonError == "" {
		delete(a.seenCmp, key)
	} else {
		first, ok := a.seenCmp[key]
		if !ok {
			a.seenCmp[key] = time.Now()
		} else {
			grace := a.ComparisonGrace
			if grace == 0 {
				grace = 20 * time.Second
			}
			app.ComparisonPersistent = time.Since(first) >= grace
		}
	}
	prev := a.cleared[key]
	a.mu.Unlock()
	if app.StaleOperation == "" {
		a.mu.Lock()
		delete(a.cleared, key)
		a.mu.Unlock()
		return
	}
	if prev != "" {
		app.Cleared = prev
		return
	}
	if a.KeepStaleOperations {
		app.Cleared = "not ended: this runner keeps stale operations (keep_stale_operations)"
		return
	}
	if err := cl.TerminateOperation(ctx, it.Application); err != nil {
		app.Cleared = "ending it failed: " + err.Error()
		return
	}
	app.Cleared = "the deploy runner ended that operation so that the application's own sync starts a fresh one at the intended revisions"
	a.mu.Lock()
	a.cleared[key] = app.Cleared
	a.mu.Unlock()
}

// revertPin finishes the rollback of a deployment that was written as two
// commits (see pairing.go): the values were reverted first (the previous image
// under the new chart, which the chart's compatibility rule allows), and now
// the application's own manifest is reverted in a commit of its own, and the
// runner waits until the cluster shows the old pin. It returns that commit, or
// "" when the deployment made no pin commit.
func (a *Agent) revertPin(ctx context.Context, job *engine.Job, valueFiles []string, log func(string, ...any)) (string, error) {
	d := job.Deploy
	repo, unlock, err := a.repoWrite(ctx, job, log)
	if err != nil {
		return "", err
	}
	pinCommit, err := repo.Find(ctx, d.Deployment+"-pin")
	if err != nil || pinCommit == "" {
		unlock()
		return "", err
	}
	files, err := repo.CommitFiles(ctx, pinCommit)
	if err != nil {
		unlock()
		return "", err
	}
	if held := a.preflightPin(ctx, repo, job, pinCommit, files, valueFiles, log); held != "" {
		unlock()
		return "", &HeldError{Msg: held}
	}
	res, err := repo.Revert(ctx, d.Deployment+"-pin", pinCommit, d.Message+"\n\n(the application's own manifest, after the values)")
	var pins []pinned
	if err == nil {
		for _, f := range files {
			if data, rerr := repo.ReadFile(f); rerr == nil && manifestKind(data, d.Application) == "application" {
				pins = intendedPins(data, d.Application)
			}
		}
	}
	unlock()
	if err != nil {
		return "", fmt.Errorf("reverting the application's own manifest (%s): %w", short(pinCommit), err)
	}
	log("Rollback, step 2 of 2: reverted the application's own manifest %s with %s; waiting for the cluster to show the old chart pin before the sync is judged", short(pinCommit), short(res.Commit))
	if err := a.waitForPin(ctx, job, pins, res.Commit, log); err != nil {
		return "", fmt.Errorf("%w. The pin revert %s is in Git but the cluster does not show it: Git and the cluster may disagree until it does. To put Git back to what is running, revert %s on %s",
			err, short(res.Commit), short(res.Commit), d.Branch)
	}
	if bad := a.watchRender(ctx, job); bad != "" {
		restored, rerr := a.restorePin(ctx, job, res.Commit, log)
		if rerr != nil {
			return "", fmt.Errorf("after the pin revert %s the controller cannot render the application (%s), and Launchway could not put the pin back (%v). Git now names the OLD chart pin but the cluster still runs the new version: revert %s on %s to make Git match, then clear the application's stuck operation (launchway deploy doctor)",
				short(res.Commit), bad, rerr, short(res.Commit), d.Branch)
		}
		return "", &HeldError{Msg: fmt.Sprintf("Rolling back could not be finished: after the pin revert the controller cannot render the application (%s). Launchway put the chart pin back (commit %s), so Git again names what is running: the new chart with the reverted values. The previous image runs under the new chart; the deployment needs a person: fix the values for the old chart and roll back again, or deploy forward", bad, short(restored))}
	}
	return res.Commit, nil
}

// reask is what requestSync asks of the controller about the commit it wrote: one normal refresh at once, then,
// while the application has not shown the commit, a hard one with the pause and bound of refresh.go, saying once
// what it did or why it could not.
type reask struct {
	a        *Agent
	cl       *Cluster
	ok       bool // sync.refresh allows asking and the credentials may
	key, app string
	commit   string
	said     bool
	log      func(string, ...any)
}

func (r *reask) first(ctx context.Context) {
	if !r.ok {
		return
	}
	if err := r.cl.Refresh(ctx, r.app); err != nil {
		r.log("Could not ask Argo CD to refresh %s (%v); it will notice the commit at its next poll", r.app, err)
	}
}

func (r *reask) again(ctx context.Context, seen bool) {
	if seen || !r.ok {
		return
	}
	n, denied, err := r.a.refresh.try(ctx, r.cl, r.key, r.app, r.a.refreshFirst())
	switch {
	case r.said:
	case denied || err != nil:
		r.said = true
		r.log("Could not ask Argo CD to refresh %s (%v); it will notice the commit at its next poll. To ask now: %s", r.app, errors.Join(err), howToRefresh(r.app))
	case n == 1:
		r.said = true
		r.log("Asked Argo CD to re-read the repository for %s; it is asked again, with a growing pause, until it shows commit %s", r.app, short(r.commit))
	}
}
