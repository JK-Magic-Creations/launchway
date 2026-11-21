package argocd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/deploy/inspect"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Looking into a delivery for the server (see internal/engine/inspect.go):
// the object tree, a manifest, logs, a few workload controls. Everything
// is checked against the runner's own configuration first: the application
// must be allowed for the project, and only the target's rollout
// namespaces are read.

// Answer is what a runner sends back for an inspect request.
type Answer struct {
	Tree    *inspect.Tree     `json:"tree,omitempty"`
	Actions []inspect.Allowed `json:"actions,omitempty"`
	Text    string            `json:"text,omitempty"`  // a manifest
	Lines   []inspect.LogLine `json:"lines,omitempty"` // a log's tail
	Notes   []string          `json:"notes,omitempty"`
	Result  string            `json:"result,omitempty"` // a control's result
	Detail  string            `json:"detail,omitempty"`
}

// LogParams are a logs request's parameters.
type LogParams struct {
	Pod          string `json:"pod,omitempty"`
	Container    string `json:"container,omitempty"`
	ReplicaSet   string `json:"replica_set,omitempty"`
	Role         string `json:"role,omitempty"`
	Job          string `json:"job,omitempty"`
	Previous     bool   `json:"previous,omitempty"`
	Tail         int    `json:"tail,omitempty"`
	SinceSeconds int    `json:"since_seconds,omitempty"`
}

// ControlParams are a control request's parameters.
type ControlParams struct {
	Action   string `json:"action"`
	Object   string `json:"object"`
	Replicas int    `json:"replicas,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ManifestParams are a manifest request's parameters.
type ManifestParams struct {
	Object string `json:"object"`
}

type treeEntry struct {
	tree *inspect.Tree
	acts []inspect.Allowed
	at   time.Time
}

var treeCache struct {
	mu sync.Mutex
	m  map[string]treeEntry
}

const treeTTL = 3 * time.Second

func (c *Cluster) dynamicReader() inspect.Dynamic { return inspect.Dynamic{Client: c.dyn} }

// logSource returns the cluster's log reader.
func (c *Cluster) logSource() (inspect.LogSource, error) {
	if c.Logs != nil {
		return c.Logs, nil
	}
	if c.cfg == nil {
		return nil, errors.New("this cluster connection cannot read logs")
	}
	src, err := inspect.NewClientLogs(c.cfg)
	if err != nil {
		return nil, err
	}
	c.Logs = src
	return src, nil
}

// applicationRefs lists the objects an Argo CD application manages, and
// the hook resources of its last sync.
func applicationRefs(app map[string]any) []inspect.Ref {
	var out []inspect.Ref
	seen := map[string]bool{}
	add := func(r map[string]any) {
		ref := inspect.Ref{Kind: inspStr(r, "kind"), Namespace: inspStr(r, "namespace"), Name: inspStr(r, "name")}
		if ref.Kind == "" || ref.Name == "" || seen[ref.ID()] {
			return
		}
		seen[ref.ID()] = true
		out = append(out, ref)
	}
	status := mapOf(app, "status")
	for _, r := range inspList(status, "resources") {
		if rm, ok := r.(map[string]any); ok {
			add(rm)
		}
	}
	for _, r := range inspList(mapOf(mapOf(status, "operationState"), "syncResult"), "resources") {
		if rm, ok := r.(map[string]any); ok && inspStr(rm, "hookType") != "" {
			add(rm)
		}
	}
	return out
}

func inspStr(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func inspList(m map[string]any, k string) []any { l, _ := m[k].([]any); return l }

// inspectTree builds (or reuses, within a few seconds) a delivery's tree.
func (a *Agent) inspectTree(ctx context.Context, cl *Cluster, t Target, j *engine.InspectJob) (*inspect.Tree, []inspect.Allowed, error) {
	key := j.Target + "/" + j.Deployment
	treeCache.mu.Lock()
	if e, ok := treeCache.m[key]; ok && time.Since(e.at) < treeTTL {
		treeCache.mu.Unlock()
		return e.tree, e.acts, nil
	}
	treeCache.mu.Unlock()
	req := inspect.Request{Application: j.Application, Namespaces: t.RolloutNamespaces, Since: j.Since}
	if j.Rollout.Name != "" {
		req.Rollout = &inspect.Ref{Kind: "Rollout", Namespace: j.Rollout.Namespace, Name: j.Rollout.Name}
	}
	app, err := cl.Application(ctx, j.Application)
	if err != nil {
		return nil, nil, err
	}
	var notes []string
	if app == nil {
		notes = append(notes, "Application "+j.Application+" was not found in the cluster; only the rollout the step names is shown.")
	} else {
		req.Resources = applicationRefs(app)
	}
	b := &inspect.Builder{R: cl.dynamicReader(), Red: inspect.NewRedactor()}
	tree := b.Build(ctx, req)
	tree.Notes = append(notes, tree.Notes...)
	acts := inspect.Actions(tree, t.Inspect.ScaleMax)
	if !t.Inspect.Control {
		for i := range acts {
			acts[i].OK, acts[i].Why = false, "controls are off for this Airfield: an administrator sets deploy.targets."+j.Target+".inspect.control"
		}
	}
	treeCache.mu.Lock()
	if treeCache.m == nil {
		treeCache.m = map[string]treeEntry{}
	}
	if len(treeCache.m) > 200 {
		treeCache.m = map[string]treeEntry{}
	}
	treeCache.m[key] = treeEntry{tree, acts, time.Now()}
	treeCache.mu.Unlock()
	return tree, acts, nil
}

// Inspect answers one inspect request. chunk sends a piece of a followed
// log and says whether to stop.
func (a *Agent) Inspect(ctx context.Context, j *engine.InspectJob, chunk func(string) (bool, error)) (string, error) {
	cl, t, err := a.cluster(j.Target)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !t.AppAllowed(j.Project, j.Application) {
		return "", fmt.Errorf("this runner may not act on application %s for project %s", j.Application, j.Project)
	}
	switch {
	case !t.Inspect.Read:
		return "", fmt.Errorf("looking into deliveries is off for Airfield %s: set deploy.targets.%s.inspect.read: true in this deploy runner's configuration, and grant its cluster role the read rights (chart value delivery.deployRunner.rbac.inspect.read)", j.Target, j.Target)
	case (j.Kind == engine.InspectLogs) && !t.Inspect.Logs:
		return "", fmt.Errorf("reading logs is off for Airfield %s: set deploy.targets.%s.inspect.logs: true (and the chart value delivery.deployRunner.rbac.inspect.logs)", j.Target, j.Target)
	case j.Kind == engine.InspectControl && !t.Inspect.Control:
		return "", fmt.Errorf("controls are off for Airfield %s: set deploy.targets.%s.inspect.control: true (and the chart value delivery.deployRunner.rbac.inspect.control)", j.Target, j.Target)
	}
	tree, acts, err := a.inspectTree(ctx, cl, t, j)
	if err != nil {
		return "", err
	}
	var ans Answer
	switch j.Kind {
	case engine.InspectTree:
		ans.Tree, ans.Actions = tree, acts
	case engine.InspectManifest:
		var p ManifestParams
		if err := json.Unmarshal(j.Params, &p); err != nil {
			return "", err
		}
		b := &inspect.Builder{R: cl.dynamicReader(), Red: inspect.NewRedactor()}
		y, err := b.Manifest(ctx, tree, p.Object)
		if err != nil {
			return "", err
		}
		ans.Text = y
	case engine.InspectLogs:
		return a.inspectLogs(ctx, cl, tree, j, chunk)
	case engine.InspectControl:
		var p ControlParams
		if err := json.Unmarshal(j.Params, &p); err != nil {
			return "", err
		}
		ref, err := parseObject(p.Object)
		if err != nil {
			return "", err
		}
		b := &inspect.Builder{R: cl.dynamicReader(), Red: inspect.NewRedactor()}
		ans.Result, ans.Detail = b.Apply(ctx, cl.dynamicReader(), tree, inspect.Action{Action: p.Action, Target: ref, Replicas: p.Replicas, ScaleMax: t.Inspect.ScaleMax, Reason: p.Reason})
		treeCache.mu.Lock()
		delete(treeCache.m, j.Target+"/"+j.Deployment)
		treeCache.mu.Unlock()
	default:
		return "", fmt.Errorf("unknown inspect request %q", j.Kind)
	}
	out, err := json.Marshal(ans)
	return string(out), err
}

func parseObject(s string) (inspect.Ref, error) {
	parts := strings.SplitN(s, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return inspect.Ref{}, fmt.Errorf("an object is written Kind/namespace/name, not %q", s)
	}
	return inspect.Ref{Kind: parts[0], Namespace: parts[1], Name: parts[2]}, nil
}

func (a *Agent) inspectLogs(ctx context.Context, cl *Cluster, tree *inspect.Tree, j *engine.InspectJob, chunk func(string) (bool, error)) (string, error) {
	var p LogParams
	if err := json.Unmarshal(j.Params, &p); err != nil {
		return "", err
	}
	targets, notes, err := tree.Targets(inspect.LogSpec{Pod: p.Pod, Container: p.Container, ReplicaSet: p.ReplicaSet, Role: p.Role, Job: p.Job})
	if err != nil {
		return "", err
	}
	src, err := cl.logSource()
	if err != nil {
		return "", err
	}
	// Known secret values of the release are hidden in logs as well.
	lg := &inspect.Logger{Src: src, Red: tree.Redactor()}
	opt := inspect.LogOptions{Follow: j.Follow, Previous: p.Previous, TailLines: int64(p.Tail), SinceSeconds: int64(p.SinceSeconds)}
	if !j.Follow {
		var all []inspect.LogLine
		n, err := lg.Read(ctx, targets, opt, func(l []inspect.LogLine) bool { all = append(all, l...); return true })
		if err != nil {
			return "", err
		}
		sort.SliceStable(all, func(i, k int) bool { return all[i].T < all[k].T })
		out, err := json.Marshal(Answer{Lines: all, Notes: append(notes, n...)})
		return string(out), err
	}
	stopped := false
	n, err := lg.Read(ctx, targets, opt, func(l []inspect.LogLine) bool {
		body, _ := json.Marshal(l)
		stop, err := chunk(string(body))
		if err != nil || stop {
			stopped = true
		}
		return !stopped
	})
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(Answer{Notes: append(notes, n...)})
	return string(out), err
}
