// Package osv finds the known vulnerabilities of discovered dependencies in
// OSV data: either the OSV API (a public one or an installation's own) or a
// directory holding an OSV database mirror (one JSON record per file, any
// depth), which is the only choice when the installation is offline. Matches
// become findings the Security tab shows, as a SARIF document the pipeline
// publishes like any scanner's.
package osv

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/version"
)

// Vuln is one OSV record, reduced to what is used.
type Vuln struct {
	ID       string    `json:"id"`
	Aliases  []string  `json:"aliases"`
	Summary  string    `json:"summary"`
	Details  string    `json:"details"`
	Modified time.Time `json:"modified"`
	Severity []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
	Affected []Affected `json:"affected"`
	DBSpec   struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
	References []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"references"`
	Withdrawn *time.Time `json:"withdrawn"`
}

// Affected is one package's part of a record.
type Affected struct {
	Package struct {
		Ecosystem string `json:"ecosystem"`
		Name      string `json:"name"`
	} `json:"package"`
	Ranges []struct {
		Type   string `json:"type"`
		Events []struct {
			Introduced   string `json:"introduced"`
			Fixed        string `json:"fixed"`
			LastAffected string `json:"last_affected"`
		} `json:"events"`
	} `json:"ranges"`
	Versions []string `json:"versions"`
}

// Query is one package version to look up.
type Query struct{ Ecosystem, Name, Version string }

// Index answers which records affect which versions.
type Index interface {
	// Lookup returns the records that may affect the package (the caller
	// checks the version with Affects; an API may already have done so).
	Lookup(ctx context.Context, q Query) ([]Vuln, error)
}

// Ecosystem maps a depupdate ecosystem to OSV's name; "" when OSV has no
// package records for it.
func Ecosystem(e string) string {
	switch e {
	case depupdate.Go:
		return "Go"
	case depupdate.NPM:
		return "npm"
	case depupdate.Python:
		return "PyPI"
	}
	return ""
}

// Match is one dependency affected by one record.
type Match struct {
	Dep      depupdate.Dep `json:"dep"`
	Vuln     Vuln          `json:"-"`
	ID       string        `json:"id"`
	Aliases  []string      `json:"aliases,omitempty"`
	Summary  string        `json:"summary"`
	Severity string        `json:"severity"` // critical, high, medium, low, unknown
	Score    float64       `json:"score"`
	FixedIn  string        `json:"fixed_in,omitempty"` // lowest fixed version above the current one, if any
	URL      string        `json:"url,omitempty"`
}

// Check looks up every dependency with a known version and returns the
// matches, most severe first. Transitive dependencies count: the file is the
// lock file that records them.
func Check(ctx context.Context, idx Index, deps []depupdate.Dep) ([]Match, error) {
	var out []Match
	seen := map[string]bool{}
	for _, d := range deps {
		eco := Ecosystem(d.Ecosystem)
		if eco == "" || d.Current == "" {
			continue
		}
		vs, err := idx.Lookup(ctx, Query{eco, d.Name, d.Current})
		if err != nil {
			return out, err
		}
		for _, v := range vs {
			if v.Withdrawn != nil {
				continue
			}
			ok, fixed := Affects(v, eco, d.Name, d.Current)
			if !ok {
				continue
			}
			k := d.Key() + "|" + v.ID
			if seen[k] {
				continue
			}
			seen[k] = true
			sev, score := Severity(v)
			m := Match{Dep: d, Vuln: v, ID: v.ID, Aliases: v.Aliases, Summary: firstLine(v), Severity: sev, Score: score, FixedIn: fixed}
			for _, r := range v.References {
				if r.Type == "ADVISORY" || r.Type == "WEB" {
					m.URL = r.URL
					break
				}
			}
			out = append(out, m)
		}
	}
	sortMatches(out)
	return out, nil
}

func firstLine(v Vuln) string {
	if v.Summary != "" {
		return v.Summary
	}
	d := strings.TrimSpace(v.Details)
	if i := strings.IndexByte(d, '\n'); i > 0 {
		d = d[:i]
	}
	if len(d) > 200 {
		d = d[:200]
	}
	return d
}

// Affects reports whether the version is inside an affected range of the
// record for this package, and the lowest fixed version above it.
func Affects(v Vuln, ecosystem, name, ver string) (bool, string) {
	for _, a := range v.Affected {
		if a.Package.Ecosystem != ecosystem || !sameName(ecosystem, a.Package.Name, name) {
			continue
		}
		for _, x := range a.Versions {
			if x == ver || strings.TrimPrefix(x, "v") == strings.TrimPrefix(ver, "v") {
				return true, lowestFix(a, ecosystem, ver)
			}
		}
		for _, r := range a.Ranges {
			if r.Type == "GIT" {
				continue
			}
			if inRange(ecosystem, r.Events, ver) {
				return true, lowestFix(a, ecosystem, ver)
			}
		}
	}
	return false, ""
}

func sameName(eco, a, b string) bool {
	if eco == "PyPI" {
		n := func(s string) string { return strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(s)) }
		return n(a) == n(b)
	}
	return a == b
}

type event = struct {
	Introduced   string `json:"introduced"`
	Fixed        string `json:"fixed"`
	LastAffected string `json:"last_affected"`
}

func cmp(eco, a, b string) (int, bool) {
	if eco == "PyPI" {
		x, ok1 := version.ParsePEP440(a)
		y, ok2 := version.ParsePEP440(b)
		return x.Compare(y), ok1 && ok2
	}
	x, ok1 := version.ParseSemver(a)
	y, ok2 := version.ParseSemver(b)
	return x.Compare(y), ok1 && ok2
}

func inRange(eco string, events []event, ver string) bool {
	affected := false
	for _, ev := range events {
		switch {
		case ev.Introduced != "":
			if ev.Introduced == "0" {
				affected = true
			} else if c, ok := cmp(eco, ver, ev.Introduced); ok && c >= 0 {
				affected = true
			}
		case ev.Fixed != "":
			if c, ok := cmp(eco, ver, ev.Fixed); ok && c >= 0 {
				affected = false
			}
		case ev.LastAffected != "":
			if c, ok := cmp(eco, ver, ev.LastAffected); ok && c > 0 {
				affected = false
			}
		}
	}
	return affected
}

func lowestFix(a Affected, eco, ver string) string {
	best := ""
	for _, r := range a.Ranges {
		for _, e := range r.Events {
			if e.Fixed == "" {
				continue
			}
			if c, ok := cmp(eco, e.Fixed, ver); !ok || c <= 0 {
				continue
			}
			if best == "" {
				best = e.Fixed
			} else if c, ok := cmp(eco, e.Fixed, best); ok && c < 0 {
				best = e.Fixed
			}
		}
	}
	return best
}

// Raw is for tests and mirrors: decode one record.
func Raw(b []byte) (Vuln, error) {
	var v Vuln
	err := json.Unmarshal(b, &v)
	return v, err
}
