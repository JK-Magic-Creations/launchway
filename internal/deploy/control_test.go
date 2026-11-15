package deploy

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func intp(n int) *int { return &n }

// The captured rollouts reduce to what the control decisions need.
func TestGoldenPauses(t *testing.T) {
	ind := ReduceRollout(load(t, "rollout-paused-indefinite.json"), nil)
	if r, ok := IndefinitePause(ind); !ok || r != "CanaryPauseStep" {
		t.Fatalf("indefinite pause not seen: %+v", ind)
	}
	timed := ReduceRollout(load(t, "rollout-paused-timed.json"), nil)
	if _, ok := IndefinitePause(timed); ok || timed.PauseUntil == 0 {
		t.Fatalf("a timed pause taken for indefinite: %+v", timed)
	}
	ab := ReduceRollout(load(t, "rollout-aborted-manual.json"), nil)
	if _, ok := IndefinitePause(ab); ok || !ab.Aborted {
		t.Fatalf("aborted: %+v", ab)
	}
	// A runner too old to say pause_indefinite: the step name tells.
	old := *ind
	old.PauseIndefinite = false
	if _, ok := IndefinitePause(&old); !ok {
		t.Fatal("fallback on the step name failed")
	}
}

func pausedSnap() Snapshot {
	yes := true
	return Snapshot{App: &App{Found: true, Sync: "Synced", Health: "Suspended", Contains: &yes, Revision: "abc"},
		Rollout: &Rollout{Found: true, Phase: "Paused", ObservedKnown: true, CurrentPodHash: "h2", StableRS: "h1", Step: intp(1), Steps: 3,
			StepNames: []string{"weight 50%", "pause", "weight 100%"}, Paused: true, PauseReasons: []string{"CanaryPauseStep"}, PauseIndefinite: true}}
}

func TestAvailableAndDecide(t *testing.T) {
	s := pausedSnap()
	x, why := Available(Promote, s, "h2")
	if why != "" || x.PodHash != "h2" || *x.Step != 1 {
		t.Fatalf("promote at the pause: %+v %q", x, why)
	}
	if _, why := Available(Retry, s, "h2"); why == "" {
		t.Fatal("retry offered on a rollout that is not aborted")
	}
	if _, why := Available(Promote, s, "h1"); !strings.Contains(why, "another version") {
		t.Fatalf("another deployment's hash: %q", why)
	}
	ro := *s.Rollout
	for _, tc := range []struct {
		name   string
		action string
		mut    func(r *Rollout)
		apply  bool
		result string
	}{
		{"promote at the pause", Promote, func(*Rollout) {}, true, ""},
		{"promote, rollout past the step", Promote, func(r *Rollout) { r.Step, r.PauseReasons = intp(2), nil }, false, AlreadyApplied},
		{"promote, pause lifted, not yet moved", Promote, func(r *Rollout) { r.PauseReasons = nil }, false, AlreadyApplied},
		{"promote, superseded hash", Promote, func(r *Rollout) { r.CurrentPodHash = "h3" }, false, NotApplied},
		{"promote, aborted", Promote, func(r *Rollout) { r.Aborted = true }, false, NotApplied},
		{"promote, complete", Promote, func(r *Rollout) { r.Phase, r.StableRS = "Healthy", "h2" }, false, AlreadyApplied},
		{"abort", Abort, func(*Rollout) {}, true, ""},
		{"abort twice", Abort, func(r *Rollout) { r.Aborted = true }, false, AlreadyApplied},
		{"abort a completed rollout", Abort, func(r *Rollout) { r.Phase, r.StableRS = "Healthy", "h2" }, false, NotApplied},
		{"abort another version", Abort, func(r *Rollout) { r.CurrentPodHash = "h3" }, false, NotApplied},
		{"retry after abort", Retry, func(r *Rollout) { r.Aborted = true }, true, ""},
		{"retry, not aborted", Retry, func(*Rollout) {}, false, AlreadyApplied},
		{"promote-full", PromoteFull, func(*Rollout) {}, true, ""},
		{"promote-full under way", PromoteFull, func(r *Rollout) { r.PromoteFull = true }, false, AlreadyApplied},
	} {
		r := ro
		tc.mut(&r)
		d := Decide(tc.action, Expect{PodHash: "h2", Step: intp(1)}, &r)
		if d.Apply != tc.apply || d.Result != tc.result {
			t.Errorf("%s: %+v", tc.name, d)
		}
	}
	if p := StatusPatch(Promote); len(p) != 1 || p["pauseConditions"] != nil {
		t.Fatalf("promote patch %v", p)
	}
}

func TestPauseVerdicts(t *testing.T) {
	ref := &pipeline.RolloutRef{Name: "shop", Namespace: "shop", Until: "healthy"}
	spec := Spec{Kind: "deploy", Commit: "c", Health: "application", Windows: "wait", Rollout: ref}
	s := pausedSnap()
	if v := Evaluate(spec, s); v.Outcome != Continue || v.Phase != PhasePaused || !strings.Contains(v.Detail, "waiting for someone to promote") {
		t.Fatalf("wait: %+v", v)
	}
	ref.Until = "paused"
	if v := Evaluate(spec, s); v.Outcome != Succeeded || v.Phase != PhasePaused {
		t.Fatalf("until paused: %+v", v)
	}
	ref.Until, ref.Pauses = "healthy", "promote"
	if v := Evaluate(spec, s); v.Outcome != Command || v.Action != Promote || v.Expect.PodHash != "h2" {
		t.Fatalf("pauses promote: %+v", v)
	}
	ref.Pauses = "fail"
	if v := Evaluate(spec, s); v.Outcome != Failed {
		t.Fatalf("pauses fail: %+v", v)
	}
	// An inconclusive analysis is never promoted by itself.
	ref.Pauses = "promote"
	s.Rollout.PauseReasons = []string{"InconclusiveAnalysis"}
	if v := Evaluate(spec, s); v.Outcome != Continue {
		t.Fatalf("inconclusive: %+v", v)
	}
	// A rollout step that promoted step 1 with until: paused ends at a later
	// pause, not at the same one.
	rs := Spec{Kind: "rollout", Commit: "c", Health: "application", Windows: "wait", Rollout: &pipeline.RolloutRef{Name: "shop", Namespace: "shop"}, Until: "paused"}
	x := &Expect{PodHash: "h2", Step: intp(1)}
	if v := AfterAction(rs, Promote, x, pausedSnap()); v.Outcome != Continue {
		t.Fatalf("same pause: %+v", v)
	}
	later := pausedSnap()
	later.Rollout.Step = intp(2)
	if v := AfterAction(rs, Promote, x, later); v.Outcome != Succeeded {
		t.Fatalf("later pause: %+v", v)
	}
	// A rollback that aborted first waits for the revert, not fails.
	rb := Spec{Kind: "rollback", Commit: "c", Health: "application", Windows: "wait", Rollout: ref}
	ab := pausedSnap()
	ab.App.Health, ab.Rollout.Aborted, ab.Rollout.Phase = "Degraded", true, "Degraded"
	if v := Evaluate(rb, ab); v.Outcome != Continue || v.Phase != PhaseAborted {
		t.Fatalf("rollback over an abort: %+v", v)
	}
	if !AbortedOn(ab, "h2") || AbortedOn(ab, "h1") {
		t.Fatal("AbortedOn")
	}
}

// Seen on kind: Argo CD reports the new revision while the application is
// still OutOfSync, and the rollout, not yet changed, is observed at its old
// generation: its pod template is then not the deployment's.
func TestOurPodHashWaitsForSync(t *testing.T) {
	s := pausedSnap()
	s.App.Sync = "OutOfSync"
	if h := OurPodHash(s); h != "" {
		t.Fatalf("hash %q before the sync", h)
	}
	s.App.Resources = []Resource{{Kind: "Rollout", Name: "shop", Found: true, Sync: "Synced"}}
	if OurPodHash(s) != "h2" {
		t.Fatal("a synced Rollout resource is enough")
	}
}
