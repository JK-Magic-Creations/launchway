package version

import (
	"strconv"
	"strings"
)

// Semver is a parsed semantic version. Go's leading "v" is accepted.
type Semver struct {
	Major, Minor, Patch uint64
	Pre                 []string // dot separated pre-release identifiers
	Build               string
	Raw                 string
}

// ParseSemver reads "1.2.3", "v1.2.3-rc.1+meta". Missing minor or patch are
// zero ("1.2" is 1.2.0) so npm and Go short forms compare.
func ParseSemver(s string) (Semver, bool) {
	v := Semver{Raw: s}
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		v.Build, s = s[i+1:], s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.Pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 3 || s == "" {
		return v, false
	}
	nums := [3]uint64{}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return v, false
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, true
}

// IsPre reports a pre-release. A Go pseudo-version counts as one.
func (v Semver) IsPre() bool { return len(v.Pre) > 0 }

// IsPseudo reports a Go pseudo-version (vX.Y.Z-yyyymmddhhmmss-abcdef123456).
func (v Semver) IsPseudo() bool {
	if len(v.Pre) == 0 {
		return false
	}
	last := v.Pre[len(v.Pre)-1]
	return len(last) == 27 && last[14] == '-' && isDigits(last[:14]) && isHex(last[15:])
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// String is the version without a leading v.
func (v Semver) String() string {
	s := strconv.FormatUint(v.Major, 10) + "." + strconv.FormatUint(v.Minor, 10) + "." + strconv.FormatUint(v.Patch, 10)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// Compare orders two versions: -1, 0, 1. Build metadata is ignored.
func (v Semver) Compare(o Semver) int {
	for _, p := range [][2]uint64{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		a, b := v.Pre[i], o.Pre[i]
		an, aerr := strconv.ParseUint(a, 10, 64)
		bn, berr := strconv.ParseUint(b, 10, 64)
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		case a != b:
			if a < b {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(v.Pre) < len(o.Pre):
		return -1
	case len(v.Pre) > len(o.Pre):
		return 1
	}
	return 0
}

// Kind of an update.
type Kind string

// The kinds of update, from least to most risky.
const (
	KindNone  Kind = "none"
	KindPatch Kind = "patch"
	KindMinor Kind = "minor"
	KindMajor Kind = "major"
	KindOther Kind = "other" // not comparable by number: a digest refresh, a different tag family
)

// KindOf says what kind of change going from a to b is. Before 1.0 a minor
// step is treated as major would be for compatibility, but still named
// minor: policy decides what to do with it.
func KindOf(from, to Semver) Kind {
	switch {
	case to.Compare(from) <= 0:
		return KindNone
	case to.Major != from.Major:
		return KindMajor
	case to.Minor != from.Minor:
		return KindMinor
	}
	return KindPatch
}
