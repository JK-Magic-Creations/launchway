package deploy

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func b(v bool) *bool { return &v }
func n(v int) *int   { return &v }

func okApp() *App {
	return &App{Found: true, Sync: "Synced", Health: "Healthy", Revision: "abcdef1234567", Contains: b(true), Automated: true}
}

func okRollout() *Rollout {
	return &Rollout{Found: true, Phase: "Healthy", ObservedKnown: true, CurrentPodHash: "h2", StableRS: "h2", Step: n(4), Steps: 4,
		CanaryWeight: n(100), Replicas: 2, Ready: 2, Updated: 2, Available: 2}
}

var rolloutSpec = Spec{Kind: "deploy", Commit: "abc", Health: "application", Windows: "wait",
	Rollout: &pipeline.RolloutRef{Name: "demo", Namespace: "lw-demo", Until: "healthy"}}

func TestEvaluateRules(t *testing.T) {
	appOnly := Spec{Kind: "deploy", Commit: "abc", Health: "application", Windows: "wait"}
	resSpec := Spec{Kind: "deploy", Commit: "abc", Health: "resources", Windows: "wait",
		Resources: []pipeline.Resource{{Group: "argoproj.io", Kind: "Rollout", Name: "demo", Namespace: "lw-demo"}}}
	cases := []struct {
		name    string
		spec    Spec
		snap    func() Snapshot
		outcome Outcome
		phase   string
		detail  string
	}{
		{"unreachable", appOnly, func() Snapshot { return Snapshot{Error: "dial tcp 10.0.0.1:443: i/o timeout"} }, Continue, PhaseUnobserved, "i/o timeout"},
		{"nothing reported", appOnly, func() Snapshot { return Snapshot{} }, Continue, PhaseUnobserved, ""},
		{"app missing", appOnly, func() Snapshot { return Snapshot{App: &App{}} }, Continue, PhaseAppMissing, ""},
		{"wrong repository", appOnly, func() Snapshot { return Snapshot{App: &App{Found: true, SourceError: "no source from x"}} }, Failed, PhaseSourceBad, "no source"},
		{"revision unknown", appOnly, func() Snapshot { a := okApp(); a.Contains = nil; return Snapshot{App: a} }, Continue, PhaseSyncing, "waiting for Argo CD to see"},
		{"old revision, healthy: stale is never a pass", appOnly, func() Snapshot { a := okApp(); a.Contains = b(false); return Snapshot{App: a} }, Continue, PhaseSyncing, "it is at abcdef1234"},
		{"our sync failed", appOnly, func() Snapshot {
			a := okApp()
			a.Contains, a.Sync = b(false), "OutOfSync"
			a.Operation = &Operation{Phase: "Failed", Message: "one or more objects failed to apply: Service is invalid", Revision: "def", Contains: b(true)}
			return Snapshot{App: a}
		}, Failed, PhaseSyncFailed, "Service is invalid"},
		{"an older sync failed: not ours", appOnly, func() Snapshot {
			a := okApp()
			a.Contains = b(false)
			a.Operation = &Operation{Phase: "Failed", Message: "old failure", Revision: "old", Contains: b(false)}
			return Snapshot{App: a}
		}, Continue, PhaseSyncing, ""},
		{"window holds sync, wait", appOnly, func() Snapshot {
			a := okApp()
			a.Contains = b(false)
			a.Operation = &Operation{Phase: "Running", Message: "Sync operation blocked by sync window"}
			return Snapshot{App: a}
		}, Continue, PhaseBlocked, "waiting until a window allows it"},
		{"window holds sync, fail", Spec{Kind: "deploy", Commit: "abc", Health: "application", Windows: "fail"}, func() Snapshot {
			a := okApp()
			a.Contains = b(false)
			a.Operation = &Operation{Phase: "Running", Message: "Sync operation blocked by sync window"}
			return Snapshot{App: a}
		}, Failed, PhaseBlocked, "sync.windows says fail"},
		{"synced, progressing", appOnly, func() Snapshot { a := okApp(); a.Health = "Progressing"; return Snapshot{App: a} }, Continue, PhaseProgress, "Progressing"},
		{"out of sync at our revision", appOnly, func() Snapshot { a := okApp(); a.Sync = "OutOfSync"; return Snapshot{App: a} }, Continue, PhaseSyncing, "OutOfSync"},
		{"degraded app", appOnly, func() Snapshot {
			a := okApp()
			a.Health, a.HealthMessage = "Degraded", "Deployment exceeded its progress deadline"
			return Snapshot{App: a}
		}, Failed, PhaseDegraded, "progress deadline"},
		{"healthy app", appOnly, func() Snapshot { return Snapshot{App: okApp()} }, Succeeded, PhaseHealthy, "synced at abcdef1234"},
		{"rollout healthy", rolloutSpec, func() Snapshot { return Snapshot{App: okApp(), Rollout: okRollout()} }, Succeeded, PhaseHealthy, "rollout healthy on h2"},
		{"rollout not observed yet", rolloutSpec, func() Snapshot {
			r := okRollout()
			r.ObservedKnown = false
			return Snapshot{App: okApp(), Rollout: r}
		}, Continue, PhaseRollout, "rollout controller"},
		{"rollout stable is the old template", rolloutSpec, func() Snapshot {
			r := okRollout()
			r.StableRS = "h1"
			r.Phase = "Progressing"
			return Snapshot{App: okApp(), Rollout: r}
		}, Continue, PhaseRollout, "Progressing"},
		{"rollout paused at a timed step", rolloutSpec, func() Snapshot {
			a := okApp()
			a.Health = "Suspended"
			r := okRollout()
			r.Phase, r.Paused, r.PauseReasons, r.Step, r.CanaryWeight, r.StableRS = "Paused", true, []string{"CanaryPauseStep"}, n(1), n(50), "h1"
			return Snapshot{App: a, Rollout: r}
		}, Continue, PhasePaused, "at step 2 of 4 (CanaryPauseStep), canary at 50%"},
		{"analysis running", rolloutSpec, func() Snapshot {
			a := okApp()
			a.Health = "Progressing"
			r := okRollout()
			r.Phase, r.StableRS, r.Step = "Progressing", "h1", n(2)
			return Snapshot{App: a, Rollout: r, Analysis: []AnalysisRun{{Name: "demo-h2-2", Phase: "Running", PodHash: "h2"}}}
		}, Continue, PhaseRollout, "analysis running: demo-h2-2"},
		{"rollout aborted by analysis", rolloutSpec, func() Snapshot {
			a := okApp()
			a.Health = "Degraded"
			r := okRollout()
			r.Phase, r.Aborted, r.Message, r.StableRS = "Degraded", true, "RolloutAborted: Rollout aborted update to revision 2", "h1"
			return Snapshot{App: a, Rollout: r, Analysis: []AnalysisRun{
				{Name: "demo-h2-2", Phase: "Failed", PodHash: "h2", Metrics: []Metric{{Name: "verdict", Phase: "Failed", Count: 1, Failed: 1}}},
				{Name: "demo-h1-2", Phase: "Failed", PodHash: "h1", Metrics: []Metric{{Name: "other", Phase: "Failed"}}},
			}}
		}, Failed, PhaseAborted, "analysis demo-h2-2 failed: metric verdict failed (1 failed, 0 errors, 0 inconclusive of 1)"},
		{"rollout degraded (crash loop)", rolloutSpec, func() Snapshot {
			a := okApp()
			a.Health = "Progressing"
			r := okRollout()
			r.Phase, r.Message, r.StableRS = "Degraded", "ProgressDeadlineExceeded: ReplicaSet demo-h2 has timed out progressing.", "h1"
			return Snapshot{App: a, Rollout: r}
		}, Failed, PhaseDegraded, "timed out progressing"},
		{"rollout missing", rolloutSpec, func() Snapshot { return Snapshot{App: okApp(), Rollout: &Rollout{}} }, Continue, PhaseRollout, "waiting for rollout lw-demo/demo"},
		{"resources: rollout healthy, app degraded elsewhere", Spec{Kind: "deploy", Commit: "abc", Health: "resources", Windows: "wait", Resources: resSpec.Resources}, func() Snapshot {
			a := okApp()
			a.Sync, a.Health = "OutOfSync", "Degraded"
			a.Resources = []Resource{{Group: "argoproj.io", Kind: "Rollout", Namespace: "lw-demo", Name: "demo", Found: true, Sync: "Synced", Health: "Healthy"}}
			return Snapshot{App: a}
		}, Succeeded, PhaseHealthy, ""},
		{"resources: one not synced", resSpec, func() Snapshot {
			a := okApp()
			a.Resources = []Resource{{Group: "argoproj.io", Kind: "Rollout", Namespace: "lw-demo", Name: "demo", Found: true, Sync: "OutOfSync", Health: "Healthy"}}
			return Snapshot{App: a}
		}, Continue, PhaseSyncing, "Rollout demo is OutOfSync"},
		{"resources: missing", resSpec, func() Snapshot { return Snapshot{App: okApp()} }, Continue, PhaseSyncing, "to appear"},
		{"resources: degraded", resSpec, func() Snapshot {
			a := okApp()
			a.Resources = []Resource{{Group: "argoproj.io", Kind: "Rollout", Namespace: "lw-demo", Name: "demo", Found: true, Sync: "Synced", Health: "Degraded", Message: "aborted"}}
			return Snapshot{App: a}
		}, Failed, PhaseDegraded, "Rollout demo is Degraded: aborted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.snap()
			v := Evaluate(tc.spec, s)
			if v.Outcome != tc.outcome || v.Phase != tc.phase || !strings.Contains(v.Detail, tc.detail) {
				t.Fatalf("got %+v\nwant %s %s %q", v, tc.outcome, tc.phase, tc.detail)
			}
			// The same snapshot always gives the same verdict.
			if again := Evaluate(tc.spec, s); again != v {
				t.Fatalf("not deterministic: %+v then %+v", v, again)
			}
		})
	}
}
