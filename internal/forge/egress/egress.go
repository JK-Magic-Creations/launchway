// Package egress is the guard on addresses Launchway is asked to contact on
// someone's behalf (a mirror's URL, an import source). A person who may set
// up a mirror must not thereby be able to make the server reach its own
// loopback, the cluster's internal services or a cloud metadata address: an
// internal address is refused unless an administrator has allowed it by name
// or by network (forge.mirror_allow). A host on a private network, such as a
// Git server in the same data centre, is therefore reached by listing it, and
// the refusal says exactly that.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// Guard decides which addresses may be contacted.
type Guard struct {
	nets  []netip.Prefix
	hosts []string // exact names, or "*.suffix"
	// AllowFiles lets file:// and plain paths through. It exists for tests of
	// the code around the guard; the server never sets it.
	AllowFiles bool
	// Lookup resolves a name; default net.DefaultResolver.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

// New makes a guard from an allow-list whose entries are networks (10.0.0.0/8),
// single addresses (192.168.1.5) or host names (git.example.internal, or
// *.example.internal for every host under a domain).
func New(allow []string) (*Guard, error) {
	g := &Guard{}
	for _, e := range allow {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			g.nets = append(g.nets, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(e); err == nil {
			g.nets = append(g.nets, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
			continue
		}
		if strings.ContainsAny(e, "/: @") || e == "*" || e == "*." {
			return nil, fmt.Errorf("the allow-list entry %q is not a network, an address or a host name", e)
		}
		g.hosts = append(g.hosts, strings.ToLower(e))
	}
	return g, nil
}

// ErrRefused is wrapped by every refusal.
var ErrRefused = errors.New("address refused")

// special are ranges that are not public although the standard library does
// not call them private.
var special = mustPrefixes("100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "2001:db8::/32")

func mustPrefixes(s ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, v := range s {
		out = append(out, netip.MustParsePrefix(v))
	}
	return out
}

// Internal reports whether an address is not a public one.
func Internal(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range special {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (g *Guard) hostAllowed(h string) bool {
	h = strings.ToLower(h)
	for _, e := range g.hosts {
		if e == h || (strings.HasPrefix(e, "*.") && strings.HasSuffix(h, e[1:])) {
			return true
		}
	}
	return false
}

func (g *Guard) addrAllowed(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range g.nets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func refusal(host string, a netip.Addr) error {
	return fmt.Errorf("%w: %s is %s, an internal address. Launchway does not contact internal addresses unless an administrator allows them: add the host or its network to forge.mirror_allow in the server configuration (for example %q or %q)",
		ErrRefused, host, a, host, netip.PrefixFrom(a, 24).Masked().String())
}

// Check says whether a repository URL may be contacted. Only https and http
// are accepted, with no user name or password in them. The host's addresses
// are looked up here, and every one must be public or allowed.
func (g *Guard) Check(ctx context.Context, raw string) error {
	if g.AllowFiles && (strings.HasPrefix(raw, "file://") || strings.HasPrefix(raw, "/")) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: %q is not an http or https address", ErrRefused, redactURL(raw))
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("%w: only https and http addresses can be mirrored, not %q", ErrRefused, u.Scheme+"://")
	}
	if u.User != nil {
		return fmt.Errorf("%w: the address must not contain a user name or password; give the credential as a reference", ErrRefused)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: the address must not contain a query or fragment", ErrRefused)
	}
	host := u.Hostname()
	if g.hostAllowed(host) {
		return nil
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		lookup := g.Lookup
		if lookup == nil {
			lookup = func(ctx context.Context, h string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
			}
		}
		if addrs, err = lookup(ctx, host); err != nil || len(addrs) == 0 {
			return fmt.Errorf("the host %s cannot be resolved from this server", host)
		}
	}
	for _, a := range addrs {
		if Internal(a) && !g.addrAllowed(a) {
			return refusal(host, a.Unmap())
		}
	}
	return nil
}

func redactURL(s string) string {
	if i := strings.Index(s, "@"); i >= 0 {
		return "…" + s[i:]
	}
	return s
}
