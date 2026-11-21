package fleet

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PathfinderSpec brings a new group of hosts into service by weights. The
// Stable group keeps serving the release it has; the New group gets the new
// release while it receives no traffic, is tested directly, and is then given
// 10 percent, 50 percent, 100 percent of the traffic, with verification
// between steps. A failed verification puts the weights back at once.
type PathfinderSpec struct {
	Release  string
	Artifact []byte
	// Steps are the new group's shares in percent, rising; default 10, 50, 100.
	Steps []int
	// Bake is how long each step is watched before Verify judges it.
	Bake time.Duration
	// Verify judges a step (it is called at the end of the bake, with the
	// share just reached). Required: a Pathfinder with no verification is
	// refused.
	Verify func(ctx context.Context, percent int) error
	// Watch is the Overwatch after 100 percent, verified once at its end;
	// the stable group stays in place, at weight 0, for the way back.
	Watch                   time.Duration
	Healthy                 int
	CheckEvery, CheckWithin time.Duration
	DrainMax                time.Duration
}

// PathfinderReport says what a Pathfinder did.
type PathfinderReport struct {
	Release  string         `json:"release"`
	Reached  int            `json:"reached_percent"`
	Failure  string         `json:"failure,omitempty"`
	Returned bool           `json:"returned"`
	Weights  map[string]int `json:"final_weights"`
	Install  *Report        `json:"install,omitempty"`
}

// ErrPathfinderReturned means a step failed and the weights went back.
var ErrPathfinderReturned = errors.New("the Pathfinder failed and the weights were put back")

// WeightsFor returns the weight of one host of each group so that the new
// group gets the share (percent) of new connections, in integers of at most
// 256 (the balancer's range).
func WeightsFor(stable, newHosts, percent int) (stableW, newW int, err error) {
	if percent < 0 || percent > 100 || stable < 1 || newHosts < 1 {
		return 0, 0, fmt.Errorf("a share of %d percent between %d stable and %d new hosts cannot be set", percent, stable, newHosts)
	}
	switch percent {
	case 0:
		return 100, 0, nil
	case 100:
		return 0, 100, nil
	}
	// share = N*nw / (N*nw + S*sw)  with nw = p*S and sw = (100-p)*N
	nw, sw := percent*stable, (100-percent)*newHosts
	g := gcd(nw, sw)
	nw, sw = nw/g, sw/g
	if nw > 256 || sw > 256 {
		return 0, 0, fmt.Errorf("a share of %d percent between %d stable and %d new hosts needs weights above 256", percent, stable, newHosts)
	}
	return sw, nw, nil
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// pathRun is one Pathfinder in progress.
type pathRun struct {
	f             *Fleet
	stable, fresh []Host
	rep           *PathfinderReport
}

func (f *Fleet) pathPreflight(stable, fresh []Host, spec *PathfinderSpec) error {
	if f.Traffic == nil {
		return errors.New("a Pathfinder moves traffic by weights and needs a traffic provider")
	}
	if spec.Verify == nil {
		return errors.New("a Pathfinder needs a verification between steps; it will not move traffic on faith")
	}
	if len(spec.Steps) == 0 {
		spec.Steps = []int{10, 50, 100}
	}
	last := 0
	for _, p := range spec.Steps {
		if p <= last || p > 100 {
			return fmt.Errorf("steps must rise and end at most at 100: %v", spec.Steps)
		}
		last = p
	}
	if len(stable) == 0 || len(fresh) == 0 {
		return errors.New("a Pathfinder needs a stable group and a new group, each with at least one host")
	}
	for _, h := range append(append([]Host{}, stable...), fresh...) {
		if err := h.Validate(); err != nil {
			return err
		}
		if h.Member == "" {
			return fmt.Errorf("host %s has no load balancer member", h.Name)
		}
		if f.Refuse != nil {
			if err := f.Refuse(h); err != nil {
				return err
			}
		}
	}
	return nil
}

// setW gives the new group the share (percent) of traffic.
func (r *pathRun) setW(ctx context.Context, percent int) error {
	sw, nw, err := WeightsFor(len(r.stable), len(r.fresh), percent)
	if err != nil {
		return err
	}
	// Raise the side that gains first, so capacity never dips.
	for _, h := range r.fresh {
		if err := r.f.Traffic.SetWeight(ctx, h.Member, nw); err != nil {
			return err
		}
		r.rep.Weights[h.Name] = nw
	}
	for _, h := range r.stable {
		if err := r.f.Traffic.SetWeight(ctx, h.Member, sw); err != nil {
			return err
		}
		r.rep.Weights[h.Name] = sw
	}
	w := map[string]int{}
	for k, v := range r.rep.Weights {
		w[k] = v
	}
	if r.f.Events != nil {
		r.f.Events(Event{Time: time.Now(), Phase: "weights", Message: fmt.Sprintf("new group at %d percent", percent), Weights: w})
	}
	return nil
}

// back puts the weights back at once and reports the failure.
func (r *pathRun) back(ctx context.Context, why error) (*PathfinderReport, error) {
	r.rep.Failure = why.Error()
	r.f.Events.emit("", "failed", "%v; putting the weights back", why)
	if err := r.setW(context.WithoutCancel(ctx), 0); err != nil {
		r.f.Events.emit("", "quarantine", "the weights could not be put back: %v", err)
		return r.rep, fmt.Errorf("%w; and putting the weights back failed: %v", ErrPathfinderReturned, err)
	}
	r.rep.Returned, r.rep.Reached = true, 0
	return r.rep, fmt.Errorf("%w: %v", ErrPathfinderReturned, why)
}

// Pathfinder runs the plan.
func (f *Fleet) Pathfinder(ctx context.Context, stable, fresh []Host, spec PathfinderSpec) (*PathfinderReport, error) {
	rep := &PathfinderReport{Release: spec.Release, Weights: map[string]int{}}
	if err := f.pathPreflight(stable, fresh, &spec); err != nil {
		return rep, err
	}
	r := &pathRun{f: f, stable: stable, fresh: fresh, rep: rep}
	// The new group takes no traffic while it is changed and tested.
	for _, h := range fresh {
		if err := f.Traffic.SetWeight(ctx, h.Member, 0); err != nil {
			return rep, err
		}
	}
	if err := r.setW(ctx, 0); err != nil {
		return rep, err
	}
	// Change the new group in place while it takes nothing (weight 0): the
	// whole group as one batch, drained first.
	ir, err := f.rollGroup(ctx, fresh, RollSpec{Release: spec.Release, Artifact: spec.Artifact, MaxUnavailable: len(fresh),
		DrainMax: spec.DrainMax, Healthy: spec.Healthy, CheckEvery: spec.CheckEvery, CheckWithin: spec.CheckWithin})
	rep.Install = ir
	if err != nil {
		// rollGroup returned the new group; the stable group was never touched.
		_ = r.setW(ctx, 0)
		rep.Failure, rep.Returned = err.Error(), true
		return rep, fmt.Errorf("%w: %v", ErrPathfinderReturned, err)
	}
	for _, p := range spec.Steps {
		f.Events.emit("", "weights", "moving the new group to %d percent", p)
		if err := r.setW(ctx, p); err != nil {
			return r.back(ctx, err)
		}
		rep.Reached = p
		select {
		case <-time.After(spec.Bake):
		case <-ctx.Done():
			return r.back(ctx, ctx.Err())
		}
		if err := spec.Verify(ctx, p); err != nil {
			return r.back(ctx, fmt.Errorf("verification at %d percent failed: %w", p, err))
		}
		f.Events.emit("", "verify", "verified at %d percent", p)
	}
	if spec.Watch > 0 {
		f.Events.emit("", "watch", "Overwatch for %s with the old group kept at weight 0", spec.Watch)
		select {
		case <-time.After(spec.Watch):
		case <-ctx.Done():
		}
		if err := spec.Verify(ctx, 100); err != nil {
			return r.back(ctx, fmt.Errorf("overwatch failed: %w", err))
		}
	}
	f.Events.emit("", "done", "release %s serves 100 percent; the old group is kept at weight 0 for the way back", spec.Release)
	return rep, nil
}

// rollGroup is Roll without the balancer-wide capacity rule: the group is
// out of rotation by weight already.
func (f *Fleet) rollGroup(ctx context.Context, hosts []Host, spec RollSpec) (*Report, error) {
	spec.defaults()
	rep := &Report{Release: spec.Release, Previous: map[string]string{}}
	r := &roller{f: f, spec: spec, state: map[string]*hostState{}, rep: rep}
	for _, h := range hosts {
		if err := f.Check.Healthy(ctx, f.CheckURL(h)); err != nil {
			return rep, fmt.Errorf("host %s is not healthy before the update (%v); nothing was changed", h.Name, err)
		}
	}
	if err := r.batch(ctx, hosts); err != nil {
		rep.Failure = err.Error()
		r.returnAll(context.WithoutCancel(ctx), hosts)
		return rep, err
	}
	return rep, nil
}
