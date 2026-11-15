package jsonscan

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CleanText makes s safe to store and show: invalid UTF-8 becomes U+FFFD,
// terminal escape sequences (colours and the like) are removed, CR LF and a
// lone CR become LF, a tab becomes a space, and every other control
// character is dropped, as are the Unicode controls that reorder text on
// screen (bidirectional overrides and isolates). Newlines are kept.
func CleanText(s string) string {
	if isPlain(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x20 && c < 0x7f || c == '\n' {
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			b.WriteRune(utf8.RuneError)
		case r == 0x1b:
			i = skipEscape(s, i+size)
			continue
		case r == '\r':
			if i+1 >= len(s) || s[i+1] != '\n' {
				b.WriteByte('\n')
			}
		case r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || isBidiControl(r):
			// dropped
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

func isBidiControl(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200E || r == 0x200F || r == 0x061C
}

func isPlain(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 0x20 && c != '\n') || c >= 0x7f {
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
	case '[': // CSI: ESC [ params intermediates final
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
	case ']': // OSC: ends with BEL or ESC \
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

// Block cleans multi-line text (a message, a help text): blank lines at the
// start and white space at both ends are removed, newlines inside are kept,
// and the result is cut to max bytes.
func Block(s string, max int) string {
	t := CleanText(s)
	t = strings.TrimRight(t, " \n")
	for {
		i := strings.IndexByte(t, '\n')
		if i < 0 || strings.TrimSpace(t[:i]) != "" {
			break
		}
		t = t[i+1:]
	}
	return Cut(strings.TrimSpace(t), max)
}

// Line cleans a name, a path or a label: one line, no surrounding white
// space, at most max bytes.
func Line(s string, max int) string {
	t := CleanText(s)
	if strings.IndexByte(t, '\n') >= 0 {
		t = strings.ReplaceAll(t, "\n", " ")
	}
	return Cut(strings.TrimSpace(t), max)
}

// CutMark ends text that was shortened.
const CutMark = "…"

// Cut keeps the start of s within max bytes, ending a cut with "…", and
// never splits a character.
func Cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= len(CutMark) {
		return s[:RuneStartAtOrBefore(s, max(n, 0))]
	}
	return s[:RuneStartAtOrBefore(s, n-len(CutMark))] + CutMark
}

// RuneStartAtOrBefore moves i back to the start of the character it falls in.
func RuneStartAtOrBefore(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// Snippet quotes the first few characters of b for an error message.
func Snippet(b []byte) string {
	var sb strings.Builder
	for n := 0; len(b) > 0 && n < 9; n++ {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		if r == utf8.RuneError || unicode.IsControl(r) || isBidiControl(r) {
			r = '?'
		}
		sb.WriteRune(r)
	}
	if len(b) > 0 {
		sb.WriteString("…")
	}
	return strconv.Quote(sb.String())
}
