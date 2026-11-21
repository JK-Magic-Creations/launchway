package gitexec

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Version is the installed git's version.
type Version struct {
	Major, Minor, Patch int
	Raw                 string
}

// String is the version as git printed it, such as "2.47.1".
func (v Version) String() string { return v.Raw }

// AtLeast reports whether the version is major.minor or newer.
func (v Version) AtLeast(major, minor int) bool {
	if v.Major != major {
		return v.Major > major
	}
	return v.Minor >= minor
}

// Features later stories rely on, with the git version that added each.
var Features = []struct {
	Name         string
	Major, Minor int
}{
	{"protocol version 2 (fetch)", 2, 26},
	{"the reference-transaction hook (the push commit point)", 2, 28},
	{"merge-tree --write-tree (landing without a working tree)", 2, 38},
}

var versionRE = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// ParseVersion reads the output of `git version`.
func ParseVersion(out string) (Version, error) {
	m := versionRE.FindStringSubmatch(out)
	if m == nil {
		return Version{}, fmt.Errorf("cannot read a version from %q", out)
	}
	maj, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	return Version{maj, minor, pat, m[0]}, nil
}

// Installed asks the git on PATH for its version.
func Installed(ctx context.Context) (Version, error) {
	res, err := Run(ctx, Spec{Args: []string{"version"}, Timeout: 20 * time.Second})
	if err != nil {
		return Version{}, err
	}
	return ParseVersion(res.Stdout)
}

// Missing lists the features of Features the version lacks.
func (v Version) Missing() []string {
	var out []string
	for _, f := range Features {
		if !v.AtLeast(f.Major, f.Minor) {
			out = append(out, fmt.Sprintf("%s needs git %d.%d", f.Name, f.Major, f.Minor))
		}
	}
	return out
}
