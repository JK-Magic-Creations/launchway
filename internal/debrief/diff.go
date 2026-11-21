package debrief

import (
	"regexp"
	"strings"
)

// Limits on what a diff is computed over, so a huge value cannot cost much.
const (
	maxDiffLines = 400
	maxDiffBytes = 64 << 10
)

// expectedActual patterns, tried in order over a failure message (and the
// start of its details). Each captures the expected value first, the actual
// second, unless swapped is true.
var pairPatterns = []struct {
	re      *regexp.Regexp
	swapped bool
}{
	// JUnit, TestNG and AssertJ style: expected:<a> but was:<b>
	{regexp.MustCompile(`(?is)expected:?\s*<(.*?)>\s*but\s+(?:was|got):?\s*<(.*?)>`), false},
	// expected [a] but found [b]
	{regexp.MustCompile(`(?is)expected\s*\[(.*?)\]\s*(?:but|and)\s+(?:found|was|got)\s*\[(.*?)\]`), false},
	// Jest and Vitest: Expected: a \n Received: b
	{regexp.MustCompile(`(?is)\bExpected(?: value)?:?\s*(.+?)\s*\n\s*Received:?\s*(.+?)(?:\n\s*\n|\z)`), false},
	// testify: expected: "a" \n actual  : "b"
	{regexp.MustCompile(`(?is)\bexpected\s*:\s*(.+?)\s*\n\s*actual\s*:\s*(.+?)(?:\n\s*(?:Diff|Test|Messages)|\n\s*\n|\z)`), false},
	// Go: got X, want Y   /   got: X \n want: Y
	{regexp.MustCompile(`(?is)\bgot:?\s*(.+?)[,\n]\s*want:?\s*(.+?)(?:\n|\z)`), true},
	// Go: want X, got Y
	{regexp.MustCompile(`(?is)\bwant:?\s*(.+?)[,\n]\s*got:?\s*(.+?)(?:\n|\z)`), false},
	// pytest and unittest: 'a' != 'b'
	{regexp.MustCompile(`(?s)AssertionError:\s*(.+?)\s+!=\s+(.+?)(?:\n|\z)`), false},
	// expected X, actual Y / expected X but got Y
	{regexp.MustCompile(`(?is)\bexpected\s+(.+?)\s*[,;]?\s*(?:but\s+)?(?:actual(?:ly)?|got|received|was)\s*:?\s+(.+?)(?:\n|\z)`), false},
}

// Detect finds an expected and an actual value in a failure message, and
// returns the diff of the two. It returns nil when it cannot tell: an
// assertion that says nothing of values is shown as its message only.
func Detect(message, details string) *Diff {
	// The message first, then the first part of the details, which for some
	// tools carries the full assertion text.
	for _, text := range []string{message, headOf(details, 4096)} {
		if text == "" {
			continue
		}
		for _, p := range pairPatterns {
			m := p.re.FindStringSubmatch(text)
			if m == nil {
				continue
			}
			exp, act := unquote(m[1]), unquote(m[2])
			if p.swapped {
				exp, act = act, exp
			}
			if exp == act || (exp == "" && act == "") {
				continue
			}
			return NewDiff(exp, act)
		}
	}
	return nil
}

func headOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// unquote removes one pair of matching quotes or backticks and turns the
// escaped newlines that tools write inside a one-line value back into
// newlines.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		f, l := s[0], s[len(s)-1]
		if (f == '"' || f == '\'' || f == '`') && f == l {
			s = s[1 : len(s)-1]
		}
	}
	if strings.Contains(s, `\n`) && !strings.Contains(s, "\n") {
		s = strings.ReplaceAll(s, `\n`, "\n")
	}
	return s
}

// NewDiff makes a Diff of two values, with the line diff filled in. Values
// are cut to a limit; a cut is marked at its end.
func NewDiff(expected, actual string) *Diff {
	expected, actual = cutBytes(expected, maxDiffBytes), cutBytes(actual, maxDiffBytes)
	return &Diff{Expected: expected, Actual: actual, Lines: LineDiff(expected, actual)}
}

func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "\n[cut]"
}

// LineDiff is the line-by-line difference of two texts by the longest
// common subsequence. A one-line pair is returned as one removed line and
// one added line. Texts beyond maxDiffLines lines are shown without
// comparing (every line of the first removed, every line of the second
// added) rather than costing a long computation.
func LineDiff(a, b string) []DiffLine {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	if len(al) > maxDiffLines || len(bl) > maxDiffLines {
		out := make([]DiffLine, 0, len(al)+len(bl))
		for _, l := range al {
			out = append(out, DiffLine{"-", l})
		}
		for _, l := range bl {
			out = append(out, DiffLine{"+", l})
		}
		return out
	}
	// lcs[i][j] is the length of the common subsequence of al[i:] and bl[j:].
	lcs := make([][]int, len(al)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(bl)+1)
	}
	for i := len(al) - 1; i >= 0; i-- {
		for j := len(bl) - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []DiffLine
	i, j := 0, 0
	for i < len(al) && j < len(bl) {
		switch {
		case al[i] == bl[j]:
			out = append(out, DiffLine{" ", al[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffLine{"-", al[i]})
			i++
		default:
			out = append(out, DiffLine{"+", bl[j]})
			j++
		}
	}
	for ; i < len(al); i++ {
		out = append(out, DiffLine{"-", al[i]})
	}
	for ; j < len(bl); j++ {
		out = append(out, DiffLine{"+", bl[j]})
	}
	return out
}

// InlineParts splits two one-line values around their common start and end,
// so that a renderer can highlight only the middle that differs.
func InlineParts(a, b string) (prefix, midA, midB, suffix string) {
	ra, rb := []rune(a), []rune(b)
	p := 0
	for p < len(ra) && p < len(rb) && ra[p] == rb[p] {
		p++
	}
	s := 0
	for s < len(ra)-p && s < len(rb)-p && ra[len(ra)-1-s] == rb[len(rb)-1-s] {
		s++
	}
	return string(ra[:p]), string(ra[p : len(ra)-s]), string(rb[p : len(rb)-s]), string(ra[len(ra)-s:])
}
