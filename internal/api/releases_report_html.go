package api

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/release"
)

// renderReportHTML writes the release report as one self-contained page: no
// script, no outside file, light and dark by the reader's setting, with each chart
// followed by the table it is drawn from.
func renderReportHTML(r *engine.ReleaseReport) string {
	var b strings.Builder
	e := html.EscapeString
	day := func(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02") }
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Release report</title><style>
:root{color-scheme:light dark;--bg:#fff;--fg:#1b1f24;--mut:#57606a;--line:#d0d7de;--bar:#1f6feb;--bad:#cf222e}
@media (prefers-color-scheme:dark){:root{--bg:#0d1117;--fg:#e6edf3;--mut:#9da7b3;--line:#30363d;--bar:#58a6ff;--bad:#ff7b72}}
body{margin:0 auto;max-width:60rem;padding:1.5rem 1rem;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif}
h1{font-size:1.5rem}h2{font-size:1.15rem;margin-top:2rem}table{border-collapse:collapse;width:100%;margin:.5rem 0 1rem}
th,td{border-bottom:1px solid var(--line);padding:.35rem .5rem;text-align:left}th{color:var(--mut);font-weight:600}.n{text-align:right}
svg{max-width:100%;height:auto}.mut{color:var(--mut)}</style></head><body>`)
	scope := "every Flight"
	if r.Project != "" {
		scope = r.Project
	}
	fmt.Fprintf(&b, "<h1>Release report: %s</h1><p class=\"mut\">%s to %s (UTC)</p>", e(scope), day(r.From), day(r.To))
	m := r.Measures
	b.WriteString("<h2>The four delivery measures</h2><table><thead><tr><th>Measure</th><th class=\"n\">Value</th></tr></thead><tbody>")
	fmt.Fprintf(&b, "<tr><td>Launches</td><td class=\"n\">%d</td></tr>", m.Launches)
	fmt.Fprintf(&b, "<tr><td>Frequency (successful launches a week)</td><td class=\"n\">%.2f</td></tr>", m.FrequencyPerWeek)
	fmt.Fprintf(&b, "<tr><td>Lead time, median, merge to Airfield (%d launches measured)</td><td class=\"n\">%s</td></tr>", m.LeadTimeCount, spanText(m.LeadTimeMedianSec))
	fmt.Fprintf(&b, "<tr><td>Change failure rate (%d of %d)</td><td class=\"n\">%.0f%%</td></tr>", m.Failures, m.Launches, m.ChangeFailureRate*100)
	fmt.Fprintf(&b, "<tr><td>Time to restore, median (%d restored, %d not yet)</td><td class=\"n\">%s</td></tr></tbody></table>", m.RestoreCount, m.Unrestored, spanText(m.RestoreMedianSec))
	// A chart of launches per period, and its table.
	if len(r.Series) > 0 {
		maxN := 1
		for _, s := range r.Series {
			maxN = max(maxN, s.Launches)
		}
		w, h := 600, 140
		bw := max(4, (w-20)/len(r.Series)-2)
		fmt.Fprintf(&b, `<h2>Launches over time</h2><svg role="img" aria-label="Launches per period, with the table below" viewBox="0 0 %d %d">`, w, h+20)
		for i, s := range r.Series {
			okn := s.Launches - s.Failures
			x := 10 + i*(bw+2)
			hh := okn * h / maxN
			fh := s.Failures * h / maxN
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" fill="var(--bar)"/>`, x, h-hh, bw, hh)
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" fill="var(--bad)"/>`, x, h-hh-fh, bw, fh)
		}
		b.WriteString(`</svg><p class="mut">Blue: launches that succeeded. Red: launches that failed or were returned to base.</p>`)
		b.WriteString(`<table><caption class="mut">The same data as a table</caption><thead><tr><th>From</th><th class="n">Launches</th><th class="n">Failed</th><th class="n">Median lead time</th></tr></thead><tbody>`)
		for _, s := range r.Series {
			fmt.Fprintf(&b, "<tr><td>%s</td><td class=\"n\">%d</td><td class=\"n\">%d</td><td class=\"n\">%s</td></tr>", day(s.From.UnixMilli()), s.Launches, s.Failures, spanText(s.LeadTimeMedianSec))
		}
		b.WriteString("</tbody></table>")
	}
	table := func(title string, by map[string]release.Measures) {
		if len(by) == 0 {
			return
		}
		keys := make([]string, 0, len(by))
		for k := range by {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "<h2>%s</h2><table><thead><tr><th>Name</th><th class=\"n\">Launches</th><th class=\"n\">Failed</th><th class=\"n\">Failure rate</th><th class=\"n\">Median lead time</th><th class=\"n\">Median time to restore</th></tr></thead><tbody>", e(title))
		for _, k := range keys {
			x := by[k]
			fmt.Fprintf(&b, "<tr><td>%s</td><td class=\"n\">%d</td><td class=\"n\">%d</td><td class=\"n\">%.0f%%</td><td class=\"n\">%s</td><td class=\"n\">%s</td></tr>", e(k), x.Launches, x.Failures, x.ChangeFailureRate*100, spanText(x.LeadTimeMedianSec), spanText(x.RestoreMedianSec))
		}
		b.WriteString("</tbody></table>")
	}
	table("By Airfield", r.ByAirfield)
	table("By Flight", r.ByProject)
	b.WriteString("<h2>What shipped</h2>")
	if len(r.Shipped) == 0 {
		b.WriteString("<p>No release was published in this period.</p>")
	} else {
		b.WriteString("<table><thead><tr><th>Release</th><th>Published</th><th>Reached</th><th class=\"n\">Failures</th><th class=\"n\">Returns</th></tr></thead><tbody>")
		for _, row := range r.Shipped {
			var reached []string
			for _, a := range row.Reached {
				s := a.Airfield + " " + day(a.At)
				if a.LeadSeconds > 0 {
					s += " (lead " + spanText(float64(a.LeadSeconds)) + ")"
				}
				reached = append(reached, s)
			}
			fmt.Fprintf(&b, "<tr><td>%s %s</td><td>%s by %s</td><td>%s</td><td class=\"n\">%d</td><td class=\"n\">%d</td></tr>", e(row.Release.Project), e(row.Release.Version), day(row.Release.PublishedAt), e(row.Release.PublishedBy), e(strings.Join(reached, "; ")), row.Failures, row.Returns)
		}
		b.WriteString("</tbody></table>")
	}
	b.WriteString("</body></html>")
	return b.String()
}

func spanText(sec float64) string {
	switch {
	case sec <= 0:
		return "none"
	case sec < 90:
		return fmt.Sprintf("%.0f s", sec)
	case sec < 5400:
		return fmt.Sprintf("%.0f min", sec/60)
	case sec < 48*3600:
		return fmt.Sprintf("%.1f h", sec/3600)
	}
	return fmt.Sprintf("%.1f days", sec/86400)
}
