package version

import (
	"strconv"
	"strings"
)

// PEP440 is a parsed Python version.
type PEP440 struct {
	Epoch   uint64
	Release []uint64
	PreKind string // "a", "b", "rc" or ""
	PreNum  uint64
	Post    int64 // -1: none
	Dev     int64 // -1: none
	Local   string
	Raw     string
}

// ParsePEP440 reads a PEP 440 version ("1.0", "2.1.0rc1", "1!2.0.post1").
// Common spellings (alpha, beta, c, pre, preview, -1 post) are normalized.
func ParsePEP440(s string) (PEP440, bool) {
	v := PEP440{Post: -1, Dev: -1, Raw: s}
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		v.Local, s = s[i+1:], s[:i]
	}
	if i := strings.IndexByte(s, '!'); i >= 0 {
		e, err := strconv.ParseUint(s[:i], 10, 64)
		if err != nil {
			return v, false
		}
		v.Epoch, s = e, s[i+1:]
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	rel := strings.TrimSuffix(s[:i], ".")
	rest := s[i:]
	if i > 0 && s[i-1] == '.' {
		rest = "." + rest
	}
	if rel == "" {
		return v, false
	}
	for _, p := range strings.Split(rel, ".") {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return v, false
		}
		v.Release = append(v.Release, n)
	}
	rest = strings.TrimLeft(rest, ".-_")
	for rest != "" {
		word := ""
		j := 0
		for j < len(rest) && rest[j] >= 'a' && rest[j] <= 'z' {
			j++
		}
		word, rest = rest[:j], rest[j:]
		k := 0
		for k < len(rest) && rest[k] >= '0' && rest[k] <= '9' {
			k++
		}
		num := int64(0)
		if k > 0 {
			num, _ = strconv.ParseInt(rest[:k], 10, 64)
		}
		rest = strings.TrimLeft(rest[k:], ".-_")
		switch word {
		case "a", "alpha":
			v.PreKind, v.PreNum = "a", uint64(num)
		case "b", "beta":
			v.PreKind, v.PreNum = "b", uint64(num)
		case "rc", "c", "pre", "preview":
			v.PreKind, v.PreNum = "rc", uint64(num)
		case "post", "rev", "r":
			v.Post = num
		case "dev":
			v.Dev = num
		case "":
			if k > 0 { // "1.0-1" is post 1
				v.Post = num
			} else {
				return v, false
			}
		default:
			return v, false
		}
	}
	return v, true
}

// IsPre reports a pre-release or a development release.
func (v PEP440) IsPre() bool { return v.PreKind != "" || v.Dev >= 0 }

func trimmed(r []uint64) []uint64 {
	for len(r) > 1 && r[len(r)-1] == 0 {
		r = r[:len(r)-1]
	}
	return r
}

// Compare orders two versions: -1, 0, 1.
func (v PEP440) Compare(o PEP440) int {
	if v.Epoch != o.Epoch {
		return cmpU(v.Epoch, o.Epoch)
	}
	a, b := trimmed(v.Release), trimmed(o.Release)
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y uint64
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return cmpU(x, y)
		}
	}
	if c := cmpI(v.preRank(), o.preRank()); c != 0 {
		return c
	}
	if v.PreKind != "" && v.PreKind == o.PreKind && v.PreNum != o.PreNum {
		return cmpU(v.PreNum, o.PreNum)
	}
	if c := cmpI(v.Post, o.Post); c != 0 {
		return c
	}
	if v.Dev != o.Dev {
		if v.Dev < 0 {
			return 1
		}
		if o.Dev < 0 {
			return -1
		}
		return cmpI(v.Dev, o.Dev)
	}
	return 0
}

// preRank puts dev-only before a, b, rc, and final releases last.
func (v PEP440) preRank() int64 {
	switch v.PreKind {
	case "a":
		return 1
	case "b":
		return 2
	case "rc":
		return 3
	}
	if v.Dev >= 0 {
		return 0
	}
	return 4
}

func cmpU(a, b uint64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func cmpI(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// KindOfPEP440 names the change from one release to another by its first
// differing release segment.
func KindOfPEP440(from, to PEP440) Kind {
	if to.Compare(from) <= 0 {
		return KindNone
	}
	at := func(v PEP440, i int) uint64 {
		if i < len(v.Release) {
			return v.Release[i]
		}
		return 0
	}
	switch {
	case to.Epoch != from.Epoch || at(to, 0) != at(from, 0):
		return KindMajor
	case at(to, 1) != at(from, 1):
		return KindMinor
	}
	return KindPatch
}
