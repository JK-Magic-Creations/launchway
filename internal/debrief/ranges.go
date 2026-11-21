package debrief

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Ranges is a set of line numbers.
type Ranges map[int]bool

// ParseRanges reads "1-5,9,12-20". Anything that is not a range is skipped,
// and a range wider than a million lines is ignored, so a hostile value costs
// little.
func ParseRanges(s string) Ranges {
	out := Ranges{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		a, z, isRange := strings.Cut(part, "-")
		lo, err := strconv.Atoi(a)
		if err != nil || lo < 1 {
			continue
		}
		hi := lo
		if isRange {
			if hi, err = strconv.Atoi(z); err != nil || hi < lo || hi-lo > 1_000_000 {
				continue
			}
		}
		for n := lo; n <= hi && len(out) < 2_000_000; n++ {
			out[n] = true
		}
	}
	return out
}

// String writes the set as ranges.
func (r Ranges) String() string {
	if len(r) == 0 {
		return ""
	}
	ns := make([]int, 0, len(r))
	for n := range r {
		ns = append(ns, n)
	}
	slices.Sort(ns)
	var parts []string
	start, prev := ns[0], ns[0]
	flush := func() {
		if start == prev {
			parts = append(parts, strconv.Itoa(start))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", start, prev))
		}
	}
	for _, n := range ns[1:] {
		if n == prev+1 {
			prev = n
			continue
		}
		flush()
		start, prev = n, n
	}
	flush()
	return strings.Join(parts, ", ")
}

// PatchLines reads a unified diff of one file and returns the lines of the
// new side that the change added or modified.
func PatchLines(patch string) Ranges {
	out := Ranges{}
	line := 0
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			// @@ -a,b +c,d @@
			i := strings.Index(l, "+")
			if i < 0 {
				continue
			}
			rest := l[i+1:]
			if j := strings.IndexAny(rest, ", "); j >= 0 {
				rest = rest[:j]
			}
			n, err := strconv.Atoi(rest)
			if err != nil {
				line = 0
				continue
			}
			line = n
		case line == 0, strings.HasPrefix(l, "\\"), strings.HasPrefix(l, "diff "), strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "+++ "):
		case strings.HasPrefix(l, "+"):
			out[line] = true
			line++
		case strings.HasPrefix(l, "-"):
		default:
			line++
		}
	}
	return out
}

// PathMatches says whether the path a coverage report uses and the path of a
// file in the repository are the same file: equal, or one is the end of the
// other after a slash (a module path or an absolute path on a runner).
func PathMatches(report, repo string) bool {
	report, repo = strings.TrimPrefix(report, "./"), strings.TrimPrefix(repo, "./")
	switch {
	case report == "" || repo == "":
		return false
	case report == repo:
		return true
	}
	return strings.HasSuffix(report, "/"+repo) || strings.HasSuffix(repo, "/"+report)
}

// SplitPatch splits a combined unified diff into the diff of each file by
// the path of its new side.
func SplitPatch(diff string) map[string]string {
	out := map[string]string{}
	var path string
	var cur strings.Builder
	flush := func() {
		if path != "" {
			out[path] = cur.String()
		}
		cur.Reset()
	}
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "diff --git ") {
			flush()
			// diff --git a/x b/y: the new path is after " b/"
			if i := strings.LastIndex(l, " b/"); i >= 0 {
				path = l[i+3:]
			} else {
				path = ""
			}
		}
		cur.WriteString(l)
		cur.WriteByte('\n')
	}
	flush()
	return out
}
