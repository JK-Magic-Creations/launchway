package edit

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	tagRe       = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	componentRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	hostRe      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*(?::[0-9]{1,5})?$`)
	digestRe    = regexp.MustCompile(`^[a-z0-9]+(?:[.+_-][a-z0-9]+)*:[0-9a-fA-F]{32,}$`)
)

// maxImageLen bounds an image name, as registries do.
const maxImageLen = 255

// ref is an image reference: [registry/]repository[:tag][@digest].
type ref struct {
	registry string // empty when the reference names no registry
	repo     string // the repository path, without the registry
	tag      string
	digest   string // without the @
}

// name is the reference without its tag or digest, as it was written.
func (r ref) name() string {
	if r.registry == "" {
		return r.repo
	}
	return r.registry + "/" + r.repo
}

// isRegistry reports whether the first part of a reference is a registry
// host: it has a dot or a port, or is localhost.
func isRegistry(part string) bool {
	return (strings.ContainsAny(part, ".:") || part == "localhost") && hostRe.MatchString(part)
}

// parseRef reads s as an image reference. ok is false when s is not one.
func parseRef(s string) (r ref, ok bool) {
	if s == "" || len(s) > maxImageLen+130+80 {
		return ref{}, false
	}
	if at := strings.IndexByte(s, '@'); at >= 0 {
		r.digest = s[at+1:]
		s = s[:at]
		if !digestRe.MatchString(r.digest) {
			return ref{}, false
		}
	}
	if c := strings.LastIndexByte(s, ':'); c > strings.LastIndexByte(s, '/') {
		r.tag = s[c+1:]
		s = s[:c]
		if !tagRe.MatchString(r.tag) {
			return ref{}, false
		}
	}
	parts := strings.Split(s, "/")
	if len(parts) > 1 && isRegistry(parts[0]) {
		r.registry = parts[0]
		parts = parts[1:]
	}
	for _, p := range parts {
		if !componentRe.MatchString(p) {
			return ref{}, false
		}
	}
	r.repo = strings.Join(parts, "/")
	return r, len(r.name()) <= maxImageLen
}

// refersTo reports whether r is a reference to image. An image given with
// a registry matches only that registry; one given without matches the
// same repository in any registry, or in none.
func (r ref) refersTo(image ref) bool {
	if r.repo != image.repo {
		return false
	}
	return image.registry == "" || strings.EqualFold(r.registry, image.registry)
}

// CheckImage reports whether an image name is written correctly: a
// repository such as homelab/beacon or registry.example:5000/team/app,
// without a tag or digest. nil if it is.
func CheckImage(image string) error {
	if image == "" {
		return fmt.Errorf("the image is empty; give a repository such as homelab/beacon")
	}
	if strings.TrimSpace(image) != image {
		return fmt.Errorf("image %q has spaces around it; remove them", image)
	}
	r, ok := parseRef(image)
	if !ok {
		if _, lower := parseRef(strings.ToLower(image)); lower {
			return fmt.Errorf("image %q must be lower case (a registry host may use capitals)", image)
		}
	}
	switch {
	case !ok:
		return fmt.Errorf("image %q is not a valid repository; write one such as homelab/beacon or registry.example:5000/team/app", image)
	case r.digest != "":
		return fmt.Errorf("image %q has a digest; give the repository only, without @%s", image, r.digest)
	case r.tag != "":
		return fmt.Errorf("image %q has a tag (:%s); give the repository only, the tag is set separately", image, r.tag)
	}
	return nil
}

// CheckTag reports whether a tag is valid ([A-Za-z0-9_][A-Za-z0-9_.-]{0,127}).
func CheckTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("the tag is empty")
	}
	if !tagRe.MatchString(tag) {
		return fmt.Errorf("tag %q is not valid: use letters, digits, _, . and -, up to 128 characters, not starting with . or -", tag)
	}
	return nil
}

// sha256DigestRe is a digest Launchway pins: sha256 and 64 hex digits.
var sha256DigestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// CheckDigest reports whether a digest is sha256:<64 lower-case hex digits>.
func CheckDigest(d string) error {
	if !sha256DigestRe.MatchString(d) {
		return fmt.Errorf("digest %q is not a sha256 digest (sha256: and 64 hex digits)", shortDigest(d))
	}
	return nil
}

// shortDigest shortens a digest for an error message.
func shortDigest(d string) string {
	if c := strings.IndexByte(d, ':'); c >= 0 && len(d) > c+13 {
		return d[:c+13] + "…"
	}
	return d
}
