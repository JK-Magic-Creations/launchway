package argocd

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/deploy/gitops"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Ordered deploys of an application that is itself managed by a parent
// application (app of apps).
//
// The problem. A deployment may change two things an Argo CD application is
// built from: its own manifest (the chart pin, targetRevision, which a PARENT
// application applies) and the values it reads from the repository (the image
// tag and digest, which the application itself reads). Written as one commit,
// the application's automated sync sees the new values within seconds,
// while it still points at the old chart pin, because the parent has not yet
// applied the manifest: a pod with the NEW image rendered by the OLD chart.
// What the chart adds (a mounted key, environment, network policy) is
// missing, and the new image may refuse to start without it.
//
// What the runner does. When one deployment changes an Application manifest
// and other files, it writes TWO commits, in this order:
//
//  1. the Application manifest alone (trailer deployment "<id>-pin");
//  2. then it waits until the cluster shows the new pin in the application's
//     own spec (asking the parent application to refresh first, and to sync
//     when it does not sync by itself and this runner may sync it), and only
//     then
//  3. writes the values (trailer deployment "<id>": the commit a rollback finds).
//
// Between 1 and 3 the application may sync the new chart with the old image:
// the previous image under the new chart, which the chart's compatibility
// rule (a chart works with the image of the release before it) allows, and
// which is never the harmful pair. Where the cluster cannot be read, the
// runner cannot see the pin arrive; it then waits PairSettle between the two
// commits and says so ("fallback: two commits with a pause").
//
// After the sync it checks that every source of the application was synced at
// what the deployment intended and, if a sync ran with a mixed set anyway,
// says so plainly and asks for a corrective sync.

// pairPlan is what Apply found out about a deployment's files.
type pairPlan struct {
	manifest []engine.DeployChange // changes to the application's own manifest
	values   []engine.DeployChange // everything else
}

// classify splits a deployment's changes into those that edit the target
// application's own manifest and the rest. It returns a plain refusal for an
// ApplicationSet, whose generated applications it cannot sequence.
func classify(ctx context.Context, repo *gitops.Repo, d *engine.DeployJob) (pairPlan, error) {
	var pl pairPlan
	kinds := map[string]string{}
	for _, c := range d.Changes {
		k, ok := kinds[c.File]
		if !ok {
			data, err := repo.Read(ctx, c.File)
			if err != nil {
				return pl, err
			}
			k = manifestKind(data, d.Application)
			kinds[c.File] = k
		}
		switch k {
		case "application":
			pl.manifest = append(pl.manifest, c)
		case "applicationset":
			return pl, fmt.Errorf("%s is an ApplicationSet: the applications it generates are written from a template, and Launchway cannot tell when a template change has reached %s, so it cannot order this deployment's image and chart changes. "+
				"Keep the chart pin and the image values in separate deployments (pin the chart first), or write the chart pin into a plain Application manifest", c.File, d.Application)
		default:
			pl.values = append(pl.values, c)
		}
	}
	return pl, nil
}

// manifestKind says what a file is, for sequencing: "application" when it holds
// the Application named name, "applicationset" when it holds an ApplicationSet,
// else "".
func manifestKind(data []byte, name string) string {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			return ""
		}
		kind, _ := doc["kind"].(string)
		api, _ := doc["apiVersion"].(string)
		if !strings.HasPrefix(api, "argoproj.io/") {
			continue
		}
		switch kind {
		case "Application":
			if meta, _ := doc["metadata"].(map[string]any); meta != nil && meta["name"] == name {
				return "application"
			}
		case "ApplicationSet":
			return "applicationset"
		}
	}
}

// pinned is one source's intended revision, by position in the application's spec.
type pinned struct {
	Index    int
	Repo     string
	Chart    string
	Revision string
}

// intendedPins reads the application manifest as committed.
func intendedPins(data []byte, name string) []pinned {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			return nil
		}
		meta, _ := doc["metadata"].(map[string]any)
		if doc["kind"] != "Application" || meta == nil || meta["name"] != name {
			continue
		}
		spec, _ := doc["spec"].(map[string]any)
		var srcs []any
		if l, ok := spec["sources"].([]any); ok {
			srcs = l
		} else if s, ok := spec["source"].(map[string]any); ok {
			srcs = []any{s}
		}
		var out []pinned
		for i, s := range srcs {
			m, _ := s.(map[string]any)
			p := pinned{Index: i}
			p.Repo, _ = m["repoURL"].(string)
			p.Chart, _ = m["chart"].(string)
			p.Revision, _ = m["targetRevision"].(string)
			out = append(out, p)
		}
		return out
	}
}

// liveSources are the application's sources as the cluster has them.
func liveSources(app map[string]any) []map[string]any {
	spec := mapOf(app, "spec")
	var out []map[string]any
	if l := listOf(spec, "sources"); len(l) > 0 {
		for _, s := range l {
			m, _ := s.(map[string]any)
			out = append(out, m)
		}
		return out
	}
	if s := mapOf(spec, "source"); s != nil {
		out = append(out, s)
	}
	return out
}

// mismatch lists the intended pins the application's own spec does not show yet.
func mismatch(app map[string]any, want []pinned) []string {
	live := liveSources(app)
	var out []string
	for _, w := range want {
		if w.Revision == "" {
			continue
		}
		got := ""
		if w.Index < len(live) {
			got, _ = live[w.Index]["targetRevision"].(string)
		}
		if got != w.Revision {
			out = append(out, fmt.Sprintf("source %d (%s) is at %q, the deployment wants %q", w.Index, firstOf(w.Chart, w.Repo), got, w.Revision))
		}
	}
	return out
}

func firstOf(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// parentOf names the application that applies this application's manifest, from
// the controller's tracking annotation ("parent:argoproj.io/Application:argocd/child")
// or, with label tracking, the instance label; "" when it has none.
func parentOf(app map[string]any, self string) string {
	meta := mapOf(app, "metadata")
	if ann := mapOf(meta, "annotations"); ann != nil {
		if id, _ := ann["argocd.argoproj.io/tracking-id"].(string); id != "" {
			if p, _, ok := strings.Cut(id, ":"); ok && p != self {
				return p
			}
		}
	}
	if lbl := mapOf(meta, "labels"); lbl != nil {
		if p, _ := lbl["app.kubernetes.io/instance"].(string); p != "" && p != self {
			return p
		}
	}
	return ""
}

func (a *Agent) pairTimeout() time.Duration {
	if a.PairTimeout > 0 {
		return a.PairTimeout
	}
	return 3 * time.Minute
}

func (a *Agent) pairSettle() time.Duration {
	if a.PairSettle > 0 {
		return a.PairSettle
	}
	return 90 * time.Second
}

func (a *Agent) pairEvery() time.Duration {
	if a.PairEvery > 0 {
		return a.PairEvery
	}
	return 2 * time.Second
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// applySequenced writes the manifest commit, waits for the cluster to show the
// pin, then writes the values commit. It returns the facts of the values commit.
func (a *Agent) applySequenced(ctx context.Context, job *engine.Job, pl pairPlan, manifestCS, valuesCS gitops.ChangeSet, log func(string, ...any)) (res gitops.ApplyResult, pins []pinned, err error) {
	d := job.Deploy
	repo, unlock, err := a.repoWrite(ctx, job, log)
	if err != nil {
		return res, nil, err
	}
	log("This deployment changes the application's own manifest (%s) and its values (%s) of %s. Written together, the application would sync the new values while still on the old chart pin. Writing the manifest first, then waiting for the cluster to show it, then the values",
		files(pl.manifest), files(pl.values), d.Application)
	mres, err := repo.Apply(ctx, manifestCS)
	if err != nil {
		unlock()
		return res, nil, err
	}
	for _, c := range pl.manifest {
		if data, rerr := repo.ReadFile(c.File); rerr == nil && data != nil {
			if p := intendedPins(data, d.Application); len(p) > 0 {
				pins = p
				break
			}
		}
	}
	unlock()
	switch {
	case mres.Existing:
		log("Step 1 of 2: the manifest commit %s is already on %s (an earlier try made it)", short(mres.Commit), d.Branch)
	case mres.Changed:
		log("Step 1 of 2: committed the Application manifest %s as %s on %s", files(pl.manifest), short(mres.Commit), d.Branch)
	default:
		log("Step 1 of 2: the manifest already holds these pins; nothing to commit")
	}
	if err := a.waitForPin(ctx, job, pins, mres.Commit, log); err != nil {
		return res, pins, err
	}
	repo, unlock, err = a.repoWrite(ctx, job, log)
	if err != nil {
		return res, pins, err
	}
	res, err = repo.Apply(ctx, valuesCS)
	unlock()
	if err == nil {
		log("Step 2 of 2: the values (%s) are written; the application can now render the new image under the new chart", files(pl.values))
	}
	return res, pins, err
}

func files(cs []engine.DeployChange) string {
	seen := map[string]bool{}
	var out []string
	for _, c := range cs {
		if !seen[c.File] {
			seen[c.File] = true
			out = append(out, c.File)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// waitForPin waits until the application's own spec shows the intended pins.
func (a *Agent) waitForPin(ctx context.Context, job *engine.Job, pins []pinned, commit string, log func(string, ...any)) error {
	d := job.Deploy
	cl, t, err := a.cluster(d.Target)
	var app map[string]any
	if err == nil {
		app, err = cl.Application(ctx, d.Application)
	}
	if err != nil || app == nil || len(pins) == 0 {
		why := "the cluster cannot be read"
		if err == nil && app == nil {
			why = "the application was not found"
		} else if err == nil {
			why = "the manifest names no revision to wait for"
		} else {
			why = "the cluster cannot be read (" + err.Error() + ")"
		}
		log("Mode: FALLBACK, two commits with a pause: %s, so the arrival of the new pin cannot be seen. Waiting %s for the parent application to apply it before writing the values", why, a.pairSettle())
		return sleepCtx(ctx, a.pairSettle())
	}
	parent := parentOf(app, d.Application)
	if parent != "" {
		log("Mode: CONTROLLER-ORDERED. %s is managed by the parent application %s; waiting until the cluster shows the new chart pin in %s's own spec", d.Application, parent, d.Application)
		if rerr := cl.RefreshHard(ctx, parent); rerr != nil {
			log("Could not ask Argo CD to refresh the parent %s (%v); waiting for it to notice the commit", parent, rerr)
		} else {
			log("Asked Argo CD to refresh the parent application %s (hard) so it applies the manifest at %s", parent, short(commit))
		}
	} else {
		log("Mode: CONTROLLER-ORDERED. No parent application was found for %s (no tracking annotation); waiting until the cluster shows the new chart pin in its spec", d.Application)
	}
	deadline := time.Now().Add(a.pairTimeout())
	syncedParent := false
	for {
		app, err = cl.Application(ctx, d.Application)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		if app == nil {
			return fmt.Errorf("application %s disappeared while waiting for its new chart pin", d.Application)
		}
		mm := mismatch(app, pins)
		if len(mm) == 0 {
			log("The cluster shows the new pin in %s's spec: %s", d.Application, pinWords(pins))
			return nil
		}
		if !syncedParent && parent != "" && time.Until(deadline) < a.pairTimeout()/2 {
			syncedParent = true
			a.syncParent(ctx, cl, t, job, parent, log)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the parent application %s has not applied the new manifest of %s within %s (%s). The values were NOT written, so %s keeps rendering the old image with the old chart and nothing was mixed. "+
				"The manifest commit %s is on %s: sync %s (or fix why it does not), then run the deploy step again; it picks up from here",
				firstOf(parent, "that manages it"), d.Application, a.pairTimeout(), strings.Join(mm, "; "), d.Application, short(commit), d.Branch, firstOf(parent, "the application that manages it"))
		}
		if err := sleepCtx(ctx, a.pairEvery()); err != nil {
			return err
		}
	}
}

func pinWords(pins []pinned) string {
	var out []string
	for _, p := range pins {
		if p.Revision != "" {
			out = append(out, fmt.Sprintf("%s@%s", firstOf(p.Chart, p.Repo), p.Revision))
		}
	}
	return strings.Join(out, ", ")
}

// syncParent asks Argo CD to sync the parent, only where the parent does not
// sync by itself and this runner is allowed to deploy it.
func (a *Agent) syncParent(ctx context.Context, cl *Cluster, t Target, job *engine.Job, parent string, log func(string, ...any)) {
	pa, err := cl.Application(ctx, parent)
	if err != nil || pa == nil {
		log("Could not read the parent application %s: it must sync itself", parent)
		return
	}
	if auto := mapOf(mapOf(mapOf(pa, "spec"), "syncPolicy"), "automated"); auto != nil {
		log("The parent %s syncs by itself; waiting", parent)
		return
	}
	if !t.AppAllowed(job.Project, parent) {
		log("The parent %s does not sync by itself, and this runner may not sync it for project %s (deploy.targets.%s.applications): sync it yourself", parent, job.Project, job.Deploy.Target)
		return
	}
	rv, _ := mapOf(pa, "metadata")["resourceVersion"].(string)
	if err := cl.Sync(ctx, parent, rv, map[string]any{"prune": false}, "Launchway deployment "+job.Deploy.Deployment+": apply the manifest first"); err != nil {
		log("Could not ask Argo CD to sync the parent %s (%v)", parent, err)
		return
	}
	log("Asked Argo CD to sync the parent application %s (it does not sync by itself)", parent)
}

// verifyPair checks, after the sync, that every source was rendered at what the
// deployment intended. A sync that ran with a mixed set is reported plainly and
// followed by a corrective sync.
func (a *Agent) verifyPair(ctx context.Context, job *engine.Job, pins []pinned, commit string, log func(string, ...any)) (string, error) {
	d := job.Deploy
	cl, _, err := a.cluster(d.Target)
	if err != nil {
		return "", nil
	}
	app, err := cl.Application(ctx, d.Application)
	if err != nil || app == nil {
		return "", nil
	}
	if mm := mismatch(app, pins); len(mm) > 0 {
		return "", fmt.Errorf("the application's spec no longer shows the new chart pin (%s): something changed it back. Not syncing a mixed set", strings.Join(mm, "; "))
	}
	revs, _ := mapOf(mapOf(app, "status"), "sync")["revisions"].([]any)
	var wrong []string
	revisions := make([]any, len(liveSources(app)))
	for i := range revisions {
		revisions[i] = ""
	}
	for _, p := range pins {
		// Only a pinned source can be compared with what was synced: a chart
		// version, or a commit. A branch name resolves to a commit by itself.
		if p.Revision == "" || p.Index >= len(revs) || (p.Chart == "" && !isSHA(p.Revision)) {
			continue
		}
		if got, _ := revs[p.Index].(string); got != "" && got != p.Revision && !strings.HasPrefix(p.Revision, got) {
			wrong = append(wrong, fmt.Sprintf("source %d was synced at %s, the deployment wants %s", p.Index, got, p.Revision))
			revisions[p.Index] = p.Revision
		}
	}
	if len(wrong) == 0 {
		return "", nil
	}
	log("MIXED RENDER: a sync of %s ran with a set of sources that is not the one this deployment intended (%s). Asking for a corrective sync", d.Application, strings.Join(wrong, "; "))
	meta, _ := app["metadata"].(map[string]any)
	rv, _ := meta["resourceVersion"].(string)
	if err := cl.Sync(ctx, d.Application, rv, map[string]any{"prune": d.Sync.Prune, "revisions": revisions}, "Launchway deployment "+d.Deployment+": corrective sync after a mixed render"); err != nil && !errors.Is(err, errConflict) {
		return "", fmt.Errorf("a mixed render happened (%s) and the corrective sync could not be requested: %v", strings.Join(wrong, "; "), err)
	}
	return "; a mixed render was found (" + strings.Join(wrong, "; ") + ") and a corrective sync was requested", nil
}

func isSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
