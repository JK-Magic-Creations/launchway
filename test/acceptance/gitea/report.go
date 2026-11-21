package giteaaccept

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Result is what one test did, with the values it compared.
type Result struct {
	ID       string            `json:"id"`
	Status   string            `json:"status"` // pass, fail, skip
	Reason   string            `json:"reason,omitempty"`
	Evidence []string          `json:"evidence,omitempty"`
	Not      []string          `json:"not_automated,omitempty"`
	Seconds  float64           `json:"seconds"`
	Coverage string            `json:"coverage"`
	Extra    map[string]string `json:"-"`
}

var (
	mu      sync.Mutex
	results = map[string]*Result{}
)

// begin registers a test under its acceptance-test ID. The test records what
// it compared with Evidence, so a pass is never silent.
func begin(t *testing.T, id string) *Result {
	t.Helper()
	r := &Result{ID: id}
	for _, e := range Catalog {
		if e.ID == id {
			r.Coverage = e.Coverage
		}
	}
	start := time.Now()
	t.Cleanup(func() {
		r.Seconds = time.Since(start).Seconds()
		switch {
		case t.Failed():
			r.Status = "fail"
		case t.Skipped():
			r.Status = "skip"
		default:
			r.Status = "pass"
		}
		mu.Lock()
		results[id] = r
		mu.Unlock()
	})
	return r
}

// Saw records and logs a compared value.
func (r *Result) Saw(t *testing.T, format string, a ...any) {
	t.Helper()
	s := fmt.Sprintf(format, a...)
	r.Evidence = append(r.Evidence, s)
	t.Logf("%s evidence: %s", r.ID, s)
}

// NotAutomated records a part of the acceptance test that this run did not cover.
func (r *Result) NotAutomated(t *testing.T, why string) {
	t.Helper()
	r.Not = append(r.Not, why)
	t.Logf("%s NOT COVERED: %s", r.ID, why)
}

// skip ends the test as skipped, with the reason on the record.
func (r *Result) skip(t *testing.T, why string) {
	t.Helper()
	r.Reason = why
	t.Skip(why)
}

// writeReport writes report.json and report.md into dir.
func writeReport(dir, host, flavor string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var all []*Result
	for _, e := range Catalog {
		if r, ok := results[e.ID]; ok {
			all = append(all, r)
			continue
		}
		all = append(all, &Result{ID: e.ID, Status: "skip", Coverage: e.Coverage, Reason: "not automated: " + e.Why})
	}
	b, _ := json.MarshalIndent(map[string]any{"host": host, "flavor": flavor, "date": time.Now().UTC().Format(time.RFC3339), "results": all}, "", " ")
	if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o644); err != nil {
		return err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# Gitea-replacement acceptance report\n\nHost %s (%s), %s\n\n| Test | Result | Coverage | Evidence or reason |\n|---|---|---|---|\n", host, flavor, time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	for _, r := range all {
		ev := strings.Join(r.Evidence, "; ")
		if r.Reason != "" {
			ev = r.Reason
		}
		if len(r.Not) > 0 {
			ev += " [not covered: " + strings.Join(r.Not, "; ") + "]"
		}
		fmt.Fprintf(&md, "| %s | %s | %s | %s |\n", r.ID, r.Status, r.Coverage, strings.ReplaceAll(ev, "|", "/"))
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(md.String()), 0o644)
}
