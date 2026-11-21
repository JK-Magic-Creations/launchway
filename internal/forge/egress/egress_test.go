package egress

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func lookup(m map[string][]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, h string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, a := range m[h] {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
}

func TestInternalAddressesAreRefusedUnlessAllowed(t *testing.T) {
	dns := map[string][]string{
		"git.public.example": {"93.184.216.34"},
		"git.corp.example":   {"10.20.0.7"},
		"rebinder.example":   {"93.184.216.34", "127.0.0.1"}, // one good answer and one internal
		"v6.example":         {"fd00::5"},
		"mapped.example":     {"::ffff:10.0.0.1"},
		"metadata.example":   {"169.254.169.254"},
	}
	cases := []struct {
		allow []string
		url   string
		ok    bool
	}{
		{nil, "https://git.public.example/a/b.git", true},
		{nil, "http://93.184.216.34/a.git", true},
		{nil, "https://git.corp.example/a.git", false},
		{[]string{"10.20.0.0/16"}, "https://git.corp.example/a.git", true},
		{[]string{"git.corp.example"}, "https://git.corp.example/a.git", true},
		{[]string{"*.corp.example"}, "https://git.corp.example/a.git", true},
		{[]string{"corp.example"}, "https://git.corp.example/a.git", false},
		{nil, "https://rebinder.example/a.git", false},
		{[]string{"10.0.0.0/8"}, "https://rebinder.example/a.git", false}, // 127.0.0.1 is still not allowed
		{nil, "https://v6.example/a.git", false},
		{nil, "https://mapped.example/a.git", false},
		{nil, "https://metadata.example/a.git", false},
		{nil, "http://[::1]:3000/a.git", false},
		{nil, "http://localhost/a.git", false},
		{[]string{"127.0.0.1"}, "http://127.0.0.1:3000/a.git", true},
		{nil, "http://2130706433/a.git", false}, // decimal form of 127.0.0.1 is not an address Go parses: it must fail to resolve, not pass
		{nil, "ftp://git.public.example/a.git", false},
		{nil, "git://git.public.example/a.git", false},
		{nil, "ssh://git@git.public.example/a.git", false},
		{nil, "file:///etc/passwd", false},
		{nil, "/etc/passwd", false},
		{nil, "https://user:pw@git.public.example/a.git", false},
		{nil, "https://git.public.example/a.git?token=x", false},
	}
	for _, c := range cases {
		g, err := New(c.allow)
		if err != nil {
			t.Fatal(err)
		}
		g.Lookup = lookup(dns)
		err = g.Check(context.Background(), c.url)
		if (err == nil) != c.ok {
			t.Errorf("allow %v, %s: got %v, want ok=%v", c.allow, c.url, err, c.ok)
		}
		if err != nil && strings.Contains(err.Error(), "pw@") {
			t.Errorf("the refusal repeats the password: %v", err)
		}
	}
}

func TestRefusalSaysHowToAllow(t *testing.T) {
	g, _ := New(nil)
	err := g.Check(context.Background(), "http://10.1.2.3/x.git")
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "forge.mirror_allow") || !strings.Contains(err.Error(), "10.1.2.3") {
		t.Fatalf("%v", err)
	}
}

func TestBadAllowListEntries(t *testing.T) {
	for _, e := range []string{"*", "a b", "http://x", "10.0.0.0/33", "host:80"} {
		if _, err := New([]string{e}); err == nil {
			t.Errorf("%q accepted", e)
		}
	}
}
