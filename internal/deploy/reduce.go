package deploy

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// The reducers read objects as the Kubernetes dynamic client returns them
// (unstructured maps), so that Launchway needs no Argo CD or Argo Rollouts
// Go modules. Fields they do not find are left empty, never guessed.

// ContainsFunc reports whether revision includes the deployment's commit.
// The runner answers it from its clone of the repository.
type ContainsFunc func(revision string) (bool, error)

// ReduceApplication reduces an Argo CD Application. repoURL is the
// repository the deployment commits to, as Argo CD knows it: the source
// whose repoURL matches is the one compared with the commit.
func ReduceApplication(obj map[string]any, spec Spec, repoURL string, contains ContainsFunc) *App {
	if obj == nil {
		return &App{Found: false}
	}
	app := &App{Found: true}
	status := mapAt(obj, "status")
	app.Sync = str(mapAt(status, "sync"), "status")
	app.Health = str(mapAt(status, "health"), "status")
	app.HealthMessage = str(mapAt(status, "health"), "message")
	app.ReconciledAt = parseTime(str(status, "reconciledAt"))
	if auto := mapAt(mapAt(mapAt(obj, "spec"), "syncPolicy"), "automated"); auto != nil {
		// Argo CD 3 lets automated sync be written but switched off.
		enabled, set := auto["enabled"].(bool)
		app.Automated = !set || enabled
	}
	for _, c := range list(status, "conditions") {
		cm, _ := c.(map[string]any)
		if t := str(cm, "type"); strings.HasSuffix(t, "Error") || strings.HasSuffix(t, "Warning") {
			app.Conditions = append(app.Conditions, t+": "+str(cm, "message"))
		}
	}

	// Which source is ours.
	index, multi := -1, false
	if sources := list(mapAt(obj, "spec"), "sources"); len(sources) > 0 {
		multi = true
		for i, s := range sources {
			if sameRepo(str(asMap(s), "repoURL"), repoURL) {
				index = i
				break
			}
		}
	} else if src := mapAt(mapAt(obj, "spec"), "source"); src != nil && sameRepo(str(src, "repoURL"), repoURL) {
		index = 0
	}
	if index < 0 {
		app.SourceError = fmt.Sprintf("the application has no source from %s, the repository the deployment commits to", redactURL(repoURL))
		return app
	}
	app.Source = index
	revisionOf := func(m map[string]any) string {
		if multi {
			revs := list(m, "revisions")
			if index < len(revs) {
				s, _ := revs[index].(string)
				return s
			}
			return ""
		}
		return str(m, "revision")
	}
	check := func(rev string) *bool {
		if rev == "" || contains == nil {
			return nil
		}
		ok, err := contains(rev)
		if err != nil {
			return nil
		}
		return &ok
	}
	app.Revision = revisionOf(mapAt(status, "sync"))
	app.Contains = check(app.Revision)

	if os := mapAt(status, "operationState"); os != nil {
		op := &Operation{Phase: str(os, "phase"), Message: str(os, "message"),
			StartedAt: parseTime(str(os, "startedAt")), FinishedAt: parseTime(str(os, "finishedAt"))}
		op.Revision = revisionOf(mapAt(os, "syncResult"))
		if op.Revision == "" {
			op.Revision = revisionOf(mapAt(mapAt(os, "operation"), "sync"))
		}
		op.Contains = check(op.Revision)
		app.Operation = op
	}

	if spec.Health == "resources" {
		for _, want := range spec.Resources {
			r := Resource{Group: want.Group, Kind: want.Kind, Namespace: want.Namespace, Name: want.Name}
			for _, x := range list(status, "resources") {
				xm := asMap(x)
				if str(xm, "kind") == want.Kind && str(xm, "name") == want.Name && str(xm, "group") == want.Group &&
					(want.Namespace == "" || str(xm, "namespace") == want.Namespace) {
					r.Found = true
					r.Namespace = str(xm, "namespace")
					r.Sync = str(xm, "status")
					r.Health = str(mapAt(xm, "health"), "status")
					r.Message = str(mapAt(xm, "health"), "message")
					break
				}
			}
			app.Resources = append(app.Resources, r)
		}
	}
	return app
}

// ReduceRollout reduces an Argo Rollouts Rollout. workloadGeneration is the
// generation of the workload a rollout with workloadRef takes its pod
// template from, or nil when the runner could not read it.
func ReduceRollout(obj map[string]any, workloadGeneration *int64) *Rollout {
	if obj == nil {
		return &Rollout{Found: false}
	}
	ro := &Rollout{Found: true}
	meta, spec, status := mapAt(obj, "metadata"), mapAt(obj, "spec"), mapAt(obj, "status")
	ro.Generation = integer(meta["generation"])
	ro.Revision = str(mapAt(meta, "annotations"), revisionAnnotation)
	ro.ObservedGeneration = scalarString(status["observedGeneration"])
	ro.Phase = str(status, "phase")
	ro.Message = str(status, "message")
	ro.CurrentPodHash = str(status, "currentPodHash")
	ro.StableRS = str(status, "stableRS")
	ro.Aborted, _ = status["abort"].(bool)
	ro.Replicas = int(integer(status["replicas"]))
	ro.Ready = int(integer(status["readyReplicas"]))
	ro.Updated = int(integer(status["updatedReplicas"]))
	ro.Available = int(integer(status["availableReplicas"]))
	observed := ro.ObservedGeneration != "" && ro.ObservedGeneration == strconv.FormatInt(ro.Generation, 10)
	if mapAt(spec, "workloadRef") != nil {
		ro.WorkloadRef = true
		ro.WorkloadUnread = workloadGeneration == nil
		wog := scalarString(status["workloadObservedGeneration"])
		observed = observed && workloadGeneration != nil && wog == strconv.FormatInt(*workloadGeneration, 10)
	}
	ro.ObservedKnown = observed

	strategy := mapAt(spec, "strategy")
	var steps []any
	switch {
	case mapAt(strategy, "canary") != nil:
		ro.Strategy = "canary"
		steps = list(mapAt(strategy, "canary"), "steps")
	case mapAt(strategy, "blueGreen") != nil:
		ro.Strategy = "blueGreen"
	}
	ro.PromoteFull, _ = status["promoteFull"].(bool)
	bgAuto := false
	if bg := mapAt(strategy, "blueGreen"); bg != nil && integer(bg["autoPromotionSeconds"]) > 0 {
		bgAuto = true
	}
	ro.Steps = len(steps)
	for _, s := range steps {
		ro.StepNames = append(ro.StepNames, stepName(asMap(s)))
	}
	if v, ok := status["currentStepIndex"]; ok && v != nil {
		i := int(integer(v))
		ro.Step = &i
	}
	if w := mapAt(mapAt(mapAt(status, "canary"), "weights"), "canary"); w != nil {
		if v, ok := w["weight"]; ok {
			n := int(integer(v))
			ro.CanaryWeight = &n
		}
	}
	paused, _ := spec["paused"].(bool)
	ro.Paused = paused || status["controllerPause"] == true
	for _, pc := range list(status, "pauseConditions") {
		pm := asMap(pc)
		ro.Paused = true
		ro.PauseReasons = append(ro.PauseReasons, str(pm, "reason"))
		// A timed pause of the current step ends at its start plus its
		// duration.
		if str(pm, "reason") == "CanaryPauseStep" && ro.Step != nil && *ro.Step < len(steps) {
			if d := pauseDuration(asMap(steps[*ro.Step])); d > 0 {
				if start := parseTime(str(pm, "startTime")); start > 0 {
					ro.PauseUntil = start + d.Milliseconds()
				}
			} else {
				ro.PauseIndefinite = true
			}
		}
		switch str(pm, "reason") {
		case "BlueGreenPause":
			ro.PauseIndefinite = ro.PauseIndefinite || !bgAuto
		case "InconclusiveAnalysis", "InconclusiveExperiment":
			ro.PauseIndefinite = true
		}
	}
	return ro
}

// ReduceAnalysisRuns reduces the analysis runs a rollout owns, newest first.
func ReduceAnalysisRuns(items []map[string]any, rolloutUID string) []AnalysisRun {
	type dated struct {
		run AnalysisRun
		at  string
	}
	var out []dated
	for _, obj := range items {
		meta := mapAt(obj, "metadata")
		owned := false
		for _, o := range list(meta, "ownerReferences") {
			if str(asMap(o), "uid") == rolloutUID && rolloutUID != "" {
				owned = true
			}
		}
		if !owned {
			continue
		}
		labels := mapAt(meta, "labels")
		status := mapAt(obj, "status")
		a := AnalysisRun{Name: str(meta, "name"), Phase: str(status, "phase"), Message: str(status, "message"),
			Kind: str(labels, "rollout-type"), PodHash: str(labels, "rollouts-pod-template-hash"),
			Revision: str(mapAt(meta, "annotations"), revisionAnnotation), CreatedAt: parseTime(str(meta, "creationTimestamp")),
			Namespace: str(meta, "namespace")}
		specs := map[string]map[string]any{}
		for _, m := range list(mapAt(obj, "spec"), "metrics") {
			specs[str(asMap(m), "name")] = asMap(m)
		}
		if a.Phase == "" {
			a.Phase = "Pending"
		}
		if si := str(labels, "step-index"); si != "" {
			if n, err := strconv.Atoi(si); err == nil {
				a.StepIndex = &n
			}
		}
		for _, m := range list(status, "metricResults") {
			mm := asMap(m)
			m := Metric{Name: str(mm, "name"), Phase: str(mm, "phase"), Message: str(mm, "message"),
				Count: int(integer(mm["count"])), Successful: int(integer(mm["successful"])), Failed: int(integer(mm["failed"])),
				Inconclusive: int(integer(mm["inconclusive"])), Error: int(integer(mm["error"]))}
			reduceMetricSpec(&m, specs[m.Name])
			reduceMeasurements(&m, list(mm, "measurements"), a.Namespace)
			a.Metrics = append(a.Metrics, m)
		}
		out = append(out, dated{a, str(meta, "creationTimestamp")})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].at != out[j].at {
			return out[i].at > out[j].at
		}
		return out[i].run.Name > out[j].run.Name
	})
	runs := make([]AnalysisRun, len(out))
	for i, d := range out {
		runs[i] = d.run
	}
	if len(runs) > maxAnalysisRuns {
		runs = runs[:maxAnalysisRuns]
	}
	return runs
}

// revisionAnnotation is where Argo Rollouts writes a rollout's revision,
// and, on an analysis run, the revision it was made for.
const revisionAnnotation = "rollout.argoproj.io/revision"

// maxAnalysisRuns bounds the analysis runs a snapshot carries.
const maxAnalysisRuns = 10

// SpecFor is the watch spec of a deploy step for a commit.
func SpecFor(kind string, step *pipeline.Step, commit string) Spec {
	s := Spec{Kind: kind, Commit: commit, Health: "application", Windows: "wait"}
	if step == nil || step.ArgoCD == nil {
		return s
	}
	s.Health = step.ArgoCD.Health
	s.Resources = step.ArgoCD.Sync.Resources
	s.Windows = step.ArgoCD.Sync.Windows
	s.Rollout = step.Rollout
	return s
}

func stepName(s map[string]any) string {
	for _, k := range []string{"setWeight", "pause", "analysis", "experiment", "setCanaryScale", "setHeaderRoute", "setMirrorRoute", "plugin"} {
		if v, ok := s[k]; ok {
			switch k {
			case "setWeight":
				return fmt.Sprintf("weight %d%%", integer(v))
			case "pause":
				if d := pauseDuration(s); d > 0 {
					return "pause " + d.String()
				}
				return "pause"
			}
			return k
		}
	}
	return "step"
}

// pauseDuration is a pause step's duration, or 0 for an indefinite pause.
func pauseDuration(step map[string]any) time.Duration {
	p := mapAt(step, "pause")
	if p == nil {
		return 0
	}
	switch v := p["duration"].(type) {
	case string:
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	case float64:
		return time.Duration(v) * time.Second
	case int64:
		return time.Duration(v) * time.Second
	}
	return 0
}

// sameRepo compares repository URLs as Argo CD does loosely: ignoring case
// of the host, a trailing slash or .git, and credentials.
func sameRepo(a, b string) bool {
	return NormalizeRepo(a) == NormalizeRepo(b) && a != ""
}

// NormalizeRepo writes a repository URL in the form sameRepo compares.
func NormalizeRepo(s string) string {
	s = strings.TrimSpace(s)
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		u.User = nil
		u.Host = strings.ToLower(u.Host)
		u.Scheme = strings.ToLower(u.Scheme)
		s = u.String()
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}

func redactURL(s string) string {
	if u, err := url.Parse(s); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	return s
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func mapAt(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	return asMap(m[key])
}

func list(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	l, _ := m[key].([]any)
	return l
}

func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// integer reads a number that may be an int64 (dynamic client), a float64
// (encoding/json) or a numeric string.
func integer(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		x, _ := strconv.ParseInt(n, 10, 64)
		return x
	}
	return 0
}

// scalarString writes a number or a string as a string: Argo Rollouts has
// written observedGeneration both ways.
func scalarString(v any) string {
	switch n := v.(type) {
	case string:
		return n
	case nil:
		return ""
	}
	return strconv.FormatInt(integer(v), 10)
}

func parseTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// maxMeasurements bounds the measurements a snapshot keeps per metric.
const maxMeasurements = 12

// reduceMetricSpec reads what a metric checks from the analysis run's spec.
func reduceMetricSpec(m *Metric, spec map[string]any) {
	if spec == nil {
		return
	}
	m.SuccessCondition, m.FailureCondition = str(spec, "successCondition"), str(spec, "failureCondition")
	m.Interval, m.FailureLimit = str(spec, "interval"), int(integer(spec["failureLimit"]))
	for k := range mapAt(spec, "provider") {
		m.Provider = k
		break
	}
}

// reduceMeasurements keeps the latest measurements of a metric, and names
// the Job a job metric ran.
func reduceMeasurements(m *Metric, items []any, ns string) {
	for _, it := range items {
		mm := asMap(it)
		at := parseTime(str(mm, "finishedAt"))
		if at == 0 {
			at = parseTime(str(mm, "startedAt"))
		}
		m.Measurements = append(m.Measurements, Measurement{Phase: str(mm, "phase"), Message: str(mm, "message"), Value: str(mm, "value"), At: at})
		if job := str(mapAt(mm, "metadata"), "job-name"); job != "" {
			m.Job = &JobRef{Name: job, Namespace: ns}
			if m.Provider == "" {
				m.Provider = "job"
			}
		}
	}
	if len(m.Measurements) > maxMeasurements {
		m.Measurements = m.Measurements[len(m.Measurements)-maxMeasurements:]
	}
}
