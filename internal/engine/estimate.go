package engine

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Time estimates. What a step usually takes is read from the same step of
// earlier runs of the same pipeline in the same project: no table of its
// own, only the step records that already exist. Nothing here pretends to be
// exact: a step has a typical time (the median) and a range (the 20th to the
// 80th percentile), the whole run has the longest path through what is
// left, and a step or run with too little history says so.

const (
	// MinEstimateSamples is how many earlier successful runs of a step are
	// needed before it gets an estimate.
	MinEstimateSamples = 3
	// estimateRuns is how many earlier runs are looked at.
	estimateRuns = 60
	// recencyWeight makes each older sample count a little less (the
	// 4th-newest counts about half).
	recencyWeight = 0.85
	// historyTTL is how long what was read from the database is reused.
	historyTTL = 20 * time.Second
)

// StepEstimate is what a step usually takes.
type StepEstimate struct {
	// Samples is how many earlier runs of the step it is based on.
	Samples int `json:"samples"`
	// Enough says there were at least MinEstimateSamples; without it the
	// durations below are zero.
	Enough bool `json:"enough"`
	// TypicalMs is the median; LowMs and HighMs the 20th and 80th
	// percentiles; SlowMs the 90th: past it a step is taking longer than
	// usual.
	TypicalMs int64 `json:"typical_ms,omitempty"`
	LowMs     int64 `json:"low_ms,omitempty"`
	HighMs    int64 `json:"high_ms,omitempty"`
	SlowMs    int64 `json:"slow_ms,omitempty"`
	// Basis says whose history it is: "same_kind" (runs of the same kind,
	// pull requests or not) or "all_runs" (too few of the same kind).
	Basis string `json:"basis"`
	// Bimodal is set when the history has two distinct groups of times, as a
	// step does that sometimes finds its cache and sometimes does not.
	// TypicalMs is then the more common group's.
	Bimodal *Bimodal `json:"bimodal,omitempty"`
	// Person marks a step that waits for a person (an approval): it has no
	// estimate and is left out of the countdown.
	Person bool `json:"person,omitempty"`
	// LastRun and LastRunNumber name the latest earlier run in which the
	// step succeeded, to compare with.
	LastRun       string `json:"last_run,omitempty"`
	LastRunNumber int    `json:"last_run_number,omitempty"`
	LastMs        int64  `json:"last_ms,omitempty"`
}

// Bimodal describes a history of two groups of times.
type Bimodal struct {
	FastMs    int64   `json:"fast_ms"`
	SlowMs    int64   `json:"slow_ms"`
	FastShare float64 `json:"fast_share"`
}

// RunEstimate is what a run is expected to take.
type RunEstimate struct {
	// AsOf is when RemainingMs was worked out (ms since 1970).
	AsOf int64 `json:"as_of"`
	// Known says there is an estimate: at least some of the steps still to
	// run have history. Reason says why not otherwise: "no_history" (none of
	// the steps has run before) or "too_few" (fewer than MinEstimateSamples
	// earlier runs).
	Known  bool   `json:"known"`
	Reason string `json:"reason,omitempty"`
	// TotalMs is what the whole run usually takes, from its start to its
	// end, along its longest path.
	TotalMs int64 `json:"total_ms,omitempty"`
	// RemainingMs is the usual time left, with the range from the steps'
	// low and high times; zero once the run is over.
	RemainingMs     int64 `json:"remaining_ms,omitempty"`
	RemainingLowMs  int64 `json:"remaining_low_ms,omitempty"`
	RemainingHighMs int64 `json:"remaining_high_ms,omitempty"`
	// Partial: some steps still to run have no history, so the time left is
	// at least this. UnknownSteps names them.
	Partial      bool     `json:"partial,omitempty"`
	UnknownSteps []string `json:"unknown_steps,omitempty"`
	// Overrun: a running step is past the usual range, so the time left is
	// less certain than usual.
	Overrun bool `json:"overrun,omitempty"`
	// WaitingForPerson names steps waiting for a decision. Their wait is not
	// counted; the time left is what remains after the decision.
	WaitingForPerson []string `json:"waiting_for_person,omitempty"`
	// Queued lists steps that wait for a runner or room, with why. The wait
	// is not counted either.
	Queued []QueuedStep `json:"queued,omitempty"`
	// Basis is "same_kind" or "all_runs" (see StepEstimate).
	Basis string `json:"basis,omitempty"`
	// Once the run is over: how long it took, how long such a run
	// usually takes, and the difference in percent (negative: faster).
	TookMs      int64 `json:"took_ms,omitempty"`
	UsualMs     int64 `json:"usual_ms,omitempty"`
	DiffPercent int   `json:"diff_percent,omitempty"`
	// Runs is how many earlier runs the history holds.
	Runs int `json:"runs"`
}

// QueuedStep is a step waiting to be taken, and why.
type QueuedStep struct {
	Step   string `json:"step"`
	Since  int64  `json:"since"`
	Reason string `json:"reason,omitempty"`
}

// stepSample is one earlier run of a step.
type stepSample struct {
	run    string
	number int
	ms     int64
	kind   string // "pull_request" or "branch"
	person bool
}

// history is what earlier runs of one pipeline took.
type history struct {
	steps map[string][]stepSample // newest first
	runs  []runSample             // newest first
	read  time.Time
	stamp int64 // the newest finish among the pipeline's runs when this was read
}

type runSample struct {
	run  string
	ms   int64
	kind string
}

func runKind(event string) string {
	if event == "pull_request" || event == "draft" {
		return "pull_request"
	}
	return "branch"
}

type historyCache struct {
	mu sync.Mutex
	m  map[string]*history
}

// pipelineHistory reads (or reuses) what the last runs of a pipeline did.
func (e *Engine) pipelineHistory(project, pipeline, exceptRun string) (*history, error) {
	key := project + "\x00" + pipeline
	e.hist.mu.Lock()
	h := e.hist.m[key]
	e.hist.mu.Unlock()
	// A run that has ended since the last read makes it stale at once.
	var stamp int64
	if err := e.db.QueryRow(`SELECT COALESCE(MAX(finished_at), 0) FROM runs WHERE project = ? AND pipeline = ?`, project, pipeline).Scan(&stamp); err != nil {
		return nil, err
	}
	if h != nil && time.Since(h.read) < historyTTL && h.stamp == stamp {
		return h, nil
	}
	h = &history{steps: map[string][]stepSample{}, read: time.Now(), stamp: stamp}
	rows, err := e.db.Query(`SELECT s.step_id, r.id, r.number, r.event, s.started_at, s.finished_at, s.decided_by
		FROM steps s JOIN runs r ON r.id = s.run_id
		WHERE r.id IN (SELECT id FROM runs WHERE project = ? AND pipeline = ? ORDER BY created_at DESC LIMIT ?)
		AND s.status = 'succeeded' AND s.started_at > 0 AND s.finished_at >= s.started_at
		ORDER BY r.created_at DESC, s.position`, project, pipeline, estimateRuns)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, run, event, by string
		var number int
		var from, to int64
		if err := rows.Scan(&id, &run, &number, &event, &from, &to, &by); err != nil {
			rows.Close()
			return nil, err
		}
		h.steps[id] = append(h.steps[id], stepSample{run: run, number: number, ms: to - from, kind: runKind(event), person: by != ""})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rr, err := e.db.Query(`SELECT id, event, started_at, finished_at FROM runs WHERE project = ? AND pipeline = ? AND status = 'succeeded'
		AND started_at > 0 AND finished_at >= started_at ORDER BY created_at DESC LIMIT ?`, project, pipeline, estimateRuns)
	if err != nil {
		return nil, err
	}
	defer rr.Close()
	for rr.Next() {
		var id, event string
		var from, to int64
		if err := rr.Scan(&id, &event, &from, &to); err != nil {
			return nil, err
		}
		h.runs = append(h.runs, runSample{run: id, ms: to - from, kind: runKind(event)})
	}
	if err := rr.Err(); err != nil {
		return nil, err
	}
	e.hist.mu.Lock()
	if e.hist.m == nil {
		e.hist.m = map[string]*history{}
	}
	e.hist.m[key] = h
	e.hist.mu.Unlock()
	return h, nil
}

// weighted is a value with the weight of its age.
type weighted struct {
	v float64
	w float64
}

// quantile is the q-th (0..1) weighted quantile of sorted values.
func quantile(s []weighted, q float64) float64 {
	if len(s) == 0 {
		return 0
	}
	var total float64
	for _, x := range s {
		total += x.w
	}
	target := q * total
	var acc float64
	for _, x := range s {
		acc += x.w
		if acc >= target {
			return x.v
		}
	}
	return s[len(s)-1].v
}

func sortedByValue(v []weighted) []weighted {
	out := append([]weighted(nil), v...)
	sort.Slice(out, func(i, j int) bool { return out[i].v < out[j].v })
	return out
}

// dropOutliers removes values far above the rest (above the third quartile
// by three times the spread between the quartiles), when there are enough
// values to tell.
func dropOutliers(v []weighted) []weighted {
	if len(v) < 6 {
		return v
	}
	s := sortedByValue(v)
	q1, q3 := quantile(s, 0.25), quantile(s, 0.75)
	fence := q3 + 3*(q3-q1)
	if q3 == q1 {
		return v
	}
	var out []weighted
	for _, x := range v {
		if x.v <= fence {
			out = append(out, x)
		}
	}
	return out
}

// findBimodal splits the values in two groups at their largest gap, and
// reports it when the groups are clearly apart and both are real (at least
// two values and a fifth of the total).
func findBimodal(v []weighted) (lo, hi []weighted, ok bool) {
	if len(v) < 5 {
		return nil, nil, false
	}
	s := sortedByValue(v)
	best, at := 0.0, -1
	for i := 1; i < len(s); i++ {
		if s[i-1].v <= 0 {
			continue
		}
		if g := s[i].v / s[i-1].v; g > best {
			best, at = g, i
		}
	}
	if at < 0 || best < 1.6 {
		return nil, nil, false
	}
	lo, hi = s[:at], s[at:]
	minN := max(2, int(math.Ceil(float64(len(s))*0.2)))
	if len(lo) < minN || len(hi) < minN {
		return nil, nil, false
	}
	if quantile(hi, 0.5) < 2.5*quantile(lo, 0.5) {
		return nil, nil, false
	}
	return lo, hi, true
}

func shareOf(part, all []weighted) float64 {
	var p, a float64
	for _, x := range part {
		p += x.w
	}
	for _, x := range all {
		a += x.w
	}
	if a == 0 {
		return 0
	}
	return p / a
}

// estimateStep works out what a step usually takes from its earlier runs
// (newest first). kind prefers runs of the same kind (pull request or not)
// when there are enough of them.
func estimateStep(samples []stepSample, kind string) *StepEstimate {
	est := &StepEstimate{Basis: "all_runs"}
	for _, s := range samples {
		if s.person {
			est.Person = true
		}
	}
	use := samples
	var same []stepSample
	for _, s := range samples {
		if s.kind == kind {
			same = append(same, s)
		}
	}
	if len(same) >= MinEstimateSamples {
		use, est.Basis = same, "same_kind"
	}
	est.Samples = len(use)
	if len(samples) > 0 {
		est.LastRun, est.LastRunNumber, est.LastMs = samples[0].run, samples[0].number, samples[0].ms
	}
	if est.Person {
		est.Enough = false
		return est
	}
	if len(use) < MinEstimateSamples {
		return est
	}
	est.Enough = true
	vals := make([]weighted, len(use))
	w := 1.0
	for i, s := range use {
		vals[i] = weighted{float64(s.ms), w}
		w *= recencyWeight
	}
	if lo, hi, ok := findBimodal(vals); ok {
		fastShare := shareOf(lo, vals)
		main := hi
		if fastShare >= 0.5 {
			main = lo
		}
		est.Bimodal = &Bimodal{FastMs: int64(quantile(sortedByValue(lo), 0.5)), SlowMs: int64(quantile(sortedByValue(hi), 0.5)), FastShare: math.Round(fastShare*100) / 100}
		vals = main
	} else {
		vals = dropOutliers(vals)
	}
	s := sortedByValue(vals)
	est.TypicalMs = int64(quantile(s, 0.5))
	est.LowMs = int64(quantile(s, 0.2))
	est.HighMs = int64(quantile(s, 0.8))
	est.SlowMs = int64(quantile(s, 0.9))
	if est.Bimodal != nil {
		// Past the slower group's usual time is the only "slow".
		est.SlowMs = max(est.SlowMs, est.Bimodal.SlowMs)
	}
	return est
}

// estStep is a step as the remaining-time calculation sees it.
type estStep struct {
	ID        string
	Needs     []string
	Status    StepStatus
	StartedAt int64
	QueuedAt  int64
	Person    bool // waits for a person: an approval or a decision
	Reason    string
}

// remaining is the longest path through the steps still to run, starting
// now. Steps that wait for a person count for nothing; steps with no
// estimate count for nothing and are listed.
func remaining(steps []estStep, ests map[string]*StepEstimate, now int64) (typ, low, high int64, unknown, waiting []string, overrun bool) {
	byID := map[string]*estStep{}
	for i := range steps {
		byID[steps[i].ID] = &steps[i]
	}
	type fin struct{ typ, low, high int64 }
	done := map[string]fin{}
	var visit func(id string, depth int) fin
	visit = func(id string, depth int) fin {
		if f, ok := done[id]; ok {
			return f
		}
		s := byID[id]
		if s == nil || depth > len(steps) {
			return fin{}
		}
		var start fin
		for _, n := range s.Needs {
			f := visit(n, depth+1)
			start.typ, start.low, start.high = max(start.typ, f.typ), max(start.low, f.low), max(start.high, f.high)
		}
		var d fin
		est := ests[id]
		switch {
		case s.Status.Terminal():
			done[id] = fin{}
			return fin{}
		case s.Person || (est != nil && est.Person):
			waiting = append(waiting, id)
		case est == nil || !est.Enough:
			unknown = append(unknown, id)
		case s.Status.Active() && s.StartedAt > 0:
			el := now - s.StartedAt
			left := func(usual int64) int64 {
				if el < usual {
					return usual - el
				}
				return max(usual/10, 3000)
			}
			d = fin{left(est.TypicalMs), left(est.LowMs), left(est.HighMs)}
			if el > est.HighMs {
				overrun = true
			}
		default:
			d = fin{est.TypicalMs, est.LowMs, est.HighMs}
		}
		f := fin{start.typ + d.typ, start.low + d.low, start.high + d.high}
		done[id] = f
		return f
	}
	for _, s := range steps {
		f := visit(s.ID, 0)
		typ, low, high = max(typ, f.typ), max(low, f.low), max(high, f.high)
	}
	sort.Strings(unknown)
	sort.Strings(waiting)
	return
}

// estimateFor works out the step estimates and the run's estimate from
// the history. ran is how many of the steps have started or finished.
func estimateFor(h *history, exceptRun, kind string, steps []estStep, now int64, runDone bool, tookMs int64) (*RunEstimate, map[string]*StepEstimate) {
	ests := map[string]*StepEstimate{}
	re := &RunEstimate{AsOf: now, Runs: len(h.runs), Basis: "all_runs"}
	same := 0
	known := 0
	for _, s := range steps {
		est := estimateStep(withoutRun(h.steps[s.ID], exceptRun), kind)
		ests[s.ID] = est
		if est.Enough {
			known++
			if est.Basis == "same_kind" {
				same++
			}
		}
	}
	if known > 0 && same*2 >= known {
		re.Basis = "same_kind"
	}
	if runDone {
		var usual []weighted
		var any []weighted
		w := 1.0
		for _, r := range h.runs {
			if r.run == exceptRun {
				continue
			}
			any = append(any, weighted{float64(r.ms), w})
			if r.kind == kind {
				usual = append(usual, weighted{float64(r.ms), w})
			}
			w *= recencyWeight
		}
		if len(usual) < MinEstimateSamples {
			usual = any
		}
		if len(usual) >= MinEstimateSamples && tookMs > 0 {
			re.Known = true
			re.TookMs = tookMs
			re.UsualMs = int64(quantile(sortedByValue(dropOutliers(usual)), 0.5))
			if re.UsualMs > 0 {
				re.DiffPercent = int(math.Round(100 * float64(tookMs-re.UsualMs) / float64(re.UsualMs)))
			}
		} else {
			re.Reason = "too_few"
		}
		return re, ests
	}
	fresh := make([]estStep, len(steps))
	for i, s := range steps {
		fresh[i] = estStep{ID: s.ID, Needs: s.Needs, Status: StepPending, Person: s.Person}
	}
	re.TotalMs, _, _, _, _, _ = remaining(fresh, ests, now)
	typ, low, high, unknown, waiting, overrun := remaining(steps, ests, now)
	re.RemainingMs, re.RemainingLowMs, re.RemainingHighMs = typ, low, high
	re.UnknownSteps, re.WaitingForPerson, re.Overrun = unknown, waiting, overrun
	for _, s := range steps {
		if s.Status == StepQueuedS {
			re.Queued = append(re.Queued, QueuedStep{Step: s.ID, Since: s.QueuedAt, Reason: s.Reason})
		}
	}
	remainingSteps := 0
	for _, s := range steps {
		if !s.Status.Terminal() {
			remainingSteps++
		}
	}
	switch {
	case known == 0 && len(h.runs) == 0 && len(h.steps) == 0:
		re.Reason = "no_history"
	case known == 0:
		re.Reason = "too_few"
	default:
		re.Known = true
	}
	// Nothing left that could be estimated, though steps remain: only
	// people to wait for, or steps never run before.
	re.Partial = len(unknown) > 0 && re.Known
	if re.Known && typ == 0 && len(unknown) > 0 && len(waiting) == 0 {
		re.Known, re.Reason = false, "no_history"
	}
	return re, ests
}

// attachEstimates fills the estimates of a run's detail.
func (e *Engine) attachEstimates(d *RunDetail) {
	h, err := e.pipelineHistory(d.Project, d.Pipeline, d.ID)
	if err != nil || h == nil {
		return
	}
	now := e.now().UnixMilli()
	steps := make([]estStep, len(d.StepList))
	for i, v := range d.StepList {
		steps[i] = estStep{ID: v.ID, Needs: v.Needs, Status: v.Status, StartedAt: v.StartedAt, QueuedAt: v.QueuedAt,
			Person: v.Kind == "approval" || (v.Status == StepWaitingS && v.WaitKind == ""), Reason: v.WaitingFor}
	}
	done := d.Status.Terminal()
	var took int64
	if done && d.Status == RunSucceeded && d.StartedAt > 0 && d.FinishedAt >= d.StartedAt {
		took = d.FinishedAt - d.StartedAt
	}
	est, per := estimateFor(h, d.ID, runKind(d.Event), steps, now, done && d.Status == RunSucceeded, took)
	if done && d.Status != RunSucceeded {
		est = &RunEstimate{AsOf: now, Runs: len(h.runs)}
	}
	d.Estimate = est
	for i := range d.StepList {
		d.StepList[i].Estimate = per[d.StepList[i].ID]
	}
}

func withoutRun(s []stepSample, run string) []stepSample {
	for i := range s {
		if s[i].run == run {
			out := append([]stepSample(nil), s[:i]...)
			for _, x := range s[i+1:] {
				if x.run != run {
					out = append(out, x)
				}
			}
			return out
		}
	}
	return s
}

// listEstimates gives the runs that are running in a list their time
// left (only a few: each costs a read of its steps).
func (e *Engine) listEstimates(runs []*RunSummary) {
	n := 0
	for _, r := range runs {
		if r.Status != RunRunning || n >= 12 {
			continue
		}
		n++
		h, err := e.pipelineHistory(r.Project, r.Pipeline, r.ID)
		if err != nil {
			continue
		}
		rows, err := e.db.Query(`SELECT step_id, started_at, queued_at FROM steps WHERE run_id = ?`, r.ID)
		if err != nil {
			continue
		}
		started := map[string][2]int64{}
		for rows.Next() {
			var id string
			var a, b int64
			if rows.Scan(&id, &a, &b) == nil {
				started[id] = [2]int64{a, b}
			}
		}
		rows.Close()
		waiting := map[string]bool{}
		for _, w := range r.Waiting {
			waiting[w] = true
		}
		steps := make([]estStep, len(r.Steps))
		for i, b := range r.Steps {
			steps[i] = estStep{ID: b.ID, Needs: b.Needs, Status: b.Status, StartedAt: started[b.ID][0], QueuedAt: started[b.ID][1], Person: waiting[b.ID]}
		}
		est, _ := estimateFor(h, r.ID, runKind(r.Event), steps, e.now().UnixMilli(), false, 0)
		r.Estimate = est
	}
}
