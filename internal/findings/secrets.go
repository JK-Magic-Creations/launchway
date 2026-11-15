package findings

import (
	"regexp"
	"strings"
)

// Secret findings.
//
// A finding is judged to be a secret finding when its tool is a secret
// scanner or its rule talks about secrets. The test is deliberately broad:
// calling a finding a secret when it is not only hides its message and
// raises its severity to high, while missing one could store and show a
// live credential. Over-classifying is the safe direction.

// secretTools are words that, found in a tool's name (in any case), mark it
// as a secret scanner.
var secretTools = []string{
	"gitleaks", "trufflehog", "detect-secrets", "ggshield", "talisman",
	"secretlint", "kingfisher", "noseyparker", "whispers",
}

// secretWords are words that, found in a rule's id, name, short
// description or tags (in any case), mark it as a rule about secrets.
var secretWords = []string{
	"secret", "password", "passwd", "credential",
	"private key", "private-key", "private_key", "privatekey",
	"api key", "api-key", "api_key", "apikey",
	"access key", "access-key", "access_key", "accesskey",
	"token",
}

// secretCWEs are the CWE numbers for hard-coded credentials, passwords and
// cryptographic keys, as tags spell them ("external/cwe/cwe-798",
// "CWE-798: Use of Hard-coded Credentials").
var secretCWEs = []string{"cwe-798", "cwe-259", "cwe-321"}

func isSecretTool(tool string) bool {
	t := strings.ToLower(tool)
	for _, w := range secretTools {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// mentionsSecret says whether any of the texts talk about secrets.
func mentionsSecret(texts ...string) bool {
	for _, s := range texts {
		t := strings.ToLower(s)
		for _, w := range secretWords {
			if strings.Contains(t, w) {
				return true
			}
		}
		for _, cwe := range secretCWEs {
			for i := strings.Index(t, cwe); i >= 0; {
				end := i + len(cwe)
				if end >= len(t) || t[end] < '0' || t[end] > '9' {
					return true // cwe-798, not cwe-7980
				}
				next := strings.Index(t[end:], cwe)
				if next < 0 {
					break
				}
				i = end + next
			}
		}
	}
	return false
}

// secretText ends the fixed message every secret finding gets.
const secretText = ": a secret was found here; its value is not shown"

// Masked replaces each secret-shaped piece of text.
const Masked = "[masked]"

type maskPattern struct {
	hints []string // the pattern can only match if one of these is in the text (lower-cased for fold)
	fold  bool
	re    *regexp.Regexp
	repl  string
}

// The patterns Mask applies, in order. The hints keep Mask cheap: most
// messages contain none of them, so no regular expression runs at all.
// Go's regular expressions take time in proportion to the text, whatever
// it holds.
var maskPatterns = []maskPattern{
	// PEM private key blocks, through the END line or the end of the text.
	{hints: []string{"PRIVATE KEY-----"}, re: regexp.MustCompile(`-----BEGIN[A-Z0-9 ]*PRIVATE KEY-----(?s:.*?)(?:-----END[A-Z0-9 ]*PRIVATE KEY-----|\z)`), repl: Masked},
	// Passwords in URLs: scheme://user:password@host.
	{hints: []string{"://"}, re: regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9+.-]*://[^/\s:@]+:)[^/\s@]+@`), repl: "${1}" + Masked + "@"},
	// password=..., passwd: ..., client_secret = "...", "token": "...":
	// the name stays, the value goes.
	{hints: []string{"password", "passwd", "secret", "token", "key"}, fold: true,
		re:   regexp.MustCompile(`(?i)(\b[a-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)[a-z0-9_]*["']?\s*[:=]\s*)("[^"\n]*"|'[^'\n]*'|[^\s"',;)}\]]+)`),
		repl: "${1}" + Masked},
	// AWS access key ids.
	{hints: []string{"AKIA", "ASIA"}, re: regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), repl: Masked},
	// GitHub tokens.
	{hints: []string{"gh", "github_pat_"}, re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`), repl: Masked},
	// GitLab personal access tokens.
	{hints: []string{"glpat-"}, re: regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`), repl: Masked},
	// Slack tokens.
	{hints: []string{"xox"}, re: regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), repl: Masked},
	// Stripe live keys.
	{hints: []string{"_live_"}, re: regexp.MustCompile(`\b(?:sk|rk)_live_[A-Za-z0-9]{10,}`), repl: Masked},
	// Google API keys.
	{hints: []string{"AIza"}, re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`), repl: Masked},
	// JSON Web Tokens: header.payload.signature, both JSON parts base64url.
	{hints: []string{"eyJ"}, re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`), repl: Masked},
}

// Mask replaces secret-shaped text with "[masked]": PEM private keys,
// passwords in URLs (scheme://user:password@host), the
// values in password=/secret:/token= style assignments, AWS access key ids,
// GitHub, GitLab, Slack, Stripe and Google keys, JSON Web Tokens, and any
// other run of 32 or more characters from [A-Za-z0-9+/=_-] that holds both
// letters and digits (see maskLongTokens for the runs that are left alone).
func Mask(s string) string {
	var lower string
	for i := range maskPatterns {
		p := &maskPatterns[i]
		hay := s
		if p.fold {
			if lower == "" {
				lower = strings.ToLower(s)
			}
			hay = lower
		}
		hit := false
		for _, h := range p.hints {
			if strings.Contains(hay, h) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		if t := p.re.ReplaceAllString(s, p.repl); t != s {
			s = t
			lower = ""
		}
	}
	return maskLongTokens(s)
}

func isTokenChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '+' || c == '/' || c == '=' || c == '_' || c == '-'
}

// minTokenLen is the shortest run of token characters masked as a likely
// key when no named pattern matched it.
const minTokenLen = 32

// maskLongTokens masks runs of 32 or more characters from
// [A-Za-z0-9+/=_-] that hold both letters and digits: the shape of most
// API keys and base64 secrets. These runs are left alone:
//
//   - vulnerability ids (CVE-..., GHSA-..., GO-...), which scanners print
//     everywhere and which are never secret;
//   - a run of exactly 40 or 64 hex digits, which is how Git commit ids and
//     SHA-256 digests are written: scanners print them in nearly every
//     message, and masking them would make messages useless. A secret of
//     that exact shape is rare, and a finding from a secret rule never
//     shows its message anyway;
//   - a run that holds a "/" but does not look random, which is a file
//     or module path (src/main/java/com/example/OrderService1). A run
//     looks random when at least a fifth of its neighbouring letters and
//     digits switch between capital, small letter and digit: a random
//     base64 or hex string switches about half the time, words and
//     CamelCase names rarely.
func maskLongTokens(s string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		if !isTokenChar(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isTokenChar(s[j]) {
			j++
		}
		if j-i >= minTokenLen && looksLikeKey(s[i:j]) {
			if b.Len() == 0 {
				b.Grow(len(s))
			}
			b.WriteString(s[last:i])
			b.WriteString(Masked)
			last = j
		}
		i = j
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func looksLikeKey(t string) bool {
	letters, digits, hex, slash := false, false, true, false
	pairs, switches := 0, 0
	prev := 0 // the class of the previous character: 1 small letter, 2 capital, 3 digit, 0 other
	for i := 0; i < len(t); i++ {
		c := t[i]
		class := 0
		switch {
		case c >= '0' && c <= '9':
			digits, class = true, 3
		case c >= 'a' && c <= 'z':
			letters, class = true, 1
			hex = hex && c <= 'f'
		case c >= 'A' && c <= 'Z':
			letters, class = true, 2
			hex = hex && c <= 'F'
		case c == '/':
			slash, hex = true, false
		default:
			hex = false
		}
		if prev != 0 && class != 0 {
			pairs++
			if prev != class {
				switches++
			}
		}
		prev = class
	}
	if !letters || !digits {
		return false
	}
	if hex && (len(t) == 40 || len(t) == 64) {
		return false
	}
	if slash && switches*5 < pairs {
		return false
	}
	u := strings.ToUpper(t)
	for _, p := range []string{"CVE-", "GHSA-", "GO-"} {
		if strings.HasPrefix(u, p) {
			return false
		}
	}
	return true
}
