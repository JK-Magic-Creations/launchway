package executor

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Before a run's pod is made, what its containers ask for is checked against
// what the runner's operator and the namespace allow, so that a step asking
// for more fails at once with a message that names the limit and where it
// comes from, instead of the API server rejecting the pod with a message
// about a quota, or the pod waiting for ever.
//
// Three sources are read:
//
//   - the runner's own settings, kubernetes.max_cpu and kubernetes.max_memory:
//     the most one container may ask for;
//   - the namespace's LimitRanges: the most (and least) a container, and the
//     whole pod, may have of each resource;
//   - the namespace's ResourceQuotas: how much the namespace may hold in all.
//
// A runner may not be allowed to read LimitRanges and ResourceQuotas; then
// the check is made against what it could read, the log says so, and the
// cluster still has the last word when it admits the pod.

var (
	resCPU    = corev1.ResourceCPU
	resMemory = corev1.ResourceMemory
)

// podAsk is what one container asks of the cluster.
type podAsk struct {
	name, image string
	res         corev1.ResourceRequirements
}

// checkResources fails, naming the limit, if the containers ask for more than
// is allowed. A shortage that may pass (other pods use the quota now) is an
// infrastructure error: the step is run again later.
func (k *Kubernetes) checkResources(ctx context.Context, specs []ContainerSpec, sys func(string, ...any)) error {
	asks := []podAsk{{name: helperName, image: k.o.HelperImage, res: k.resources(ContainerSpec{})}}
	for _, sp := range specs {
		asks = append(asks, podAsk{name: sp.Name, image: sp.Image, res: k.resources(sp)})
	}
	if err := k.checkOperatorLimits(asks); err != nil {
		return err
	}
	if k.client == nil {
		return nil
	}
	ns := k.o.Namespace
	lrs, err := k.client.CoreV1().LimitRanges(ns).List(ctx, metav1.ListOptions{})
	switch {
	case err == nil:
		if err := checkLimitRanges(ns, lrs.Items, asks); err != nil {
			return err
		}
	case apierrors.IsForbidden(err):
		k.noteOnce("limitranges", sys, "The runner may not read the namespace's LimitRanges, so it cannot check what the containers ask for against them; "+
			"the cluster will. Set kubernetes.max_memory and kubernetes.max_cpu to have the runner check, or let its service account list limitranges")
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		return infra("reading the LimitRanges of namespace %s: %v", ns, err)
	}
	rqs, err := k.client.CoreV1().ResourceQuotas(ns).List(ctx, metav1.ListOptions{})
	switch {
	case err == nil:
		return checkQuotas(ns, rqs.Items, asks)
	case apierrors.IsForbidden(err):
		k.noteOnce("resourcequotas", sys, "The runner may not read the namespace's ResourceQuotas, so it cannot check the pod against them; the cluster will")
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		return infra("reading the ResourceQuotas of namespace %s: %v", ns, err)
	}
	return nil
}

// noteOnce says something once for this runner.
func (k *Kubernetes) noteOnce(key string, sys func(string, ...any), format string, args ...any) {
	k.mu.Lock()
	if k.noted == nil {
		k.noted = map[string]bool{}
	}
	done := k.noted[key]
	k.noted[key] = true
	k.mu.Unlock()
	if !done {
		sys(format, args...)
	}
}

func (a podAsk) who() string {
	if a.image == "" {
		return "container " + a.name
	}
	return fmt.Sprintf("container %s (%s)", a.name, a.image)
}

// checkOperatorLimits holds each container to the runner's max_cpu and
// max_memory.
func (k *Kubernetes) checkOperatorLimits(asks []podAsk) error {
	for _, a := range asks {
		for _, f := range []struct {
			kind, setting, max string
			list               corev1.ResourceList
			key                corev1.ResourceName
		}{
			{"request", "max_cpu", k.o.MaxCPU, a.res.Requests, resCPU},
			{"limit", "max_cpu", k.o.MaxCPU, a.res.Limits, resCPU},
			{"request", "max_memory", k.o.MaxMemory, a.res.Requests, resMemory},
			{"limit", "max_memory", k.o.MaxMemory, a.res.Limits, resMemory},
		} {
			if f.max == "" {
				continue
			}
			got, ok := f.list[f.key]
			if !ok {
				continue
			}
			if max := resource.MustParse(f.max); got.Cmp(max) > 0 {
				return fmt.Errorf("%s asks for %s %s %s, which is above kubernetes.%s %s in this runner's configuration, "+
					"the most a container may ask for on this runner (its operator sets it)",
					a.who(), f.key, f.kind, got.String(), f.setting, max.String())
			}
		}
	}
	return nil
}

// checkLimitRanges holds the containers, and the pod as a whole, to what the
// namespace's LimitRanges allow.
func checkLimitRanges(ns string, lrs []corev1.LimitRange, asks []podAsk) error {
	for _, lr := range lrs {
		for _, item := range lr.Spec.Limits {
			switch item.Type {
			case corev1.LimitTypeContainer:
				for _, a := range asks {
					for _, r := range []corev1.ResourceName{resCPU, resMemory} {
						if err := checkRange(a, r, item, lr.Name, ns); err != nil {
							return err
						}
					}
				}
			case corev1.LimitTypePod:
				for _, r := range []corev1.ResourceName{resCPU, resMemory} {
					for _, kind := range []string{"request", "limit"} {
						total, n := sumAsks(asks, kind, r)
						if max, ok := item.Max[r]; ok && n > 0 && total.Cmp(max) > 0 {
							return fmt.Errorf("the run's pod asks for %s %s %s in all (%d containers), which is above the %s that LimitRange %q in namespace %s allows "+
								"per pod (its max for %s)", r, kind, total.String(), n, max.String(), lr.Name, ns, r)
						}
					}
				}
			}
		}
	}
	return nil
}

// checkRange checks one container against one LimitRange item of type
// Container.
func checkRange(a podAsk, r corev1.ResourceName, item corev1.LimitRangeItem, name, ns string) error {
	for _, kind := range []struct {
		name string
		list corev1.ResourceList
	}{{"request", a.res.Requests}, {"limit", a.res.Limits}} {
		got, ok := kind.list[r]
		if !ok {
			continue // a container without a limit gets the LimitRange's default, which the namespace chose
		}
		if max, ok := item.Max[r]; ok && got.Cmp(max) > 0 {
			return fmt.Errorf("%s asks for %s %s %s, which is above the %s that LimitRange %q in namespace %s allows per container (its max for %s)",
				a.who(), r, kind.name, got.String(), max.String(), name, ns, r)
		}
		if min, ok := item.Min[r]; ok && got.Cmp(min) < 0 {
			return fmt.Errorf("%s asks for %s %s %s, which is below the %s that LimitRange %q in namespace %s requires per container (its min for %s)",
				a.who(), r, kind.name, got.String(), min.String(), name, ns, r)
		}
	}
	return nil
}

// sumAsks adds up what the containers ask for of a resource, as requests or
// limits, and says how many containers ask.
func sumAsks(asks []podAsk, kind string, r corev1.ResourceName) (resource.Quantity, int) {
	var total resource.Quantity
	n := 0
	for _, a := range asks {
		list := a.res.Requests
		if kind == "limit" {
			list = a.res.Limits
		}
		if q, ok := list[r]; ok {
			total.Add(q)
			n++
		}
	}
	return total, n
}

// quotaKeys are the names a quota may use for each thing the pod asks for.
var quotaKeys = []struct {
	kind string
	r    corev1.ResourceName
	keys []corev1.ResourceName
}{
	{"request", resCPU, []corev1.ResourceName{corev1.ResourceRequestsCPU, resCPU}},
	{"request", resMemory, []corev1.ResourceName{corev1.ResourceRequestsMemory, resMemory}},
	{"limit", resCPU, []corev1.ResourceName{corev1.ResourceLimitsCPU}},
	{"limit", resMemory, []corev1.ResourceName{corev1.ResourceLimitsMemory}},
}

// checkQuotas holds the pod to what the namespace's quotas hold in all: a pod
// that could never fit fails; one that does not fit while other pods use the
// quota is run again later.
func checkQuotas(ns string, rqs []corev1.ResourceQuota, asks []podAsk) error {
	for _, rq := range rqs {
		for _, q := range quotaKeys {
			total, n := sumAsks(asks, q.kind, q.r)
			if n == 0 {
				continue
			}
			for _, key := range q.keys {
				hard, ok := rq.Status.Hard[key]
				if !ok {
					continue
				}
				if total.Cmp(hard) > 0 {
					return fmt.Errorf("the run's pod asks for %s %s in all (%d containers), which is above the %s that ResourceQuota %q in namespace %s allows "+
						"(its %s); no pod of this size can run there", key, total.String(), n, hard.String(), rq.Name, ns, key)
				}
				used := rq.Status.Used[key]
				left := hard.DeepCopy()
				left.Sub(used)
				if total.Cmp(left) > 0 {
					return infra("the run's pod asks for %s %s in all, but ResourceQuota %q in namespace %s has %s of its %s left (%s is in use by other pods); the step is tried again later",
						key, total.String(), rq.Name, ns, left.String(), hard.String(), used.String())
				}
			}
		}
	}
	return nil
}
