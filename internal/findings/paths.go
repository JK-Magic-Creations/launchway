package findings

import (
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/findings/jsonscan"
)

// filePath turns a result's artifactLocation into the path shown. The path
// is only ever shown, never opened, so a path that is absolute or climbs out
// of the workspace with ".." is kept (cleaned) rather than refused.
//
//   - With a uriBaseId (%SRCROOT%, ROOTPATH, SRCROOT and so on) the uri is
//     relative to the workspace already and is kept as it is.
//   - A file:// uri is made relative when it starts with the file:// uri
//     that originalUriBaseIds gives for some base; otherwise only the
//     scheme is removed.
//   - Percent escapes are decoded, a leading "./" is removed and the path is
//     normalised with path.Clean. A uri with another scheme (https://...) is
//     kept as written.
func filePath(uri, baseID string, bases map[string]string, max int) string {
	u := cutBytes(uri, 4*max)
	if u == "" {
		return ""
	}
	if hasPrefixFold(u, "file://") {
		if rel, ok := underBase(u, bases); ok {
			u = rel
		} else {
			u = u[len("file://"):]
			if hasPrefixFold(u, "localhost/") {
				u = u[len("localhost"):]
			}
		}
	}
	if d, err := url.PathUnescape(u); err == nil {
		u = d
	}
	u = jsonscan.Line(u, 4*max)
	if hasScheme(u) {
		return jsonscan.Cut(u, max)
	}
	for strings.HasPrefix(u, "./") {
		u = u[2:]
	}
	if u != "" {
		u = path.Clean(u)
	}
	return jsonscan.Cut(u, max)
}

// underBase strips the longest originalUriBaseIds file:// uri that u
// starts with.
func underBase(u string, bases map[string]string) (string, bool) {
	names := make([]string, 0, len(bases))
	for n := range bases {
		names = append(names, n)
	}
	sort.Strings(names)
	best := ""
	for _, n := range names {
		b := bases[n]
		if !hasPrefixFold(b, "file://") {
			continue
		}
		if !strings.HasSuffix(b, "/") {
			b += "/"
		}
		if len(b) > len(best) && strings.HasPrefix(u, b) {
			best = b
		}
	}
	if best == "" || len(u) == len(best) {
		return "", false
	}
	return u[len(best):], true
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// hasScheme says whether s starts with scheme:// .
func hasScheme(s string) bool {
	i := strings.Index(s, "://")
	if i <= 0 {
		return false
	}
	for j := 0; j < i; j++ {
		c := s[j]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (j == 0 || !(c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.')) {
			return false
		}
	}
	return true
}

// webURL keeps an absolute http:// or https:// URL with a host and nothing
// odd in it, and drops anything else (javascript:, data:, relative links,
// text with spaces or control characters).
func webURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxURIBytes {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= 0x20 || c == 0x7f || c == '"' || c == '<' || c == '>' || c == '\\' || c >= 0x80 {
			return ""
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return ""
	}
	if sc := strings.ToLower(u.Scheme); sc != "http" && sc != "https" {
		return ""
	}
	return s
}
