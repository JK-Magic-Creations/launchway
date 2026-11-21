package release

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Schemes name how a project's versions are written and ordered.
const (
	// SchemeSemver is Semantic Versioning 2.0.0: MAJOR.MINOR.PATCH, with an
	// optional pre-release (-rc.1) and build metadata (+build.5).
	SchemeSemver = "semver"
	// SchemeCalver is a calendar version: dot-separated numbers that start
	// with the year (2026.11.3, 2026.11.21): ordered number by number.
	SchemeCalver = "calver"
	// SchemeNatural is any text; digit runs are compared as numbers (r9 is
	// before r10).
	SchemeNatural = "natural"
	// SchemeCreated is any text; the order is the order the releases were made.
	SchemeCreated = "created"
)

// Schemes lists them, for validation and for the interfaces.
var Schemes = []string{SchemeSemver, SchemeCalver, SchemeNatural, SchemeCreated}

// ValidScheme reports whether s is one of Schemes.
func ValidScheme(s string) bool {
	for _, x := range Schemes {
		if x == s {
			return true
		}
	}
	return false
}

// ErrVersion is wrapped by every refusal of a version.
var ErrVersion = errors.New("invalid version")

// MaxVersionLen is the longest version text.
const MaxVersionLen = 100

var (
	semverRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
	calverRe = regexp.MustCompile(`^[0-9]{4}(\.[0-9]{1,6}){1,3}(-[0-9A-Za-z.-]+)?$`)
	plainRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
)

// Semver is a parsed Semantic Version.
type Semver struct {
	Major, Minor, Patch uint64
	Pre                 []string // pre-release identifiers
	Build               string   // build metadata, never part of the order
}

// ParseSemver reads a version; a leading "v" is not part of the version and is refused here (the tag prefix is the project's setting).
func ParseSemver(s string) (Semver, error) {
	var v Semver
	m := semverRe.FindStringSubmatch(s)
	if m == nil {
		return v, fmt.Errorf("%w: %q is not a semantic version (MAJOR.MINOR.PATCH, such as 1.4.0 or 2.0.0-rc.1)", ErrVersion, s)
	}
	var err error
	for i, dst := range []*uint64{&v.Major, &v.Minor, &v.Patch} {
		if *dst, err = strconv.ParseUint(m[i+1], 10, 63); err != nil {
			return v, fmt.Errorf("%w: %q has a number that is too large", ErrVersion, s)
		}
	}
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
	}
	v.Build = m[5]
	return v, nil
}

// String writes the version in its canonical form.
func (v Semver) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

// IsPre reports whether it is a pre-release.
func (v Semver) IsPre() bool { return len(v.Pre) > 0 }

func cmpU(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func isNum(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// Compare orders by Semantic Versioning precedence: build metadata is
// ignored, and a pre-release comes before its release.
func (v Semver) Compare(o Semver) int {
	if c := cmpU(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpU(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpU(v.Patch, o.Patch); c != 0 {
		return c
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
		an, bn := isNum(a), isNum(b)
		switch {
		case an && bn:
			x, _ := strconv.ParseUint(a, 10, 63)
			y, _ := strconv.ParseUint(b, 10, 63)
			if c := cmpU(x, y); c != 0 {
				return c
			}
		case an:
			return -1
		case bn:
			return 1
		default:
			if c := strings.Compare(a, b); c != 0 {
				return c
			}
		}
	}
	return cmpU(uint64(len(v.Pre)), uint64(len(o.Pre)))
}

// CheckVersion validates a version for a scheme and returns it as it is kept.
func CheckVersion(scheme, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > MaxVersionLen {
		return "", fmt.Errorf("%w: a version is 1 to %d characters", ErrVersion, MaxVersionLen)
	}
	switch scheme {
	case SchemeSemver, "":
		sv, err := ParseSemver(v)
		if err != nil {
			return "", err
		}
		return sv.String(), nil
	case SchemeCalver:
		if !calverRe.MatchString(v) {
			return "", fmt.Errorf("%w: %q is not a calendar version (the year first, then numbers: 2026.11.3)", ErrVersion, v)
		}
	case SchemeNatural, SchemeCreated:
		if !plainRe.MatchString(v) {
			return "", fmt.Errorf("%w: %q: use letters, digits and . _ + - , starting with a letter or digit", ErrVersion, v)
		}
	default:
		return "", fmt.Errorf("%w: unknown scheme %q", ErrVersion, scheme)
	}
	return v, nil
}

// Compare orders two versions of a scheme: negative if a is older. The
// "created" scheme has no order of its own: callers order by time, and Compare
// reports 0.
func Compare(scheme, a, b string) int {
	switch scheme {
	case SchemeSemver, "":
		x, e1 := ParseSemver(a)
		y, e2 := ParseSemver(b)
		if e1 != nil || e2 != nil {
			return strings.Compare(a, b)
		}
		return x.Compare(y)
	case SchemeCalver, SchemeNatural:
		return natural(a, b)
	}
	return 0
}

// natural compares digit runs as numbers and everything else as text.
func natural(a, b string) int {
	for a != "" && b != "" {
		ad, bd := digits(a), digits(b)
		if ad != "" && bd != "" {
			x, y := strings.TrimLeft(ad, "0"), strings.TrimLeft(bd, "0")
			if len(x) != len(y) {
				return cmpU(uint64(len(x)), uint64(len(y)))
			}
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
			a, b = a[len(ad):], b[len(bd):]
			continue
		}
		if a[0] != b[0] {
			return cmpU(uint64(a[0]), uint64(b[0]))
		}
		a, b = a[1:], b[1:]
	}
	return cmpU(uint64(len(a)), uint64(len(b)))
}

func digits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// IsPrerelease reports whether a version of a scheme is a pre-release (only
// semantic versions have them).
func IsPrerelease(scheme, v string) bool {
	if scheme != SchemeSemver && scheme != "" {
		return false
	}
	sv, err := ParseSemver(v)
	return err == nil && sv.IsPre()
}

// Sort orders versions oldest first.
func Sort(scheme string, vs []string) {
	sort.SliceStable(vs, func(i, j int) bool { return Compare(scheme, vs[i], vs[j]) < 0 })
}

// TagName is the tag a version is released under: the project's prefix (v)
// and the version without build metadata, which Git tags may carry but which
// would make two tags for one version.
func TagName(prefix, version string) string { return prefix + version }

// ValidPrefix checks a tag prefix.
func ValidPrefix(p string) bool {
	return len(p) <= 20 && (p == "" || regexp.MustCompile(`^[A-Za-z0-9._/-]+$`).MatchString(p)) && !strings.Contains(p, "..")
}
