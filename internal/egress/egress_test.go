package egress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fake(answers map[string][]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, h string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, s := range answers[h] {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

func TestInternalAddressesAreRefused(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.20.0.1", "192.168.1.1", "169.254.169.254", "fd00:ec2::254",
		"100.64.0.1", "0.0.0.0", "::ffff:127.0.0.1", "fe80::1", "224.0.0.1"} {
		if !Internal(netip.MustParseAddr(s)) {
			t.Errorf("%s is allowed", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "140.82.112.3", "2606:4700::1111"} {
		if Internal(netip.MustParseAddr(s)) {
			t.Errorf("%s is refused", s)
		}
	}
}

func TestLookupChecksEveryAnswerAndTheAllowList(t *testing.T) {
	g := New(Policy{})
	g.Resolve = fake(map[string][]string{
		"git.example.com":  {"140.82.112.3"},
		"evil.example.com": {"140.82.112.3", "169.254.169.254"},
		"git.internal":     {"10.0.0.5"},
		"x.corp.internal":  {"10.0.0.6"},
	})
	ctx := context.Background()
	if _, err := g.Lookup(ctx, "git.example.com"); err != nil {
		t.Error(err)
	}
	for _, h := range []string{"evil.example.com", "git.internal", "localhost", "127.0.0.1", "169.254.169.254", "[::1]"} {
		if _, err := g.Lookup(ctx, h); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: %v", h, err)
		}
	}
	if err := g.Set(Policy{Allow: []string{"git.internal", "*.corp.internal", "192.168.0.0/16"}}); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"git.internal", "x.corp.internal", "192.168.4.4"} {
		if _, err := g.Lookup(ctx, h); err != nil {
			t.Errorf("%s: allowed by the list but %v", h, err)
		}
	}
	if _, err := g.Lookup(ctx, "evil.example.com"); err == nil {
		t.Error("an allow list for other hosts let the metadata address through")
	}
	if err := g.Set(Policy{Allow: []string{"http://x"}}); err == nil {
		t.Error("a URL is not an allow-list entry")
	}
}

// A name that answers with a public address when it is checked and an
// internal one when the connection is made does not get through.
func TestDNSRebindingIsStoppedAtConnect(t *testing.T) {
	hit := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Add(1) }))
	defer srv.Close()
	port := srv.Listener.Addr().(interface{ String() string }).String()
	port = port[strings.LastIndex(port, ":"):]
	var calls atomic.Int32
	g := New(Policy{})
	g.Resolve = func(context.Context, string) ([]netip.Addr, error) {
		if calls.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("140.82.112.3")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, err := g.Lookup(context.Background(), "rebind.example.com"); err != nil {
		t.Fatal(err)
	}
	c := g.HTTPClient(5 * time.Second)
	_, err := c.Get("http://rebind.example.com" + port + "/")
	if err == nil || !errors.Is(err, ErrBlocked) || hit.Load() != 0 {
		t.Fatalf("connected: err=%v hits=%d", err, hit.Load())
	}
}

func TestClientDoesNotFollowRedirectsOrReachLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	g := New(Policy{})
	if _, err := g.HTTPClient(5 * time.Second).Get(srv.URL); !errors.Is(err, ErrBlocked) {
		t.Fatalf("loopback reached: %v", err)
	}
	if err := g.Set(Policy{Allow: []string{"127.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	resp, err := g.HTTPClient(5 * time.Second).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !Redirected(resp) || resp.Header.Get("Location") == "" {
		t.Errorf("the redirect was followed: %d", resp.StatusCode)
	}
}

func TestRepositoryAddresses(t *testing.T) {
	g := New(Policy{})
	g.Resolve = fake(map[string][]string{"github.com": {"140.82.112.3"}, "intra": {"10.0.0.9"}})
	ctx := context.Background()
	for _, a := range []string{"/srv/git/x", "file:///srv/git/x", "../x", "ext::sh -c id", "fd::3", "-uevil", "ftp://x/y", "http://localhost/x", "https://127.0.0.1/x",
		"http://169.254.169.254/x", "git@intra:team/x.git", "ssh://git@intra/x", "https://intra/x.git"} {
		if _, err := g.CheckRepo(ctx, a); err == nil {
			t.Errorf("%s: accepted", a)
		}
	}
	env, err := g.CheckRepo(ctx, "https://github.com/a/b.git")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"GIT_ALLOW_PROTOCOL=http:https:ssh:git", "http.followRedirects", "http.curloptResolve", "github.com:443:140.82.112.3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("env lacks %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "file") {
		t.Errorf("the file protocol is allowed: %s", joined)
	}
	if err := g.CheckRepoSyntax("/srv/git/x"); !errors.Is(err, ErrBlocked) {
		t.Errorf("a path with allow_local off: %v", err)
	}
	g.Set(Policy{AllowLocal: true, Allow: []string{"intra"}})
	for _, a := range []string{"/srv/git/x", "file:///srv/git/x", "https://intra/x.git", "git@intra:team/x.git"} {
		if _, err := g.CheckRepo(ctx, a); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
	env, _ = g.CheckRepo(ctx, "/srv/git/x")
	if !strings.Contains(strings.Join(env, " "), "GIT_ALLOW_PROTOCOL=file:") {
		t.Errorf("file not allowed with allow_local: %v", env)
	}
}

// An operator may give Git credentials through the server's environment
// (GIT_CONFIG_COUNT and its keys): the guard adds its settings after them and
// never replaces them.
func TestGitSettingsFollowTheOperatorsOwn(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraHeader")
	g := New(Policy{})
	g.Resolve = fake(map[string][]string{"github.com": {"140.82.112.3"}})
	env, err := g.CheckRepo(context.Background(), "https://github.com/a/b.git")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_KEY_2=http.curloptResolve", "GIT_CONFIG_COUNT=3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "GIT_CONFIG_KEY_0") {
		t.Errorf("the operator's first setting is overwritten:\n%s", joined)
	}
}
