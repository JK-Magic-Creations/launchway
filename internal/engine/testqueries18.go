package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
)

// Queries of schema 18's richer test results.

// Facet is one choice for filtering, and how many results it has.
type Facet struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

const maxFacets = 100

// caseFacets fills in the tags, variants and suites of the results a query
// matches.
func (e *Engine) caseFacets(page *CasePage, base string, args []any) error {
	top := func(col string) ([]Facet, error) {
		rows, err := e.db.Query(`SELECT `+col+`, COUNT(*) FROM test_cases tc WHERE `+base+` AND `+col+` <> '' GROUP BY `+col+`
			ORDER BY COUNT(*) DESC, `+col+` LIMIT ?`, append(slices.Clone(args), maxFacets+1)...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []Facet
		for rows.Next() {
			var f Facet
			if err := rows.Scan(&f.Value, &f.Count); err != nil {
				return nil, err
			}
			out = append(out, f)
		}
		return out, rows.Err()
	}
	var err error
	if page.Variants, err = top("tc.variant"); err != nil {
		return err
	}
	if page.Suites, err = top("tc.suite"); err != nil {
		return err
	}
	if len(page.Suites) > maxFacets {
		page.Suites = page.Suites[:maxFacets]
	}
	// Tags are kept together in one column; count them here, over a
	// bounded number of distinct combinations.
	sets, err := top("tc.tags")
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, s := range sets {
		for _, t := range tagsFromColumn(s.Value) {
			counts[t] += s.Count
		}
	}
	for t, n := range counts {
		page.Tags = append(page.Tags, Facet{t, n})
	}
	slices.SortFunc(page.Tags, func(a, b Facet) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		if a.Value < b.Value {
			return -1
		}
		return 1
	})
	if len(page.Tags) > maxFacets {
		page.Tags = page.Tags[:maxFacets]
	}
	return nil
}

// richCase reads what schema 18 keeps of one result.
func (e *Engine) richCase(c *TestCase) error {
	if !e.knows(schemaReports) {
		return nil
	}
	var tags, extra string
	if err := e.db.QueryRow(`SELECT tags, variant, extra FROM test_cases WHERE id = ?`, c.ID).Scan(&tags, &c.Variant, &extra); err != nil {
		return err
	}
	c.Tags = tagsFromColumn(tags)
	if extra == "" {
		return nil
	}
	var x caseExtra
	if err := json.Unmarshal([]byte(extra), &x); err != nil {
		return nil // written by this code; unreadable means nothing to show
	}
	c.Retries, c.Attempts, c.Steps, c.StepsCut, c.Attachments, c.Properties = x.Retries, x.Attempts, x.Steps, x.StepsCut, x.Attachments, x.Properties
	c.HasSteps = len(x.Steps) > 0
	c.HasDetails = true
	return nil
}

// AttachmentFile is an attachment's content, opened for reading.
type AttachmentFile struct {
	TestAttachment
	SHA256 string
	Body   io.ReadCloser
}

// OpenTestAttachment opens the n-th attachment (from 0) of one of a run's
// results. Only a file of the artifact test-attachments or test-embedded of
// the attempt the result came from is ever opened.
func (e *Engine) OpenTestAttachment(ctx context.Context, runID string, caseID int64, n int, offset int64) (*AttachmentFile, error) {
	c, err := e.TestCase(runID, caseID)
	if err != nil {
		return nil, err
	}
	if n < 0 || n >= len(c.Attachments) {
		return nil, fmt.Errorf("%w: attachment %d of test result %d", ErrNotFound, n, caseID)
	}
	ta := c.Attachments[n]
	if ta.Problem != "" || ta.Artifact == "" {
		return nil, fmt.Errorf("%w: attachment %s is %s", ErrNotFound, ta.Name, nonEmpty(ta.Problem, "not kept"))
	}
	if ta.Artifact != "test-attachments" && ta.Artifact != "test-embedded" {
		return nil, fmt.Errorf("%w: attachment %s", ErrNotFound, ta.Name)
	}
	a, err := findArtifact(e.db, c.Run, c.Step, c.Attempt, ta.Artifact)
	if err != nil {
		return nil, err
	}
	if a.Status != artKept {
		return nil, fmt.Errorf("%w: attachment %s is %s", ErrNotFound, ta.Name, a.statusText())
	}
	f, err := e.openArtifactFile(ctx, a, ta.Path, offset)
	if err != nil {
		return nil, err
	}
	ta.Size = f.Size
	return &AttachmentFile{TestAttachment: ta, SHA256: f.SHA256, Body: f.Body}, nil
}

// ---- comparing with the previous run ----

// TestComparison is a run's results beside the previous run's on the same
// ref: what fails now and passed then, what passes now and failed then,
// and what fails in both.
type TestComparison struct {
	Previous     *RunLink      `json:"previous,omitempty"`
	NewFailures  []CompareCase `json:"new_failures"`
	Fixed        []CompareCase `json:"fixed"`
	StillFailing []CompareCase `json:"still_failing"`
	// Counts are the full numbers; the lists hold at most maxCompare each.
	Counts map[string]int `json:"counts"`
}

// CompareCase is one test in a comparison: this run's result (ID 0 when it
// is fixed and the run has no result to link, which cannot happen here: a
// fixed test passed in this run) and the earlier one.
type CompareCase struct {
	Key        string `json:"key"`
	ID         int64  `json:"id"` // this run's result
	Suite      string `json:"suite"`
	Name       string `json:"name"`
	Variant    string `json:"variant,omitempty"`
	Status     string `json:"status"`
	Before     string `json:"before"`
	Message    string `json:"message,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

const maxCompare = 200

// CompareTests compares a run's results with the previous run of the same
// ref that has results.
func (e *Engine) CompareTests(runID string) (*TestComparison, error) {
	out := &TestComparison{NewFailures: []CompareCase{}, Fixed: []CompareCase{}, StillFailing: []CompareCase{}, Counts: map[string]int{}}
	st, err := e.runState(runID)
	if err != nil {
		return nil, err
	}
	if !e.knows(schemaTests) {
		return out, nil
	}
	ef, err := e.effectiveReports([]string{runID})
	if err != nil {
		return nil, err
	}
	ts := ef.triples(runID)
	if len(ts) == 0 {
		return out, nil
	}
	prev, err := e.previousWithTests(st)
	if err != nil || prev == nil {
		return out, err
	}
	out.Previous = &RunLink{ID: prev.run, Number: prev.number}
	now, err := e.statusByKey(ts)
	if err != nil {
		return nil, err
	}
	before, err := e.statusByKey(prev.triples)
	if err != nil {
		return nil, err
	}
	failed := func(s string) bool { return s == "failed" || s == "errored" }
	keys := make([]string, 0, len(now))
	for k := range now {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		c := now[k]
		b, ok := before[k]
		if !ok {
			continue
		}
		c.Before = b.Status
		var list *[]CompareCase
		var what string
		switch {
		case failed(c.Status) && b.Status == "passed":
			list, what = &out.NewFailures, "new_failures"
		case c.Status == "passed" && failed(b.Status):
			list, what = &out.Fixed, "fixed"
		case failed(c.Status) && failed(b.Status):
			list, what = &out.StillFailing, "still_failing"
		default:
			continue
		}
		out.Counts[what]++
		if len(*list) < maxCompare {
			*list = append(*list, c)
		}
	}
	return out, nil
}

// statusByKey reads each test's result in some step attempts: the worst,
// when a test ran more than once.
func (e *Engine) statusByKey(ts []triple) (map[string]CompareCase, error) {
	out := map[string]CompareCase{}
	where, args := tripleFilter(ts)
	variant := `''`
	if e.knows(schemaReports) {
		variant = `tc.variant`
	}
	rows, err := e.db.Query(`SELECT tc.id, tc.test_key, tc.suite, tc.name, `+variant+`, tc.status, SUBSTR(tc.message, 1, 300), tc.duration_ms
		FROM test_cases tc WHERE `+where+` ORDER BY tc.id LIMIT 200000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rank := map[string]int{"errored": 3, "failed": 3, "passed": 2, "skipped": 1}
	for rows.Next() {
		var c CompareCase
		if err := rows.Scan(&c.ID, &c.Key, &c.Suite, &c.Name, &c.Variant, &c.Status, &c.Message, &c.DurationMs); err != nil {
			return nil, err
		}
		if old, ok := out[c.Key]; ok && rank[old.Status] >= rank[c.Status] {
			continue
		}
		out[c.Key] = c
	}
	return out, rows.Err()
}
