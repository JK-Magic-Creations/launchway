package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// The files in testdata are real objects captured from Argo CD v3.3.7 and
// Argo Rollouts v1.8.3 on a kind cluster (deploy/test/argo), in the states
// their names say.

func load(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func items(t *testing.T, name string) []map[string]any {
	var out []map[string]any
	for _, it := range load(t, name)["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

const gitopsURL = "http://git.lw-git.svc:8080/lwtest/gitops.git"

// containsAll says every revision includes the commit: the deployment's
// commit is synced.
func containsAll(string) (bool, error)  { return true, nil }
func containsNone(string) (bool, error) { return false, nil }

func TestGoldenApplications(t *testing.T) {
	spec := Spec{Kind: "deploy", Commit: "c", Health: "application", Windows: "wait"}
	for _, tc := range []struct {
		file     string
		contains ContainsFunc
		windows  string
		outcome  Outcome
		phase    string
		detail   string
	}{
		{"application-synced-healthy.json", containsAll, "wait", Succeeded, PhaseHealthy, "synced at 86c5f3c31b"},
		{"application-synced-healthy.json", containsNone, "wait", Continue, PhaseSyncing, "it is at 86c5f3c31b"},
		{"application-outofsync.json", containsAll, "wait", Continue, PhaseSyncing, "OutOfSync"},
		{"application-operation-running.json", containsNone, "wait", Continue, PhaseSyncing, "a sync is running"},
		{"application-sync-failed.json", containsAll, "wait", Failed, PhaseSyncFailed, "one or more objects failed to apply"},
		{"application-operation-blocked-window.json", containsNone, "wait", Continue, PhaseBlocked, "blocked by sync window"},
		{"application-operation-blocked-window.json", containsNone, "fail", Failed, PhaseBlocked, "sync.windows says fail"},
		{"application-degraded.json", containsAll, "wait", Failed, PhaseDegraded, "Degraded"},
		{"application-progressing.json", containsAll, "wait", Continue, PhaseProgress, "Progressing"},
		{"application-suspended-timed-pause.json", containsAll, "wait", Continue, PhaseProgress, "Suspended"},
		{"application-multi-source.json", containsAll, "wait", Succeeded, PhaseHealthy, ""},
	} {
		t.Run(tc.file+"/"+tc.phase, func(t *testing.T) {
			s := spec
			s.Windows = tc.windows
			app := ReduceApplication(load(t, tc.file), s, gitopsURL, tc.contains)
			v := Evaluate(s, Snapshot{App: app})
			if v.Outcome != tc.outcome || v.Phase != tc.phase || !strings.Contains(v.Detail, tc.detail) {
				t.Fatalf("got %+v\nwant %s %s %q\napp %+v", v, tc.outcome, tc.phase, tc.detail, app)
			}
		})
	}
	// Another repository: the deployment cannot be judged.
	app := ReduceApplication(load(t, "application-synced-healthy.json"), spec, "https://elsewhere.example/x.git", containsAll)
	if v := Evaluate(spec, Snapshot{App: app}); v.Outcome != Failed || v.Phase != PhaseSourceBad {
		t.Fatalf("another repository: %+v", v)
	}
	// Automated sync is read from the object.
	if !ReduceApplication(load(t, "application-synced-healthy.json"), spec, gitopsURL, nil).Automated {
		t.Fatal("the demo application syncs by itself")
	}
}

func TestGoldenRollouts(t *testing.T) {
	spec := Spec{Kind: "deploy", Commit: "c", Health: "application", Windows: "wait",
		Rollout: &pipeline.RolloutRef{Name: "demo", Namespace: "lw-demo", Until: "healthy"}}
	healthyApp := func(t *testing.T) *App {
		return ReduceApplication(load(t, "application-synced-healthy.json"), spec, gitopsURL, containsAll)
	}
	for _, tc := range []struct {
		rollout, analysis, app string
		outcome                string
		phase, detail          string
	}{
		{"rollout-healthy.json", "analysisruns-successful.json", "", "succeeded", PhaseHealthy, "rollout healthy on 54d69959b4 (2/2 ready)"},
		{"rollout-progressing.json", "", "application-progressing.json", "continue", PhaseRollout, "at step 1 of 4"},
		{"rollout-paused-timed.json", "", "application-suspended-timed-pause.json", "continue", PhasePaused, "CanaryPauseStep"},
		{"rollout-paused-indefinite.json", "", "application-suspended-timed-pause.json", "continue", PhasePaused, "paused at step 2 of 4"},
		{"rollout-analysis-running.json", "analysisruns-running.json", "application-progressing.json", "continue", PhaseRollout, "analysis running: demo-54d69959b4-8-2"},
		{"rollout-aborted-analysis.json", "analysisruns-failed.json", "application-degraded.json", "failed", PhaseAborted, "analysis demo-55c9d49c8d-3-2 failed: metric verdict failed"},
		{"rollout-aborted-manual.json", "", "application-degraded.json", "failed", PhaseAborted, "aborted"},
		{"rollout-degraded-crash.json", "", "application-degraded.json", "failed", PhaseAborted, "aborted"},
	} {
		t.Run(tc.rollout, func(t *testing.T) {
			ro := ReduceRollout(load(t, tc.rollout), nil)
			var runs []AnalysisRun
			if tc.analysis != "" {
				uid := load(t, tc.rollout)["metadata"].(map[string]any)["uid"].(string)
				runs = ReduceAnalysisRuns(items(t, tc.analysis), uid)
				if len(runs) == 0 {
					t.Fatal("no analysis run is the rollout's")
				}
			}
			app := healthyApp(t)
			if tc.app != "" {
				app = ReduceApplication(load(t, tc.app), spec, gitopsURL, containsAll)
			}
			v := Evaluate(spec, Snapshot{App: app, Rollout: ro, Analysis: runs})
			if string(v.Outcome) != tc.outcome || v.Phase != tc.phase || !strings.Contains(v.Detail, tc.detail) {
				t.Fatalf("got %+v\nwant %s %s %q\nrollout %+v", v, tc.outcome, tc.phase, tc.detail, ro)
			}
		})
	}
	// Rollouts writes observedGeneration as a string; it must still match.
	if ro := ReduceRollout(load(t, "rollout-healthy.json"), nil); !ro.ObservedKnown || ro.Strategy != "canary" || ro.Steps != 4 {
		t.Fatalf("healthy rollout: %+v", ro)
	}
	// A timed pause has an end.
	if ro := ReduceRollout(load(t, "rollout-paused-timed.json"), nil); ro.PauseUntil == 0 {
		t.Fatalf("timed pause without an end: %+v", ro)
	}
	// workloadRef: observed only when the Deployment's generation is known
	// and matches.
	wr := load(t, "rollout-workloadref-healthy.json")
	if ro := ReduceRollout(wr, nil); ro.ObservedKnown || !ro.WorkloadUnread {
		t.Fatalf("workloadRef without the Deployment: %+v", ro)
	}
	one, two := int64(1), int64(2)
	if ro := ReduceRollout(wr, &one); !ro.ObservedKnown {
		t.Fatalf("workloadRef at generation 1: %+v", ro)
	}
	if ro := ReduceRollout(wr, &two); ro.ObservedKnown {
		t.Fatalf("workloadRef behind its Deployment: %+v", ro)
	}
}
