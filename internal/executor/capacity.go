package executor

// Capacity: a step that cannot start because there is no room right now (the
// step namespace's quota is used up, or the cluster has no node with space)
// is not a failure. It waits, and the server queues it again with a pause.
// A step that asks for more than the whole quota can never start, so that one
// is a failure, at once, with the numbers.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Codes of the waits a runner reports; the server shows them beside the
// sentence and an API client can test for them.
const (
	WaitQuotaFull   = "quota_full"   // the step namespace's ResourceQuota has no room for the pod
	WaitClusterFull = "cluster_full" // no node has room for the pod
)

// ErrCapacity marks a step that cannot start for lack of room that will
// probably come back: try it again later.
var ErrCapacity = errors.New("there is no room to start the step yet")

// CapacityError is the error for a step that has to wait for room.
type CapacityError struct {
	Code   string
	Reason string
}

func (e *CapacityError) Error() string        { return e.Reason }
func (e *CapacityError) Is(target error) bool { return target == ErrCapacity }

// AsCapacity returns the capacity wait in err, if it is one.
func AsCapacity(err error) (*CapacityError, bool) {
	var c *CapacityError
	if errors.As(err, &c) {
		return c, true
	}
	return nil, false
}

var quotaText = regexp.MustCompile(`requested: (.*?), used: (.*?), limited: (.*)$`)

// quotaWait turns the API server's "exceeded quota" refusal into a wait for
// room, or into a failure when the pod asks for more than the whole quota.
//
// held is what the run's own pods in the namespace already hold, or nil when
// it is not known; the sentence says it, so that a wait for room is never
// mistaken for a step that ran out of memory.
func quotaWait(pod string, err error, held *runHold) error {
	msg := err.Error()
	if i := strings.Index(msg, "exceeded quota"); i >= 0 {
		msg = msg[i:]
	}
	m := quotaText.FindStringSubmatch(msg)
	if m == nil {
		return &CapacityError{Code: WaitQuotaFull, Reason: fmt.Sprintf("waiting for room in the step namespace: its quota is full, so pod %s cannot start yet (%s)", pod, msg)}
	}
	req, used, limit := quantities(m[1]), quantities(m[2]), quantities(m[3])
	var short []string
	for _, name := range sortedNames(req) {
		l, ok := limit[name]
		if !ok {
			continue
		}
		r := req[name]
		if r.Cmp(l) > 0 {
			return cannotStart("this step asks for %s, but the whole step namespace's quota allows only %s, so it can never start: ask for less, or raise the quota (%s)",
				describe(name, r), describe(name, l), name)
		}
		u := used[name]
		sum := u.DeepCopy()
		sum.Add(r)
		if sum.Cmp(l) > 0 {
			need := sum.DeepCopy()
			need.Sub(l)
			short = append(short, fmt.Sprintf("%s the namespace allows %s and %s is reserved in all; this pod needs %s more, so %s must be released first",
				held.sentence(name), describe(name, l), describeAmount(name, u), describeAmount(name, r), describeAmount(name, need)))
		}
	}
	if len(short) == 0 {
		short = []string{msg}
	}
	return &CapacityError{Code: WaitQuotaFull, Reason: "waiting for room in the step namespace: " + strings.Join(short, "; ") +
		". This is what is reserved by the pods, not what a step uses: nothing ran out of memory"}
}

// runHold is what one run's pods in the step namespace have reserved: per
// quota resource name (such as requests.memory), the sum, and how many pods
// hold any of it.
type runHold struct {
	total map[string]resource.Quantity
	pods  map[string]int
}

// holdOf adds up what the pods ask of the quota. A pod that has ended holds
// nothing. Each pod counts what its containers ask for, as the quota does.
func holdOf(pods []corev1.Pod) *runHold {
	h := &runHold{total: map[string]resource.Quantity{}, pods: map[string]int{}}
	for _, p := range pods {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		add := func(name string, q resource.Quantity) {
			t := h.total[name]
			t.Add(q)
			h.total[name] = t
			h.pods[name]++
		}
		add("pods", *resource.NewQuantity(1, resource.DecimalSI))
		sums := map[string]resource.Quantity{}
		for _, c := range p.Spec.Containers {
			for r, q := range c.Resources.Requests {
				t := sums["requests."+string(r)]
				t.Add(q)
				sums["requests."+string(r)] = t
			}
			for r, q := range c.Resources.Limits {
				t := sums["limits."+string(r)]
				t.Add(q)
				sums["limits."+string(r)] = t
			}
		}
		for name, q := range sums {
			add(name, q)
			if strings.HasPrefix(name, "requests.") { // a quota may name the request without the prefix
				add(strings.TrimPrefix(name, "requests."), q)
			}
		}
	}
	return h
}

// sentence says what the run holds of a quota resource ("this run has
// reserved 8448Mi memory across 2 pods;"), or that it is not known.
func (h *runHold) sentence(name string) string {
	if h == nil {
		return "the run's own share is not known:"
	}
	q, ok := h.total[name]
	if !ok || q.IsZero() {
		return "this run holds none of it;"
	}
	n := h.pods[name]
	pl := "pods"
	if n == 1 {
		pl = "pod"
	}
	return fmt.Sprintf("this run has reserved %s across %d %s;", describeAmount(name, q), n, pl)
}

// quantities reads "requests.cpu=500m,requests.memory=1Gi".
func quantities(text string) map[string]resource.Quantity {
	out := map[string]resource.Quantity{}
	for _, f := range strings.Split(text, ",") {
		name, val, ok := strings.Cut(strings.TrimSpace(f), "=")
		if !ok {
			continue
		}
		if q, err := resource.ParseQuantity(strings.TrimSpace(val)); err == nil {
			out[strings.TrimSpace(name)] = q
		}
	}
	return out
}

func sortedNames(m map[string]resource.Quantity) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// describeAmount writes a quota quantity in words people use: "6.2 CPU",
// "3Gi memory", "4 pods".
func describeAmount(name string, q resource.Quantity) string {
	switch {
	case strings.HasSuffix(name, "cpu"):
		return fmt.Sprintf("%.4g CPU", float64(q.MilliValue())/1000)
	case strings.HasSuffix(name, "memory"), strings.HasSuffix(name, "ephemeral-storage"):
		kind := "memory"
		if strings.HasSuffix(name, "ephemeral-storage") {
			kind = "scratch storage"
		}
		return q.String() + " " + kind
	case name == "pods":
		return q.String() + " pods"
	}
	return q.String() + " " + name
}

// describe is describeAmount for the limit: "8 CPU" as the total.
func describe(name string, q resource.Quantity) string { return describeAmount(name, q) }
