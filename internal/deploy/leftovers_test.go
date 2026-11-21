package deploy

import (
	"strings"
	"testing"
)

func appWithResources(res ...any) map[string]any {
	return map[string]any{"metadata": map[string]any{"name": "shop"},
		"spec": map[string]any{"source": map[string]any{"repoURL": "https://g/x.git"}},
		"status": map[string]any{"sync": map[string]any{"status": "OutOfSync", "revision": "c1"}, "health": map[string]any{"status": "Healthy"},
			"resources": res}}
}

func res(kind, name, status string, prune bool) map[string]any {
	return map[string]any{"group": "networking.k8s.io", "kind": kind, "name": name, "namespace": "shop", "status": status, "requiresPruning": prune}
}

func contains(string) (bool, error) { return true, nil }

// With pruning off, a NetworkPolicy the release no longer renders fails the
// deployment and is named; other kinds only warn; a step may accept them.
func TestLeftoversFailOrWarnWithoutPruning(t *testing.T) {
	spec := Spec{Kind: "deploy", Commit: "c1", Health: "application", Windows: "wait"}
	snap := func(rs ...any) Snapshot {
		return Snapshot{App: ReduceApplication(appWithResources(rs...), spec, "https://g/x.git", contains)}
	}
	v := Evaluate(spec, snap(res("Deployment", "shop", "Synced", false), res("NetworkPolicy", "shop-allow", "OutOfSync", true), res("NetworkPolicy", "shop-db", "OutOfSync", true)))
	if v.Outcome != Failed || v.Phase != PhaseLeftovers || !strings.Contains(v.Detail, "NetworkPolicy/shop-allow") ||
		!strings.Contains(v.Detail, "2 object(s) from an earlier render are still present") || !strings.Contains(v.Detail, "pruning is off") {
		t.Fatalf("%+v", v)
	}
	// Only a ConfigMap is left: the deployment passes, with a warning.
	v = Evaluate(spec, snap(res("Deployment", "shop", "Synced", false), res("ConfigMap", "old", "OutOfSync", true)))
	if v.Outcome != Succeeded || !strings.Contains(v.Detail, "warning") || !strings.Contains(v.Detail, "ConfigMap/old") {
		t.Fatalf("%+v", v)
	}
	// leftovers: warn accepts the security kinds too.
	warn := spec
	warn.Leftovers = "warn"
	v = Evaluate(warn, snap(res("NetworkPolicy", "shop-allow", "OutOfSync", true)))
	if v.Outcome != Succeeded || !strings.Contains(v.Detail, "NetworkPolicy/shop-allow") {
		t.Fatalf("%+v", v)
	}
	// With pruning requested by the step, they are the sync's to remove: no verdict from this check.
	prune := spec
	prune.Prune = true
	if v := Evaluate(prune, snap(res("NetworkPolicy", "shop-allow", "OutOfSync", true))); v.Phase == PhaseLeftovers {
		t.Fatalf("%+v", v)
	}
	// Something truly out of sync is still syncing, not a leftover.
	if v := Evaluate(spec, snap(res("Deployment", "shop", "OutOfSync", false))); v.Outcome != Continue {
		t.Fatalf("%+v", v)
	}
}

func TestCrashLoopFailsWithThePodsOwnWords(t *testing.T) {
	spec := Spec{Kind: "deploy", Commit: "c1", Health: "application", Windows: "wait"}
	app := ReduceApplication(appWithResources(), spec, "https://g/x.git", contains)
	app.Sync, app.Health = "Synced", "Progressing"
	v := Evaluate(spec, Snapshot{App: app, Evidence: []PodEvidence{{Pod: "p", Reason: "CrashLoopBackOff", Crash: true, Restarts: 3, LogTail: []string{"boom: x", ""}}}})
	if v.Outcome != Failed || v.Phase != PhaseCrashLoop || !strings.Contains(v.Detail, `"boom: x"`) {
		t.Fatalf("%+v", v)
	}
	// A pod that has restarted once is not yet a loop.
	if v := Evaluate(spec, Snapshot{App: app, Evidence: []PodEvidence{{Pod: "p", Crash: true, Restarts: 1}}}); v.Outcome != Continue {
		t.Fatalf("%+v", v)
	}
}
