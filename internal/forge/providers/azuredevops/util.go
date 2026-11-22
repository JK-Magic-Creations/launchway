package azuredevops

import (
	"net/url"
	"strings"
	"time"
)

// apiVersion is the version every call names. The reference pages are for 7.1,
// and the server product's reference has a 7.1 as well.
const apiVersion = "7.1"

// qs builds the query of a call: api-version first, then key, value pairs.
func qs(kv ...string) string {
	v := url.Values{}
	v.Set("api-version", apiVersion)
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return "?" + v.Encode()
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// noUser drops the user name an address may carry ("https://org@dev.azure.com/...")
// so that a clone address never holds a credential or a name.
func noUser(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.User = nil
	return u.String()
}

func trimRef(name string) string {
	for _, p := range []string{"refs/heads/", "refs/tags/"} {
		name = strings.TrimPrefix(name, p)
	}
	return name
}

// zeros is the all-zero object id a ref has before it exists.
var zeros = strings.Repeat("0", 40)

// sshURL turns the host's scp-style address ("git@ssh.dev.azure.com:v3/org/project/repo")
// into the ssh:// form, which is a valid address; an address that already is one is kept.
func sshURL(raw string) string {
	if raw == "" || strings.Contains(raw, "://") {
		return raw
	}
	user, rest, ok := strings.Cut(raw, "@")
	if !ok {
		return raw
	}
	host, path, ok := strings.Cut(rest, ":")
	if !ok {
		return raw
	}
	return "ssh://" + user + "@" + host + "/" + strings.TrimPrefix(path, "/")
}
