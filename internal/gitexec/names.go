package gitexec

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidName is returned for a ref name or pathspec git would not take
// as a plain name.
var ErrInvalidName = errors.New("invalid name")

// ValidRef checks a full or short ref name by the rules of git
// check-ref-format (the same rules, in Go, so the check needs no process),
// and also refuses a name that starts with "-", which git would read as an
// option. Pass names to git after "--" or as part of a refspec as well.
func ValidRef(name string) error {
	bad := func(why string) error { return fmt.Errorf("%w: %q %s", ErrInvalidName, name, why) }
	switch {
	case name == "":
		return bad("is empty")
	case name == "@":
		return bad("is a reserved name")
	case strings.HasPrefix(name, "-"):
		return bad("starts with '-'")
	case strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/"):
		return bad("starts or ends with '/'")
	case strings.HasSuffix(name, "."):
		return bad("ends with '.'")
	case strings.Contains(name, ".."):
		return bad("contains '..'")
	case strings.Contains(name, "@{"):
		return bad("contains '@{'")
	case strings.Contains(name, "//"):
		return bad("contains '//'")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return bad("contains a character git does not allow")
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return bad("has a component starting with '.'")
		}
		if strings.HasSuffix(part, ".lock") {
			return bad("has a component ending with '.lock'")
		}
	}
	return nil
}

// ValidBranch checks a branch name (without "refs/heads/").
func ValidBranch(name string) error {
	if strings.HasPrefix(name, "refs/") {
		return fmt.Errorf("%w: %q: give the branch name without refs/", ErrInvalidName, name)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("%w: %q starts with '-'", ErrInvalidName, name)
	}
	return ValidRef("refs/heads/" + name)
}

// ValidSHA reports whether s is a full object name (40 or 64 hex digits).
func ValidSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Literal makes a pathspec that matches exactly that path: no globbing and
// no magic, whatever characters it contains.
func Literal(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: empty path", ErrInvalidName)
	}
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("%w: path contains NUL", ErrInvalidName)
	}
	return ":(literal)" + path, nil
}
