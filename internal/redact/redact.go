// Package redact removes credentials from text shown to people.
package redact

import (
	"regexp"
	"strings"
)

// userinfo matches the user[:password]@ part of a URL.
var userinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)

// secretQuery matches a token-style query parameter and its value.
var secretQuery = regexp.MustCompile(`(?i)([?&](?:access_token|private_token|token|api_?key|apikey|key|password|passwd|secret|sig|signature|auth)=)[^&\s#]+`)

// URLs replaces the credentials in any URL inside s, so that
// https://user:token@host/repo becomes https://***@host/repo and
// https://host/r?token=abc becomes https://host/r?token=***.
func URLs(s string) string {
	s = userinfo.ReplaceAllString(s, "${1}***@")
	return secretQuery.ReplaceAllString(s, "${1}***")
}

// SecretQueryName reports whether a query parameter name usually carries a
// credential.
func SecretQueryName(name string) bool {
	return secretQuery.MatchString("?" + strings.ToLower(name) + "=x")
}
