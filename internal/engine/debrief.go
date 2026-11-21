package engine

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/debrief"
)

// The Debrief of a run: what its tests say, put in the order a person needs
// them (see internal/debrief for the model and the exports). The engine
// builds it from the stored results; nothing here changes the run.

// DebriefOptions say how a Debrief is built.
type DebriefOptions struct {
	// DefaultRef is the project's default branch as a full ref, the base
	// the run is compared with.
	DefaultRef string
	// Brand is the installation's name, logo and address.
	Brand debrief.Brand
	// EmbedImages reads small image attachments into the report (for the
	// HTML and PDF exports); the API's JSON never carries image bytes.
	EmbedImages bool
	// MaxFailures bounds the failures listed in full (default 100).
	MaxFailures int
	// ChangeLines are the lines a pull request changed, by file, set by the
	// API for runs of pull requests (see debrief_change.go).
	Change *ChangedLines
}

// Bounds of a Debrief.
const (
	debriefMaxFailures  = 100
	debriefSlowest      = 10
	debriefSuiteNodes   = 400
	debriefMatrixRows   = 300
	debriefMaxImageSize = 256 << 10
	debriefMaxImages    = 8
	debriefTrendRuns    = 30
	debriefFlakyShown   = 50
)

type baseRef struct {
	debrief.RunRef
	id      string
	triples []triple
}

// dbuild holds what the phases of building a Debrief share.
type dbuild struct {
	e    *Engine
	o    DebriefOptions
	sum  *RunSummary
	st   *RunState
	r    *debrief.Report
	ef   *effective
	refs []string // the refs a reader may learn history of; nil is all
	quar map[string]QuarantineHit
	cond string
	args []any
	base *baseRef

	counted    []failRow
	basePassed map[string]bool
	shown      []failRow
	hist       map[string][]debrief.Observation
}

type failRow struct {
	id                                 int64
	key, step, suite, name, file, stat string
	ms                                 int64
	msg                                string
}

// Debrief builds the Debrief of a run.
func (e *Engine) Debrief(runID string, o DebriefOptions) (*debrief.Report, error) {
	sum, err := e.runSummary(runID)
	if err != nil {
		return nil, err
	}
	st, err := e.runState(runID)
	if err != nil {
		return nil, err
	}
	if o.MaxFailures <= 0 || o.MaxFailures > debriefMaxFailures {
		o.MaxFailures = debriefMaxFailures
	}
	b := &dbuild{e: e, o: o, sum: sum, st: st}
	b.r = &debrief.Report{
		Version: debrief.Version, Generated: e.now().UnixMilli(), Install: o.Brand,
		Project: sum.Project, Result: string(sum.Status),
		Run:      e.runRef(sum, o.Brand.URL),
		Failures: []debrief.Failure{}, Flaky: []debrief.FlakyCase{}, Quarantined: []debrief.Quarantined{},
		Slowest: []debrief.SlowCase{}, Suites: []debrief.Suite{}, Trend: []debrief.TrendPoint{}, Notes: []string{},
	}
	// What a reader of a restricted run may learn of other branches: its
	// own and the default branch, nothing else.
	if st.restricted() {
		b.refs = []string{sum.Ref}
		if o.DefaultRef != "" && o.DefaultRef != sum.Ref {
			b.refs = append(b.refs, o.DefaultRef)
		}
		b.r.Notes = append(b.r.Notes, "This run is restricted (a pull request from a fork or an automation account), so its history is limited to its own branch and the default branch.")
	}
	if !e.knows(schemaTests) {
		return b.r, nil
	}
	if b.ef, err = e.effectiveReports([]string{runID}); err != nil {
		return nil, err
	}
	t := b.ef.totals(runID)
	if t == nil {
		b.r.Notes = append(b.r.Notes, "No test results were collected for this run.")
		return b.r, nil
	}
	b.summarize(runID, t)
	steps := []func() error{b.findFailures, b.compare, b.slowest, b.failures, b.flaky, b.quarantined, b.structure, b.trendAndCoverage}
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, err
		}
	}
	return b.r, nil
}

func (b *dbuild) summarize(runID string, t *TestTotals) {
	r := b.r
	r.Summary = debrief.Summary{Total: t.Total, Passed: t.Passed, Failed: t.Failed, Errored: t.Errored, Skipped: t.Skipped,
		DurationMs: t.DurationMs, Truncated: t.Truncated}
	r.Summary.PassRate = 100
	if ran := t.Total - t.Skipped; ran > 0 {
		r.Summary.PassRate = float64(t.Passed) * 100 / float64(ran)
	}
	if t.Truncated {
		r.Notes = append(r.Notes, "Not every result was kept: a report was cut short or the run has more results than it keeps. The totals count them all.")
	}
	for _, step := range b.ef.steps[runID] {
		var d struct {
			Notes []string `json:"notes"`
		}
		_ = json.Unmarshal([]byte(step.detail), &d)
		for _, n := range d.Notes {
			if len(r.Notes) < 12 && !containsStr(r.Notes, n) {
				r.Notes = append(r.Notes, n)
			}
		}
	}
	b.quar = b.e.quarantineOf(b.ef, runID)
	b.cond, b.args = tripleFilter(b.ef.triples(runID))
}

// findFailures reads the failing results, leaving out those a live
// quarantine entry covers (an expired entry covers nothing), and finds the
// run to compare with.
func (b *dbuild) findFailures() error {
	var err error
	if b.base, err = b.e.baseFor(b.sum, b.o.DefaultRef); err != nil {
		return err
	}
	rows, err := b.e.db.Query(`SELECT tc.id, tc.test_key, tc.step_id, tc.suite, tc.name, tc.file, tc.status, tc.duration_ms, SUBSTR(tc.message, 1, ?)
		FROM test_cases tc WHERE `+b.cond+` AND tc.status IN ('failed','errored') ORDER BY tc.suite, tc.name LIMIT 2000`, append([]any{messagePreview}, b.args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var f failRow
		if err := rows.Scan(&f.id, &f.key, &f.step, &f.suite, &f.name, &f.file, &f.stat, &f.ms, &f.msg); err != nil {
			return err
		}
		if h, ok := b.quar[f.key]; ok && !h.Expired {
			continue
		}
		b.counted = append(b.counted, f)
	}
	return rows.Err()
}

// compare fills how the run moved against the base.
func (b *dbuild) compare() error {
	if b.base == nil {
		return nil
	}
	e, r := b.e, b.r
	failKeys := map[string]bool{}
	for _, f := range b.counted {
		failKeys[f.key] = true
	}
	r.Change = &debrief.Change{Base: b.base.RunRef, Slower: []debrief.Slower{}}
	var err error
	if b.basePassed, err = e.keysWithStatus(b.base.triples, []string{"passed"}, failKeys, 0); err != nil {
		return err
	}
	for k := range b.basePassed {
		r.Change.NewlyFailKeys = append(r.Change.NewlyFailKeys, k)
	}
	sort.Strings(r.Change.NewlyFailKeys)
	r.Change.NewlyFailing = len(b.basePassed)
	baseFailing, err := e.keysWithStatus(b.base.triples, []string{"failed", "errored"}, nil, 5000)
	if err != nil {
		return err
	}
	nowPassing, err := e.keysWithStatus(b.ef.triples(b.sum.ID), []string{"passed"}, baseFailing, 0)
	if err != nil {
		return err
	}
	r.Change.NewlyPassing = len(nowPassing)
	if len(nowPassing) > 0 {
		keys := make([]string, 0, len(nowPassing))
		for k := range nowPassing {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		keys = keys[:min(len(keys), 20)]
		names, err := e.testNames(b.sum.Project, keys)
		if err != nil {
			return err
		}
		for _, k := range keys {
			r.Change.NewlyPassNames = append(r.Change.NewlyPassNames, debrief.TestName{Key: k, Suite: names[k].Suite, Name: names[k].Name})
		}
	}
	bef, err := e.effectiveReports([]string{b.base.id})
	if err != nil {
		return err
	}
	if bt := bef.totals(b.base.id); bt != nil {
		r.Change.TotalDelta = r.Summary.Total - bt.Total
		r.Change.DurationDelta = r.Summary.DurationMs - bt.DurationMs
	}
	return nil
}

// slowest lists the slowest tests, what they usually take, and which are
// slower than on the base.
func (b *dbuild) slowest() error {
	e, r := b.e, b.r
	cs, err := e.slowestOf(b.cond, b.args, debriefSlowest)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(cs))
	for _, c := range cs {
		keys = append(keys, c.Key)
	}
	avgs, err := e.avgDurations(b.sum.Project, keys)
	if err != nil {
		return err
	}
	for _, c := range cs {
		r.Slowest = append(r.Slowest, debrief.SlowCase{TestName: debrief.TestName{Key: c.Key, Suite: c.Suite, Name: c.Name}, DurationMs: c.DurationMs, AvgMs: avgs[c.Key]})
	}
	if r.Change == nil {
		return nil
	}
	baseMs, err := e.durationsIn(b.base.triples, keys)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if m, ok := baseMs[c.Key]; ok && c.DurationMs >= 2*m && c.DurationMs-m >= 200 {
			r.Change.Slower = append(r.Change.Slower, debrief.Slower{TestName: debrief.TestName{Key: c.Key, Suite: c.Suite, Name: c.Name}, BaseMs: m, Ms: c.DurationMs})
		}
	}
	return nil
}

// failures fills the failures in full: new breakage first.
func (b *dbuild) failures() error {
	e, r, o := b.e, b.r, b.o
	counted := b.counted
	sort.SliceStable(counted, func(i, j int) bool { return b.basePassed[counted[i].key] && !b.basePassed[counted[j].key] })
	b.shown = counted
	if len(b.shown) > o.MaxFailures {
		b.shown = b.shown[:o.MaxFailures]
		r.Notes = append(r.Notes, fmt.Sprintf("The Debrief shows the first %d of %d failing tests in full; the Tests tab lists them all.", o.MaxFailures, len(counted)))
	}
	keys := make([]string, 0, len(b.shown))
	for _, f := range b.shown {
		keys = append(keys, f.key)
	}
	first, err := e.firstFailed(b.sum, keys, b.refs)
	if err != nil {
		return err
	}
	onBase, err := e.statusIn(b.base, o.DefaultRef, b.sum, keys)
	if err != nil {
		return err
	}
	if b.hist, err = e.observations(b.sum.Project, keys, b.refs, max(b.sum.Number-historyWindow, 0)); err != nil {
		return err
	}
	images := 0
	for _, f := range b.shown {
		full, err := e.TestCase(b.sum.ID, f.id)
		if err != nil {
			return err
		}
		fl := debrief.Failure{TestName: debrief.TestName{Key: f.key, Suite: f.suite, Name: f.name}, CaseID: f.id, File: f.file, Step: f.step,
			Status: f.stat, DurationMs: f.ms, Message: full.Message, Details: full.Details, Output: full.Output, Variant: full.Variant,
			NewHere: b.basePassed[f.key], FirstFailedIn: first[f.key], FailingOnBase: onBase[f.key], Retried: full.Retries > 0}
		base := strings.TrimRight(o.Brand.URL, "/")
		if o.Brand.URL != "" {
			fl.Link = fmt.Sprintf("%s/runs/%s?tab=tests&case=%d", base, b.sum.ID, f.id)
		}
		fl.Diff = debrief.Detect(full.Message, full.Details)
		fl.File, fl.Line = lineIn(f.file, full.Message, full.Details)
		for n, a := range full.Attachments {
			at := debrief.Attachment{Name: a.Name, Kind: a.Kind, ContentType: a.ContentType, Bytes: a.Size}
			if o.Brand.URL != "" {
				at.Link = fmt.Sprintf("%s/api/v1/runs/%s/tests/cases/%d/attachments/%d", base, b.sum.ID, f.id, n)
			}
			if o.EmbedImages && a.Kind == "image" && a.Size <= debriefMaxImageSize && a.Problem == "" && images < debriefMaxImages {
				if data := e.readAttachment(b.sum.ID, f.id, n); data != "" {
					at.Data = data
					images++
				}
			}
			fl.Attachments = append(fl.Attachments, at)
		}
		if v := debrief.Score(b.hist[f.key]); v.Flaky {
			fl.FlakyScore = v.Score
		}
		r.Failures = append(r.Failures, fl)
	}
	return nil
}

// flaky lists tests retried in this run, and failures whose history is flaky.
func (b *dbuild) flaky() error {
	e, r := b.e, b.r
	flakyHere, err := e.flakyInRun(b.sum.ID, b.ef)
	if err != nil {
		return err
	}
	fkeys := map[string]string{}
	for _, ks := range flakyHere {
		for k := range ks {
			fkeys[k] = "passed on retry"
		}
	}
	r.Summary.Flaky = len(fkeys)
	for _, f := range b.shown {
		if _, ok := fkeys[f.key]; !ok && debrief.Score(b.hist[f.key]).Flaky {
			fkeys[f.key] = "failed"
		}
	}
	all := make([]string, 0, len(fkeys))
	for k := range fkeys {
		all = append(all, k)
	}
	sort.Strings(all)
	if len(all) > debriefFlakyShown {
		all = all[:debriefFlakyShown]
		r.Notes = append(r.Notes, fmt.Sprintf("The first %d flaky tests are listed; the project's Flaky tests page lists them all.", debriefFlakyShown))
	}
	fh, err := e.observations(b.sum.Project, all, b.refs, max(b.sum.Number-historyWindow, 0))
	if err != nil {
		return err
	}
	names, err := e.testNames(b.sum.Project, all)
	if err != nil {
		return err
	}
	for _, k := range all {
		v := debrief.Score(fh[k])
		fc := debrief.FlakyCase{TestName: debrief.TestName{Key: k, Suite: names[k].Suite, Name: names[k].Name}, Score: max(v.Score, 1), ThisRun: fkeys[k],
			Reason: v.Reason, Branches: shortRefs(v.Branches), FirstSeen: v.FirstSeen, Commits: v.Commits, SameCommit: v.SameCommit}
		if fc.Reason == "" {
			fc.Reason = "passed only after a retry in this run"
		}
		r.Flaky = append(r.Flaky, fc)
	}
	sort.SliceStable(r.Flaky, func(i, j int) bool { return r.Flaky[i].Score > r.Flaky[j].Score })
	return nil
}

// quarantined lists the results the quarantine file covers.
func (b *dbuild) quarantined() error {
	r := b.r
	for _, h := range b.quar {
		r.Quarantined = append(r.Quarantined, debrief.Quarantined{TestName: debrief.TestName{Key: h.Key, Suite: h.Suite, Name: h.Name},
			Owner: h.Owner, Expires: h.Expires, Reason: h.Reason, Message: h.Message, Status: h.Status, Expired: h.Expired})
		if !h.Expired && (h.Status == "failed" || h.Status == "errored") {
			r.Summary.Quarantined++
		}
	}
	sort.Slice(r.Quarantined, func(i, j int) bool {
		return r.Quarantined[i].Suite+r.Quarantined[i].Name < r.Quarantined[j].Suite+r.Quarantined[j].Name
	})
	return nil
}

func (b *dbuild) structure() error {
	if err := b.e.suiteTree(b.r, b.cond, b.args); err != nil {
		return err
	}
	return b.e.matrixOf(b.r, b.cond, b.args)
}

// trendAndCoverage adds this branch's trend and the run's coverage; neither
// is essential, so a problem reading them leaves them out.
func (b *dbuild) trendAndCoverage() error {
	r := b.r
	ref := b.sum.Ref
	ov, err := b.e.TestOverview(b.sum.Project, ref, debriefTrendRuns)
	// A branch with only a few runs has no trend yet: show the default
	// branch's, and say so.
	if (err != nil || countWithTests(ov) < 5) && b.o.DefaultRef != "" && b.o.DefaultRef != ref {
		if dov, derr := b.e.TestOverview(b.sum.Project, b.o.DefaultRef, debriefTrendRuns); derr == nil && countWithTests(dov) >= 2 {
			ov, err = dov, nil
			r.Notes = append(r.Notes, fmt.Sprintf("The trend shows %s: %s has fewer than 5 runs with tests so far.", strings.TrimPrefix(b.o.DefaultRef, "refs/heads/"), strings.TrimPrefix(ref, "refs/heads/")))
		}
	}
	if err == nil {
		for _, or := range ov.Runs {
			if or.Tests == nil || or.Tests.Total == 0 {
				continue
			}
			pr := 100.0
			if ran := or.Tests.Total - or.Tests.Skipped; ran > 0 {
				pr = float64(or.Tests.Passed) * 100 / float64(ran)
			}
			r.Trend = append(r.Trend, debrief.TrendPoint{Number: or.Number, SHA: or.SHA, At: or.CreatedAt, Total: or.Tests.Total,
				PassRate: pr, DurationMs: or.Tests.DurationMs, Flaky: or.Tests.Flaky})
		}
	}
	if cov, err := b.e.debriefCoverage(b.sum.ID, b.o); err == nil {
		r.Coverage = cov
	}
	return nil
}

func countWithTests(ov *TestOverview) int {
	n := 0
	if ov != nil {
		for _, r := range ov.Runs {
			if r.Tests != nil && r.Tests.Total > 0 {
				n++
			}
		}
	}
	return n
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (e *Engine) runRef(s *RunSummary, baseURL string) debrief.RunRef {
	rr := debrief.RunRef{ID: s.ID, Number: s.Number, Ref: s.Ref, Branch: nonEmpty(s.Branch, strings.TrimPrefix(s.Ref, "refs/heads/")),
		SHA: s.SHA, CreatedAt: s.CreatedAt, FinishedAt: s.FinishedAt, Actor: s.Actor}
	if baseURL != "" {
		rr.Link = strings.TrimRight(baseURL, "/") + "/runs/" + s.ID
	}
	return rr
}

// quarantineOf collects the quarantine hits recorded by the steps of a run
// that count, by test key.
func (e *Engine) quarantineOf(ef *effective, runID string) map[string]QuarantineHit {
	out := map[string]QuarantineHit{}
	for _, r := range ef.steps[runID] {
		var d struct {
			Quarantine *QuarantineRecord `json:"quarantine"`
		}
		if json.Unmarshal([]byte(r.detail), &d) != nil || d.Quarantine == nil {
			continue
		}
		for _, h := range d.Quarantine.Hits {
			if old, ok := out[h.Key]; !ok || (old.Expired && !h.Expired) {
				out[h.Key] = h
			}
		}
	}
	return out
}

// baseFor finds the run to compare with: the last run of the default branch
// that succeeded and has test results, below this run's number when it is on
// that branch itself.
func (e *Engine) baseFor(s *RunSummary, defaultRef string) (*baseRef, error) {
	ref := defaultRef
	if ref == "" {
		ref = s.Ref
	}
	if ref == "" {
		return nil, nil
	}
	var cands []debrief.RunRef
	var ids []string
	err := e.eachRow(`SELECT id, number, sha, ref, branch, created_at, finished_at, actor, status FROM runs
		WHERE project = ? AND ref = ? AND number < ? ORDER BY number DESC LIMIT 40`, []any{s.Project, ref, s.Number}, func(rows *sql.Rows) error {
		var rr debrief.RunRef
		var status string
		if err := rows.Scan(&rr.ID, &rr.Number, &rr.SHA, &rr.Ref, &rr.Branch, &rr.CreatedAt, &rr.FinishedAt, &rr.Actor, &status); err != nil {
			return err
		}
		if status == string(RunSucceeded) {
			cands = append(cands, rr)
			ids = append(ids, rr.ID)
		}
		return nil
	})
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	ef, err := e.effectiveReports(ids)
	if err != nil {
		return nil, err
	}
	for _, rr := range cands {
		if tt := ef.totals(rr.ID); tt != nil && tt.Total > 0 {
			if rr.Branch == "" {
				rr.Branch = strings.TrimPrefix(rr.Ref, "refs/heads/")
			}
			return &baseRef{RunRef: rr, id: rr.ID, triples: ef.triples(rr.ID)}, nil
		}
	}
	return nil, nil
}

type slowRow struct {
	Key, Suite, Name string
	DurationMs       int64
}

func (e *Engine) slowestOf(cond string, args []any, n int) ([]slowRow, error) {
	rows, err := e.db.Query(`SELECT tc.test_key, tc.suite, tc.name, tc.duration_ms FROM test_cases tc WHERE `+cond+` AND tc.status <> 'skipped'
		ORDER BY tc.duration_ms DESC, tc.id LIMIT ?`, append(append([]any{}, args...), n)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []slowRow
	for rows.Next() {
		var s slowRow
		if err := rows.Scan(&s.Key, &s.Suite, &s.Name, &s.DurationMs); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (e *Engine) avgDurations(project string, keys []string) (map[string]int64, error) {
	out := map[string]int64{}
	if !e.knows(schemaDebrief) || len(keys) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	args := []any{project}
	for _, k := range keys {
		args = append(args, k)
	}
	rows, err := e.db.Query(`SELECT test_key, avg_ms FROM test_stats WHERE project = ? AND runs > 1 AND test_key IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var ms int64
		if err := rows.Scan(&k, &ms); err != nil {
			return nil, err
		}
		out[k] = ms
	}
	return out, rows.Err()
}

func (e *Engine) durationsIn(ts []triple, keys []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(ts) == 0 || len(keys) == 0 {
		return out, nil
	}
	cond, args := tripleFilter(ts)
	cond += ` AND tc.test_key IN (` + strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",") + `)`
	for _, k := range keys {
		args = append(args, k)
	}
	rows, err := e.db.Query(`SELECT tc.test_key, MAX(tc.duration_ms) FROM test_cases tc WHERE `+cond+` GROUP BY tc.test_key`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var ms int64
		if err := rows.Scan(&k, &ms); err != nil {
			return nil, err
		}
		out[k] = ms
	}
	return out, rows.Err()
}

// firstFailed finds, for each failing test, the run on this ref where the
// stretch of failures it is in began. A stretch that reaches the start of
// the window is reported at the window's start.
func (e *Engine) firstFailed(s *RunSummary, keys []string, refs []string) (map[string]*debrief.RunRef, error) {
	out := map[string]*debrief.RunRef{}
	if len(keys) == 0 || s.Ref == "" {
		return out, nil
	}
	since := max(s.Number-historyWindow, 0)
	obs, err := e.observations(s.Project, keys, []string{s.Ref}, since)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		h := obs[k]
		start := -1
		for i := len(h) - 1; i >= 0; i-- {
			if h[i].Number > s.Number {
				continue
			}
			if h[i].Status == "failed" {
				start = i
				continue
			}
			if h[i].Status == "passed" {
				break
			}
		}
		if start >= 0 {
			o := h[start]
			out[k] = &debrief.RunRef{ID: o.Run, Number: o.Number, SHA: o.SHA, Ref: o.Ref, Branch: strings.TrimPrefix(o.Ref, "refs/heads/"), CreatedAt: o.At}
		}
	}
	return out, nil
}

// statusIn says, for each key, whether the default branch's latest run that
// ran the test fails it: yes, no or unknown.
func (e *Engine) statusIn(base *baseRef, defaultRef string, s *RunSummary, keys []string) (map[string]string, error) {
	out := map[string]string{}
	if len(keys) == 0 {
		return out, nil
	}
	for _, k := range keys {
		out[k] = "unknown"
	}
	if defaultRef == "" || s.Ref == defaultRef {
		// On the default branch itself the comparison is with its own
		// previous run, which the "first failed in" already says.
		for _, k := range keys {
			out[k] = ""
		}
		return out, nil
	}
	latest, err := e.projectLatest(s.Project)
	if err != nil {
		return nil, err
	}
	obs, err := e.observations(s.Project, keys, []string{defaultRef}, max(latest-historyWindow, 0))
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		h := obs[k]
		for i := len(h) - 1; i >= 0; i-- {
			if h[i].Status == "failed" {
				out[k] = "yes"
				break
			}
			if h[i].Status == "passed" {
				out[k] = "no"
				break
			}
		}
	}
	return out, nil
}

var lineRe = regexp.MustCompile(`([\w./\\-]+\.(?:go|js|jsx|ts|tsx|mjs|py|rb|java|kt|cs|php|rs|c|cc|cpp|h|hpp|swift|scala|ex|exs|vue|svelte)):(\d{1,7})`)

// lineIn finds where a failing assertion is: a path with :line in the message
// or the start of the trace. When the report names the test's file, only a
// path ending in that file's name counts; when it does not, the first source
// path with a line is taken, and its file is returned as well.
func lineIn(file string, texts ...string) (string, int) {
	base := file
	if i := strings.LastIndexAny(file, `/\`); i >= 0 {
		base = file[i+1:]
	}
	for _, t := range texts {
		if len(t) > 8192 {
			t = t[:8192]
		}
		for _, m := range lineRe.FindAllStringSubmatch(t, 5) {
			if file != "" && !strings.HasSuffix(m[1], base) {
				continue
			}
			n, _ := strconv.Atoi(m[2])
			return strings.TrimPrefix(m[1], "./"), n
		}
	}
	return file, 0
}

func (e *Engine) readAttachment(runID string, caseID int64, n int) string {
	f, err := e.OpenTestAttachment(context.Background(), runID, caseID, n, 0)
	if err != nil {
		return ""
	}
	defer f.Body.Close()
	data, err := io.ReadAll(io.LimitReader(f.Body, debriefMaxImageSize+1))
	if err != nil || len(data) > debriefMaxImageSize || len(data) == 0 {
		return ""
	}
	kind := SniffAttachment(data)
	var ct string
	switch kind {
	case "png":
		ct = "image/png"
	case "jpeg", "jpg":
		ct = "image/jpeg"
	case "gif":
		ct = "image/gif"
	case "webp":
		ct = "image/webp"
	default:
		return "" // SVG and others can carry script: never embedded
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// suiteTree adds up the run's results by suite into a tree by path.
func (e *Engine) suiteTree(r *debrief.Report, cond string, args []any) error {
	rows, err := e.db.Query(`SELECT tc.suite, tc.status, COUNT(*), COALESCE(SUM(tc.duration_ms), 0) FROM test_cases tc WHERE `+cond+` GROUP BY tc.suite, tc.status`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	leaf := map[string]*debrief.Suite{}
	for rows.Next() {
		var suite, status string
		var n int
		var ms int64
		if err := rows.Scan(&suite, &status, &n, &ms); err != nil {
			return err
		}
		if suite == "" {
			suite = "(no suite)"
		}
		s := leaf[suite]
		if s == nil {
			s = &debrief.Suite{Name: suite}
			leaf[suite] = s
		}
		s.Total += n
		s.DurationMs += ms
		switch status {
		case "passed":
			s.Passed += n
		case "failed", "errored":
			s.Failed += n
		case "skipped":
			s.Skipped += n
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sep := "."
	for name := range leaf {
		if strings.Contains(name, "/") {
			sep = "/"
			break
		}
	}
	nodes := map[string]*debrief.Suite{}
	direct := map[string]bool{}
	children := map[string]map[string]bool{}
	for name, l := range leaf {
		parts := strings.Split(name, sep)
		prefix := ""
		parent := ""
		for i, p := range parts {
			if i == 0 {
				prefix = p
			} else {
				prefix = prefix + sep + p
			}
			n := nodes[prefix]
			if n == nil {
				n = &debrief.Suite{Name: prefix}
				nodes[prefix] = n
			}
			n.Total += l.Total
			n.Passed += l.Passed
			n.Failed += l.Failed
			n.Skipped += l.Skipped
			n.DurationMs += l.DurationMs
			if children[parent] == nil {
				children[parent] = map[string]bool{}
			}
			children[parent][prefix] = true
			parent = prefix
		}
		direct[name] = true
	}
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		kids := make([]string, 0, len(children[parent]))
		for k := range children[parent] {
			kids = append(kids, k)
		}
		sort.Strings(kids)
		for _, k := range kids {
			if len(r.Suites) >= debriefSuiteNodes {
				return
			}
			// A node that only passes its single child through is skipped.
			if len(children[k]) == 1 && !direct[k] && parent != "" || (len(children[k]) == 1 && !direct[k] && parent == "") {
				walk(k, depth)
				continue
			}
			n := *nodes[k]
			n.Depth = depth
			r.Suites = append(r.Suites, n)
			walk(k, depth+1)
		}
	}
	walk("", 0)
	if len(r.Suites) >= debriefSuiteNodes {
		r.Notes = append(r.Notes, fmt.Sprintf("The suite tree lists the first %d suites.", debriefSuiteNodes))
	}
	return nil
}

// matrixOf builds the grid of tests by variant when the run has results with
// variants (a browser, a version).
func (e *Engine) matrixOf(r *debrief.Report, cond string, args []any) error {
	rows, err := e.db.Query(`SELECT tc.suite, tc.name, tc.variant, tc.status FROM test_cases tc WHERE `+cond+` AND tc.variant <> ''
		ORDER BY tc.suite, tc.name LIMIT 20000`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols := map[string]int{}
	var colNames []string
	type rk struct{ suite, name string }
	rowIdx := map[rk]int{}
	var m debrief.Matrix
	type cell struct {
		row int
		col string
		st  string
	}
	var cells []cell
	for rows.Next() {
		var suite, name, variant, status string
		if err := rows.Scan(&suite, &name, &variant, &status); err != nil {
			return err
		}
		if _, ok := cols[variant]; !ok {
			if len(colNames) >= 12 {
				continue
			}
			cols[variant] = len(colNames)
			colNames = append(colNames, variant)
		}
		k := rk{suite, name}
		i, ok := rowIdx[k]
		if !ok {
			if len(m.Rows) >= debriefMatrixRows {
				m.Cut = true
				continue
			}
			i = len(m.Rows)
			rowIdx[k] = i
			m.Rows = append(m.Rows, debrief.MatrixRow{TestName: debrief.TestName{Suite: suite, Name: name}})
		}
		cells = append(cells, cell{i, variant, status})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(m.Rows) == 0 {
		return nil
	}
	sort.Strings(colNames)
	idx := map[string]int{}
	for i, c := range colNames {
		idx[c] = i
	}
	m.Columns = colNames
	for i := range m.Rows {
		m.Rows[i].Cells = make([]string, len(colNames))
	}
	for _, c := range cells {
		m.Rows[c.row].Cells[idx[c.col]] = worse(m.Rows[c.row].Cells[idx[c.col]], c.st)
	}
	r.Matrix = &m
	return nil
}

// worse keeps the more serious of two statuses for one grid cell.
func worse(a, b string) string {
	rank := map[string]int{"": 0, "skipped": 1, "passed": 2, "errored": 3, "failed": 4}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// maxDebriefCases bounds the results written to the merged JUnit export.
const maxDebriefCases = 50_000

// DebriefCases lists a run's results (those that count) for the merged JUnit
// export, quarantined failures marked, at most maxDebriefCases.
func (e *Engine) DebriefCases(runID string) ([]debrief.Case, error) {
	if !e.knows(schemaTests) {
		return nil, nil
	}
	ef, err := e.effectiveReports([]string{runID})
	if err != nil {
		return nil, err
	}
	quar := e.quarantineOf(ef, runID)
	cond, args := tripleFilter(ef.triples(runID))
	rows, err := e.db.Query(`SELECT tc.test_key, tc.suite, tc.name, tc.file, tc.status, tc.duration_ms, SUBSTR(tc.message, 1, ?), tc.details, tc.output, tc.flaky
		FROM test_cases tc WHERE `+cond+` ORDER BY tc.suite, tc.name, tc.id LIMIT ?`, append(append([]any{messagePreview}, args...), maxDebriefCases)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []debrief.Case
	for rows.Next() {
		var c debrief.Case
		var key string
		var flaky int
		if err := rows.Scan(&key, &c.Suite, &c.Name, &c.File, &c.Status, &c.DurationMs, &c.Message, &c.Details, &c.Output, &flaky); err != nil {
			return nil, err
		}
		c.Flaky = flaky != 0
		if h, ok := quar[key]; ok && !h.Expired && (c.Status == "failed" || c.Status == "errored") {
			c.Quarantined = true
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
