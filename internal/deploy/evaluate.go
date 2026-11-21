package deploy

import (
	"fmt"
	"strings"
)

// Outcome is what a snapshot means for a deployment being watched.
type Outcome string

const (
	Continue  Outcome = "continue"  // not done yet: keep watching
	Succeeded Outcome = "succeeded" // deployed and healthy
	Failed    Outcome = "failed"    // it will not get there by itself
)

// Phases a deployment being watched can be in. They are stable names: the
// UI and the events use them.
const (
	PhaseUnobserved = "unobserved"   // the runner could not see the application
	PhaseAppMissing = "app_missing"  // the application does not exist
	PhaseSourceBad  = "source_error" // the application does not deploy from the repository
	PhaseSyncing    = "syncing"      // Argo CD has not synced the commit yet
	PhaseBlocked    = "sync_blocked" // a sync window does not allow syncing
	PhaseSyncFailed = "sync_failed"
	PhaseProgress   = "progressing" // synced; waiting for the application to be healthy
	PhaseDegraded   = "degraded"
	PhaseRollout    = "rollout_progressing"
	PhasePaused     = "rollout_paused"
	PhaseAborted    = "rollout_aborted"
	PhaseHealthy    = "healthy"
	PhaseLeftovers  = "leftovers" // objects of an earlier render are still there and pruning is off
)

// Verdict is the result of evaluating one snapshot.
type Verdict struct {
	Outcome Outcome `json:"outcome"`
	Phase   string  `json:"phase"`
	Detail  string  `json:"detail"` // in plain words, for the run page and the step's reason
	// Action and Expect, for Outcome Command: the command to give the
	// rollout, and what it expects (see control.go).
	Action string  `json:"action,omitempty"`
	Expect *Expect `json:"expect,omitempty"`
}

func cont(phase, format string, a ...any) Verdict {
	return Verdict{Outcome: Continue, Phase: phase, Detail: fmt.Sprintf(format, a...)}
}

func fail(phase, format string, a ...any) Verdict {
	return Verdict{Outcome: Failed, Phase: phase, Detail: fmt.Sprintf(format, a...)}
}

// Evaluate decides what a snapshot means for a deployment watched for spec.
// It is a pure function: the server calls it for every snapshot a runner
// reports, and the same snapshot always gives the same verdict.
//
// A deployment is done only when one snapshot shows all of these: the
// revision Argo CD has synced (for the source from the deployment's
// repository) includes the commit; the judged resources (or the whole
// application) are Synced and Healthy; and, if a rollout is watched, it is
// Healthy, its stable ReplicaSet is its current pod template, and the
// controller has observed its current spec. Missing or stale data is never
// a pass.
func Evaluate(spec Spec, s Snapshot) Verdict {
	if s.Error != "" {
		return cont(PhaseUnobserved, "the runner could not see the application: %s", s.Error)
	}
	app := s.App
	if app == nil {
		return cont(PhaseUnobserved, "the runner reported nothing about the application")
	}
	if !app.Found {
		return cont(PhaseAppMissing, "the application does not exist (yet)")
	}
	if app.SourceError != "" {
		return fail(PhaseSourceBad, "%s", app.SourceError)
	}
	// A sync of our commit that failed is final: Argo CD does not retry a
	// failed operation by itself unless the application syncs on its own,
	// and then it fails the same way.
	if op := app.Operation; op != nil && isTrue(op.Contains) && (op.Phase == "Failed" || op.Phase == "Error") {
		return fail(PhaseSyncFailed, "Argo CD could not sync %s: %s", short(op.Revision), nonEmpty(op.Message, "no reason given"))
	}
	if blocked := windowBlock(app); blocked != "" {
		if spec.Windows == "fail" {
			return fail(PhaseBlocked, "%s (sync.windows says fail)", blocked)
		}
		return cont(PhaseBlocked, "%s; waiting until a window allows it", blocked)
	}
	if app.Contains == nil {
		return cont(PhaseSyncing, "waiting for Argo CD to see commit %s", short(spec.Commit))
	}
	if !*app.Contains {
		what := "waiting for Argo CD to sync commit " + short(spec.Commit)
		if app.Revision != "" {
			what += " (it is at " + short(app.Revision) + ")"
		}
		if op := app.Operation; op != nil && op.Phase == "Running" {
			what += "; a sync is running"
		}
		return cont(PhaseSyncing, "%s", what)
	}

	// The revision is ours. A rollback that aborted the rollout first waits
	// for it to return to the previous version: the abort is its own doing.
	if ro := s.Rollout; spec.Kind == "rollback" && ro != nil && ro.Found && ro.Aborted {
		return cont(PhaseAborted, "the rollout is aborted; waiting for it to return to the previous version%s", colon(ro.Message))
	}
	// Objects an earlier render made, still there because pruning is off.
	lf, lfWarn := leftoverVerdict(spec, app)
	if lf != nil {
		return *lf
	}
	// Now the judged resources, or the application.
	if spec.Health == "resources" && len(spec.Resources) > 0 {
		for _, want := range spec.Resources {
			r := findResource(app.Resources, want.Group, want.Kind, want.Namespace, want.Name)
			name := want.Kind + " " + want.Name
			switch {
			case r == nil || !r.Found:
				return cont(PhaseSyncing, "waiting for %s to appear in the application", name)
			case r.Sync != "Synced":
				return cont(PhaseSyncing, "%s is %s", name, nonEmpty(r.Sync, "not synced yet"))
			case r.Health == "Degraded":
				return fail(PhaseDegraded, "%s is Degraded%s", name, colon(r.Message))
			case r.Health != "Healthy" && r.Health != "":
				return cont(PhaseProgress, "%s is %s%s", name, r.Health, colon(r.Message))
			}
		}
	} else {
		switch {
		case app.Sync != "Synced" && !app.SyncedExceptLeftovers:
			return cont(PhaseSyncing, "the application is %s at %s", nonEmpty(app.Sync, "not synced"), short(app.Revision))
		case app.Health == "Degraded":
			if v, ok := rolloutFailure(s); ok {
				return v
			}
			return fail(PhaseDegraded, "the application is Degraded%s", colon(app.HealthMessage))
		case app.Health != "Healthy":
			if v, ok := rolloutFailure(s); ok {
				return v
			}
			if s.Rollout != nil && s.Rollout.Found && spec.Rollout != nil {
				if v, ok := atPause(spec, s); ok {
					return v
				}
				return rolloutProgress(s)
			}
			return cont(PhaseProgress, "the application is %s%s", nonEmpty(app.Health, "of unknown health"), colon(app.HealthMessage))
		}
	}
	if spec.Rollout == nil {
		return Verdict{Outcome: Succeeded, Phase: PhaseHealthy, Detail: "synced at " + short(app.Revision) + " and healthy" + lfWarn}
	}
	ro := s.Rollout
	if ro == nil || !ro.Found {
		return cont(PhaseRollout, "waiting for rollout %s/%s", spec.Rollout.Namespace, spec.Rollout.Name)
	}
	if v, ok := rolloutFailure(s); ok {
		return v
	}
	switch {
	case !ro.ObservedKnown && ro.WorkloadUnread:
		return cont(PhaseRollout, "the rollout takes its pod template from a Deployment the deploy runner may not read, "+
			"so it cannot tell whether the rollout controller has seen the new version (grant it get on deployments in %s)", spec.Rollout.Namespace)
	case !ro.ObservedKnown:
		return cont(PhaseRollout, "waiting for the rollout controller to see the new version")
	case ro.Phase == "Healthy" && ro.CurrentPodHash != "" && ro.StableRS == ro.CurrentPodHash:
		return Verdict{Outcome: Succeeded, Phase: PhaseHealthy,
			Detail: fmt.Sprintf("synced at %s; rollout healthy on %s (%d/%d ready)%s", short(app.Revision), ro.CurrentPodHash, ro.Ready, ro.Replicas, lfWarn)}
	}
	if v, ok := atPause(spec, s); ok {
		return v
	}
	return rolloutProgress(s)
}

// atPause is what an indefinite pause of the deployment's version means
// (see pauseVerdict); only a rollout the controller has seen at the
// deployment's commit counts.
func atPause(spec Spec, s Snapshot) (Verdict, bool) {
	if spec.Rollout == nil || spec.Kind == "rollback" || OurPodHash(s) == "" {
		return Verdict{}, false
	}
	return pauseVerdict(spec, s)
}

// rolloutFailure reports a rollout that has given up: aborted or Degraded.
func rolloutFailure(s Snapshot) (Verdict, bool) {
	ro := s.Rollout
	if ro == nil || !ro.Found {
		return Verdict{}, false
	}
	why := ""
	if a := failedAnalysis(s.Analysis, ro.CurrentPodHash); a != "" {
		why = "; " + a
	}
	switch {
	case ro.Aborted:
		return fail(PhaseAborted, "the rollout was aborted%s%s", colon(ro.Message), why), true
	case ro.Phase == "Degraded":
		return fail(PhaseDegraded, "the rollout is Degraded%s%s", colon(ro.Message), why), true
	}
	return Verdict{}, false
}

// rolloutProgress describes a rollout that is still going.
func rolloutProgress(s Snapshot) Verdict {
	ro := s.Rollout
	where := ""
	if ro.Step != nil && ro.Steps > 0 {
		where = fmt.Sprintf(" at step %d of %d", min(*ro.Step+1, ro.Steps), ro.Steps)
	}
	weight := ""
	if ro.CanaryWeight != nil {
		weight = fmt.Sprintf(", canary at %d%%", *ro.CanaryWeight)
	}
	replicas := fmt.Sprintf(" (%d/%d ready, %d updated)", ro.Ready, ro.Replicas, ro.Updated)
	if running := runningAnalysis(s.Analysis, ro.CurrentPodHash); running != "" {
		replicas += "; " + running
	}
	if ro.Paused || ro.Phase == "Paused" {
		reason := strings.Join(ro.PauseReasons, ", ")
		if reason == "" {
			reason = "paused"
		}
		return cont(PhasePaused, "the rollout is paused%s (%s)%s%s", where, reason, weight, replicas)
	}
	return cont(PhaseRollout, "the rollout is %s%s%s%s", nonEmpty(ro.Phase, "progressing"), where, weight, replicas)
}

// failedAnalysis names the failed analysis run of the current pod template
// and its failing metrics, or "".
func failedAnalysis(runs []AnalysisRun, hash string) string {
	if a := FailedAnalysisRun(runs, hash); a != nil {
		return describeFailedAnalysis(*a)
	}
	return ""
}

// FailedAnalysisRun is the failed analysis run of the pod template hash
// (any, when hash is empty), newest first; nil if none failed.
func FailedAnalysisRun(runs []AnalysisRun, hash string) *AnalysisRun {
	for i, a := range runs {
		if hash != "" && a.PodHash != "" && a.PodHash != hash {
			continue
		}
		if a.Phase == "Failed" || a.Phase == "Error" || a.Phase == "Inconclusive" {
			return &runs[i]
		}
	}
	return nil
}

func describeFailedAnalysis(a AnalysisRun) string {
	var bad []string
	for _, m := range a.Metrics {
		if m.Phase == "Failed" || m.Phase == "Error" || m.Phase == "Inconclusive" {
			one := fmt.Sprintf("metric %s %s (%d failed, %d errors, %d inconclusive of %d)",
				m.Name, strings.ToLower(m.Phase), m.Failed, m.Error, m.Inconclusive, m.Count)
			if msg := FailingMessage(m); msg != "" {
				one += ": " + msg
			}
			bad = append(bad, one)
		}
	}
	msg := fmt.Sprintf("analysis %s %s", a.Name, strings.ToLower(a.Phase))
	if len(bad) > 0 {
		msg += ": " + strings.Join(bad, ", ")
	} else if a.Message != "" {
		msg += ": " + a.Message
	}
	return msg
}

// runningAnalysis names the analysis runs still running for the current pod
// template, or "".
func runningAnalysis(runs []AnalysisRun, hash string) string {
	var names []string
	for _, a := range runs {
		if (hash == "" || a.PodHash == "" || a.PodHash == hash) && (a.Phase == "Running" || a.Phase == "Pending") {
			names = append(names, a.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "analysis running: " + strings.Join(names, ", ")
}

// windowBlock says why a sync window keeps the application from syncing,
// from what Argo CD wrote in its operation or conditions, or "".
func windowBlock(app *App) string {
	if op := app.Operation; op != nil && op.Phase == "Running" && mentionsWindow(op.Message) {
		return "Argo CD holds the sync: " + op.Message
	}
	for _, c := range app.Conditions {
		if mentionsWindow(c) {
			return "Argo CD holds the sync: " + c
		}
	}
	if app.WindowBlocked != "" && !(isTrue(app.Contains) && app.Sync == "Synced") {
		return "Argo CD may not sync: " + app.WindowBlocked
	}
	return ""
}

func mentionsWindow(s string) bool {
	return strings.Contains(strings.ToLower(s), "sync window")
}

func findResource(rs []Resource, group, kind, ns, name string) *Resource {
	for i := range rs {
		r := &rs[i]
		if r.Kind == kind && r.Name == name && r.Group == group && (ns == "" || r.Namespace == ns) {
			return r
		}
	}
	return nil
}

func isTrue(b *bool) bool { return b != nil && *b }

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	if sha == "" {
		return "an unknown revision"
	}
	return sha
}

func colon(msg string) string {
	if strings.TrimSpace(msg) == "" {
		return ""
	}
	return ": " + strings.TrimSpace(msg)
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// FailingMessage is the message of the latest measurement of a metric that
// did not pass, shortened; "" if there is none.
func FailingMessage(m Metric) string {
	for i := len(m.Measurements) - 1; i >= 0; i-- {
		x := m.Measurements[i]
		if x.Phase != "Successful" && x.Phase != "Running" && x.Phase != "Pending" && x.Message != "" {
			if len(x.Message) > 300 {
				return x.Message[:300] + "..."
			}
			return x.Message
		}
	}
	return ""
}

// leftoverVerdict judges the objects of an earlier render that are still in
// the cluster. With pruning off they stay until someone removes them, and a
// stale NetworkPolicy, role or route can widen access. A security-relevant
// kind fails the deployment (unless the step says leftovers: warn); others
// are only mentioned. Nothing is deleted. The second result is the sentence
// for a deployment that passes with leftovers.
func leftoverVerdict(spec Spec, app *App) (*Verdict, string) {
	if len(app.Leftovers) == 0 || spec.Prune || app.AutoPrune {
		return nil, ""
	}
	if op := app.Operation; op != nil && (op.Phase == "Running" || op.Phase == "Terminating") {
		return nil, "" // a sync is still going; it may yet prune
	}
	var sec, other []string
	for _, l := range app.Leftovers {
		if l.Security {
			sec = append(sec, l.String())
		} else {
			other = append(other, l.String())
		}
	}
	words := func(n int, names []string) string {
		return fmt.Sprintf("%d object(s) from an earlier render are still present and no longer part of the release: %s; pruning is off for this application, so they stay until removed",
			n, strings.Join(names, ", "))
	}
	if len(sec) > 0 && spec.Leftovers != "warn" {
		v := fail(PhaseLeftovers, "%s. Security-relevant leftovers can widen access. Remove them (kubectl delete), turn pruning on for the application, or set sync.leftovers: warn on the step to accept them", words(len(sec), sec))
		return &v, ""
	}
	all := append(append([]string{}, sec...), other...)
	return nil, "; warning: " + words(len(all), all)
}
