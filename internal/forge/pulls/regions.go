package pulls

import (
	"fmt"
	"strings"
)

// Region is one piece of a conflicted text file: text both sides agree on, or a
// place where they disagree.
type Region struct {
	Index int `json:"index"`
	// Kind is "common" or "conflict".
	Kind string `json:"kind"`
	// Text is the agreed text of a common region.
	Text string `json:"text,omitempty"`
	// Ours is what the pull request's branch has there, Theirs what the target
	// branch has, Base what both started from (the last common ancestor).
	Ours   string `json:"ours,omitempty"`
	Base   string `json:"base,omitempty"`
	Theirs string `json:"theirs,omitempty"`
}

const (
	markOurs   = "<<<<<<< "
	markBase   = "||||||| "
	markSep    = "======="
	markTheirs = ">>>>>>> "
)

// parseRegions reads the output of git merge-file --diff3 into regions. It
// returns false when the text is not that (the files held marker-like lines of
// their own and the pieces do not fit), and then no region choices are offered:
// the person gives the whole text.
func parseRegions(merged string) ([]Region, bool) {
	var out []Region
	var common strings.Builder
	flush := func() {
		if common.Len() > 0 {
			out = append(out, Region{Index: len(out), Kind: "common", Text: common.String()})
			common.Reset()
		}
	}
	lines := strings.SplitAfter(merged, "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if !strings.HasPrefix(l, markOurs) {
			if strings.HasPrefix(l, markBase) || strings.HasPrefix(l, markTheirs) || strings.TrimRight(l, "\r\n") == markSep {
				return nil, false
			}
			common.WriteString(l)
			continue
		}
		flush()
		var ours, base, theirs strings.Builder
		part := &ours
		stage := 0
		closed := false
		for i++; i < len(lines); i++ {
			m := lines[i]
			switch {
			case stage == 0 && strings.HasPrefix(m, markBase):
				stage, part = 1, &base
			case stage <= 1 && strings.TrimRight(m, "\r\n") == markSep:
				stage, part = 2, &theirs
			case stage == 2 && strings.HasPrefix(m, markTheirs):
				closed = true
			case strings.HasPrefix(m, markOurs):
				return nil, false
			default:
				part.WriteString(m)
			}
			if closed {
				break
			}
		}
		if !closed {
			return nil, false
		}
		out = append(out, Region{Index: len(out), Kind: "conflict", Ours: ours.String(), Base: base.String(), Theirs: theirs.String()})
	}
	flush()
	return out, true
}

// RegionChoice is a person's answer for one conflicting region.
type RegionChoice struct {
	Index int `json:"index"`
	// Take is "ours", "theirs", "both" (ours, then theirs) or "custom" (Text).
	Take string `json:"take"`
	Text string `json:"text,omitempty"`
}

// applyRegions builds the file from regions and the choices for its conflicting
// ones; a conflicting region with no choice is an error.
func applyRegions(regions []Region, choices []RegionChoice) (string, error) {
	by := map[int]RegionChoice{}
	for _, c := range choices {
		if _, dup := by[c.Index]; dup {
			return "", invalid("region %d is answered twice", c.Index)
		}
		by[c.Index] = c
	}
	var b strings.Builder
	var missing []string
	for _, r := range regions {
		if r.Kind == "common" {
			if _, ok := by[r.Index]; ok {
				return "", invalid("region %d is text both sides agree on; it takes no answer", r.Index)
			}
			b.WriteString(r.Text)
			continue
		}
		c, ok := by[r.Index]
		if !ok {
			missing = append(missing, fmt.Sprint(r.Index))
			continue
		}
		switch c.Take {
		case "ours":
			b.WriteString(r.Ours)
		case "theirs":
			b.WriteString(r.Theirs)
		case "both":
			b.WriteString(r.Ours)
			b.WriteString(r.Theirs)
		case "custom":
			b.WriteString(c.Text)
		default:
			return "", invalid("region %d: take ours, theirs, both or custom, not %q", r.Index, c.Take)
		}
	}
	if len(missing) > 0 {
		return "", invalid("these conflicting regions have no answer: %s", strings.Join(missing, ", "))
	}
	for idx := range by {
		if idx < 0 || idx >= len(regions) {
			return "", invalid("there is no region %d", idx)
		}
	}
	return b.String(), nil
}

// hasMarkers reports whether text holds the shape of a conflict left in by
// mistake: a line opening one (seven "<") and a later line closing it (seven
// ">"). A line of equal signs alone is no marker (Markdown headings use them).
func hasMarkers(text string) bool {
	open := false
	for _, l := range strings.SplitAfter(text, "\n") {
		t := strings.TrimRight(l, "\r\n")
		switch {
		case t == "<<<<<<<" || strings.HasPrefix(l, markOurs):
			open = true
		case open && (t == ">>>>>>>" || strings.HasPrefix(l, markTheirs)):
			return true
		}
	}
	return false
}
