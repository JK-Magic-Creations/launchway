package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jk-magic-creations/launchway/internal/debrief"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// The Debrief, a project's flaky tests, timings and shards from the command
// line. The same things the API, the MCP server and the web pages give.

const debriefUsage = `Usage:
  launchway debrief show <run-id> [--json]                       the report of a run's tests, failures first
  launchway debrief summary <run-id>                            the short form (Markdown) for a pull request comment
  launchway debrief export <run-id> --format html|pdf|json|junit [--out FILE]
                                                                a self-contained report to keep or send
  launchway tests flaky <Flight> [--limit N] [--json]           tests whose history shows them flaky, with a quarantine entry to copy
  launchway tests timings <Flight> [--limit N] [--json]         each test's typical duration, slowest first
  launchway tests split <Flight> --shards N [--shard I] [--json]
                                                                a balanced split of the tests for N shards; --shard I prints only
                                                                the names of shard I (from 0), one per line, for a test tool's arguments
`

func cmdDebrief(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, debriefUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "show":
			return cl.debriefShow(args[1:])
		case "summary":
			return cl.debriefSummary(args[1:])
		case "export":
			return cl.debriefExport(args[1:])
		}
		return usageErr("unknown command debrief %s\n\n%s", args[0], debriefUsage)
	})
}

func cmdTests(args []string) error {
	return runClient(func() error {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, debriefUsage)
			return cliErr{exitUsage, ""}
		}
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		cl := newClient(c, os.Stdout)
		switch args[0] {
		case "flaky":
			return cl.testsFlaky(args[1:])
		case "timings":
			return cl.testsTimings(args[1:])
		case "split":
			return cl.testsSplit(args[1:])
		}
		return usageErr("unknown command tests %s\n\n%s", args[0], debriefUsage)
	})
}

func (cl *client) debriefShow(args []string) error {
	fs := flag.NewFlagSet("debrief show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway debrief show <run-id>")
	}
	var r debrief.Report
	if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(pos[0])+"/debrief", nil, &r); err != nil {
		return err
	}
	return cl.print(r, *asJSON, func() {
		fmt.Fprintf(cl.out, "Debrief of %s run #%d (%s, %s)\n%s\n\n", r.Project, r.Run.Number, r.Run.Branch, short8(r.Run.SHA), debrief.Headline(&r))
		s := r.Summary
		fmt.Fprintf(cl.out, "%d tests: %d passed, %d failed, %d errored, %d skipped, %d flaky, %d quarantined; %s; pass rate %.1f%%\n",
			s.Total, s.Passed, s.Failed, s.Errored, s.Skipped, s.Flaky, s.Quarantined, time.Duration(s.DurationMs)*time.Millisecond, s.PassRate)
		if c := r.Change; c != nil {
			fmt.Fprintf(cl.out, "Against %s run #%d: %d newly failing, %d newly passing, %d slower\n", c.Base.Branch, c.Base.Number, c.NewlyFailing, c.NewlyPassing, len(c.Slower))
		}
		for _, f := range r.Failures {
			where := ""
			switch f.FailingOnBase {
			case "yes":
				where = "  [also failing on the default branch]"
			case "no":
				where = "  [passes on the default branch: this change broke it]"
			}
			fmt.Fprintf(cl.out, "\n  FAILED %s  %s%s\n", f.Suite, f.Name, where)
			if f.FirstFailedIn != nil {
				fmt.Fprintf(cl.out, "    first failed in run #%d (%s)\n", f.FirstFailedIn.Number, short8(f.FirstFailedIn.SHA))
			}
			if f.Message != "" {
				fmt.Fprintf(cl.out, "    %s\n", firstLine(f.Message, 200))
			}
			if f.Diff != nil {
				fmt.Fprintf(cl.out, "    expected: %s\n    actual:   %s\n", firstLine(f.Diff.Expected, 120), firstLine(f.Diff.Actual, 120))
			}
		}
		for _, f := range r.Flaky {
			fmt.Fprintf(cl.out, "\n  FLAKY %s  %s  score %d: %s (%s)\n", f.Suite, f.Name, f.Score, f.Reason, f.ThisRun)
		}
		for _, q := range r.Quarantined {
			fmt.Fprintf(cl.out, "\n  QUARANTINED %s  %s  owner %s, expires %s\n", q.Suite, q.Name, q.Owner, q.Expires)
		}
		if c := r.Coverage; c != nil {
			fmt.Fprintf(cl.out, "\nCoverage: lines %.1f%%", c.Lines.Percent)
			if cc := c.Change; cc != nil {
				fmt.Fprintf(cl.out, "; of the changed lines %.1f%% (%d of %d)", cc.Percent, cc.Covered, cc.Total)
				if cc.Advisory != "" {
					fmt.Fprintf(cl.out, "\n  Advice: %s", cc.Advisory)
				}
			}
			fmt.Fprintln(cl.out)
		}
		for _, n := range r.Notes {
			fmt.Fprintf(cl.out, "\nNote: %s", n)
		}
		fmt.Fprintln(cl.out)
	})
}

func short8(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func (cl *client) debriefSummary(args []string) error {
	fs := flag.NewFlagSet("debrief summary", flag.ContinueOnError)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway debrief summary <run-id>")
	}
	var body []byte
	if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(pos[0])+"/debrief/summary", nil, &body); err != nil {
		return err
	}
	_, err = cl.out.Write(body)
	return err
}

func (cl *client) debriefExport(args []string) error {
	fs := flag.NewFlagSet("debrief export", flag.ContinueOnError)
	format := fs.String("format", "html", "html, pdf, json or junit")
	out := fs.String("out", "", "the file to write (default debrief-<run-id>.<ext> here; - for standard output)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which run: launchway debrief export <run-id> --format html|pdf|json|junit")
	}
	ext := map[string]string{"html": "html", "pdf": "pdf", "json": "json", "junit": "xml"}[*format]
	if ext == "" {
		return usageErr("--format is html, pdf, json or junit")
	}
	var body []byte
	if err := cl.call("GET", "/api/v1/runs/"+url.PathEscape(pos[0])+"/debrief/export?format="+*format, nil, &body); err != nil {
		return err
	}
	if *out == "-" {
		_, err := cl.out.Write(body)
		return err
	}
	name := *out
	if name == "" {
		name = "debrief-" + pos[0] + "." + ext
	}
	if err := os.WriteFile(name, body, 0o644); err != nil { //nolint:gosec // G306: a report to share, not a secret
		return usageErr("could not write %s: %v", name, err)
	}
	fmt.Fprintf(cl.out, "Wrote %s (%d bytes)\n", name, len(body))
	return nil
}

func (cl *client) testsFlaky(args []string) error {
	fs := flag.NewFlagSet("tests flaky", flag.ContinueOnError)
	limit := fs.Int("limit", 50, "at most this many")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which Flight: launchway tests flaky <Flight>")
	}
	var fl engine.FlakyList
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/tests/flaky?limit="+strconv.Itoa(*limit), nil, &fl); err != nil {
		return err
	}
	return cl.print(fl, *asJSON, func() {
		if len(fl.Tests) == 0 {
			fmt.Fprintf(cl.out, "No flaky tests in the last %d runs (%d tests both passed and failed).\n", fl.Window, fl.Considered)
			return
		}
		tw := tabwriter.NewWriter(cl.out, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SCORE\tTEST\tBRANCHES\tWHY")
		for _, t := range fl.Tests {
			name := t.Name
			if t.Suite != "" {
				name = t.Suite + "::" + name
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", t.Score, name, strings.Join(t.Branches, ","), t.Reason)
		}
		tw.Flush()
		fmt.Fprintf(cl.out, "\n%s\n\nTo quarantine one, add an entry like this to %s in the repository (nothing writes it for you):\n\n%s\n", fl.Rules, debrief.QuarantinePath, fl.Tests[0].Suggest)
	})
}

func (cl *client) testsTimings(args []string) error {
	fs := flag.NewFlagSet("tests timings", flag.ContinueOnError)
	limit := fs.Int("limit", 50, "at most this many")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageErr("say which Flight: launchway tests timings <Flight>")
	}
	var tl engine.TimingList
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/tests/timings?limit="+strconv.Itoa(*limit), nil, &tl); err != nil {
		return err
	}
	return cl.print(tl, *asJSON, func() {
		if len(tl.Tests) == 0 {
			fmt.Fprintln(cl.out, "No timings yet: they are kept as test reports arrive.")
			return
		}
		tw := tabwriter.NewWriter(cl.out, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "AVERAGE\tLONGEST\tRUNS\tTEST")
		for _, t := range tl.Tests {
			name := t.Name
			if t.Suite != "" {
				name = t.Suite + "::" + name
			}
			fmt.Fprintf(tw, "%dms\t%dms\t%d\t%s\n", t.AvgMs, t.MaxMs, t.Samples, name)
		}
		tw.Flush()
		if tl.Truncated {
			fmt.Fprintln(cl.out, "(more tests than shown)")
		}
	})
}

func (cl *client) testsSplit(args []string) error {
	fs := flag.NewFlagSet("tests split", flag.ContinueOnError)
	shards := fs.Int("shards", 0, "how many shards (1 to 256)")
	only := fs.Int("shard", -1, "print only the names of this shard (from 0), one per line")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *shards < 1 {
		return usageErr("say the Flight and the shards: launchway tests split <Flight> --shards N")
	}
	var ts engine.TestShards
	if err := cl.call("GET", "/api/v1/projects/"+url.PathEscape(pos[0])+"/tests/shards?count="+strconv.Itoa(*shards), nil, &ts); err != nil {
		return err
	}
	if *only >= 0 {
		if *only >= len(ts.Shards) {
			return usageErr("--shard is from 0 to %d", len(ts.Shards)-1)
		}
		for _, n := range ts.Shards[*only].Tests {
			fmt.Fprintln(cl.out, n)
		}
		return nil
	}
	return cl.print(ts, *asJSON, func() {
		fmt.Fprintf(cl.out, "%d tests in %d shards (longest %dms, shortest %dms)", ts.Tests, ts.Count, ts.MaxMs, ts.MinMs)
		if ts.Unknown > 0 {
			fmt.Fprintf(cl.out, "; %d have no history and were given the average", ts.Unknown)
		}
		fmt.Fprintln(cl.out)
		for _, s := range ts.Shards {
			fmt.Fprintf(cl.out, "  shard %d: %d tests, %dms\n", s.Index, len(s.Tests), s.TotalMs)
		}
	})
}
