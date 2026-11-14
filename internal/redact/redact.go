// Package redact removes credentials from text shown to people.
package redact

import "regexp"

// userinfo matches the user[:password]@ part of a URL.
var userinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)

// URLs replaces the credentials in any URL inside s, so that
// https://user:token@host/repo becomes https://***@host/repo.
func URLs(s string) string {
	return userinfo.ReplaceAllString(s, "${1}***@")
}
