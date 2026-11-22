package metrics

import (
	"fmt"
	"strings"
)

// Markdown renders the reference page for every metric in the registry, so
// the page cannot drift from the code: a test compares it with
// docs/reference/metrics.md.
func Markdown(r *Registry) string {
	var b strings.Builder
	b.WriteString("<!-- Generated from the metrics registry (internal/metrics/catalog.go). Do not edit by hand: run\n" +
		"     LAUNCHWAY_UPDATE_DOCS=1 go test ./internal/metrics -run TestReferencePageMatchesRegistry -->\n\n")
	b.WriteString("# Metrics reference\n\n")
	b.WriteString("Every metric the servers, Stations and the deploy runner expose on the metrics address, in the text exposition format. ")
	b.WriteString("See [Metrics and traces](../howto/metrics-and-traces.md) for how to scrape them.\n\n")
	b.WriteString("Labels come from small closed sets. A repository, a branch, a person or a run id is never a label. ")
	b.WriteString("Every metric holds at most 500 distinct label sets (the Flight metric 300); more are dropped and counted in `launchway_metrics_series_dropped_total`.\n\n")
	b.WriteString("| Metric | Type | Labels | Meaning |\n|---|---|---|---|\n")
	for _, d := range r.Descs() {
		labels := "none"
		if len(d.Labels) > 0 {
			labels = "`" + strings.Join(d.Labels, "`, `") + "`"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", d.Name, d.Kind, labels, strings.ReplaceAll(d.Help, "|", "\\|"))
	}
	return b.String()
}
