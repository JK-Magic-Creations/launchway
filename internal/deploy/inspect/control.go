package inspect

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// Control actions the inspector applies itself (promote, abort and retry
// are the rollout's status commands, applied by the existing command
// path).
const (
	ActPause   = "pause"
	ActResume  = "resume"
	ActRestart = "restart"
	ActScale   = "scale"
)

// Results of a control action.
const (
	Applied        = "applied"
	AlreadyApplied = "already_applied"
	NotApplied     = "not_applied"
	Failed         = "failed"
)

// Writer is what controls need beyond reading.
type Writer interface {
	Reader
	// MergePatch changes an object's spec (never its status).
	MergePatch(ctx context.Context, kind, ns, name string, patch map[string]any) error
}

// Action is one control request.
type Action struct {
	Action   string
	Target   Ref
	Replicas int
	// ScaleMax is the Airfield's declared upper bound (0: scaling is off).
	ScaleMax int
	Reason   string
}

// Apply does a control action on a workload of the release.
func (b *Builder) Apply(ctx context.Context, w Writer, t *Tree, a Action) (result, detail string) {
	n, ok := t.Find(a.Target.ID())
	if !ok {
		return NotApplied, ErrNotInRelease.Error()
	}
	switch n.Kind {
	case "Rollout", "Deployment", "StatefulSet", "DaemonSet":
	default:
		return NotApplied, fmt.Sprintf("%s cannot be %s: controls apply to a Rollout, Deployment, StatefulSet or DaemonSet", n.Kind, a.Action)
	}
	obj, err := w.Get(ctx, n.Kind, n.Namespace, n.Name)
	if err != nil {
		return Failed, err.Error()
	}
	if obj == nil {
		return NotApplied, n.ID + " no longer exists"
	}
	patch := map[string]any{}
	switch a.Action {
	case ActPause, ActResume:
		if n.Kind != "Rollout" && n.Kind != "Deployment" {
			return NotApplied, n.Kind + " cannot be paused"
		}
		want := a.Action == ActPause
		if (digS(obj, "spec", "paused") == "true") == want {
			return AlreadyApplied, fmt.Sprintf("%s is already %s", n.ID, map[bool]string{true: "paused", false: "not paused by a person"}[want])
		}
		patch["spec"] = map[string]any{"paused": want}
	case ActRestart:
		now := time.Now().UTC().Format(time.RFC3339)
		if n.Kind == "Rollout" {
			patch["spec"] = map[string]any{"restartAt": now}
		} else {
			patch["spec"] = map[string]any{"template": map[string]any{"metadata": map[string]any{"annotations": map[string]any{"kubectl.kubernetes.io/restartedAt": now}}}}
		}
	case ActScale:
		if n.Kind == "DaemonSet" {
			return NotApplied, "a DaemonSet has no replica count"
		}
		if a.ScaleMax <= 0 {
			return NotApplied, "scaling is off for this Airfield: an administrator sets deploy.targets.<name>.scale_max to allow it"
		}
		if a.Replicas < 1 || a.Replicas > a.ScaleMax {
			return NotApplied, fmt.Sprintf("replicas must be between 1 and %d, the bound this Airfield declares", a.ScaleMax)
		}
		for _, o := range t.Nodes {
			if o.Kind == "HorizontalPodAutoscaler" && o.Namespace == n.Namespace {
				return NotApplied, "the autoscaler " + o.Name + " manages the replica count; change its bounds in the repository"
			}
		}
		if digI(obj, "spec", "replicas") == a.Replicas {
			return AlreadyApplied, fmt.Sprintf("%s already has %d replicas", n.ID, a.Replicas)
		}
		patch["spec"] = map[string]any{"replicas": a.Replicas}
	default:
		return NotApplied, "unknown action " + a.Action
	}
	if err := w.MergePatch(ctx, n.Kind, n.Namespace, n.Name, patch); err != nil {
		return Failed, WhyForbidden(err, n.Kind, n.Namespace, n.Name)
	}
	return Applied, fmt.Sprintf("%s %s", a.Action, n.ID)
}

// Allowed lists the actions that make sense for a node now, each with
// why-not words when it does not; the page shows only those that work.
type Allowed struct {
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	Why    string `json:"why,omitempty"`
}

// Actions says what can be done to the release's workload.
func Actions(t *Tree, scaleMax int) []Allowed {
	var root *Node
	for i := range t.Nodes {
		n := &t.Nodes[i]
		if n.Parent == "" && n.Change != ChangeLeftover && slices.Contains([]string{"Rollout", "Deployment", "StatefulSet", "DaemonSet"}, n.Kind) {
			if root == nil || n.Kind == "Rollout" {
				root = n
			}
		}
	}
	if root == nil {
		return nil
	}
	v := t.Rollout
	var out []Allowed
	add := func(a string, ok bool, why string) { out = append(out, Allowed{Action: a, OK: ok, Why: why}) }
	pausable := root.Kind == "Rollout" || root.Kind == "Deployment"
	paused := v != nil && v.UserPaused
	add(ActPause, pausable && !paused, whyNot(pausable, root.Kind+" cannot be paused")+whyNot(!paused, "it is already paused"))
	add(ActResume, pausable && paused, whyNot(pausable && paused, "it is not paused by a person"))
	add(ActRestart, true, "")
	switch {
	case root.Kind == "DaemonSet":
		add(ActScale, false, "a DaemonSet has no replica count")
	case scaleMax <= 0:
		add(ActScale, false, "scaling is off for this Airfield (deploy.targets.<name>.scale_max)")
	default:
		hpa := ""
		for _, n := range t.Nodes {
			if n.Kind == "HorizontalPodAutoscaler" {
				hpa = n.Name
			}
		}
		add(ActScale, hpa == "", whyNot(hpa == "", "the autoscaler "+hpa+" manages the replica count"))
	}
	return out
}

func whyNot(ok bool, why string) string {
	if ok {
		return ""
	}
	return why
}

// WhyForbidden words a failed write: a missing right names the chart
// value that grants it.
func WhyForbidden(err error, kind, ns, name string) string {
	s := err.Error()
	if containsAny(s, "forbidden", "Forbidden") {
		return fmt.Sprintf("this deploy runner may not change %s %s/%s; an administrator can allow it with the chart value delivery.deployRunner.rbac.inspect.control (see the reference page)", kind, ns, name)
	}
	return s
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		for i := 0; i+len(x) <= len(s); i++ {
			if s[i:i+len(x)] == x {
				return true
			}
		}
	}
	return false
}
