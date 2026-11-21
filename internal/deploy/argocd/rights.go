package argocd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	"github.com/jk-magic-creations/launchway/internal/deploy"
)

// What a deploy runner finds out about itself: whether the cluster lets it
// change a rollout's status (so that a command is not offered, or given,
// for nothing) and read the logs of analysis jobs. Both are asked of the
// cluster's own authorizer (a SelfSubjectAccessReview, which any
// authenticated client may make) and kept for a minute.

var (
	accessReviews = schema.GroupVersionResource{Group: "authorization.k8s.io", Version: "v1", Resource: "selfsubjectaccessreviews"}
	pods          = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
)

// ControlWhy is what an administrator must enable for a runner to apply
// commands to a rollout.
func ControlWhy(ns, name string) string {
	return fmt.Sprintf("this deploy runner may not change the status of rollout %s/%s, so it cannot apply promote, promote full, abort or retry; "+
		"an administrator must list the rollout in the chart value delivery.deployRunner.rbac.controlRollouts (for example {%s: [%s]})", ns, name, ns, name)
}

// LogsWhy is what an administrator must enable for a runner to read the
// logs of analysis jobs.
func LogsWhy(ns string) string {
	return fmt.Sprintf("this deploy runner may not read pods or their logs in namespace %s; "+
		"an administrator can set the chart value delivery.deployRunner.rbac.readPods to allow it", ns)
}

type rightsKey struct{ ns, name string }

type rightsEntry struct {
	r  deploy.Rights
	at time.Time
}

// rightsCache keeps self-checks for a minute.
type rightsCache struct {
	mu sync.Mutex
	m  map[rightsKey]rightsEntry
}

// canI asks whether the runner may do verb on a resource; ok is false when
// the cluster could not say.
func (c *Cluster) canI(ctx context.Context, group, resource, sub, verb, ns, name string) (allowed, ok bool) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview",
		"spec": map[string]any{"resourceAttributes": map[string]any{
			"group": group, "resource": resource, "subresource": sub, "verb": verb, "namespace": ns, "name": name}},
	}}
	out, err := c.dyn.Resource(accessReviews).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return false, false
	}
	a, found, _ := unstructured.NestedBool(out.Object, "status", "allowed")
	return a && found, true
}

// Rights reports what the runner may do to a rollout, from the cluster's
// authorizer; checks it could not make are left unset.
func (c *Cluster) Rights(ctx context.Context, ns, name string) *deploy.Rights {
	k := rightsKey{ns, name}
	c.rights.mu.Lock()
	if e, ok := c.rights.m[k]; ok && time.Since(e.at) < time.Minute {
		c.rights.mu.Unlock()
		r := e.r
		return &r
	}
	c.rights.mu.Unlock()
	var r deploy.Rights
	if allowed, ok := c.canI(ctx, "argoproj.io", "rollouts", "status", "patch", ns, name); ok {
		r.Control = &allowed
		if !allowed {
			r.ControlWhy = ControlWhy(ns, name)
		}
	}
	if allowed, ok := c.canI(ctx, "", "pods", "log", "get", ns, ""); ok {
		r.Logs = &allowed
		if !allowed {
			r.LogsWhy = LogsWhy(ns)
		}
	}
	if r.Control == nil && r.Logs == nil {
		return nil
	}
	c.rights.mu.Lock()
	if c.rights.m == nil {
		c.rights.m = map[rightsKey]rightsEntry{}
	}
	c.rights.m[k] = rightsEntry{r, time.Now()}
	c.rights.mu.Unlock()
	return &r
}

// maxLogLines and maxLogBytes bound the tail of a job's log a snapshot keeps.
const (
	maxLogLines = 40
	maxLogBytes = 6000
)

// JobLog reads the end of the log of the pod a Job ran. It never fails: a
// log it may not or could not read says why in its note.
func (c *Cluster) JobLog(ctx context.Context, ns, job string) *deploy.JobLog {
	list, err := c.dyn.Resource(pods).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + job})
	if apierrors.IsForbidden(err) {
		return &deploy.JobLog{Note: fmt.Sprintf("the job's log is in %s/%s; this runner may not read it", ns, job)}
	}
	if err != nil {
		return &deploy.JobLog{Note: fmt.Sprintf("the job's log is in %s/%s; reading it failed: %v", ns, job, err)}
	}
	if len(list.Items) == 0 {
		return &deploy.JobLog{Note: fmt.Sprintf("the job's pod in %s/%s is gone, so its log is too", ns, job)}
	}
	pod := list.Items[0]
	for _, it := range list.Items { // the newest pod
		if it.GetCreationTimestamp().After(pod.GetCreationTimestamp().Time) {
			pod = it
		}
	}
	if c.logs == nil {
		return &deploy.JobLog{Note: fmt.Sprintf("the job's log is in %s/%s; this runner cannot read pod logs here", ns, job)}
	}
	lines, err := c.logs(ctx, ns, pod.GetName(), maxLogLines)
	if apierrors.IsForbidden(err) {
		return &deploy.JobLog{Note: fmt.Sprintf("the job's log is in %s/%s; this runner may not read it", ns, job)}
	}
	if err != nil {
		return &deploy.JobLog{Note: fmt.Sprintf("the job's log is in %s/%s; reading it failed: %v", ns, job, err)}
	}
	return &deploy.JobLog{Lines: lines}
}

// podLogReader reads the last lines of a pod's log with the cluster's
// credentials.
func podLogReader(cfg *rest.Config, previous bool) func(ctx context.Context, ns, pod string, tail int) ([]string, error) {
	return func(ctx context.Context, ns, pod string, tail int) ([]string, error) {
		hc, err := rest.HTTPClientFor(cfg)
		if err != nil {
			return nil, err
		}
		u := strings.TrimRight(cfg.Host, "/") + "/api/v1/namespaces/" + url.PathEscape(ns) + "/pods/" + url.PathEscape(pod) +
			"/log?tailLines=" + fmt.Sprint(tail) + "&limitBytes=" + fmt.Sprint(maxLogBytes)
		if previous {
			u += "&previous=true"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods/log"}, pod, fmt.Errorf("status %d", resp.StatusCode))
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("the cluster answered %s", resp.Status)
		}
		var lines []string
		sc := bufio.NewScanner(io.LimitReader(resp.Body, maxLogBytes))
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		return lines, sc.Err()
	}
}

// addJobLogs gives each failed job metric the tail of its job's log, once:
// what was read is kept, so that watching does not read it again.
func (c *Cluster) addJobLogs(ctx context.Context, runs []deploy.AnalysisRun) {
	for i := range runs {
		metrics := runs[i].Metrics
		for j := range metrics {
			m := &metrics[j]
			if m.Job == nil || (m.Phase != "Failed" && m.Phase != "Error" && m.Phase != "Inconclusive") {
				continue
			}
			key := m.Job.Namespace + "/" + m.Job.Name
			if v, ok := c.jobLogs.Load(key); ok {
				m.JobLog = v.(*deploy.JobLog)
				continue
			}
			l := c.JobLog(ctx, m.Job.Namespace, m.Job.Name)
			if len(l.Lines) > 0 || !strings.Contains(l.Note, "failed:") { // a transient failure is tried again
				c.jobLogs.Store(key, l)
			}
			m.JobLog = l
		}
	}
}

// PodEvidence looks at the pods of an application (those labelled
// app.kubernetes.io/instance=<app>) in a namespace and reports the ones that
// are not running well, with the end of the log of the run that failed. It
// never fails: what it may not read it leaves out.
func (c *Cluster) PodEvidence(ctx context.Context, ns, app string) []deploy.PodEvidence {
	list, err := c.dyn.Resource(pods).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/instance=" + app})
	if err != nil {
		return nil
	}
	var out []deploy.PodEvidence
	for _, p := range list.Items {
		statuses, _, _ := unstructured.NestedSlice(p.Object, "status", "containerStatuses")
		for _, s := range statuses {
			cs, _ := s.(map[string]any)
			restarts, _, _ := unstructured.NestedInt64(cs, "restartCount")
			reason, _, _ := unstructured.NestedString(cs, "state", "waiting", "reason")
			msg, _, _ := unstructured.NestedString(cs, "state", "waiting", "message")
			if reason == "" {
				if code, found, _ := unstructured.NestedInt64(cs, "state", "terminated", "exitCode"); found && code != 0 {
					reason, _, _ = unstructured.NestedString(cs, "state", "terminated", "reason")
					msg, _, _ = unstructured.NestedString(cs, "state", "terminated", "message")
				}
			}
			if reason == "" || reason == "ContainerCreating" || reason == "PodInitializing" {
				continue
			}
			e := deploy.PodEvidence{Pod: p.GetName(), Namespace: ns, Reason: reason, Message: msg, Restarts: int(restarts),
				Crash: reason == "CrashLoopBackOff" || reason == "Error" || reason == "RunContainerError"}
			read := c.prevLogs
			if read == nil || restarts == 0 {
				read = c.logs
			}
			if read != nil {
				if lines, err := read(ctx, ns, p.GetName(), 8); err == nil {
					e.LogTail = lines
				}
			}
			out = append(out, e)
			break
		}
		if len(out) >= 3 {
			break
		}
	}
	return out
}
