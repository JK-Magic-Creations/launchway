package pipeline

import (
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaxAddress is the longest address a deployment keeps.
const MaxAddress = 2000

// ValidAddress says why a deployment address is not acceptable, or "" when it is: an http or https address with a
// host, no spaces or control characters, no sign-in details in it, at most MaxAddress characters. It is shown as text
// and as a link, so nothing else (javascript:, data:, a file) may be one.
func ValidAddress(a string) string {
	if a == "" || len(a) > MaxAddress {
		return "an address is 1 to 2000 characters"
	}
	for _, r := range a {
		if r <= ' ' || r == 0x7f {
			return "an address has no spaces or control characters"
		}
	}
	u, err := url.Parse(a)
	if err != nil {
		return "an address is a full web address such as https://preview.example.com/pay-12"
	}
	if u.Scheme != "" {
		if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
			return "an address starts with http:// or https://"
		}
	}
	if u.Scheme == "" || u.Host == "" || u.Hostname() == "" {
		return "an address is a full web address such as https://preview.example.com/pay-12"
	}
	if u.User != nil {
		return "an address holds no user name or password"
	}
	return ""
}

// parseAddress reads a deploy step's address: a value, and when it is written in place, a web address.
func (ps *parser) parseAddress(n *yaml.Node, where string, s *Step) {
	s.Address, s.addressLine = ps.value(n, where), n.Line
	if s.Address != nil && s.Address.Literal != "" {
		if why := ValidAddress(s.Address.Literal); why != "" {
			ps.add(n, where, "%s", why)
		}
	}
}
