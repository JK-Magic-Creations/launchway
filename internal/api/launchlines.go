package api

import (
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// A launch line is a read model for the Home page: where each recent change is
// on its way from the commit to running software. It is worked out from a run's
// steps and deployments, and nothing about it is stored. The stations are the
// places the change has to pass: the commit, its checks, its tests, the package
// (image) built from it, each environment it is deployed to, and "verified".
// A station is left out when the run has nothing that belongs to it.

// Station states.
const (
	stationDone    = "done"
	stationRunning = "running"
	// stationHolding: every step of the station that is active is queued: it waits for a runner or for room.
	// It is drawn without a spinner: nothing is running.
	stationHolding = "holding"
	stationWaiting = "waiting"
	stationFailed  = "failed"
	stationIdle    = "idle"
	stationSkipped = "skipped"
)

// Station is one place on a change's way.
type Station struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	State string `json:"state"`
	// Detail is one short plain phrase: what happened here, or why it waits.
	Detail string `json:"detail"`
	// Step is the step to open to see more, when one stands for the station.
	Step string `json:"step,omitempty"`
	// Parallel is set when the station stands for several steps: how many, how many run together, how each is doing.
	Parallel *Parallel `json:"parallel,omitempty"`
}

// LaunchLine is one run drawn as stations, with one sentence about where it is.
type LaunchLine struct {
	Run        string    `json:"run"`
	Number     int       `json:"number"`
	Project    string    `json:"project"`
	Message    string    `json:"message"`
	Actor      string    `json:"actor"`
	Ref        string    `json:"ref"`
	SHA        string    `json:"sha"`
	Status     string    `json:"status"`
	CreatedAt  int64     `json:"created_at"`
	FinishedAt int64     `json:"finished_at"`
	Stations   []Station `json:"stations"`
	// Tone is the worst state on the line: bad, wait, run or ok.
	Tone string `json:"tone"`
	// Now says where the change is, in one plain sentence.
	Now string `json:"now"`
}

func (s *Server) launchRoutes() []route {
	return []route{{"GET /api/v1/launch-lines", signedIn, "", s.listLaunchLines}}
}

func (s *Server) listLaunchLines(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 12
	}
	limit = min(limit, 50)
	only, err := s.visibleProjects(identityFrom(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	if id := q.Get("run"); id != "" { // the line of one run, as the pull request page asks for its latest
		d, err := s.Engine.GetRun(id)
		if err == nil && only != nil && !slices.Contains(only, d.Project) {
			err = engine.ErrNotFound
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, []LaunchLine{launchLineOf(d)})
		return
	}
	runs, err := s.Engine.ListRuns(engine.RunFilter{Project: q.Get("project"), Limit: limit, OnlyProjects: only})
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]LaunchLine, 0, len(runs))
	for _, sum := range runs {
		d, err := s.Engine.GetRun(sum.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		out = append(out, launchLineOf(d))
	}
	writeJSON(w, http.StatusOK, out)
}

// stationBuild collects the steps that belong to one station.
type stationBuild struct {
	Station
	order int
	steps []engine.StepView
}

// launchLineOf works out the line of one run.
func launchLineOf(d *engine.RunDetail) LaunchLine {
	byID := map[string]engine.StepView{}
	for _, st := range d.StepList {
		byID[st.ID] = st
	}
	deps := map[string]engine.DeploymentView{}
	for _, dp := range d.Deployments { // newest first: the first one seen for a step is its latest
		if _, ok := deps[dp.Step]; !ok {
			deps[dp.Step] = dp
		}
	}
	stations := map[string]*stationBuild{}
	add := func(key, label string, order int, st engine.StepView) {
		b := stations[key]
		if b == nil {
			b = &stationBuild{Station: Station{Key: key, Label: label}, order: order}
			stations[key] = b
		}
		b.steps = append(b.steps, st)
	}
	envOf := func(st engine.StepView) string {
		if st.Environment != "" {
			return st.Environment
		}
		if dep, ok := byID[st.Deployment]; ok {
			return dep.Environment
		}
		return ""
	}
	after := stepsAfterGates(d.StepList)
	for _, st := range d.StepList {
		switch st.Kind {
		case "deploy", "rollout", "rollback", "approval":
			if env := envOf(st); env != "" && (st.Kind != "rollback" || st.Status != engine.StepSkippedS) {
				add("env:"+env, env, 40+st.Position, st)
				continue
			}
			if st.Kind == "approval" {
				add("approval", "Approval", 30+st.Position, st)
			}
		case "image":
			add("package", "Package", 20, st)
		case "verify":
			add("verified", "Verified", 1000, st)
		default:
			id := strings.ToLower(st.ID)
			switch {
			case st.Tests != nil || strings.Contains(id, "test"):
				add("tests", "Tests", 10, st)
			case strings.Contains(id, "package") || strings.Contains(id, "build") || strings.Contains(id, "image"):
				add("package", "Package", 20, st)
			case after[st.ID]:
				add("release", "Release", 90+st.Position, st)
			default:
				add("checks", "Checks", 5, st)
			}
		}
	}
	list := make([]*stationBuild, 0, len(stations))
	for _, b := range stations {
		list = append(list, b)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].order < list[j].order })

	line := LaunchLine{Run: d.ID, Number: d.Number, Project: d.Project, Message: d.Message, Actor: d.Actor, Ref: d.Ref,
		SHA: d.SHA, Status: string(d.Status), CreatedAt: d.CreatedAt, FinishedAt: d.FinishedAt}
	line.Stations = append(line.Stations, Station{Key: "commit", Label: "Commit", State: stationDone, Detail: shortSHA(d.SHA)})
	for _, b := range list {
		resolve(b, deps, d)
		b.Parallel = parallelOf(b.steps, d.Levels)
		line.Stations = append(line.Stations, b.Station)
	}
	// Verified stands at the end of a line that deploys anywhere: when no verify
	// step says so, it holds once the last environment reports healthy.
	if hasEnv(line.Stations) && stations["verified"] == nil {
		line.Stations = append(line.Stations, verifiedFrom(line.Stations, d))
	}
	line.Tone, line.Now = whereItIs(d, line.Stations)
	return line
}

// stepsAfterGates finds the plain steps that come after an approval or a deploy: they belong to the release,
// not to the checks before it.
func stepsAfterGates(steps []engine.StepView) map[string]bool {
	byID := map[string]engine.StepView{}
	for _, st := range steps {
		byID[st.ID] = st
	}
	var gated func(id string, seen map[string]bool) bool
	gated = func(id string, seen map[string]bool) bool {
		if seen[id] {
			return false
		}
		seen[id] = true
		for _, n := range byID[id].Needs {
			up := byID[n]
			if up.Kind == "approval" || up.Kind == "deploy" || up.Kind == "rollout" || gated(n, seen) {
				return true
			}
		}
		return false
	}
	out := map[string]bool{}
	for _, st := range steps {
		if gated(st.ID, map[string]bool{}) {
			out[st.ID] = true
		}
	}
	return out
}

func hasEnv(ss []Station) bool {
	for _, s := range ss {
		if strings.HasPrefix(s.Key, "env:") {
			return true
		}
	}
	return false
}

// resolve gives a station its state and detail from its steps and deployments.
func resolve(b *stationBuild, deps map[string]engine.DeploymentView, d *engine.RunDetail) {
	var failed, waiting, running, holding, done, skipped, idle int
	var failedStep, waitStep, runStep *engine.StepView
	for i := range b.steps {
		st := &b.steps[i]
		switch st.Status {
		case engine.StepFailed:
			if st.AllowedFailure {
				done++
			} else {
				failed++
				if failedStep == nil {
					failedStep = st
				}
			}
		case engine.StepWaitingS:
			waiting++
			if waitStep == nil {
				waitStep = st
			}
		case engine.StepRunning, engine.StepLeasedS:
			running++
			if runStep == nil {
				runStep = st
			}
		case engine.StepQueuedS:
			holding++
		case engine.StepSucceeded:
			done++
		case engine.StepSkippedS, engine.StepCanceledS:
			skipped++
		default:
			idle++
		}
	}
	switch {
	case failed > 0:
		b.State, b.Step = stationFailed, failedStep.ID
		b.Detail = failureDetail(failedStep)
	case waiting > 0:
		b.State, b.Step = stationWaiting, waitStep.ID
		b.Detail = waitReason(waitStep, deps, d)
	case running > 0:
		b.State, b.Step = stationRunning, runStep.ID
		b.Detail = runningReason(runStep, deps, d)
	case holding > 0:
		held := b.firstWith(engine.StepQueuedS)
		b.State, b.Step = stationHolding, held.ID
		b.Detail = holdingReason(held, d)
	case done > 0 && idle == 0:
		b.State = stationDone
		b.Step = b.steps[len(b.steps)-1].ID
		b.Detail = doneDetail(b, deps)
	case done == 0 && idle == 0 && skipped > 0:
		b.State, b.Detail = stationSkipped, "Skipped"
	default:
		b.State, b.Detail = stationIdle, "Not started"
		if done > 0 && d.Status == engine.RunRunning {
			b.State, b.Detail = stationRunning, strconv.Itoa(done)+" of "+strconv.Itoa(len(b.steps))+" steps done"
		}
	}
	// A deploy that succeeded as a step may still be rolling out.
	if b.State == stationDone && strings.HasPrefix(b.Key, "env:") {
		for _, st := range b.steps {
			if dep, ok := deps[st.ID]; ok && dep.Phase != "" && dep.Phase != "healthy" && dep.Verdict == "" {
				b.State, b.Step, b.Detail = stationRunning, st.ID, dep.Detail
			}
		}
	}
}

// failureDetail says what failed in one line: the server's own words, with the step named.
func failureDetail(st *engine.StepView) string {
	e := stationLine(st.Error, "")
	switch {
	case e == "":
		return st.ID + " failed"
	case strings.HasPrefix(e, "exit code"):
		return st.ID + " ended with " + e
	}
	return e
}

func doneDetail(b *stationBuild, deps map[string]engine.DeploymentView) string {
	switch {
	case strings.HasPrefix(b.Key, "env:"):
		for _, st := range b.steps {
			if dep, ok := deps[st.ID]; ok && dep.Version != "" {
				return "Version " + dep.Version
			}
		}
		return "Deployed"
	case b.Key == "tests":
		for _, st := range b.steps {
			if st.Tests != nil {
				return strconv.Itoa(st.Tests.Passed) + " passed"
			}
		}
		return "Passed"
	case b.Key == "package":
		return "Built"
	}
	return "Passed"
}

// waitReason says why a step waits, in plain words, from what the server knows.
func waitReason(st *engine.StepView, deps map[string]engine.DeploymentView, d *engine.RunDetail) string {
	if st.Kind == "approval" {
		if st.Message != "" {
			return "Waiting for a decision: " + st.Message
		}
		return "Waiting for a person to approve"
	}
	if dep, ok := deps[st.ID]; ok && dep.Detail != "" {
		return dep.Detail
	}
	switch st.WaitKind {
	case "lock":
		return "Waiting for another deployment to leave " + st.Environment
	case "observe":
		return "Waiting for a deploy runner to report on " + st.Environment
	case "verify":
		return "Waiting for a person to confirm the result"
	}
	return "Waiting"
}

func runningReason(st *engine.StepView, deps map[string]engine.DeploymentView, d *engine.RunDetail) string {
	if dep, ok := deps[st.ID]; ok && dep.Detail != "" {
		return dep.Detail
	}
	if st.Status == engine.StepLeasedS {
		return "A runner has taken " + st.ID + " and is starting it"
	}
	return st.ID + " is running"
}

// firstWith is the first step of the station in this status.
func (b *stationBuild) firstWith(status engine.StepStatus) *engine.StepView {
	for i := range b.steps {
		if b.steps[i].Status == status {
			return &b.steps[i]
		}
	}
	return &b.steps[0]
}

// holdingReason says why a queued step is not running: the one reason every screen shows for it.
func holdingReason(st *engine.StepView, d *engine.RunDetail) string {
	switch {
	case st.Attempt > 1 && st.FailureCause == "infrastructure":
		return "Holding: retrying on another runner, attempt " + strconv.Itoa(st.Attempt) + ", after the last one stopped responding"
	case st.Attempt > 1 && st.FailureCause != "":
		return "Holding: retrying " + st.ID + ", attempt " + strconv.Itoa(st.Attempt)
	case st.WaitingFor != "":
		return "Holding: " + st.WaitingFor
	case d.PoolHint != "":
		return "Holding: " + d.PoolHint
	case d.RunnerHint != "":
		return "Holding: " + d.RunnerHint
	}
	return "Holding: waiting for a runner to pick up " + st.ID
}

func verifiedFrom(ss []Station, d *engine.RunDetail) Station {
	last := Station{}
	for _, s := range ss {
		if strings.HasPrefix(s.Key, "env:") {
			last = s
		}
	}
	v := Station{Key: "verified", Label: "Verified", State: stationIdle, Detail: "Not yet"}
	switch last.State {
	case stationDone:
		if healthyEverywhere(d) {
			v.State, v.Detail = stationDone, "Healthy"
		} else {
			v.State, v.Detail = stationRunning, "Checking that it is healthy"
		}
	case stationFailed:
		v.State, v.Detail = stationSkipped, "Not reached"
	}
	return v
}

func healthyEverywhere(d *engine.RunDetail) bool {
	if len(d.Deployments) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, dp := range d.Deployments {
		if seen[dp.Step] {
			continue
		}
		seen[dp.Step] = true
		if dp.Phase != "healthy" && dp.Verdict != "deployed" {
			return false
		}
	}
	return true
}

// whereItIs picks the tone of the line and one sentence about its first
// station that is not done.
func whereItIs(d *engine.RunDetail, ss []Station) (tone, now string) {
	if d.Status == engine.RunCanceled {
		return "idle", "Canceled before it finished."
	}
	for _, s := range ss {
		switch s.State {
		case stationFailed:
			return "bad", "Stopped at " + lower(s.Label) + ". " + s.Detail
		}
	}
	for _, s := range ss {
		switch s.State {
		case stationWaiting:
			return "wait", s.Label + ": " + s.Detail
		}
	}
	for _, s := range ss {
		if s.State == stationRunning {
			return "run", s.Label + ": " + s.Detail
		}
	}
	for _, s := range ss {
		if s.State == stationHolding {
			return "hold", s.Label + ": " + s.Detail
		}
	}
	for _, s := range ss {
		if s.State == stationIdle {
			return "run", "Next: " + lower(s.Label) + "."
		}
	}
	return "ok", "Everything passed."
}

func lower(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func stationLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 140 {
		s = s[:140] + "..."
	}
	return s
}
