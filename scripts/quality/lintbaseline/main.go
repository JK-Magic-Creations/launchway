// Command lintbaseline compares golangci-lint findings (JSON report) with the
// recorded baseline in quality/lint-baseline.json.
//
//	lintbaseline check  report.json baseline.json   fail on findings beyond the baseline
//	lintbaseline update report.json baseline.json   rewrite the baseline (it may only shrink)
//
// A finding is keyed by file, linter and message, not by line number, so
// editing a file does not invalidate its baseline. Each key holds a count; a
// count above the baseline is a new finding.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type report struct {
	Issues []struct {
		FromLinter string
		Text       string
		Pos        struct {
			Filename string
			Line     int
		}
	}
}

type entry struct {
	File   string `json:"file"`
	Linter string `json:"linter"`
	Text   string `json:"text"`
	Count  int    `json:"count"`
}

type baseline struct {
	Comment string  `json:"comment"`
	Total   int     `json:"total"`
	Entries []entry `json:"entries"`
}

func key(file, linter, text string) string { return file + "\x00" + linter + "\x00" + text }

func main() {
	if len(os.Args) != 4 || (os.Args[1] != "check" && os.Args[1] != "update") {
		fmt.Fprintln(os.Stderr, "usage: lintbaseline check|update report.json baseline.json")
		os.Exit(2)
	}
	mode, reportPath, basePath := os.Args[1], os.Args[2], os.Args[3]
	var rep report
	mustRead(reportPath, &rep)
	current := map[string]int{}
	where := map[string][]int{}
	for _, i := range rep.Issues {
		k := key(i.Pos.Filename, i.FromLinter, i.Text)
		current[k]++
		where[k] = append(where[k], i.Pos.Line)
	}
	var base baseline
	if b, err := os.ReadFile(basePath); err == nil {
		if err := json.Unmarshal(b, &base); err != nil {
			fatal("%s: %v", basePath, err)
		}
	}
	recorded := map[string]int{}
	for _, e := range base.Entries {
		recorded[key(e.File, e.Linter, e.Text)] = e.Count
	}

	if mode == "update" {
		// Only shrinking is allowed unless the baseline does not exist yet.
		if len(recorded) > 0 {
			if grown := newFindings(current, recorded, where); len(grown) > 0 {
				fmt.Fprintln(os.Stderr, "refusing to update: the baseline may only shrink. New findings:")
				for _, g := range grown {
					fmt.Fprintln(os.Stderr, "  "+g)
				}
				os.Exit(1)
			}
		}
		write(basePath, current)
		fmt.Printf("baseline written: %d findings\n", len(rep.Issues))
		return
	}

	grown := newFindings(current, recorded, where)
	fixed := 0
	for k, n := range recorded {
		if current[k] < n {
			fixed += n - current[k]
		}
	}
	if len(grown) > 0 {
		fmt.Fprintf(os.Stderr, "lint: %d new finding(s) beyond the baseline:\n", len(grown))
		for _, g := range grown {
			fmt.Fprintln(os.Stderr, "  "+g)
		}
		fmt.Fprintln(os.Stderr, "Fix them. Do not add them to the baseline (see docs/development/quality-gates.md).")
		os.Exit(1)
	}
	fmt.Printf("lint: ok (%d findings, all in the baseline of %d)\n", len(rep.Issues), base.Total)
	if fixed > 0 {
		fmt.Printf("lint: %d baselined finding(s) are fixed; run `make lint-baseline-update` to shrink the baseline\n", fixed)
	}
}

// measured are the linters whose message carries a measure of the code (a
// function's length, its complexity). Such a finding that is the same except
// that every number in it is smaller than the recorded one is a function that
// got shorter or simpler, not a new finding.
var measured = map[string]bool{"funlen": true, "gocyclo": true, "gocognit": true, "cyclop": true, "nestif": true}

var numberRe = regexp.MustCompile(`\d+`)

// shrunk reports whether the finding is a recorded one of the same shape whose
// measures are all no larger.
func shrunk(k string, recorded map[string]int) bool {
	p := strings.Split(k, "\x00")
	if len(p) != 3 || !measured[p[1]] {
		return false
	}
	shape := numberRe.ReplaceAllString(p[2], "#")
	now := numberRe.FindAllString(p[2], -1)
	for rk := range recorded {
		q := strings.Split(rk, "\x00")
		if len(q) != 3 || q[0] != p[0] || q[1] != p[1] || numberRe.ReplaceAllString(q[2], "#") != shape {
			continue
		}
		was := numberRe.FindAllString(q[2], -1)
		if len(was) != len(now) {
			continue
		}
		ok := true
		for i := range now {
			a, _ := strconv.Atoi(now[i])
			b, _ := strconv.Atoi(was[i])
			// The limit in "(211 > 150)" is the same number in both; the measure may fall.
			if a > b {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func newFindings(current, recorded map[string]int, where map[string][]int) []string {
	var out []string
	for k, n := range current {
		if n > recorded[k] && (recorded[k] > 0 || !shrunk(k, recorded)) {
			p := strings.Split(k, "\x00")
			out = append(out, fmt.Sprintf("%s [%s] %s (lines %v; %d, baseline %d)", p[0], p[1], p[2], where[k], n, recorded[k]))
		}
	}
	sort.Strings(out)
	return out
}

func write(path string, current map[string]int) {
	b := baseline{Comment: "Findings tolerated because they predate the quality gates. May only shrink: `make lint-baseline-update`."}
	for k, n := range current {
		p := strings.Split(k, "\x00")
		b.Entries = append(b.Entries, entry{File: p[0], Linter: p[1], Text: p[2], Count: n})
		b.Total += n
	}
	sort.Slice(b.Entries, func(i, j int) bool {
		a, c := b.Entries[i], b.Entries[j]
		if a.File != c.File {
			return a.File < c.File
		}
		if a.Linter != c.Linter {
			return a.Linter < c.Linter
		}
		return a.Text < c.Text
	})
	out, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		fatal("%v", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		fatal("%v", err)
	}
}

func mustRead(path string, v any) {
	b, err := os.ReadFile(path)
	if err != nil {
		fatal("%v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		fatal("%s: %v", path, err)
	}
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
