package debrief

import (
	"fmt"
	"strings"
)

// Annotation is a note on a line of a file, for a diff view or a check:
// a failing assertion where the test says it failed.
type Annotation struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Level   string `json:"level"` // failure
	Title   string `json:"title"`
	Message string `json:"message"`
}

// Annotations lists a place for each failure that names a file and a line.
func Annotations(r *Report) []Annotation {
	out := []Annotation{}
	for _, f := range r.Failures {
		if f.File == "" || f.Line <= 0 {
			continue
		}
		msg := f.Message
		if len(msg) > 600 {
			msg = msg[:600] + " [cut]"
		}
		out = append(out, Annotation{Path: f.File, Line: f.Line, Level: "failure", Title: f.Name + " failed", Message: msg})
		if len(out) >= 50 {
			break
		}
	}
	return out
}

// Markdown is the short summary of a Debrief for a pull request comment: one
// block that a later run replaces, never a new one beside it.
func Markdown(r *Report) string {
	var b strings.Builder
	s := r.Summary
	fmt.Fprintf(&b, "### Tests: %s\n\n", Headline(r))
	fmt.Fprintf(&b, "| Passed | Failed | Skipped | Flaky | Quarantined | Duration |\n|---:|---:|---:|---:|---:|---:|\n| %d | %d | %d | %d | %d | %s |\n\n",
		s.Passed, s.Failed+s.Errored, s.Skipped, s.Flaky, s.Quarantined, fmtMs(s.DurationMs))
	if c := r.Change; c != nil {
		fmt.Fprintf(&b, "Against %s run #%d: %d newly failing, %d newly passing.\n\n", branchName(c.Base.Branch), c.Base.Number, c.NewlyFailing, c.NewlyPassing)
	}
	var fresh, other []Failure
	for _, f := range r.Failures {
		if f.NewHere {
			fresh = append(fresh, f)
		} else {
			other = append(other, f)
		}
	}
	list := func(title string, fs []Failure) {
		if len(fs) == 0 {
			return
		}
		fmt.Fprintf(&b, "**%s** (%d)\n\n", title, len(fs))
		for i, f := range fs {
			if i == 10 {
				fmt.Fprintf(&b, "- and %d more in the Debrief\n", len(fs)-10)
				break
			}
			name := f.Name
			if f.Suite != "" {
				name = f.Suite + " › " + f.Name
			}
			line := fmt.Sprintf("- `%s`", strings.ReplaceAll(name, "`", "'"))
			if m := oneLine(f.Message, 140); m != "" {
				line += ": " + m
			}
			if f.Link != "" {
				line += fmt.Sprintf(" ([details](%s))", f.Link)
			}
			switch f.FailingOnBase {
			case "yes":
				line += " (also failing on the default branch)"
			case "no":
				line += " (passes on the default branch)"
			}
			if f.FlakyScore > 0 {
				line += " (flaky history)"
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}
	list("New failures", fresh)
	list("Failing", other)
	if len(r.Flaky) > 0 {
		fmt.Fprintf(&b, "**Flaky** (%d): ", len(r.Flaky))
		for i, f := range r.Flaky {
			if i == 5 {
				fmt.Fprintf(&b, " and %d more", len(r.Flaky)-5)
				break
			}
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "`%s` (%s)", strings.ReplaceAll(f.Name, "`", "'"), f.ThisRun)
		}
		b.WriteString("\n\n")
	}
	if s.Quarantined > 0 {
		fmt.Fprintf(&b, "**Quarantined**: %d failing tests are excused by the repository's quarantine file.\n\n", s.Quarantined)
	}
	if c := r.Coverage; c != nil {
		fmt.Fprintf(&b, "**Coverage**: %s of lines", fmtPct(c.Lines.Percent))
		if cc := c.Change; cc != nil {
			fmt.Fprintf(&b, "; **coverage of the change %s** (%d of %d changed lines)", fmtPct(cc.Percent), cc.Covered, cc.Total)
			if cc.Advisory != "" {
				b.WriteString(". Advice: " + cc.Advisory)
			}
		}
		b.WriteString("\n\n")
	}
	if r.Run.Link != "" {
		fmt.Fprintf(&b, "[Open the Debrief](%s)\n", r.Run.Link)
	}
	return b.String()
}

func oneLine(s string, n int) string {
	s, _, _ = strings.Cut(s, "\n")
	s = strings.TrimSpace(s)
	if len(s) > n {
		r := []rune(s)
		if len(r) > n {
			s = string(r[:n]) + "..."
		}
	}
	return strings.ReplaceAll(s, "|", "\\|")
}
