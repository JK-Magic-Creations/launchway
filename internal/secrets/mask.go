package secrets

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"
)

// MinMaskLen is the length, in characters, of the shortest value that is
// masked. Masking shorter ones would turn ordinary output into stars without
// hiding much.
const MinMaskLen = 4

// Masker replaces secret values in text with ***. A nil *Masker masks
// nothing.
type Masker struct {
	r      *strings.Replacer
	pieces []string
}

// NewMasker returns a masker for the given values, keyed by variable name.
// Output arrives a line at a time, so a value of several lines is masked
// line by line. It also returns, sorted, the names of variables with a line
// too short to mask.
func NewMasker(values map[string]string) (*Masker, []string) {
	var pieces, short []string
	for name, v := range values {
		tooShort := false
		for _, line := range strings.Split(v, "\n") {
			line = strings.TrimSuffix(line, "\r")
			switch {
			case line == "":
			case utf8.RuneCountInString(line) < MinMaskLen:
				tooShort = true
			default:
				pieces = append(pieces, line)
			}
		}
		if tooShort {
			short = append(short, name)
		}
	}
	slices.Sort(short)
	if len(pieces) == 0 {
		return nil, short
	}
	// Longest first: where values overlap, the longer one is hidden whole.
	slices.SortFunc(pieces, func(a, b string) int { return cmp.Or(len(b)-len(a), strings.Compare(a, b)) })
	pieces = slices.Compact(pieces)
	pairs := make([]string, 0, 2*len(pieces))
	for _, p := range pieces {
		pairs = append(pairs, p, "***")
	}
	return &Masker{r: strings.NewReplacer(pairs...), pieces: pieces}, short
}

// Mask returns s with every secret value replaced by ***.
func (m *Masker) Mask(s string) string {
	if m == nil {
		return s
	}
	return m.r.Replace(s)
}

// Hold says how many bytes at the end of s could be the start of a secret
// value: text that stops there, without a newline, must wait for more before
// it is shown, or the secret would be cut in two and escape masking.
func (m *Masker) Hold(s string) int {
	if m == nil {
		return 0
	}
	most := 0
	for _, p := range m.pieces {
		for k := min(len(p)-1, len(s)); k > most; k-- {
			if strings.HasSuffix(s, p[:k]) {
				most = k
				break
			}
		}
	}
	return most
}
