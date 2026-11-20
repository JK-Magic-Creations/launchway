package verify

import (
	"regexp"
	"sort"
	"strings"
)

// redacted replaces what Redact removes.
const redacted = "***"

var (
	// PEM private key blocks, to their end line or the end of the text.
	pemRe = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?s:.*?)(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`)
	// Authorization headers: keep the header name and scheme.
	authRe = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization["']?\s*[:=]\s*["']?)(?:(bearer|basic|digest|token|negotiate|aws4-hmac-sha256)\s+)?[^\s"',;]+`)
	// A bare "Bearer x" or "Basic x".
	schemeRe = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)([A-Za-z0-9._~+/=-]{8,})`)
	// URL userinfo with a password: scheme://user:password@host.
	userinfoRe = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@:]+:[^/\s@]*@`)
	// JSON web tokens: three base64url segments, the first a JSON object.
	jwtRe = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`)
	// AWS access key ids.
	awsRe = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)
	// key=value, key: value and "key":"value" where the key names a
	// secret.
	kvRe = regexp.MustCompile(`(?i)(["']?)\b([a-z0-9_.-]*?(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credentials?|cookie)[a-z0-9_.-]*)(["']?)(\s*[:=]\s*)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s,;&"'}\])]+)`)
	// Long runs that look like generated tokens.
	longTokenRe = regexp.MustCompile(`[A-Za-z0-9_\-]{32,}`)
)

// Redact removes likely secrets from a log line or any other text:
// private key blocks, Authorization headers and bare Bearer/Basic
// credentials, passwords in URLs, JSON web tokens, AWS access key ids,
// the values of key=value, key: value and "key": "value" pairs whose key
// names a secret (password, passwd, pwd, secret, token, api_key, apikey,
// access_key, private_key, credentials, cookie), and any run of 32 or more
// letters, digits, '_' and '-' that has both letters and digits. What it
// removes becomes ***.
//
// It is pattern matching: it removes what looks like a secret, so it also
// removes some things that are not (long ids, hashes, pod names), and it
// cannot know a secret that looks like an ordinary word. MaskWith adds the
// values that are known to be secret.
func Redact(s string) string {
	if s == "" {
		return s
	}
	s = pemRe.ReplaceAllString(s, redacted)
	s = authRe.ReplaceAllStringFunc(s, func(m string) string {
		g := authRe.FindStringSubmatch(m)
		if g[2] != "" {
			return g[1] + g[2] + " " + redacted
		}
		return g[1] + redacted
	})
	s = schemeRe.ReplaceAllStringFunc(s, func(m string) string {
		g := schemeRe.FindStringSubmatch(m)
		// "basic authentication" is prose; a credential has a digit,
		// mixed case or base64 punctuation.
		if !credentialLike(g[3]) {
			return m
		}
		return g[1] + g[2] + redacted
	})
	s = userinfoRe.ReplaceAllString(s, "${1}"+redacted+"@")
	s = jwtRe.ReplaceAllString(s, redacted)
	s = awsRe.ReplaceAllString(s, redacted)
	s = kvRe.ReplaceAllStringFunc(s, func(m string) string {
		g := kvRe.FindStringSubmatch(m)
		v := g[5]
		switch {
		case v == redacted, v == `"`+redacted+`"`, v == `'`+redacted+`'`:
			return m
		case strings.HasPrefix(v, `"`):
			v = `"` + redacted + `"`
		case strings.HasPrefix(v, `'`):
			v = `'` + redacted + `'`
		default:
			v = redacted
		}
		return g[1] + g[2] + g[3] + g[4] + v
	})
	s = longTokenRe.ReplaceAllStringFunc(s, func(m string) string {
		if hasDigit(m) && hasLetter(m) {
			return redacted
		}
		return m
	})
	return s
}

func credentialLike(s string) bool {
	if hasDigit(s) || strings.ContainsAny(s, "+/=") {
		return true
	}
	return strings.ToLower(s) != s && strings.ToUpper(s) != s
}

// MaskWith returns a redaction function that first replaces each of the
// given known secret values (those of at least 4 bytes; shorter ones would
// mask ordinary text) with ***, longest first, and then applies Redact.
func MaskWith(values []string) func(string) string {
	var vs []string
	seen := map[string]bool{}
	for _, v := range values {
		if len(v) >= 4 && !seen[v] {
			seen[v] = true
			vs = append(vs, v)
		}
	}
	sort.Slice(vs, func(i, j int) bool {
		if len(vs[i]) != len(vs[j]) {
			return len(vs[i]) > len(vs[j])
		}
		return vs[i] < vs[j]
	})
	return func(s string) string {
		for _, v := range vs {
			s = strings.ReplaceAll(s, v, redacted)
		}
		return Redact(s)
	}
}
