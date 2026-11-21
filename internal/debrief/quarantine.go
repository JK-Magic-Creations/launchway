package debrief

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// QuarantinePath is where a repository keeps its quarantine list.
const QuarantinePath = ".launchway/quarantine.yml"

// Limits of the quarantine file.
const (
	MaxQuarantineBytes   = 256 << 10
	MaxQuarantineEntries = 1000
)

// QuarantineEntry is one quarantined test: a pattern, who answers for it,
// the day it stops being excused, and why.
type QuarantineEntry struct {
	Test    string `yaml:"test" json:"test"`
	Owner   string `yaml:"owner" json:"owner"`
	Expires string `yaml:"expires" json:"expires"` // YYYY-MM-DD
	Reason  string `yaml:"reason" json:"reason,omitempty"`

	expiry time.Time
}

// Quarantine is a repository's list, read from QuarantinePath at the commit
// a run tested. It is advice from the code under test: it excuses failures
// only until the entry's expiry, and only for the pull request or branch
// that carries it, so a quarantine is reviewed like any other change.
type Quarantine struct {
	Entries  []QuarantineEntry
	Problems []string // entries that were left out, and why
}

// ParseQuarantine reads the file. A bad entry is left out and named in
// Problems; the rest still apply. Every entry needs an owner and an expiry
// (a quarantine without an end is a deletion), and an expiry more than a
// year ahead is refused.
func ParseQuarantine(data []byte, now time.Time) *Quarantine {
	q := &Quarantine{}
	if len(data) > MaxQuarantineBytes {
		q.Problems = append(q.Problems, fmt.Sprintf("the quarantine file is larger than %d bytes and was not read", MaxQuarantineBytes))
		return q
	}
	var doc struct {
		Quarantine []QuarantineEntry `yaml:"quarantine"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		q.Problems = append(q.Problems, "the quarantine file is not valid YAML: "+err.Error())
		return q
	}
	for i, e := range doc.Quarantine {
		if i >= MaxQuarantineEntries {
			q.Problems = append(q.Problems, fmt.Sprintf("more than %d entries; the rest were left out", MaxQuarantineEntries))
			break
		}
		e.Test = strings.TrimSpace(e.Test)
		switch {
		case e.Test == "":
			q.Problems = append(q.Problems, fmt.Sprintf("entry %d has no test", i+1))
			continue
		case strings.TrimSpace(e.Owner) == "":
			q.Problems = append(q.Problems, fmt.Sprintf("%s: an owner is required", e.Test))
			continue
		}
		t, err := time.Parse("2006-01-02", strings.TrimSpace(e.Expires))
		if err != nil {
			q.Problems = append(q.Problems, fmt.Sprintf("%s: expires must be a day, YYYY-MM-DD", e.Test))
			continue
		}
		if t.After(now.AddDate(1, 0, 1)) {
			q.Problems = append(q.Problems, fmt.Sprintf("%s: expires more than a year ahead; shorten it", e.Test))
			continue
		}
		e.expiry = t
		q.Entries = append(q.Entries, e)
	}
	return q
}

// Match finds the entry that covers a test, and whether it has expired (an
// expired entry excuses nothing). The pattern is the test's name, or its
// suite and name written suite::name; * matches any run of characters.
func (q *Quarantine) Match(suite, name string, now time.Time) (e QuarantineEntry, found, expired bool) {
	if q == nil {
		return e, false, false
	}
	for _, c := range q.Entries {
		if globMatch(c.Test, suite+"::"+name) || globMatch(c.Test, name) {
			// The first entry that matches decides, but a live entry beats
			// an expired one for the same test.
			dead := !now.Before(c.expiry.AddDate(0, 0, 1))
			if !found || (expired && !dead) {
				e, found, expired = c, true, dead
			}
		}
	}
	return e, found, expired
}

// globMatch is a match where * is any run of characters and nothing else is
// special.
func globMatch(pat, s string) bool {
	parts := strings.Split(pat, "*")
	if len(parts) == 1 {
		return pat == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}
