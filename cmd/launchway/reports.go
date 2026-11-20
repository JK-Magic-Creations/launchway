package main

import (
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// The reports a run kept, from the command line: its tests, its coverage
// and its HTML reports.

// tests prints a run's test results: totals, then the failures (or every
// result with --all), with their messages.
func (cl *client) tests(args []string) error {
	fs := flag.NewFlagSet("runs tests", flag.ContinueOnError)
	all := fs.Bool("all", false, "list every result, not only the failures")
	status := fs.String("status", "", "only results with this status: failed, passed, skipped or flaky")
	tag := fs.String("tag", "", "only results with this tag")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway runs tests <run-id>")
	}
	id := url.PathEscape(pos[0])
	var sum engine.RunTests
	if err := cl.call("GET", "/api/v1/runs/"+id+"/tests", nil, &sum); err != nil {
		return err
	}
	q := url.Values{"limit": {"1000"}}
	switch {
	case *status != "":
		q.Set("status", *status)
	case !*all:
		q.Set("status", "failed")
	}
	if *tag != "" {
		q.Set("tag", *tag)
	}
	var page engine.CasePage
	if err := cl.call("GET", "/api/v1/runs/"+id+"/tests/cases?"+q.Encode(), nil, &page); err != nil {
		return err
	}
	return cl.print(struct {
		Summary *engine.RunTests `json:"summary"`
		Cases   *engine.CasePage `json:"cases"`
	}{&sum, &page}, *asJSON, func() {
		t := sum.Totals
		if t == nil {
			fmt.Fprintln(cl.out, "No test results.")
			return
		}
		fmt.Fprintf(cl.out, "%d tests: %d passed, %d failed, %d errors, %d skipped", t.Total, t.Passed, t.Failed, t.Errored, t.Skipped)
		if sum.Flaky > 0 {
			fmt.Fprintf(cl.out, ", %d flaky", sum.Flaky)
		}
		if sum.NewFailures > 0 && sum.Previous != nil {
			fmt.Fprintf(cl.out, "; %d new since #%d", sum.NewFailures, sum.Previous.Number)
		}
		fmt.Fprintln(cl.out)
		for _, c := range page.Cases {
			name := c.Name
			if c.Variant != "" {
				name += " [" + c.Variant + "]"
			}
			fmt.Fprintf(cl.out, "  %-8s %s  %s  (%s, %dms)\n", c.Status, c.Suite, name, c.Step, c.DurationMs)
			if c.Message != "" && (c.Status == "failed" || c.Status == "errored") {
				fmt.Fprintf(cl.out, "           %s\n", firstLine(c.Message, 160))
			}
		}
		if page.Matching > len(page.Cases) {
			fmt.Fprintf(cl.out, "  … and %d more\n", page.Matching-len(page.Cases))
		}
	})
}

// coverage prints a run's coverage, step by step, and how it moved.
func (cl *client) coverage(args []string) error {
	fs := flag.NewFlagSet("runs coverage", flag.ContinueOnError)
	files := fs.Bool("files", false, "also list each file, least covered first")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway runs coverage <run-id>")
	}
	id := url.PathEscape(pos[0])
	var rc engine.RunCoverage
	if err := cl.call("GET", "/api/v1/runs/"+id+"/coverage", nil, &rc); err != nil {
		return err
	}
	var fl engine.CoverageFiles
	if *files {
		if err := cl.call("GET", "/api/v1/runs/"+id+"/coverage/files?sort=lines&limit=5000", nil, &fl); err != nil {
			return err
		}
	}
	return cl.print(struct {
		Coverage *engine.RunCoverage   `json:"coverage"`
		Files    *engine.CoverageFiles `json:"files,omitempty"`
	}{&rc, map[bool]*engine.CoverageFiles{true: &fl}[*files]}, *asJSON, func() {
		if rc.Totals == nil {
			fmt.Fprintln(cl.out, "No coverage.")
			return
		}
		fmt.Fprintf(cl.out, "Coverage: %s\n", coverageLine(*rc.Totals))
		if rc.Previous != nil {
			fmt.Fprintf(cl.out, "  previous run #%d: %s\n", rc.Previous.Run.Number, coverageLine(rc.Previous.Totals))
		}
		if rc.Base != nil {
			fmt.Fprintf(cl.out, "  %s (#%d): %s\n", strings.TrimPrefix(rc.Base.Ref, "refs/heads/"), rc.Base.Run.Number, coverageLine(rc.Base.Totals))
		}
		for _, s := range rc.Steps {
			fmt.Fprintf(cl.out, "  %s: %s\n", s.Step, coverageLine(s.CoverageTotals))
			if s.FailedUnder != "" {
				fmt.Fprintf(cl.out, "    %s\n", s.FailedUnder)
			}
		}
		for _, f := range fl.Files {
			fmt.Fprintf(cl.out, "  %7s  %s\n", percent(f.Lines), f.Path)
		}
	})
}

// reports prints links to a run's HTML reports.
func (cl *client) reports(args []string) error {
	fs := flag.NewFlagSet("runs reports", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway runs reports <run-id>")
	}
	var out struct {
		Reports []struct {
			engine.HTMLReport
			URL string `json:"url"`
		} `json:"reports"`
		Declared []string `json:"declared"`
	}
	if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(pos[0])+"/reports", nil, &out); err != nil {
		return err
	}
	return cl.print(out, *asJSON, func() {
		if len(out.Reports) == 0 {
			fmt.Fprintln(cl.out, "No HTML reports.")
			return
		}
		for _, r := range out.Reports {
			link := r.URL
			if strings.HasPrefix(link, "/") {
				link = strings.TrimRight(cl.c.Server, "/") + link
			}
			if link == "" {
				link = "(its index page " + r.Index + " is missing)"
			}
			fmt.Fprintf(cl.out, "%s/%s (%d files): %s\n", r.Step, r.Name, r.Files, link)
		}
		fmt.Fprintln(cl.out, "Links work for a few hours, for anyone who has them.")
	})
}

func coverageLine(t engine.CoverageTotals) string {
	unit := "lines"
	if t.Unit == "statements" {
		unit = "statements"
	}
	parts := []string{unit + " " + percent(t.Lines)}
	if t.Branches.Total > 0 {
		parts = append(parts, "branches "+percent(t.Branches))
	}
	if t.Functions.Total > 0 {
		parts = append(parts, "functions "+percent(t.Functions))
	}
	return strings.Join(parts, ", ")
}

func percent(c engine.CoverageCounter) string {
	if c.Total == 0 {
		return "-"
	}
	return strconv.FormatFloat(100*float64(c.Covered)/float64(c.Total), 'f', 1, 64) + "%"
}

func firstLine(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}
