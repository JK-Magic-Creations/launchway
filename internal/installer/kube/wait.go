package kube

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Waiter waits for things to be ready and says how it is going, in plain words.
type Waiter struct {
	CS    kubernetes.Interface
	Out   io.Writer
	Poll  time.Duration
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
	// Stuck is how long a pod may sit in a failing state before the wait gives up early.
	Stuck time.Duration
}

func (w *Waiter) poll() time.Duration {
	if w.Poll > 0 {
		return w.Poll
	}
	return 2 * time.Second
}

func (w *Waiter) sleep(ctx context.Context, d time.Duration) error {
	if w.Sleep != nil {
		return w.Sleep(ctx, d)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func (w *Waiter) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// WaitJob waits for a Job to finish; a failed Job is an error that includes the end of its log.
func (w *Waiter) WaitJob(ctx context.Context, ns, name string, limit time.Duration) error {
	deadline := w.now().Add(limit)
	for {
		j, err := w.CS.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			if j.Status.Succeeded > 0 {
				return nil
			}
			for _, c := range j.Status.Conditions {
				if c.Type == batchv1.JobFailed && c.Status == "True" {
					return fmt.Errorf("the setup job %s failed: %s", name, c.Message)
				}
			}
		}
		if w.now().After(deadline) {
			return fmt.Errorf("the setup job %s did not finish in %s%s", name, limit, describe(Diagnose(ctx, w.CS, ns)))
		}
		if err := w.sleep(ctx, w.poll()); err != nil {
			return err
		}
	}
}

// WaitDeployments waits until every named Deployment has all its replicas ready.
func (w *Waiter) WaitDeployments(ctx context.Context, ns string, names []string, limit time.Duration) error {
	deadline := w.now().Add(limit)
	last := ""
	var problemSince time.Time
	for {
		var parts []string
		ready := 0
		for _, n := range names {
			d, err := w.CS.AppsV1().Deployments(ns).Get(ctx, n, metav1.GetOptions{})
			if err != nil {
				parts = append(parts, n+" not created yet")
				continue
			}
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			parts = append(parts, fmt.Sprintf("%s %d/%d ready", strings.TrimPrefix(n, ""), d.Status.ReadyReplicas, want))
			if d.Status.ReadyReplicas >= want && want > 0 {
				ready++
			}
		}
		if line := strings.Join(parts, ", "); line != last {
			fmt.Fprintf(w.Out, "      %s\n", line)
			last = line
		}
		if ready == len(names) {
			return nil
		}
		if ps := Diagnose(ctx, w.CS, ns); len(ps) > 0 {
			if problemSince.IsZero() {
				problemSince = w.now()
			}
			stuck := w.Stuck
			if stuck == 0 {
				stuck = 90 * time.Second
			}
			if w.now().Sub(problemSince) > stuck {
				return &StuckError{Problems: ps}
			}
		} else {
			problemSince = time.Time{}
		}
		if w.now().After(deadline) {
			return &StuckError{Problems: Diagnose(ctx, w.CS, ns), Timeout: limit}
		}
		if err := w.sleep(ctx, w.poll()); err != nil {
			return err
		}
	}
}

// StuckError says the installation did not come up, and why, in words a person can act on.
type StuckError struct {
	Problems []Problem
	Timeout  time.Duration
}

func (e *StuckError) Error() string {
	if len(e.Problems) == 0 {
		return fmt.Sprintf("the pods were not all ready after %s and nothing in the cluster says why; `kubectl -n NAMESPACE get pods` and `kubectl -n NAMESPACE describe pod` will", e.Timeout)
	}
	return "the installation did not come up:" + describe(e.Problems)
}

func describe(ps []Problem) string {
	seen := map[string]bool{}
	var lines []string
	for _, p := range ps {
		key := p.Kind + p.What
		if seen[key] {
			continue
		}
		seen[key] = true
		lines = append(lines, "\n  - "+p.What+"\n    What to do: "+p.Fix)
	}
	sort.Strings(lines)
	return strings.Join(lines, "")
}
