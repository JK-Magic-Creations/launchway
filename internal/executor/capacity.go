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
func quotaWait(pod string, err error) error {
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
			short = append(short, fmt.Sprintf("%s of %s already requested, and this step needs %s more", describeAmount(name, u), describe(name, l), describeAmount(name, r)))
		}
	}
	if len(short) == 0 {
		short = []string{msg}
	}
	return &CapacityError{Code: WaitQuotaFull, Reason: "waiting for room in the step namespace: " + strings.Join(short, "; ")}
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
