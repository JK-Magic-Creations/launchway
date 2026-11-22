package argocd

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/deploy/gitops"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Rolling back the chart pin is only safe if the application can still render
// with the values Git holds when the pin is the old one. A chart's values
// schema (values.schema.json) refuses keys it does not know ("additional
// properties 'networkPolicy' not allowed"), so a values file written for the
// NEW chart cannot be rendered by the OLD one. Before the pin revert is
// committed the runner therefore:
//
//  1. looks at what it can see of the old pin: the chart source in a Git
//     repository the runner is configured for, at the old revision, and its
//     values.schema.json (a chart served by a chart repository, which the
//     runner cannot read, is "unverified" and the revert goes on as before);
//  2. compares the values the application will read with that schema;
//  3. if the old chart refuses them, does NOT revert the pin: it says exactly
//     why and leaves Git as it is, at the new pin (the values were reverted
//     first and the application renders that pair), for a person to decide.
//
// After the pin revert, the runner watches the application for a render
// failure (a ComparisonError) for a short grace period. If the controller
// cannot render, the runner puts the pin back (a revert of its own revert),
// so that Git again names what is running, and says so.

// HeldError is a rollback the runner did not finish because finishing it would
// leave the environment unable to render. The environment is as it was; the
// message says what state it is in and what a person can do.
type HeldError struct{ Msg string }

func (e *HeldError) Error() string { return e.Msg }

// appSources lists the sources of the Application a manifest holds, as maps.
func appSources(data []byte, name string) []map[string]any {
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
		var out []map[string]any
		if l, ok := spec["sources"].([]any); ok {
			for _, s := range l {
				m, _ := s.(map[string]any)
				out = append(out, m)
			}
		} else if s, ok := spec["source"].(map[string]any); ok {
			out = append(out, s)
		}
		return out
	}
}

// schemaRejects lists the dotted paths of the values a chart's values schema
// does not allow: keys the schema does not know where it sets
// additionalProperties to false (at any depth the values reach).
func schemaRejects(schemaJSON []byte, valuesYAML ...[]byte) []string {
	var sc map[string]any
	if json.Unmarshal(schemaJSON, &sc) != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, v := range valuesYAML {
		var vals map[string]any
		if yaml.Unmarshal(v, &vals) != nil {
			continue
		}
		walkSchema(sc, vals, "", seen)
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func walkSchema(sc map[string]any, vals map[string]any, at string, out map[string]bool) {
	props, _ := sc["properties"].(map[string]any)
	closed := false
	if b, ok := sc["additionalProperties"].(bool); ok && !b {
		closed = true
	}
	for k, v := range vals {
		child, known := props[k].(map[string]any)
		if !known {
			if closed {
				out[strings.TrimPrefix(at+"."+k, ".")] = true
			}
			continue
		}
		if m, ok := v.(map[string]any); ok {
			walkSchema(child, m, at+"."+k, out)
		}
	}
}

// chartSchema finds the values schema of a chart source at a revision, from a
// Git repository the runner is configured for; nil when it cannot see it.
//
// held is the clone the caller already holds (the rollback's own): a chart in
// that same repository and branch is read from it, never by opening it a
// second time, which would wait for the caller itself.
func (a *Agent) chartSchema(ctx context.Context, job *engine.Job, held *gitops.Repo, src map[string]any) ([]byte, error) {
	if a.ChartSchema != nil {
		return a.ChartSchema(ctx, src)
	}
	url, _ := src["repoURL"].(string)
	rev, _ := src["targetRevision"].(string)
	dir, _ := src["path"].(string)
	if url == "" || rev == "" || dir == "" || src["chart"] != nil {
		return nil, nil // a chart repository: not readable here
	}
	norm := func(s string) string { return strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git") }
	for name, rc := range a.Config.Repos {
		if norm(rc.URL) != norm(url) && norm(rc.ArgoCDURL) != norm(url) || !rc.AllowsProject(job.Project) {
			continue
		}
		if held != nil && held.Dir == a.repoDir(rc, name, "main") {
			return held.ReadAtRevision(ctx, rev, path.Join(dir, "values.schema.json"))
		}
		repo, _, unlock, err := a.repo(ctx, job.Project, name, "main", func(string, ...any) {})
		if err != nil {
			return nil, err
		}
		data, err := repo.ReadAtRevision(ctx, rev, path.Join(dir, "values.schema.json"))
		unlock()
		return data, err
	}
	return nil, nil
}

// preflightPin checks that the application can render the values Git holds
// with the chart pins the pin revert would restore. It returns a held message
// when it cannot, "" when it can or when it cannot tell (then it logs why).
func (a *Agent) preflightPin(ctx context.Context, repo *gitops.Repo, job *engine.Job, pinCommit string, manifests, valueFiles []string, log func(string, ...any)) string {
	d := job.Deploy
	var values [][]byte
	var names []string
	for _, f := range valueFiles {
		data, err := repo.ReadFile(f)
		if err != nil || data == nil || manifestKind(data, d.Application) != "" {
			continue
		}
		values, names = append(values, data), append(names, f)
	}
	if len(values) == 0 {
		log("Before reverting the chart pin: no values file of this deployment to check against the old chart")
		return ""
	}
	for _, f := range manifests {
		now, _ := repo.ReadFile(f)
		if manifestKind(now, d.Application) != "application" {
			continue
		}
		old, err := repo.ReadAtParent(ctx, pinCommit, f)
		if err != nil || old == nil {
			continue
		}
		for _, src := range appSources(old, d.Application) {
			schema, err := a.chartSchema(ctx, job, repo, src)
			if err != nil || schema == nil {
				log("Before reverting the chart pin: could not read the old chart's values schema (%s at %v); not checked", firstOf(fmt.Sprint(src["chart"]), fmt.Sprint(src["path"])), src["targetRevision"])
				continue
			}
			if bad := schemaRejects(schema, values...); len(bad) > 0 {
				chart := firstOf(strOf(src, "chart"), strOf(src, "path"), strOf(src, "repoURL"))
				return fmt.Sprintf("Rolling back would pin chart %s at %s, which does not accept these values (%s): %s. "+
					"The chart pin was NOT reverted. The environment is running the NEW version's chart with the previous image (values reverted); Git names exactly that pair (the new pin, the reverted values). "+
					"Next: remove or rename %s in %s and roll back again, or deploy a fixed version forward",
					chart, strOf(src, "targetRevision"), strings.Join(names, ", "), strings.Join(bad, ", "), strings.Join(bad, ", "), strings.Join(names, ", "))
			}
			log("Before reverting the chart pin: the old chart %s at %s accepts the values (checked its values schema)", firstOf(strOf(src, "chart"), strOf(src, "path")), strOf(src, "targetRevision"))
		}
	}
	return ""
}

func strOf(m map[string]any, k string) string { s, _ := m[k].(string); return s }

// watchRender looks at the application for a short while after a pin revert:
// a ComparisonError that does not go away means it cannot render the pair Git
// now holds. It returns that error's text, or "" when it renders.
func (a *Agent) watchRender(ctx context.Context, job *engine.Job) string {
	cl, _, err := a.cluster(job.Deploy.Target)
	if err != nil {
		return ""
	}
	grace := a.ComparisonGrace
	if grace == 0 {
		grace = 20 * time.Second
	}
	_ = cl.RefreshHard(ctx, job.Deploy.Application)
	deadline := time.Now().Add(grace)
	last, clean := "", 0
	for {
		app, err := cl.Application(ctx, job.Deploy.Application)
		if err != nil || app == nil {
			return ""
		}
		last = ""
		for _, c := range listOf(mapOf(app, "status"), "conditions") {
			cm, _ := c.(map[string]any)
			if cm["type"] == "ComparisonError" {
				last, _ = cm["message"].(string)
			}
		}
		if last == "" {
			if clean++; mapOf(mapOf(app, "status"), "sync")["status"] == "Synced" || clean >= 3 {
				return "" // renders: Synced, or no error on three looks in a row
			}
		} else {
			clean = 0
		}
		if time.Now().After(deadline) {
			return last
		}
		if sleepCtx(ctx, a.pairEvery()) != nil {
			return last
		}
	}
}

// restorePin reverts the pin revert, so that Git names the chart pin the
// application is really running, and says what it did.
func (a *Agent) restorePin(ctx context.Context, job *engine.Job, revertCommit string, log func(string, ...any)) (string, error) {
	repo, unlock, err := a.repoWrite(ctx, job, log)
	if err != nil {
		return "", err
	}
	res, err := repo.Revert(ctx, job.Deploy.Deployment+"-pin-restore", revertCommit,
		"Restore the chart pin: rolling back "+job.Deploy.Service+" could not render\n\n(Launchway put Git back to what is running)")
	unlock()
	if err != nil {
		return "", err
	}
	return res.Commit, nil
}

// Diagnosis is what Doctor found about an application with no deployment in
// progress.
type Diagnosis struct {
	// Stale words an operation the controller is running or retrying with
	// revisions that differ from the application's spec; "" when there is none.
	Stale string
	// Controller is what the controller says it is doing.
	Controller string
	// Cleared says the operation was ended (clear was asked for).
	Cleared bool
}

// Doctor looks for the stuck state a failed rollback or hook leaves: a sync
// operation in Running or Terminating retried with revisions that differ from
// the application's spec, while nothing is being deployed. With end it ends
// that operation, so the application's own sync starts a fresh one at the
// intended revisions. It needs the controller permissions the runner already
// has for deploying: get and patch on applications.argoproj.io in the Argo CD
// namespace (patching removes the operation field). It changes nothing else.
func (a *Agent) Doctor(ctx context.Context, target, application string, end bool) (Diagnosis, error) {
	var dg Diagnosis
	cl, _, err := a.cluster(target)
	if err != nil {
		return dg, err
	}
	obj, err := cl.Application(ctx, application)
	if err != nil {
		return dg, err
	}
	if obj == nil {
		return dg, fmt.Errorf("application %s was not found", application)
	}
	url := ""
	for _, src := range liveSources(obj) {
		if u := strOf(src, "repoURL"); u != "" {
			url = u
			break
		}
	}
	app := deploy.ReduceApplication(obj, deploy.Spec{}, url, func(string) (bool, error) { return false, nil })
	dg.Stale, dg.Controller = app.StaleOperation, app.Controller
	if dg.Stale != "" && end {
		if err := cl.TerminateOperation(ctx, application); err != nil {
			return dg, err
		}
		dg.Cleared = true
	}
	return dg, nil
}
