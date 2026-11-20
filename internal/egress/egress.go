// Package egress is the guard every server-side fetch of an address a user
// supplied goes through: a repository, an identity provider, a model. It
// refuses addresses that lead into the server's own network (loopback,
// link-local and cloud metadata, private and shared address ranges) unless an
// administrator listed the host or range, and local paths and the file
// protocol unless the server was started allowing them. Addresses are
// checked after name resolution and again when the connection is made, so a
// name that changes its answer between the two (DNS rebinding) does not get
// through. The hosted service uses the same package for all its fetches.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrBlocked is what a refused address wraps. Its message is safe to show
// to the caller; the detail goes to the log.
var ErrBlocked = errors.New("that address is not allowed")

// Policy is what the guard allows beyond public addresses.
type Policy struct {
	// AllowLocal lets local paths and the file protocol be used as
	// repository addresses. Off by default; `make run` turns it on.
	AllowLocal bool
	// Allow lists hosts (exact names, or *.suffix), addresses and CIDR ranges
	// that may be reached although they are internal.
	Allow []string
}

// Guard applies a Policy that can change while the server runs.
type Guard struct {
	mu     sync.RWMutex
	policy Policy
	nets   []netip.Prefix
	hosts  []string
	// Resolve looks a name up; tests replace it.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

// Default is the guard of the running server, set from its configuration.
var Default = New(Policy{})

// New returns a guard with a policy.
func New(p Policy) *Guard {
	g := &Guard{Resolve: resolve}
	_ = g.Set(p)
	return g
}

func resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.Unmap())
	}
	return out, nil
}

// CheckAllowList reports what is wrong with the entries of an allow list.
func CheckAllowList(list []string) error {
	var errs []error
	for _, e := range list {
		if _, _, err := parseEntry(e); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func parseEntry(e string) (netip.Prefix, string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	if e == "" {
		return netip.Prefix{}, "", errors.New("an empty entry in the allowed hosts")
	}
	if p, err := netip.ParsePrefix(e); err == nil {
		return p.Masked(), "", nil
	}
	if a, err := netip.ParseAddr(e); err == nil {
		return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), "", nil
	}
	h := strings.TrimPrefix(e, "*.")
	if h == "" || strings.ContainsAny(h, "/:@ *") {
		return netip.Prefix{}, "", fmt.Errorf("%q: give a host name (git.internal or *.internal), an address or a range such as 10.0.0.0/8", e)
	}
	return netip.Prefix{}, e, nil
}

// Set replaces the policy.
func (g *Guard) Set(p Policy) error {
	var nets []netip.Prefix
	var hosts []string
	for _, e := range p.Allow {
		n, h, err := parseEntry(e)
		if err != nil {
			return err
		}
		if h != "" {
			hosts = append(hosts, h)
		} else {
			nets = append(nets, n)
		}
	}
	g.mu.Lock()
	g.policy, g.nets, g.hosts = p, nets, hosts
	g.mu.Unlock()
	return nil
}

// Policy returns the policy in effect.
func (g *Guard) Policy() Policy {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.policy
}

// AllowLocal reports whether local paths and the file protocol are allowed.
func (g *Guard) AllowLocal() bool { return g.Policy().AllowLocal }

var blockedRanges = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/3",
	"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "64:ff9b::/96", "2002::/16",
)

func mustPrefixes(l ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(l))
	for i, s := range l {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// Internal reports whether an address is one the guard refuses by default.
func Internal(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified() || a.IsPrivate() {
		return true
	}
	for _, p := range blockedRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Hosts and entries are stored as typed; wildcard entries keep their "*.".
func (g *Guard) matchHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, h := range g.hosts {
		if h == host {
			return true
		}
		if rest, ok := strings.CutPrefix(h, "*."); ok && strings.HasSuffix(host, "."+rest) {
			return true
		}
	}
	return false
}

func (g *Guard) addrAllowed(a netip.Addr) bool {
	for _, p := range g.nets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// CheckAddr says whether host (a name, or null if only the address is
// known) may be reached at the address.
func (g *Guard) CheckAddr(host string, a netip.Addr) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	a = a.Unmap()
	if !Internal(a) || g.addrAllowed(a) || (host != "" && g.matchHost(host)) {
		return nil
	}
	return fmt.Errorf("%w: %s resolves to %s, an internal address", ErrBlocked, hostOr(host, a), a)
}

func hostOr(host string, a netip.Addr) string {
	if host == "" {
		return a.String()
	}
	return host
}

// Lookup resolves host and checks every address it has, returning them.
// A literal address is checked as is.
func (g *Guard) Lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.Trim(host, "[]")
	if host == "" {
		return nil, fmt.Errorf("%w: no host", ErrBlocked)
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a.Unmap()}, g.CheckAddr("", a)
	}
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		g.mu.RLock()
		ok := g.matchHost("localhost")
		g.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("%w: localhost is this server", ErrBlocked)
		}
	}
	ips, err := g.Resolve(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("could not resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("could not resolve %s", host)
	}
	for _, a := range ips {
		if err := g.CheckAddr(host, a); err != nil {
			return nil, err
		}
	}
	return ips, nil
}

// Dialer returns a DialContext that resolves, checks and connects to the
// address it checked, so what is checked is what is connected to and a
// name that answers differently the next time cannot redirect it.
func (g *Guard) Dialer(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := g.Lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		var last error
		for _, ip := range ips {
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
			last = err
		}
		return nil, last
	}
}

// HTTPClient returns a client that never follows a redirect (the caller
// sees it and decides) and connects only to addresses the guard allows.
func (g *Guard) HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           g.Dialer(10 * time.Second),
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: timeout,
			DisableKeepAlives:     true,
			Proxy:                 nil, // a proxy would be where the connection goes: no
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Redirected reports whether a response is a redirect, which the guarded
// client does not follow.
func Redirected(r *http.Response) bool { return r.StatusCode >= 300 && r.StatusCode < 400 }

// Target is a repository address read for what it asks the server to do.
type Target struct {
	// Local: a path on this server, or the file protocol.
	Local bool
	// Scheme is http, https, ssh or git for a network address.
	Scheme string
	Host   string
	Port   string
}

// ParseRepo classifies a Git address. It refuses what Git would run as a
// command (ext::), and options.
func ParseRepo(addr string) (Target, error) {
	addr = strings.TrimSpace(addr)
	switch {
	case addr == "":
		return Target{}, errors.New("give the repository's address")
	case strings.HasPrefix(addr, "-"):
		return Target{}, errors.New("that is not a Git address")
	}
	if i := strings.Index(addr, "::"); i > 0 && !strings.ContainsAny(addr[:i], "/@") {
		return Target{}, errors.New("that is not a Git address; use an https:// or ssh address, or a path on this server")
	}
	if !strings.Contains(addr, "://") {
		// user@host:path is ssh; a path with a colon before its first slash
		// and an @ is the same; everything else is a path.
		if at := strings.Index(addr, "@"); at > 0 {
			rest := addr[at+1:]
			if c := strings.Index(rest, ":"); c > 0 && !strings.Contains(addr[:at], "/") {
				return Target{Scheme: "ssh", Host: strings.Trim(rest[:c], "[]")}, nil
			}
		}
		if c := strings.Index(addr, ":"); c > 0 && !strings.Contains(addr[:c], "/") && len(addr[:c]) > 1 {
			return Target{Scheme: "ssh", Host: strings.Trim(addr[:c], "[]")}, nil
		}
		return Target{Local: true}, nil
	}
	u, err := url.Parse(addr)
	if err != nil {
		return Target{}, errors.New("that is not a valid address")
	}
	switch strings.ToLower(u.Scheme) {
	case "file":
		return Target{Local: true}, nil
	case "http", "https", "ssh", "git":
		if u.Hostname() == "" {
			return Target{}, errors.New("the address has no host")
		}
		return Target{Scheme: strings.ToLower(u.Scheme), Host: u.Hostname(), Port: u.Port()}, nil
	case "git+ssh", "ssh+git":
		return Target{Scheme: "ssh", Host: u.Hostname(), Port: u.Port()}, nil
	}
	return Target{}, fmt.Errorf("the %s protocol is not supported; use https, ssh or git", u.Scheme)
}

// CheckRepo checks a repository address before the server fetches from it
// (resolving a name, so it may take a moment). It returns the Git
// environment to fetch with: only the protocols the policy allows, no
// redirects, and the checked addresses pinned for http and https so that
// Git connects to what was checked.
func (g *Guard) CheckRepo(ctx context.Context, addr string) ([]string, error) {
	t, err := ParseRepo(addr)
	if err != nil {
		return nil, err
	}
	env := gitEnv(g.AllowLocal())
	if t.Local {
		if !g.AllowLocal() {
			return nil, fmt.Errorf("%w: a path on the server or a file: address; this server was not started allowing them (egress.allow_local in its bootstrap file)", ErrBlocked)
		}
		return env, nil
	}
	ips, err := g.Lookup(ctx, t.Host)
	if err != nil {
		return nil, err
	}
	if _, err := netip.ParseAddr(strings.Trim(t.Host, "[]")); err != nil && (t.Scheme == "http" || t.Scheme == "https") {
		port := t.Port
		if port == "" {
			port = map[string]string{"http": "80", "https": "443"}[t.Scheme]
		}
		var l []string
		for _, ip := range ips {
			l = append(l, ip.String())
		}
		env = appendGitConfig(env, "http.curloptResolve", t.Host+":"+port+":"+strings.Join(l, ","))
	}
	return env, nil
}

// CheckRepoSyntax is CheckRepo without name resolution: it refuses local
// addresses the policy does not allow and literal internal addresses, for
// validating settings where a lookup would be slow or flaky.
func (g *Guard) CheckRepoSyntax(addr string) error {
	t, err := ParseRepo(addr)
	if err != nil {
		return err
	}
	if t.Local {
		if !g.AllowLocal() {
			return fmt.Errorf("%w: a path on the server or a file: address; this server was not started allowing them (egress.allow_local in its bootstrap file)", ErrBlocked)
		}
		return nil
	}
	return g.CheckHostSyntax(t.Host)
}

// CheckHostSyntax refuses localhost and literal internal addresses.
func (g *Guard) CheckHostSyntax(host string) error {
	host = strings.Trim(host, "[]")
	if a, err := netip.ParseAddr(host); err == nil {
		return g.CheckAddr("", a)
	}
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		g.mu.RLock()
		defer g.mu.RUnlock()
		if !g.matchHost("localhost") {
			return fmt.Errorf("%w: localhost is this server", ErrBlocked)
		}
	}
	return nil
}

func gitEnv(allowLocal bool) []string {
	protos := "http:https:ssh:git"
	if allowLocal {
		protos = "file:" + protos
	}
	env := []string{"GIT_ALLOW_PROTOCOL=" + protos, "GIT_CONFIG_COUNT=0"}
	env = appendGitConfig(env, "http.followRedirects", "false")
	return env
}

// appendGitConfig adds a setting through GIT_CONFIG_COUNT/KEY_n/VALUE_n.
func appendGitConfig(env []string, key, value string) []string {
	n := 0
	out := env[:0:0]
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "GIT_CONFIG_COUNT="); ok {
			fmt.Sscanf(v, "%d", &n)
			continue
		}
		out = append(out, e)
	}
	out = append(out, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", n, key), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, value), fmt.Sprintf("GIT_CONFIG_COUNT=%d", n+1))
	return out
}
