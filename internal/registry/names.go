package registry

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Limits of names.
const (
	maxNameLen   = 255
	maxNameDepth = 8
	maxTagLen    = 128
)

var (
	nameComponent = `[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*`
	nameRe        = regexp.MustCompile(`^` + nameComponent + `(?:/` + nameComponent + `)*$`)
	tagRe         = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	digestRe      = regexp.MustCompile(`^(sha256:[a-f0-9]{64}|sha512:[a-f0-9]{128})$`)
	uploadIDRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
)

// ValidName checks a repository name: lower-case path components of
// letters, digits and the separators . _ __ and -; the first component is
// the project the repository belongs to.
func ValidName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("the repository name is empty")
	case len(name) > maxNameLen:
		return fmt.Errorf("the repository name %q is longer than %d characters", name, maxNameLen)
	case !nameRe.MatchString(name):
		return fmt.Errorf("%q is not a repository name: use lower-case letters, digits and . _ - separators, in path components such as project/image", name)
	case strings.Count(name, "/")+1 > maxNameDepth:
		return fmt.Errorf("the repository name %q has more than %d path components", name, maxNameDepth)
	}
	return nil
}

// ProjectOf is the project a repository belongs to: its first path component.
func ProjectOf(name string) string {
	p, _, _ := strings.Cut(name, "/")
	return p
}

// ValidTag checks a tag.
func ValidTag(tag string) error {
	if !tagRe.MatchString(tag) {
		return fmt.Errorf("%q is not a tag: up to %d letters, digits, '_', '.' and '-', not starting with '.' or '-'", tag, maxTagLen)
	}
	return nil
}

// ValidDigest checks an OCI digest (sha256 or sha512, lower-case hex).
func ValidDigest(d string) error {
	if !digestRe.MatchString(d) {
		return fmt.Errorf("%q is not a digest: write sha256: and 64 hex digits", d)
	}
	return nil
}

// IsDigest reports whether a reference is a digest rather than a tag.
func IsDigest(ref string) bool { return strings.Contains(ref, ":") }

// SHA256 returns the digest of data.
func SHA256(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

// newHash returns a hash for an algorithm of a digest.
func newHash(digest string) (interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}, string) {
	if strings.HasPrefix(digest, "sha512:") {
		return sha512.New(), "sha512:"
	}
	return sha256.New(), "sha256:"
}

// orgDir makes a safe object key component of an organization name.
func orgDir(org string) string {
	ok := org != "" && len(org) <= 64
	for i := 0; ok && i < len(org); i++ {
		c := org[i]
		ok = 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_'
	}
	if ok {
		return org
	}
	h := sha256.Sum256([]byte(org))
	return "o" + hex.EncodeToString(h[:8])
}
