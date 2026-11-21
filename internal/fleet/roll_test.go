package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRollUpdatesInBatchesAndNeverExceedsMaxUnavailable(t *testing.T) {
	hs := testHosts(5)
	f, w, _ := newFleet(t, hs)
	rep, err := f.Roll(context.Background(), hs, quick(RollSpec{Release: "v2", Artifact: []byte("bin"), MaxUnavailable: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Updated) != 5 {
		t.Fatalf("updated %v", rep.Updated)
	}
	if w.maxDrain > 2 {
		t.Fatalf("%d hosts were out of rotation at once; the limit is 2", w.maxDrain)
	}
	for _, h := range hs {
		if w.current[h.Name] != "v2" || w.state[h.Member] != "up" {
			t.Fatalf("%s: release %s state %s", h.Name, w.current[h.Name], w.state[h.Member])
		}
	}
}

func TestRollBadReleaseReturnsEveryTouchedHost(t *testing.T) {
	hs := testHosts(4)
	f, w, _ := newFleet(t, hs)
	w.bad["v2"] = true // every host on v2 fails its check
	rep, err := f.Roll(context.Background(), hs, quick(RollSpec{Release: "v2", Artifact: []byte("bin"), MaxUnavailable: 1}))
	if !errors.Is(err, ErrReturned) {
		t.Fatalf("err = %v", err)
	}
	for _, h := range hs {
		if w.current[h.Name] != "v1" || w.state[h.Member] != "up" {
			t.Fatalf("%s is on %s (%s): not returned", h.Name, w.current[h.Name], w.state[h.Member])
		}
	}
	if len(rep.Returned) != 1 || len(rep.Quarantined) != 0 {
		t.Fatalf("returned %v quarantined %v", rep.Returned, rep.Quarantined)
	}
	if strings.Contains(strings.Join(w.cmds, "\n"), "web-2: cat") {
		t.Fatal("the second host was changed after the first failed")
	}
}

func TestRollLaterBatchFailureReturnsEarlierBatches(t *testing.T) {
	hs := testHosts(4)
	f, w, _ := newFleet(t, hs)
	w.failRestart["web-3"] = true
	_, err := f.Roll(context.Background(), hs, quick(RollSpec{Release: "v2", Artifact: []byte("bin"), MaxUnavailable: 1}))
	if !errors.Is(err, ErrReturned) {
		t.Fatalf("err = %v", err)
	}
	for _, h := range hs {
		if w.current[h.Name] != "v1" || w.state[h.Member] != "up" {
			t.Fatalf("%s: %s %s", h.Name, w.current[h.Name], w.state[h.Member])
		}
	}
}

func TestRollRefusals(t *testing.T) {
	hs := testHosts(2)
	f, _, _ := newFleet(t, hs)
	ctx := context.Background()
	if _, err := f.Roll(ctx, hs, RollSpec{Release: "v2", Artifact: []byte("x"), MaxUnavailable: 2}); err == nil || !strings.Contains(err.Error(), "all 2 hosts") {
		t.Fatalf("max_unavailable equal to the fleet: %v", err)
	}
	if _, err := f.Roll(ctx, hs, RollSpec{Release: "../x", Artifact: []byte("x")}); err == nil {
		t.Fatal("a release name with a slash was accepted")
	}
	f.Traffic = nil
	if _, err := f.Roll(ctx, hs, RollSpec{Release: "v2", Artifact: []byte("x")}); err == nil || !strings.Contains(err.Error(), "traffic provider") {
		t.Fatalf("no traffic provider: %v", err)
	}
	f2, w2, _ := newFleet(t, hs)
	w2.bad["v1"] = true
	if _, err := f2.Roll(ctx, hs, RollSpec{Release: "v2", Artifact: []byte("x")}); err == nil || !strings.Contains(err.Error(), "not healthy before") {
		t.Fatalf("unhealthy fleet: %v", err)
	}
	if len(w2.cmds) != 0 {
		t.Fatal("something was done to an unhealthy fleet")
	}
}

func TestRollRefusesTheStationsOwnMachine(t *testing.T) {
	hs := testHosts(2)
	f, w, _ := newFleet(t, hs)
	f.Refuse = func(h Host) error {
		if h.Name == "web-2" {
			return errors.New("this is the machine the Station runs on")
		}
		return nil
	}
	if _, err := f.Roll(context.Background(), hs, RollSpec{Release: "v2", Artifact: []byte("x")}); err == nil {
		t.Fatal("accepted")
	}
	if len(w.cmds) != 0 {
		t.Fatal("something ran before the refusal")
	}
}

func TestPathfinderStepsAndWeightRollback(t *testing.T) {
	stable, fresh := testHosts(2), testHosts(4)[2:]
	all := append(append([]Host{}, stable...), fresh...)
	f, w, _ := newFleet(t, all)
	var seen []int
	spec := PathfinderSpec{Release: "v2", Artifact: []byte("x"), Healthy: 1, CheckEvery: 1, CheckWithin: 50e6, DrainMax: 50e6,
		Verify: func(_ context.Context, p int) error { seen = append(seen, p); return nil }}
	rep, err := f.Pathfinder(context.Background(), stable, fresh, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || seen[0] != 10 || seen[2] != 100 || rep.Reached != 100 {
		t.Fatalf("steps %v reached %d", seen, rep.Reached)
	}
	if w.weight["app/web-1"] != 0 || w.weight["app/web-3"] != 100 {
		t.Fatalf("final weights %v", w.weight)
	}
	// A failed verification at 50 percent puts the weights back.
	f2, w2, _ := newFleet(t, all)
	spec.Verify = func(_ context.Context, p int) error {
		if p == 50 {
			return errors.New("error rate 4%")
		}
		return nil
	}
	rep, err = f2.Pathfinder(context.Background(), stable, fresh, spec)
	if !errors.Is(err, ErrPathfinderReturned) || !rep.Returned {
		t.Fatalf("err %v rep %+v", err, rep)
	}
	if w2.weight["app/web-3"] != 0 || w2.weight["app/web-1"] == 0 {
		t.Fatalf("weights not put back: %v", w2.weight)
	}
	// A Pathfinder with nothing to verify with is refused.
	spec.Verify = nil
	if _, err := f2.Pathfinder(context.Background(), stable, fresh, spec); err == nil {
		t.Fatal("no verification accepted")
	}
}

func TestWeightsFor(t *testing.T) {
	for _, tc := range []struct{ s, n, p int }{{2, 2, 10}, {2, 2, 50}, {3, 1, 10}, {4, 4, 25}} {
		sw, nw, err := WeightsFor(tc.s, tc.n, tc.p)
		if err != nil {
			t.Fatal(err)
		}
		share := float64(tc.n*nw) / float64(tc.n*nw+tc.s*sw) * 100
		if d := share - float64(tc.p); d > 0.01 || d < -0.01 {
			t.Errorf("%+v: weights %d/%d give %.2f percent", tc, sw, nw, share)
		}
	}
}
