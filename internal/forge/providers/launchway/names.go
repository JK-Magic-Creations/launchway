package launchway

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// A repository's directory in the storage is its ID; a name is only ever a
// database value and a URL segment. These rules keep it a plain segment.

var segmentRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// reserved are owner names that would collide with Launchway's own paths, and
// names git or the file system treat specially. Compared in lower case.
var reserved = map[string]bool{
	"api": true, "git": true, "auth": true, "hosted": true, "healthz": true, "readyz": true, "mcp": true, "metrics": true,
	"assets": true, "static": true, "user": true, "admin": true, "login": true, "logout": true, "settings": true,
	"repos": true, "repositories": true, "projects": true, "runs": true, "explore": true, "org": true, "orgs": true,
	"components": true, "runners": true, "images": true, "tokens": true, "policy": true, "environments": true, "agents": true,
	"approvals": true, "deployments": true, "audit": true, "libraries": true, "waivers": true, "findings": true,
	"con": true, "prn": true, "aux": true, "nul": true, "head": true, "refs": true, "objects": true,
}

// ReservedOwner reports whether an owner name is one Launchway keeps for its
// own paths (a repository under it would be unreachable at the address other
// Git hosts use).
func ReservedOwner(owner string) bool { return reserved[strings.ToLower(owner)] }

// MaxNameLength is the longest owner or repository name.
const MaxNameLength = 100

// ValidSegment checks one name (an owner or a repository) and says what is
// wrong with it, in words.
func ValidSegment(s string) error {
	switch {
	case s == "":
		return fmt.Errorf("%w: a name cannot be empty", forge.ErrInvalid)
	case len(s) > MaxNameLength:
		return fmt.Errorf("%w: a name is at most %d characters", forge.ErrInvalid, MaxNameLength)
	case strings.Contains(s, ".."):
		return fmt.Errorf("%w: a name cannot contain ..", forge.ErrInvalid)
	case strings.HasSuffix(s, ".") || strings.HasSuffix(strings.ToLower(s), ".git") || strings.HasSuffix(strings.ToLower(s), ".lock"):
		return fmt.Errorf("%w: a name cannot end in a dot, .git or .lock", forge.ErrInvalid)
	}
	for _, r := range s {
		if r > unicode.MaxASCII || unicode.IsControl(r) {
			return fmt.Errorf("%w: a name uses only the letters A-Z, digits, '.', '_' and '-'", forge.ErrInvalid)
		}
	}
	if !segmentRE.MatchString(s) {
		return fmt.Errorf("%w: a name starts with a letter or digit and uses only letters, digits, '.', '_' and '-'", forge.ErrInvalid)
	}
	if reserved[strings.ToLower(s)] {
		return fmt.Errorf("%w: %q is reserved", forge.ErrInvalid, s)
	}
	return nil
}

// SplitPath splits "owner/name" and validates both parts.
func SplitPath(path string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(path, "/")
	if !ok || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("%w: a repository path is <owner>/<name>", forge.ErrInvalid)
	}
	if err := ValidSegment(owner); err != nil {
		return "", "", err
	}
	if err := ValidSegment(name); err != nil {
		return "", "", err
	}
	return owner, name, nil
}
