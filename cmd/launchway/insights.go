package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

func init() { commands["insights"] = cmdInsights }

var insightsUsage = themed(`Usage:
  launchway insights [--flight NAME | --squadron NAME] [--from T] [--to T] [--branch B]
                     [--bucket hour|day|week|month] [--csv FILE] [--json]

The same numbers as the Insights pages, for the {projects} you may read: run success rate and
duration (median and 95th percentile), time spent in the holding pattern, the slowest and flakiest steps, test totals,
and the four delivery measures. With neither --flight nor --squadron it counts every {project}
you may read. --csv FILE writes the series as a spreadsheet file.
`)

func cmdInsights(args []string) error {
	return runClient(func() error {
		c, err := loadCredentials()
		if err != nil {
			return err
		}
		return newClient(c, os.Stdout).insights(args)
	})
}

func (cl *client) insights(args []string) error {
	fs := flag.NewFlagSet("insights", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, insightsUsage) }
	flight := fs.String("flight", "", "one Flight")
	squadron := fs.String("squadron", "", "one Squadron")
	from := fs.String("from", "", "start of the period (default 30 days before the end)")
	to := fs.String("to", "", "end of the period (default now)")
	branch := fs.String("branch", "", "count only runs of this branch")
	bucket := fs.String("bucket", "", "hour, day, week or month")
	csvFile := fs.String("csv", "", "write the series as a CSV file")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	q := url.Values{"flight": {*flight}, "squadron": {*squadron}, "from": {*from}, "to": {*to}, "branch": {*branch}, "bucket": {*bucket}}
	if *csvFile != "" {
		q.Set("format", "csv")
		return cl.download("/api/v1/insights?"+q.Encode(), *csvFile)
	}
	var raw map[string]any
	if err := cl.call(http.MethodGet, "/api/v1/insights?"+q.Encode(), nil, &raw); err != nil {
		return err
	}
	if *asJSON {
		return cl.showJSON(raw)
	}
	in, _ := raw["insights"].(map[string]any)
	return printInsights(cl.out, raw, in)
}

func printInsights(out io.Writer, raw, in map[string]any) error {
	scope, _ := raw["scope"].(string)
	name, _ := raw["name"].(string)
	if name != "" {
		scope += " " + name
	}
	span := func(k string) string { return time.UnixMilli(int64(num(in[k]))).UTC().Format("2006-01-02") }
	fmt.Fprintf(out, "Insights for %s, %s to %s\n", scope, span("from"), span("to"))
	fmt.Fprintf(out, "runs %v: %v succeeded, %v failed, %v canceled; success rate %.0f%%\n", in["runs"], in["succeeded"], in["failed"], in["canceled"], num(in["success_rate"])*100)
	fmt.Fprintf(out, "duration: median %.0f s, 95th percentile %.0f s; holding pattern: median %.0f s, 95th percentile %.0f s\n",
		num(in["duration_p50_s"]), num(in["duration_p95_s"]), num(in["queue_wait_p50_s"]), num(in["queue_wait_p95_s"]))
	fmt.Fprintf(out, "tests: %v run, %v failed, %v flaky\n", in["tests_total"], in["tests_failed"], in["tests_flaky"])
	for _, kind := range []struct{ key, title string }{{"slowest_steps", "slowest steps"}, {"flakiest_steps", "flakiest steps"}} {
		rows, _ := in[kind.key].([]any)
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(out, "%s:\n", kind.title)
		for _, r := range rows {
			m, _ := r.(map[string]any)
			fmt.Fprintf(out, "  %-28v runs %-5v failed %-4v (%.0f%%)  median %.0f s  95th %.0f s\n", m["step"], m["runs"], m["failures"], num(m["failure_rate"])*100, num(m["p50_s"]), num(m["p95_s"]))
		}
	}
	if d, ok := in["delivery"].(map[string]any); ok {
		fmt.Fprintf(out, "delivery: %v launches, %v failures; %.2f a week; lead time (median) %.1f h; change failure rate %.0f%%; time to restore (median) %.1f h\n",
			d["launches"], d["failures"], num(d["frequency_per_week"]), num(d["lead_time_median_s"])/3600, num(d["change_failure_rate"])*100, num(d["time_to_restore_median_s"])/3600)
	}
	if t, _ := in["truncated"].(bool); t {
		fmt.Fprintln(out, "note: the period holds more rows than are counted (the oldest 50,000); narrow the period.")
	}
	return nil
}
