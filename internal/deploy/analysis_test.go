package deploy

import (
	"strings"
	"testing"
)

// An analysis run's metric carries what it checks, its measurements and the
// job it ran, and the failing measurement's message reaches the verdict.
func TestReduceAnalysisKeepsMeasurementsAndJob(t *testing.T) {
	obj := map[string]any{
		"metadata": map[string]any{"name": "shop-1-smoke", "namespace": "shop", "creationTimestamp": "2026-11-20T10:00:00Z",
			"ownerReferences": []any{map[string]any{"uid": "u1"}}, "labels": map[string]any{"rollouts-pod-template-hash": "abc"}},
		"spec": map[string]any{"metrics": []any{map[string]any{"name": "smoke", "interval": "30s", "failureLimit": int64(0),
			"provider": map[string]any{"job": map[string]any{}}}}},
		"status": map[string]any{"phase": "Failed", "metricResults": []any{map[string]any{"name": "smoke", "phase": "Failed",
			"count": int64(20), "successful": int64(19), "failed": int64(1),
			"measurements": []any{
				map[string]any{"phase": "Successful", "finishedAt": "2026-11-20T10:00:30Z"},
				map[string]any{"phase": "Failed", "message": "Job failed: Could not resolve host", "finishedAt": "2026-11-20T10:01:00Z",
					"metadata": map[string]any{"job-name": "smoke-7"}},
			}}}},
	}
	runs := ReduceAnalysisRuns([]map[string]any{obj}, "u1")
	if len(runs) != 1 || len(runs[0].Metrics) != 1 {
		t.Fatalf("%+v", runs)
	}
	m := runs[0].Metrics[0]
	if m.Provider != "job" || m.Interval != "30s" || m.FailureLimit != 0 || len(m.Measurements) != 2 {
		t.Fatalf("%+v", m)
	}
	if m.Job == nil || m.Job.Name != "smoke-7" || m.Job.Namespace != "shop" {
		t.Fatalf("job %+v", m.Job)
	}
	if got := FailingMessage(m); !strings.Contains(got, "Could not resolve host") {
		t.Fatalf("%q", got)
	}
	if a := FailedAnalysisRun(runs, "abc"); a == nil || a.Name != "shop-1-smoke" {
		t.Fatal("not found")
	}
	if a := FailedAnalysisRun(runs, "other"); a != nil {
		t.Fatal("another pod template's analysis")
	}
	if d := failedAnalysis(runs, "abc"); !strings.Contains(d, "Could not resolve host") {
		t.Fatalf("%q", d)
	}
}
