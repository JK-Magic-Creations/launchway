package testreport

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The XML reader hands raw control characters through as private-use code
// points U+E000+c (see prepareXML), so cleanText can still recognise an
// escape character and strip colour codes.
const ctrlBase = 0xE000

// cleanText makes s safe to store and show: invalid UTF-8 becomes U+FFFD,
// terminal escape sequences (colours and the like) are removed, CR LF becomes
// LF, a lone CR becomes LF, and every other control character except tab and
// newline is dropped.
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

// cleanName cleans a suite, test or file name: one line, no surrounding
// white space, at most max bytes.
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

// cleanMessage cleans a short message. cut says the raw text was already
// shortened while reading.
func cleanMessage(s string, max int, cut bool) string {
	return cutHead(strings.TrimSpace(cleanText(s)), max, cut)
}

// cleanBlock cleans a multi-line body, keeping the start.
func cleanBlock(s string, max int, cut bool) string {
	t := trimBlock(cleanText(s))
	if t == "" {
		return ""
	}
	return cutHead(t, max, cut)
}

// cleanTail cleans captured output, keeping the end.
func cleanTail(s string, max int, cut bool) string {
	t := trimBlock(cleanText(s))
	if t == "" {
		return ""
	}
	return cutTail(t, max, cut)
}

// trimBlock drops blank lines at the start and white space at the end.
func trimBlock(s string) string {
	s = strings.TrimRight(s, " \t\n")
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 || strings.TrimSpace(s[:i]) != "" {
			break
		}
		s = s[i+1:]
	}
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return s
}

const (
	headMark = "…"
	tailMark = "…\n"
)

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

// cutTail keeps the end of s within max bytes, marking a cut with "…\n" at
// the start.
func cutTail(s string, max int, force bool) string {
	if len(s) <= max && !force {
		return s
	}
	if max <= len(tailMark) {
		return s[runeStartAtOrAfter(s, len(s)-min(max, len(s))):]
	}
	keep := min(max-len(tailMark), len(s))
	return tailMark + s[runeStartAtOrAfter(s, len(s)-keep):]
}

func runeStartAtOrBefore(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

func runeStartAtOrAfter(s string, i int) int {
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// firstLine returns the first line of s that is not blank, trimmed.
func firstLine(s string) string {
	for s != "" {
		line := s
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			line, s = s[:i], s[i+1:]
		} else {
			s = ""
		}
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// rawCut keeps at most n bytes of s, copied so a huge source string is not
// kept alive, and says whether anything was cut.
func rawCut(s string, n int) (string, bool) {
	if len(s) <= n {
		return strings.Clone(s), false
	}
	return strings.Clone(s[:n]), true
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

// headBuf keeps the first max bytes written to it.
type headBuf struct {
	b    []byte
	max  int
	over bool
}

func (h *headBuf) write(p []byte) {
	room := h.max - len(h.b)
	if len(p) > room {
		if room <= 0 {
			if len(p) > 0 {
				h.over = true
			}
			return
		}
		p = p[:room]
		h.over = true
	}
	h.b = append(h.b, p...)
}

func (h *headBuf) writeString(s string) {
	room := h.max - len(h.b)
	if len(s) > room {
		if room <= 0 {
			if len(s) > 0 {
				h.over = true
			}
			return
		}
		s = s[:room]
		h.over = true
	}
	h.b = append(h.b, s...)
}

// tailBuf keeps (about) the last max bytes written to it, using at most
// twice that much memory.
type tailBuf struct {
	b       []byte
	max     int
	dropped bool
}

func (t *tailBuf) write(p []byte) {
	if len(p) >= t.max {
		if len(t.b) > 0 || len(p) > t.max {
			t.dropped = true
		}
		t.b = append(t.b[:0], p[len(p)-t.max:]...)
		return
	}
	t.b = append(t.b, p...)
	t.compact()
}

func (t *tailBuf) writeString(s string) {
	if len(s) >= t.max {
		if len(t.b) > 0 || len(s) > t.max {
			t.dropped = true
		}
		t.b = append(t.b[:0], s[len(s)-t.max:]...)
		return
	}
	t.b = append(t.b, s...)
	t.compact()
}

func (t *tailBuf) compact() {
	if len(t.b) > 2*t.max {
		n := copy(t.b, t.b[len(t.b)-t.max:])
		t.b = t.b[:n]
		t.dropped = true
	}
}

// text returns what the buffer holds and whether anything was dropped.
func (t *tailBuf) text() (string, bool) {
	if t == nil {
		return "", false
	}
	b, dropped := t.b, t.dropped
	if len(b) > t.max {
		b = b[len(b)-t.max:]
		dropped = true
	}
	if dropped {
		for len(b) > 0 && !utf8.RuneStart(b[0]) {
			b = b[1:]
		}
	}
	return string(b), dropped
}

// joinOutput puts two captured streams together, one after the other.
func joinOutput(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if !strings.HasSuffix(a, "\n") {
		a += "\n"
	}
	return a + b
}

// parseSeconds reads a duration in seconds leniently: "0.001", "1,234.5"
// and " 2s" all work; anything unreadable is 0.
func parseSeconds(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "s")
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) && f > 0 {
			return 1e7 // too big for a float: as long as allowed
		}
		return 0
	}
	return f
}
