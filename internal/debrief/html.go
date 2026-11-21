package debrief

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// HTML writes the Debrief as one file: its styles are inside, there is no
// script and no reference to anything outside it, so it opens offline from a
// mail attachment or a build artifact, in a light or dark browser, and
// prints. Images that tests attached are embedded when small. Every chart
// has the numbers beside it in a table, and no meaning is carried by color
// alone (each state has a word and a mark).
func HTML(r *Report) []byte {
	var b strings.Builder
	h := &htmlWriter{b: &b, r: r}
	h.page()
	return []byte(b.String())
}

type htmlWriter struct {
	b *strings.Builder
	r *Report
}

func esc(s string) string { return html.EscapeString(s) }

func (h *htmlWriter) p(format string, args ...any) { fmt.Fprintf(h.b, format, args...) }

// StatusMark returns the mark and word for a status, used wherever a status
// is shown so that it never depends on color.
func StatusMark(s string) (mark, word string) {
	switch s {
	case Passed:
		return "✓", "Passed"
	case Failed:
		return "✕", "Failed"
	case Errored:
		return "!", "Errored"
	case Skipped:
		return "○", "Skipped"
	case "flaky":
		return "↯", "Flaky"
	case "quarantined":
		return "⚑", "Quarantined"
	}
	return "·", s
}

func fmtMs(ms int64) string {
	switch {
	case ms < 1:
		return "<1 ms"
	case ms < 1000:
		return fmt.Sprintf("%d ms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.1f s", float64(ms)/1000)
	case ms < 3_600_000:
		return fmt.Sprintf("%dm %02ds", ms/60_000, ms/1000%60)
	}
	return fmt.Sprintf("%dh %02dm", ms/3_600_000, ms/60_000%60)
}

func fmtTime(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04 UTC")
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func fmtPct(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d%%", int64(f))
	}
	return fmt.Sprintf("%.1f%%", f)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Headline is the one sentence a Debrief opens with.
func Headline(r *Report) string {
	s := r.Summary
	bad := s.Failed + s.Errored
	switch {
	case s.Total == 0:
		return "No test results were collected."
	case bad == 0 && s.Flaky == 0 && s.Quarantined == 0:
		return fmt.Sprintf("All %d tests passed.", s.Passed)
	case bad == 0:
		out := fmt.Sprintf("%s ran with no failures", plural(s.Total, "test", "tests"))
		var parts []string
		if s.Flaky > 0 {
			parts = append(parts, plural(s.Flaky, "flaky test", "flaky tests"))
		}
		if s.Quarantined > 0 {
			parts = append(parts, fmt.Sprintf("%d quarantined", s.Quarantined))
		}
		return out + ", with " + strings.Join(parts, " and ") + "."
	}
	out := fmt.Sprintf("%d of %d tests failed", bad, s.Total)
	if r.Change != nil && r.Change.NewlyFailing > 0 {
		out += fmt.Sprintf("; %d of them pass on %s", r.Change.NewlyFailing, branchName(r.Change.Base.Branch))
	}
	return out + "."
}

func branchName(b string) string {
	if b == "" {
		return "the default branch"
	}
	return b
}

const css = `
:root{--bg:#fbfbfa;--panel:#fff;--ink:#1b1f23;--muted:#545c66;--line:#d6d9dd;--accent:#1f5fbf;--pass:#17663a;--pass-bg:#e4f4ea;--fail:#a8211b;--fail-bg:#fbe9e7;--skip:#4d5560;--skip-bg:#eceef1;--warn:#8a5300;--warn-bg:#fff1d6;--code:#f3f4f6;--add:#dff3e4;--del:#fbe3e0}
@media (prefers-color-scheme:dark){:root{--bg:#14171a;--panel:#1c2024;--ink:#e8eaed;--muted:#a9b0b9;--line:#3a4048;--accent:#8ab4f8;--pass:#6fd39a;--pass-bg:#16301f;--fail:#ff9d94;--fail-bg:#3b1a18;--skip:#b4bbc4;--skip-bg:#2a2f35;--warn:#f2c26b;--warn-bg:#3a2d10;--code:#101316;--add:#16301f;--del:#3b1a18}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
a{color:var(--accent)}
a:focus-visible,summary:focus-visible{outline:3px solid var(--accent);outline-offset:2px}
.skip{position:absolute;left:-999px}.skip:focus{left:8px;top:8px;background:var(--panel);padding:8px;z-index:9}
header.top,main,footer{max-width:1100px;margin:0 auto;padding:0 20px}
header.top{padding-top:28px}
h1{font-size:1.7rem;line-height:1.2;margin:.2rem 0}
h2{font-size:1.25rem;margin:2rem 0 .6rem;padding-bottom:.3rem;border-bottom:1px solid var(--line)}
h3{font-size:1rem;margin:0}
.brand{color:var(--muted);font-size:.9rem;display:flex;gap:10px;align-items:center}
.brand img{max-height:32px}
.meta{display:flex;flex-wrap:wrap;gap:6px 22px;color:var(--muted);margin:.4rem 0 0;padding:0;list-style:none}
.meta b{color:var(--ink);font-weight:600}
.headline{font-size:1.15rem;margin:1rem 0 .2rem}
nav.toc ul{display:flex;flex-wrap:wrap;gap:4px 18px;list-style:none;padding:0;margin:1rem 0}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(130px,1fr));gap:10px;margin:1rem 0}
.card{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:10px 12px}
.card .n{font-size:1.6rem;font-weight:700;line-height:1.1}
.card .l{color:var(--muted);font-size:.85rem}
.card.bad{border-left:5px solid var(--fail)}.card.good{border-left:5px solid var(--pass)}.card.warn{border-left:5px solid var(--warn)}.card.neutral{border-left:5px solid var(--skip)}
.pill{display:inline-block;border-radius:999px;padding:1px 9px;font-size:.8rem;font-weight:600;border:1px solid currentColor;white-space:nowrap}
.pill.passed{color:var(--pass);background:var(--pass-bg)}.pill.failed,.pill.errored{color:var(--fail);background:var(--fail-bg)}.pill.skipped{color:var(--skip);background:var(--skip-bg)}.pill.flaky,.pill.quarantined{color:var(--warn);background:var(--warn-bg)}
table{border-collapse:collapse;width:100%;background:var(--panel);font-size:.92rem}
th,td{text-align:left;padding:6px 10px;border-bottom:1px solid var(--line);vertical-align:top}
th{font-weight:600;background:var(--skip-bg)}
td.num,th.num{text-align:right;font-variant-numeric:tabular-nums}
caption{text-align:left;color:var(--muted);padding:4px 0;font-size:.85rem}
.scroll{overflow-x:auto}
details.fail{background:var(--panel);border:1px solid var(--line);border-left:5px solid var(--fail);border-radius:6px;margin:10px 0}
details.fail>summary{cursor:pointer;padding:10px 12px;list-style:none}
details.fail>summary::-webkit-details-marker{display:none}
details.fail .body{padding:0 12px 12px}
.suite{color:var(--muted);font-size:.85rem}
.name{font-weight:600;overflow-wrap:anywhere}
pre{background:var(--code);border:1px solid var(--line);border-radius:6px;padding:10px;overflow:auto;font:13px/1.45 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;white-space:pre-wrap;overflow-wrap:anywhere;max-height:420px;margin:.4rem 0}
.diff{font:13px/1.45 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;border:1px solid var(--line);border-radius:6px;overflow:auto;margin:.4rem 0}
.diff div{padding:0 10px;white-space:pre-wrap;overflow-wrap:anywhere}
.diff .add{background:var(--add)}.diff .del{background:var(--del)}
.diff .op{display:inline-block;width:1.4em;color:var(--muted);user-select:none}
.facts{display:flex;flex-wrap:wrap;gap:4px 18px;color:var(--muted);font-size:.9rem;margin:.3rem 0}
.facts b{color:var(--ink)}
.bar{display:flex;height:22px;border:1px solid var(--line);border-radius:5px;overflow:hidden;background:var(--panel)}
figure{margin:1rem 0}figcaption{color:var(--muted);font-size:.85rem;margin-top:4px}
.legend{display:flex;flex-wrap:wrap;gap:4px 16px;margin:.4rem 0;padding:0;list-style:none;font-size:.9rem}
.sw{display:inline-block;width:14px;height:14px;border:1px solid var(--ink);vertical-align:-2px;margin-right:5px}
svg{color:var(--ink)}
svg text{fill:var(--ink);font:12px system-ui,sans-serif}
.note{background:var(--warn-bg);border:1px solid var(--warn);color:var(--ink);border-radius:6px;padding:8px 12px;margin:.5rem 0}
.empty{color:var(--muted);font-style:italic}
.mx td,.mx th{text-align:center;padding:4px 8px}.mx td:first-child,.mx th:first-child{text-align:left}
details.tbl{margin:.4rem 0}details.tbl summary{cursor:pointer;color:var(--accent)}
img.shot{max-width:100%;max-height:360px;border:1px solid var(--line);border-radius:6px}
footer{color:var(--muted);font-size:.85rem;padding-bottom:40px;margin-top:2rem}
.cov{display:inline-block;height:10px;border:1px solid var(--ink);background:var(--panel);width:90px;vertical-align:middle}.cov i{display:block;height:100%;background:var(--ink)}
@media print{body{background:#fff;color:#000}details.fail{break-inside:avoid}nav.toc{display:none}pre{max-height:none}.skip{display:none}}
`

func (h *htmlWriter) page() {
	r := h.r
	title := fmt.Sprintf("Debrief: %s #%d", r.Project, r.Run.Number)
	h.p("<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n")
	h.p("<meta name=\"color-scheme\" content=\"light dark\"><meta name=\"generator\" content=\"Launchway Debrief v%d\">\n<title>%s</title>\n<style>%s</style></head><body>\n", Version, esc(title), css)
	h.p("<a class=\"skip\" href=\"#main\">Skip to the report</a>\n")
	h.header()
	h.p("<main id=\"main\">\n")
	h.summary()
	h.failures()
	h.flaky()
	h.quarantined()
	h.slowest()
	h.suites()
	h.matrix()
	h.coverage()
	h.trend()
	h.notes()
	h.p("</main>\n")
	h.p("<footer>Generated %s by %s. This file is complete in itself: it needs no network.", fmtTime(r.Generated), esc(nonEmpty(r.Install.Name, "Launchway")))
	if r.Run.Link != "" {
		h.p(" The live Debrief is at <a href=\"%s\">%s</a>.", esc(r.Run.Link), esc(r.Run.Link))
	}
	h.p("</footer></body></html>\n")
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (h *htmlWriter) header() {
	r := h.r
	h.p("<header class=\"top\"><div class=\"brand\">")
	if strings.HasPrefix(r.Install.Logo, "data:image/") {
		h.p("<img src=\"%s\" alt=\"\">", esc(r.Install.Logo))
	}
	h.p("<span>%s</span></div>\n<h1>Debrief: %s, run #%d</h1>\n", esc(nonEmpty(r.Install.Name, "Launchway")), esc(r.Project), r.Run.Number)
	h.p("<ul class=\"meta\"><li>Branch <b>%s</b></li><li>Commit <b>%s</b></li><li>Run result <b>%s</b></li><li>Started <b>%s</b></li>", esc(nonEmpty(r.Run.Branch, r.Run.Ref)), esc(shortSHA(r.Run.SHA)), esc(r.Result), fmtTime(r.Run.CreatedAt))
	if r.Run.Actor != "" {
		h.p("<li>By <b>%s</b></li>", esc(r.Run.Actor))
	}
	h.p("</ul>\n<p class=\"headline\"><strong>%s</strong></p>\n", esc(Headline(r)))
	h.p("<nav class=\"toc\" aria-label=\"Sections\"><ul><li><a href=\"#summary\">Summary</a></li><li><a href=\"#failures\">Failures</a></li><li><a href=\"#flaky\">Flaky</a></li><li><a href=\"#slowest\">Slowest</a></li><li><a href=\"#suites\">Suites</a></li><li><a href=\"#coverage\">Coverage</a></li><li><a href=\"#trend\">Trend</a></li></ul></nav></header>\n")
}

func (h *htmlWriter) card(class string, n any, label string) {
	h.p("<div class=\"card %s\"><div class=\"n\">%v</div><div class=\"l\">%s</div></div>", class, n, esc(label))
}

func (h *htmlWriter) summary() {
	r, s := h.r, h.r.Summary
	h.p("<section id=\"summary\" aria-labelledby=\"h-summary\"><h2 id=\"h-summary\">Summary</h2>\n<div class=\"cards\">")
	h.card("neutral", s.Total, "tests")
	h.card("good", s.Passed, "✓ passed")
	cls := "good"
	if s.Failed+s.Errored > 0 {
		cls = "bad"
	}
	h.card(cls, s.Failed+s.Errored, "✕ failed")
	h.card("neutral", s.Skipped, "○ skipped")
	cls = "good"
	if s.Flaky > 0 {
		cls = "warn"
	}
	h.card(cls, s.Flaky, "↯ flaky")
	if s.Quarantined > 0 {
		h.card("warn", s.Quarantined, "⚑ quarantined")
	}
	h.card("neutral", fmtMs(s.DurationMs), "duration")
	h.card("neutral", fmtPct(s.PassRate), "pass rate")
	h.p("</div>\n")
	h.resultBar()
	if c := r.Change; c != nil {
		h.p("<h3>Against %s, run #%d (%s)</h3><ul>", esc(branchName(c.Base.Branch)), c.Base.Number, esc(shortSHA(c.Base.SHA)))
		h.p("<li><strong>%d</strong> newly failing</li><li><strong>%d</strong> newly passing</li>", c.NewlyFailing, c.NewlyPassing)
		h.p("<li>Test count %+d; duration %s%s</li>", c.TotalDelta, signedMs(c.DurationDelta), "")
		if len(c.Slower) > 0 {
			h.p("<li>%s slower: ", plural(len(c.Slower), "test", "tests"))
			for i, sl := range c.Slower {
				if i > 0 {
					h.p("; ")
				}
				if i == 5 {
					h.p("and %d more", len(c.Slower)-5)
					break
				}
				h.p("%s (%s to %s)", esc(sl.Name), fmtMs(sl.BaseMs), fmtMs(sl.Ms))
			}
			h.p("</li>")
		}
		h.p("</ul>\n")
	}
	h.p("</section>\n")
}

func signedMs(ms int64) string {
	if ms < 0 {
		return "−" + fmtMs(-ms) + " faster"
	}
	return "+" + fmtMs(ms) + " slower"
}

// resultBar is the stacked bar of passed, failed, skipped, with a pattern
// per state as well as a color, and a table of the same numbers.
func (h *htmlWriter) resultBar() {
	s := h.r.Summary
	if s.Total == 0 {
		return
	}
	type seg struct {
		label string
		n     int
		fill  string
	}
	segs := []seg{{"Passed", s.Passed, "url(#p-pass)"}, {"Failed", s.Failed + s.Errored, "url(#p-fail)"}, {"Skipped", s.Skipped, "url(#p-skip)"}}
	h.p("<figure><svg role=\"img\" aria-labelledby=\"bar-t bar-d\" width=\"100%%\" height=\"28\" viewBox=\"0 0 1000 28\" preserveAspectRatio=\"none\"><title id=\"bar-t\">Results of %d tests</title><desc id=\"bar-d\">%d passed, %d failed, %d skipped; the table below has the same numbers.</desc>", s.Total, s.Passed, s.Failed+s.Errored, s.Skipped)
	h.p("<defs><pattern id=\"p-pass\" width=\"6\" height=\"6\" patternUnits=\"userSpaceOnUse\"><rect width=\"6\" height=\"6\" fill=\"#2e8b57\"/></pattern><pattern id=\"p-fail\" width=\"8\" height=\"8\" patternUnits=\"userSpaceOnUse\" patternTransform=\"rotate(45)\"><rect width=\"8\" height=\"8\" fill=\"#c0392b\"/><rect width=\"3\" height=\"8\" fill=\"#fff\" opacity=\".55\"/></pattern><pattern id=\"p-skip\" width=\"6\" height=\"6\" patternUnits=\"userSpaceOnUse\"><rect width=\"6\" height=\"6\" fill=\"#8d96a0\"/><circle cx=\"3\" cy=\"3\" r=\"1.4\" fill=\"#fff\"/></pattern></defs>")
	x := 0.0
	for _, sg := range segs {
		if sg.n == 0 {
			continue
		}
		w := float64(sg.n) * 1000 / float64(s.Total)
		w = max(w, 3)
		h.p("<rect x=\"%.1f\" y=\"1\" width=\"%.1f\" height=\"26\" fill=\"%s\" stroke=\"currentColor\" stroke-width=\"1\" vector-effect=\"non-scaling-stroke\"/>", x, w, sg.fill)
		x += w
	}
	h.p("</svg><figcaption>Share of tests by result. Solid is passed, diagonal stripes are failed, dots are skipped.</figcaption></figure>\n")
	h.p("<div class=\"scroll\"><table><caption>Results</caption><thead><tr><th scope=\"col\">Result</th><th scope=\"col\" class=\"num\">Tests</th><th scope=\"col\" class=\"num\">Share</th></tr></thead><tbody>")
	for _, sg := range segs {
		h.p("<tr><th scope=\"row\">%s</th><td class=\"num\">%d</td><td class=\"num\">%s</td></tr>", sg.label, sg.n, fmtPct(float64(sg.n)*100/float64(s.Total)))
	}
	h.p("</tbody></table></div>\n")
}

func (h *htmlWriter) failures() {
	r := h.r
	h.p("<section id=\"failures\" aria-labelledby=\"h-fail\"><h2 id=\"h-fail\">Failures</h2>\n")
	if len(r.Failures) == 0 {
		h.p("<p class=\"empty\">No test failed.</p></section>\n")
		return
	}
	h.p("<p>%s, the newest breakage first.</p>\n", plural(len(r.Failures), "failing test", "failing tests"))
	for i, f := range r.Failures {
		mark, word := StatusMark(f.Status)
		h.p("<details class=\"fail\" id=\"f%d\"%s><summary><span class=\"pill failed\">%s %s</span> <span class=\"name\">%s</span>", i, openIf(i < 10), mark, word, esc(f.Name))
		if f.Suite != "" {
			h.p(" <span class=\"suite\">in %s</span>", esc(f.Suite))
		}
		if f.NewHere {
			h.p(" <span class=\"pill flaky\">new here</span>")
		}
		h.p("</summary><div class=\"body\">")
		h.p("<div class=\"facts\">")
		if f.File != "" {
			loc := f.File
			if f.Line > 0 {
				loc += fmt.Sprintf(":%d", f.Line)
			}
			h.p("<span>File <b>%s</b></span>", esc(loc))
		}
		h.p("<span>Took <b>%s</b></span>", fmtMs(f.DurationMs))
		if f.Variant != "" {
			h.p("<span>Variant <b>%s</b></span>", esc(f.Variant))
		}
		if f.Owner != "" {
			h.p("<span>Owner <b>%s</b></span>", esc(f.Owner))
		}
		if f.FirstFailedIn != nil {
			h.p("<span>First failed in <b>run #%d</b> (%s)</span>", f.FirstFailedIn.Number, esc(shortSHA(f.FirstFailedIn.SHA)))
		}
		switch f.FailingOnBase {
		case "yes":
			h.p("<span>Also failing on the default branch: <b>yes</b>, so this change did not break it</span>")
		case "no":
			h.p("<span>Also failing on the default branch: <b>no</b>, so this change broke it</span>")
		case "unknown":
			h.p("<span>Also failing on the default branch: <b>not known</b> (it did not run there)</span>")
		}
		if f.FlakyScore > 0 {
			h.p("<span><span class=\"pill flaky\">↯ flaky history</span> score %d: this failure may not be caused by the change</span>", f.FlakyScore)
		}
		h.p("</div>")
		if f.Message != "" {
			h.p("<pre>%s</pre>", esc(f.Message))
		}
		if f.Diff != nil {
			h.diff(f.Diff)
		}
		if f.Details != "" {
			h.p("<details><summary>Stack or trace</summary><pre>%s</pre></details>", esc(f.Details))
		}
		if f.Output != "" {
			h.p("<details><summary>Captured output</summary><pre>%s</pre></details>", esc(f.Output))
		}
		for _, a := range f.Attachments {
			if a.Data != "" && a.Kind == "image" {
				h.p("<figure><img class=\"shot\" src=\"%s\" alt=\"%s, attached to %s\"><figcaption>%s</figcaption></figure>", esc(a.Data), esc(a.Name), esc(f.Name), esc(a.Name))
			} else {
				h.p("<p>Attachment: %s (%s, %d bytes), kept in the live Debrief.</p>", esc(a.Name), esc(a.Kind), a.Bytes)
			}
		}
		h.p("</div></details>\n")
	}
	h.p("</section>\n")
}

func openIf(b bool) string {
	if b {
		return " open"
	}
	return ""
}

func (h *htmlWriter) diff(d *Diff) {
	h.p("<div class=\"diff\" role=\"group\" aria-label=\"Expected against actual\">")
	if !strings.Contains(d.Expected, "\n") && !strings.Contains(d.Actual, "\n") {
		pre, a, b, suf := InlineParts(d.Expected, d.Actual)
		h.p("<div class=\"del\"><span class=\"op\">−</span><span>expected: %s<mark>%s</mark>%s</span></div>", esc(pre), esc(a), esc(suf))
		h.p("<div class=\"add\"><span class=\"op\">+</span><span>actual:&nbsp;&nbsp;%s<mark>%s</mark>%s</span></div></div>", esc(pre), esc(b), esc(suf))
		return
	}
	for _, l := range d.Lines {
		cls, op := "", " "
		switch l.Op {
		case "-":
			cls, op = "del", "−"
		case "+":
			cls, op = "add", "+"
		}
		h.p("<div class=\"%s\"><span class=\"op\">%s</span>%s</div>", cls, op, esc(l.Text))
	}
	h.p("</div>")
}

func (h *htmlWriter) flaky() {
	h.p("<section id=\"flaky\" aria-labelledby=\"h-flaky\"><h2 id=\"h-flaky\">Flaky tests</h2>\n")
	if len(h.r.Flaky) == 0 {
		h.p("<p class=\"empty\">No test in this run has a flaky history.</p></section>\n")
		return
	}
	h.p("<div class=\"scroll\"><table><caption>Tests that passed and failed without a change that explains it. A test that passed on retry is shown here and never counted as a plain pass.</caption><thead><tr><th scope=\"col\">Test</th><th scope=\"col\">In this run</th><th scope=\"col\" class=\"num\">Score</th><th scope=\"col\">Why</th><th scope=\"col\">First seen</th></tr></thead><tbody>")
	for _, f := range h.r.Flaky {
		h.p("<tr><th scope=\"row\"><span class=\"name\">%s</span><br><span class=\"suite\">%s</span></th><td>↯ %s</td><td class=\"num\">%d</td><td>%s</td><td>%s</td></tr>", esc(f.Name), esc(f.Suite), esc(f.ThisRun), f.Score, esc(f.Reason), fmtTime(f.FirstSeen))
	}
	h.p("</tbody></table></div></section>\n")
}

func (h *htmlWriter) quarantined() {
	if len(h.r.Quarantined) == 0 {
		return
	}
	h.p("<section id=\"quarantine\" aria-labelledby=\"h-q\"><h2 id=\"h-q\">Quarantined tests</h2>\n<p>These tests are listed in the repository's quarantine file. Their failures do not fail the run, and are counted here until the entry expires.</p>\n")
	h.p("<div class=\"scroll\"><table><thead><tr><th scope=\"col\">Test</th><th scope=\"col\">This run</th><th scope=\"col\">Owner</th><th scope=\"col\">Expires</th><th scope=\"col\">Why</th></tr></thead><tbody>")
	for _, q := range h.r.Quarantined {
		mark, word := StatusMark(q.Status)
		exp := q.Expires
		if q.Expired {
			exp += " (expired: it counts as a failure again)"
		}
		h.p("<tr><th scope=\"row\">%s<br><span class=\"suite\">%s</span></th><td>%s %s</td><td>%s</td><td>%s</td><td>%s</td></tr>", esc(q.Name), esc(q.Suite), mark, word, esc(q.Owner), esc(exp), esc(q.Reason))
	}
	h.p("</tbody></table></div></section>\n")
}

func (h *htmlWriter) slowest() {
	h.p("<section id=\"slowest\" aria-labelledby=\"h-slow\"><h2 id=\"h-slow\">Slowest tests</h2>\n")
	if len(h.r.Slowest) == 0 {
		h.p("<p class=\"empty\">No durations were reported.</p></section>\n")
		return
	}
	top := h.r.Slowest[0].DurationMs
	h.p("<div class=\"scroll\"><table><thead><tr><th scope=\"col\">Test</th><th scope=\"col\" class=\"num\">Took</th><th scope=\"col\" class=\"num\">Usually</th><th scope=\"col\">Relative</th></tr></thead><tbody>")
	for _, s := range h.r.Slowest {
		w := 0
		if top > 0 {
			w = int(s.DurationMs * 100 / top)
		}
		usual := "—"
		if s.AvgMs > 0 {
			usual = fmtMs(s.AvgMs)
		}
		h.p("<tr><th scope=\"row\"><span class=\"name\">%s</span><br><span class=\"suite\">%s</span></th><td class=\"num\">%s</td><td class=\"num\">%s</td><td><span class=\"cov\" aria-hidden=\"true\"><i style=\"width:%d%%\"></i></span></td></tr>", esc(s.Name), esc(s.Suite), fmtMs(s.DurationMs), usual, w)
	}
	h.p("</tbody></table></div></section>\n")
}

func (h *htmlWriter) suites() {
	h.p("<section id=\"suites\" aria-labelledby=\"h-suites\"><h2 id=\"h-suites\">Suites</h2>\n")
	if len(h.r.Suites) == 0 {
		h.p("<p class=\"empty\">No suites.</p></section>\n")
		return
	}
	h.p("<div class=\"scroll\"><table><thead><tr><th scope=\"col\">Suite</th><th scope=\"col\" class=\"num\">Tests</th><th scope=\"col\" class=\"num\">Passed</th><th scope=\"col\" class=\"num\">Failed</th><th scope=\"col\" class=\"num\">Skipped</th><th scope=\"col\" class=\"num\">Time</th></tr></thead><tbody>")
	for _, s := range h.r.Suites {
		h.p("<tr><th scope=\"row\" style=\"padding-left:%dpx\">%s</th><td class=\"num\">%d</td><td class=\"num\">%d</td><td class=\"num\">%s</td><td class=\"num\">%d</td><td class=\"num\">%s</td></tr>", 10+s.Depth*18, esc(s.Name), s.Total, s.Passed, failCell(s.Failed), s.Skipped, fmtMs(s.DurationMs))
	}
	h.p("</tbody></table></div></section>\n")
}

func failCell(n int) string {
	if n > 0 {
		return fmt.Sprintf("✕ %d", n)
	}
	return "0"
}

func (h *htmlWriter) matrix() {
	m := h.r.Matrix
	if m == nil || len(m.Rows) == 0 {
		return
	}
	h.p("<section id=\"matrix\" aria-labelledby=\"h-mx\"><h2 id=\"h-mx\">Combinations</h2>\n<div class=\"scroll\"><table class=\"mx\"><caption>Each test across the combinations it ran in. ✓ passed, ✕ failed, ○ skipped, blank did not run.</caption><thead><tr><th scope=\"col\">Test</th>")
	for _, c := range m.Columns {
		h.p("<th scope=\"col\">%s</th>", esc(c))
	}
	h.p("</tr></thead><tbody>")
	for _, row := range m.Rows {
		h.p("<tr><th scope=\"row\">%s</th>", esc(row.Name))
		for _, c := range row.Cells {
			mark, word := StatusMark(c)
			if c == "" {
				h.p("<td><span aria-label=\"did not run\"></span></td>")
				continue
			}
			h.p("<td><span aria-label=\"%s\">%s</span></td>", word, mark)
		}
		h.p("</tr>")
	}
	h.p("</tbody></table></div>")
	if m.Cut {
		h.p("<p class=\"note\">The grid shows the first %d tests only.</p>", len(m.Rows))
	}
	h.p("</section>\n")
}

func (h *htmlWriter) coverage() {
	c := h.r.Coverage
	h.p("<section id=\"coverage\" aria-labelledby=\"h-cov\"><h2 id=\"h-cov\">Coverage</h2>\n")
	if c == nil {
		h.p("<p class=\"empty\">No coverage report was collected for this run.</p></section>\n")
		return
	}
	h.p("<div class=\"cards\">")
	h.card("neutral", fmtPct(c.Lines.Percent), "lines covered")
	if c.Branches.Total > 0 {
		h.card("neutral", fmtPct(c.Branches.Percent), "branches covered")
	}
	if c.Functions.Total > 0 {
		h.card("neutral", fmtPct(c.Functions.Percent), "functions covered")
	}
	if cc := c.Change; cc != nil {
		cls := "good"
		if cc.Advisory != "" {
			cls = "warn"
		}
		h.card(cls, fmtPct(cc.Percent), "of the changed lines covered")
	}
	h.p("</div>")
	if c.BaseLines != nil {
		h.p("<p>Lines covered on the default branch: %s.</p>", fmtPct(*c.BaseLines))
	}
	if cc := c.Change; cc != nil {
		h.p("<p><strong>Coverage of the change:</strong> %d of %d changed lines that hold code are covered.", cc.Covered, cc.Total)
		if cc.Advisory != "" {
			h.p(" <span class=\"pill flaky\">⚑ advice</span> %s", esc(cc.Advisory))
		}
		h.p("</p>")
		if len(cc.Files) > 0 {
			h.fileTable("Changed files", cc.Files)
		}
	}
	if len(c.Least) > 0 {
		h.fileTable(fmt.Sprintf("Least covered files (of %d)", c.Files), c.Least)
	}
	h.p("</section>\n")
}

func (h *htmlWriter) fileTable(caption string, files []FileCoverage) {
	h.p("<div class=\"scroll\"><table><caption>%s</caption><thead><tr><th scope=\"col\">File</th><th scope=\"col\" class=\"num\">Lines</th><th scope=\"col\" class=\"num\">Covered</th><th scope=\"col\">Not covered</th></tr></thead><tbody>", esc(caption))
	for _, f := range files {
		h.p("<tr><th scope=\"row\" class=\"name\">%s</th><td class=\"num\">%d/%d</td><td class=\"num\">%s</td><td>%s</td></tr>", esc(f.Path), f.Covered, f.Total, fmtPct(f.Percent), esc(f.Missed))
	}
	h.p("</tbody></table></div>")
}

func (h *htmlWriter) trend() {
	t := h.r.Trend
	h.p("<section id=\"trend\" aria-labelledby=\"h-trend\"><h2 id=\"h-trend\">Trend</h2>\n")
	if len(t) < 2 {
		h.p("<p class=\"empty\">A trend needs at least two runs with tests on this branch.</p></section>\n")
		return
	}
	h.p("%s", TrendSVG(t))
	h.p("<details class=\"tbl\"><summary>Show the trend as a table</summary><div class=\"scroll\"><table><thead><tr><th scope=\"col\">Run</th><th scope=\"col\">Commit</th><th scope=\"col\" class=\"num\">Tests</th><th scope=\"col\" class=\"num\">Pass rate</th><th scope=\"col\" class=\"num\">Duration</th><th scope=\"col\" class=\"num\">Flaky</th></tr></thead><tbody>")
	for _, p := range t {
		h.p("<tr><th scope=\"row\">#%d</th><td>%s</td><td class=\"num\">%d</td><td class=\"num\">%s</td><td class=\"num\">%s</td><td class=\"num\">%d</td></tr>", p.Number, esc(shortSHA(p.SHA)), p.Total, fmtPct(p.PassRate), fmtMs(p.DurationMs), p.Flaky)
	}
	h.p("</tbody></table></div></details></section>\n")
}

// TrendSVG draws the pass rate of the runs as a line with a mark and a
// number at every point (never color alone), 0 to 100 on the vertical.
func TrendSVG(t []TrendPoint) string {
	const w, ht, l, r, top, bot = 640.0, 220.0, 40.0, 16.0, 14.0, 28.0
	var b strings.Builder
	fmt.Fprintf(&b, "<figure><svg role=\"img\" aria-labelledby=\"tr-t tr-d\" viewBox=\"0 0 %.0f %.0f\" width=\"100%%\" style=\"max-width:%.0fpx\"><title id=\"tr-t\">Pass rate over the last %d runs</title><desc id=\"tr-d\">Pass rate from %s to %s; the table has every value.</desc>", w, ht, w, len(t), fmtPct(t[0].PassRate), fmtPct(t[len(t)-1].PassRate))
	lo := trendFloor(t)
	for _, g := range []float64{lo, (lo + 100) / 2, 100} {
		y := top + (ht-top-bot)*(1-(g-lo)/(100-lo))
		fmt.Fprintf(&b, "<line x1=\"%.0f\" x2=\"%.0f\" y1=\"%.1f\" y2=\"%.1f\" stroke=\"currentColor\" stroke-opacity=\".25\"/><text x=\"%.0f\" y=\"%.1f\" text-anchor=\"end\">%.0f%%</text>", l, w-r, y, y, l-6, y+4, g)
	}
	pts := make([]string, len(t))
	for i, p := range t {
		x := l + (w-l-r)*float64(i)/float64(len(t)-1)
		y := top + (ht-top-bot)*(1-(p.PassRate-lo)/(100-lo))
		pts[i] = fmt.Sprintf("%.1f,%.1f", x, y)
	}
	fmt.Fprintf(&b, "<polyline points=\"%s\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\"/>", strings.Join(pts, " "))
	step := 1
	if len(t) > 12 {
		step = (len(t) + 11) / 12
	}
	for i, p := range t {
		var x, y float64
		fmt.Sscanf(pts[i], "%f,%f", &x, &y)
		if p.PassRate < 100 {
			fmt.Fprintf(&b, "<path d=\"M%.1f %.1f l5 9 h-10 z\" fill=\"currentColor\" stroke=\"#fff\" stroke-width=\"1\"/>", x, y-6) // a triangle where not everything passed
		} else {
			fmt.Fprintf(&b, "<circle cx=\"%.1f\" cy=\"%.1f\" r=\"4\" fill=\"#fff\" stroke=\"currentColor\" stroke-width=\"2\"/>", x, y)
		}
		if i%step == 0 || i == len(t)-1 {
			fmt.Fprintf(&b, "<text x=\"%.1f\" y=\"%.1f\" text-anchor=\"middle\">#%d</text>", x, ht-10, p.Number)
		}
	}
	fmt.Fprintf(&b, "</svg><figcaption>Pass rate per run; the vertical axis starts at %s. A circle is a run where everything that ran passed; a triangle is a run with failures.</figcaption></figure>", fmtPct(lo))
	return b.String()
}

func (h *htmlWriter) notes() {
	if len(h.r.Notes) == 0 {
		return
	}
	h.p("<section aria-labelledby=\"h-notes\"><h2 id=\"h-notes\">What this report leaves out</h2>")
	for _, n := range h.r.Notes {
		h.p("<p class=\"note\">%s</p>", esc(n))
	}
	h.p("</section>\n")
}

func timeOf(ms int64) time.Time {
	if ms <= 0 {
		return time.Unix(0, 0).UTC()
	}
	return time.UnixMilli(ms).UTC()
}

// trendFloor is where the vertical axis of a pass-rate chart starts: a
// multiple of 5 below the lowest value, so that small movements show.
func trendFloor(t []TrendPoint) float64 {
	lo := 100.0
	for _, p := range t {
		lo = min(lo, p.PassRate)
	}
	f := float64(int(lo/5))*5 - 5
	return min(max(f, 0), 90)
}
