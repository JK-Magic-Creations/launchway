package argocd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// The resources a deploy runner reads and writes, by group, version and
// resource. These are the only ones.
var (
	applications = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	rollouts     = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	analysisRuns = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "analysisruns"}
	deployments  = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

// RefreshAnnotation asks Argo CD to compare an application with its
// repository again at once.
const RefreshAnnotation = "argocd.argoproj.io/refresh"

// Cluster reaches one target's Kubernetes API.
type Cluster struct {
	dyn    dynamic.Interface
	argoNS string
	// logs reads the end of a pod's log; nil where it cannot (tests).
	logs func(ctx context.Context, ns, pod string, tail int) ([]string, error)
	// prevLogs reads the end of the log of the container's previous run (what
	// a crash-looping container said before it exited); nil where it cannot.
	prevLogs func(ctx context.Context, ns, pod string, tail int) ([]string, error)
	rights   rightsCache
	// jobLogs keeps the logs read of failed analysis jobs, by namespace/name.
	jobLogs sync.Map
}

// Connect makes a client for a target. It does not contact the cluster.
func Connect(t Target) (*Cluster, error) {
	var cfg *rest.Config
	var err error
	if t.Kubernetes.InCluster {
		cfg, err = rest.InClusterConfig()
	} else {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		if t.Kubernetes.Kubeconfig != "" {
			rules.ExplicitPath = t.Kubernetes.Kubeconfig
		}
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
			&clientcmd.ConfigOverrides{CurrentContext: t.Kubernetes.Context}).ClientConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("connecting to Kubernetes: %w", err)
	}
	cfg.Timeout = 20 * time.Second
	cfg.UserAgent = "launchway-deploy-runner"
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	c := NewCluster(dyn, t.ArgoCDNamespace)
	c.logs = podLogReader(cfg, false)
	c.prevLogs = podLogReader(cfg, true)
	return c, nil
}

// NewCluster wraps a dynamic client (tests give a fake one).
func NewCluster(dyn dynamic.Interface, argoNamespace string) *Cluster {
	if argoNamespace == "" {
		argoNamespace = "argocd"
	}
	return &Cluster{dyn: dyn, argoNS: argoNamespace}
}

// get reads one object; nil, nil when it does not exist.
func (c *Cluster) get(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (map[string]any, error) {
	obj, err := c.dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, explain(err, gvr.Resource, ns, name)
	}
	return obj.Object, nil
}

// explain words an API error for the step's log and the run page.
func explain(err error, what, ns, name string) error {
	switch {
	case apierrors.IsForbidden(err):
		return fmt.Errorf("the deploy runner may not read %s %s/%s (check its RBAC): %v", what, ns, name, err)
	case apierrors.IsUnauthorized(err):
		return fmt.Errorf("the cluster did not accept the deploy runner's credentials: %v", err)
	}
	return fmt.Errorf("reading %s %s/%s: %w", what, ns, name, err)
}

// Application reads an Argo CD application.
func (c *Cluster) Application(ctx context.Context, name string) (map[string]any, error) {
	return c.get(ctx, applications, c.argoNS, name)
}

// Rollout reads an Argo Rollouts rollout.
func (c *Cluster) Rollout(ctx context.Context, ns, name string) (map[string]any, error) {
	return c.get(ctx, rollouts, ns, name)
}

// DeploymentGeneration reads the generation of the Deployment a rollout
// takes its pod template from; nil when it cannot be read.
func (c *Cluster) DeploymentGeneration(ctx context.Context, ns, name string) *int64 {
	obj, err := c.get(ctx, deployments, ns, name)
	if err != nil || obj == nil {
		return nil
	}
	meta, _ := obj["metadata"].(map[string]any)
	switch g := meta["generation"].(type) {
	case int64:
		return &g
	case float64:
		n := int64(g)
		return &n
	}
	return nil
}

// AnalysisRuns lists a namespace's analysis runs.
func (c *Cluster) AnalysisRuns(ctx context.Context, ns string) ([]map[string]any, error) {
	list, err := c.dyn.Resource(analysisRuns).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, explain(err, "analysisruns", ns, "*")
	}
	out := make([]map[string]any, 0, len(list.Items))
	for _, it := range list.Items {
		out = append(out, it.Object)
	}
	return out, nil
}

// ErrNoPatchRight means the runner may not change the application at all.
var ErrNoPatchRight = errors.New("the deploy runner may not change the application")

// Refresh sets the refresh annotation, so Argo CD compares the application
// with its repository at once.
func (c *Cluster) Refresh(ctx context.Context, app string) error {
	patch := map[string]any{"metadata": map[string]any{"annotations": map[string]any{RefreshAnnotation: "normal"}}}
	return c.patchApp(ctx, app, patch)
}

// RefreshHard asks Argo CD to compare an application with its repository
// again at once, ignoring its cache, so a parent application sees a new commit.
func (c *Cluster) RefreshHard(ctx context.Context, app string) error {
	patch := map[string]any{"metadata": map[string]any{"annotations": map[string]any{RefreshAnnotation: "hard"}}}
	return c.patchApp(ctx, app, patch)
}

// TerminateOperation removes the application's current operation (a sync the
// controller is running or retrying), which is how a stuck operation is ended
// from the API; the application's own sync policy then starts a new one.
func (c *Cluster) TerminateOperation(ctx context.Context, app string) error {
	return c.patchApp(ctx, app, map[string]any{"operation": nil})
}

// Sync sets the application's operation to a sync, if its resourceVersion
// is still resourceVersion: a change since it was read makes it fail with a
// conflict, and the caller reads it again.
func (c *Cluster) Sync(ctx context.Context, app, resourceVersion string, sync map[string]any, reason string) error {
	patch := map[string]any{
		"metadata": map[string]any{"resourceVersion": resourceVersion},
		"operation": map[string]any{
			"initiatedBy": map[string]any{"username": "launchway"},
			"info":        []any{map[string]any{"name": "Reason", "value": reason}},
			"sync":        sync,
		},
	}
	return c.patchApp(ctx, app, patch)
}

func (c *Cluster) patchApp(ctx context.Context, app string, patch map[string]any) error {
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.dyn.Resource(applications).Namespace(c.argoNS).Patch(ctx, app, types.MergePatchType, data, metav1.PatchOptions{FieldManager: "launchway"})
	switch {
	case err == nil:
		return nil
	case apierrors.IsForbidden(err) || apierrors.IsInvalid(err):
		return fmt.Errorf("%w: %v", ErrNoPatchRight, err)
	case apierrors.IsConflict(err):
		return errConflict
	}
	return fmt.Errorf("changing application %s: %w", app, err)
}

var errConflict = errors.New("the application changed while it was being read")

// WatchChanges sends on changed whenever the application, the rollout or
// the namespace's analysis runs change, until ctx ends. A watch that fails
// is started again after a pause; the caller also reads on a timer, so a
// lost watch costs only time.
func (c *Cluster) WatchChanges(ctx context.Context, app, rolloutNS, rollout string, changed chan<- struct{}) {
	start := func(gvr schema.GroupVersionResource, ns, name string) {
		for ctx.Err() == nil {
			opts := metav1.ListOptions{}
			if name != "" {
				opts.FieldSelector = fields.OneTermEqualSelector("metadata.name", name).String()
			}
			w, err := c.dyn.Resource(gvr).Namespace(ns).Watch(ctx, opts)
			if err == nil {
				for ev := range w.ResultChan() {
					if ev.Type == watch.Error {
						break
					}
					select {
					case changed <- struct{}{}:
					default:
					}
				}
				w.Stop()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
	go start(applications, c.argoNS, app)
	if rollout != "" {
		go start(rollouts, rolloutNS, rollout)
		go start(analysisRuns, rolloutNS, "")
	}
}

// ErrNoStatusRight means the runner may not change the rollout's status.
var ErrNoStatusRight = errors.New("the deploy runner may not change the rollout's status")

// PatchRolloutStatus sets fields of a rollout's status (promote, abort and
// the like are status changes), if its resourceVersion is still
// resourceVersion: a change since it was read fails with a conflict, and
// the caller reads it again. Only the status subresource is patched, which
// is all the deploy runner may change on a rollout.
func (c *Cluster) PatchRolloutStatus(ctx context.Context, ns, name, resourceVersion string, status map[string]any) error {
	data, err := json.Marshal(map[string]any{"metadata": map[string]any{"resourceVersion": resourceVersion}, "status": status})
	if err != nil {
		return err
	}
	_, err = c.dyn.Resource(rollouts).Namespace(ns).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{FieldManager: "launchway"}, "status")
	switch {
	case err == nil:
		return nil
	case apierrors.IsForbidden(err) || apierrors.IsInvalid(err):
		return fmt.Errorf("%w: %v", ErrNoStatusRight, err)
	case apierrors.IsConflict(err):
		return errConflict
	}
	return fmt.Errorf("%w: changing rollout %s/%s: %v", ErrUnavailable, ns, name, err)
}
