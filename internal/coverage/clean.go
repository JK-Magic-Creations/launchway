package coverage

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The helpers in this file are copied from internal/testreport (cleanText,
// cleanName, cutHead and friends) so the two readers clean text the same
// way without sharing unexported code.

// The XML reader hands raw control characters through as private-use code
// points U+E000+c (see prepareXML), so cleanText can still recognise an
// escape character and strip colour codes.
const ctrlBase = 0xE000

// cleanText makes s safe to store and show: invalid UTF-8 becomes U+FFFD,
// terminal escape sequences are removed, CR becomes LF, and every other
// control character except tab and newline is dropped.
func cleanText(s string) string {
	if isPlain(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if (c >= 0x20 && c < 0x7f) || c == '\t' || c == '\n' {
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			b.WriteRune(utf8.RuneError)
		case r == 0x1b || r == ctrlBase+0x1b:
			i = skipEscape(s, i+size)
			continue
		case r == '\r':
			if i+1 >= len(s) || s[i+1] != '\n' {
				b.WriteByte('\n')
			}
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r >= ctrlBase && r < ctrlBase+0x20):
			// dropped
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

func isPlain(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 0x20 && c != '\t' && c != '\n') || c >= 0x7f {
			return false
		}
	}
	return true
}

// skipEscape returns where the text resumes after an escape character whose
// introducer ended just before i.
func skipEscape(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		j := i + 1
		for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
			j++
		}
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
			return j + 1
		}
	case ']':
		for j := i + 1; j < len(s) && j < i+512; j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
	case '(', ')':
		if i+1 < len(s) {
			return i + 2
		}
	}
	return i
}

// cleanName cleans a path or name: one line, no surrounding white space, at
// most max bytes.
func cleanName(s string, max int) string {
	t := cleanText(s)
	t = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		return r
	}, t)
	return cutHead(strings.TrimSpace(t), max, false)
}

const headMark = "…"

// cutHead keeps the start of s within max bytes, marking a cut with "…".
// force adds the mark even when s fits, because it was cut earlier.
func cutHead(s string, max int, force bool) string {
	if len(s) <= max && !force {
		return s
	}
	if max <= len(headMark) {
		return s[:runeStartAtOrBefore(s, min(max, len(s)))]
	}
	keep := min(max-len(headMark), len(s))
	return s[:runeStartAtOrBefore(s, keep)] + headMark
}

func runeStartAtOrBefore(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// rawCut keeps at most n bytes of s, copied so a huge source string is not
// kept alive.
func rawCut(s string, n int) string {
	if len(s) <= n {
		return strings.Clone(s)
	}
	return strings.Clone(s[:n])
}

// snippet quotes the first few characters of b for an error message.
func snippet(b []byte) string {
	var sb strings.Builder
	for n := 0; len(b) > 0 && n < 9; n++ {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		if r == utf8.RuneError || unicode.IsControl(r) || (r >= ctrlBase && r < ctrlBase+0x20) {
			r = '?'
		}
		sb.WriteRune(r)
	}
	if len(b) > 0 {
		sb.WriteString("…")
	}
	return strconv.Quote(sb.String())
}

// parseCount reads a count leniently and never trusts it: negative, NaN
// and unreadable values are 0, a fraction above 0 rounds up (so a hit of
// 0.5 still counts as covered), and anything too big for an int64 is
// math.MaxInt64.
func parseCount(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		return max(v, 0)
	}
	if errors.Is(err, strconv.ErrRange) {
		if v < 0 {
			return 0
		}
		return math.MaxInt64
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0
	}
	if math.IsNaN(f) || f <= 0 {
		return 0
	}
	if f >= 9.2e18 {
		return math.MaxInt64
	}
	return int64(math.Ceil(f))
}

// parseLineNo reads a line number; ok is false for anything that is not a
// whole number from 0 up.
func parseLineNo(s string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// addSat adds two non-negative numbers, stopping at math.MaxInt64.
func addSat(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}

// dirOf returns the directory part of a path written with / or \.
func dirOf(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i <= 0 {
		return ""
	}
	return p[:i]
}
