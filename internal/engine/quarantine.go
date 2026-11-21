package engine

import (
	"fmt"
	"time"

	"github.com/jk-magic-creations/launchway/internal/debrief"
	"github.com/jk-magic-creations/launchway/internal/testreport"
)

// Quarantine: a repository lists tests it knows are unreliable in
// .launchway/quarantine.yml, each with an owner and an expiry. The runner
// sends that file with the test reports of a step. A failing result that a
// live entry covers is shown loudly and counted but does not fail the step;
// when every failing result of an attempt is covered (and there is at least
// one) a step whose command failed only because of them ends as succeeded,
// with the reason written on it. An expired entry excuses nothing. The file
// is read from the commit under test, so adding or extending an entry is a
// change to be reviewed like any other, and nothing here writes the file:
// Launchway only suggests entries.

// QuarantineRecord is what the quarantine file did in one step attempt.
type QuarantineRecord struct {
	Hits []QuarantineHit `json:"hits"`
	// Problems are entries that were left out, and why.
	Problems []string `json:"problems,omitempty"`
	// Excused counts the failing results a live entry covered.
	Excused int `json:"excused"`
	// Failing counts every failing result of the attempt, covered or not.
	Failing int `json:"failing"`
	// Entries counts the entries in the file.
	Entries int `json:"entries"`
}

// QuarantineHit is one result a quarantine entry covers.
type QuarantineHit struct {
	Key     string `json:"key"`
	Suite   string `json:"suite"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Owner   string `json:"owner"`
	Expires string `json:"expires"`
	Reason  string `json:"reason,omitempty"`
	Expired bool   `json:"expired,omitempty"`
	Message string `json:"message,omitempty"`
}

// maxQuarantineHits bounds what one attempt records of its covered results.
const maxQuarantineHits = 1000

// quarantine matches the parsed results against the file's entries.
func (p *parsedTests) quarantine(now time.Time, data []byte) {
	q := debrief.ParseQuarantine(data, now)
	rec := &QuarantineRecord{Hits: []QuarantineHit{}, Entries: len(q.Entries)}
	for _, pr := range q.Problems {
		rec.Problems = append(rec.Problems, cleanText(pr, maxReportText))
		if len(rec.Problems) >= 20 {
			break
		}
	}
	for _, c := range p.cases {
		failing := c.Status == testreport.Failed || c.Status == testreport.Errored
		if failing {
			rec.Failing++
		}
		entry, found, expired := q.Match(c.Suite, c.Name, now)
		if !found {
			continue
		}
		// A passing result is recorded too, so that people see that a
		// quarantined test is passing again and can drop the entry.
		if failing && !expired {
			rec.Excused++
		}
		if len(rec.Hits) < maxQuarantineHits {
			rec.Hits = append(rec.Hits, QuarantineHit{Key: c.key, Suite: c.Suite, Name: c.Name, Status: string(c.Status),
				Owner: cleanText(entry.Owner, 100), Expires: entry.Expires, Reason: cleanText(entry.Reason, 300), Expired: expired,
				Message: cleanText(c.Message, 300)})
		}
	}
	p.summary.Quarantine = rec
	if rec.Excused > 0 {
		p.summary.note(fmt.Sprintf("%d failing %s excused by %s", rec.Excused, plural(int64(rec.Excused), "test is", "tests are"), debrief.QuarantinePath))
	}
}

// quarantineExcusesAll says whether an attempt's failures are all covered by
// live quarantine entries: at least one failing result, every one covered,
// and every report file read without error.
func quarantineExcusesAll(t *TestSummary) bool {
	if t == nil || t.Quarantine == nil || t.Quarantine.Failing == 0 || t.Quarantine.Excused != t.Quarantine.Failing {
		return false
	}
	for _, f := range t.Files {
		if f.Error != "" {
			return false
		}
	}
	return t.Failed+t.Errored == t.Quarantine.Failing
}

// JobReportsDebrief is what schema 44 adds to the reports a step asks its
// runner to collect (embedded in JobReports).
type JobReportsDebrief struct {
	// Quarantine asks the runner to send the repository's quarantine file
	// (debrief.QuarantinePath), when it has one, with the test reports. A
	// server that does not know it never sets it, and an older runner
	// ignores it.
	Quarantine bool `json:"quarantine,omitempty"`
}

// quarantineFlip ends as succeeded a step that failed only because of tests
// the repository has quarantined, and says so in the reason.
func quarantineFlip(e *Engine, res *Result, p *Payload) {
	if res.Outcome != "failed" || !e.knows(schemaDebrief) || !quarantineExcusesAll(p.Tests) {
		return
	}
	res.Outcome = "succeeded"
	p.Reason = fmt.Sprintf("%d failing %s quarantined in %s and do not fail the run",
		p.Tests.Quarantine.Excused, plural(int64(p.Tests.Quarantine.Excused), "test is", "tests are"), debrief.QuarantinePath)
}
