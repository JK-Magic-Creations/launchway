package auth

import (
	"slices"
	"strings"
	"testing"
)

func TestPolicy(t *testing.T) {
	p, err := NewPolicy([]Binding{
		{Group: "platform-admins", Role: Admin},
		{Group: "payments", Role: Operator, Projects: []string{"payments-*"}},
		{Group: "everyone", Role: Viewer},
		{User: "dana", Role: Operator, Projects: []string{"web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		user    string
		groups  []string
		project string
		want    Role
	}{
		{"ann", []string{"platform-admins"}, "anything", Admin},
		{"bob", []string{"payments", "everyone"}, "payments-api", Operator},
		{"bob", []string{"payments", "everyone"}, "web", Viewer},
		{"bob", []string{"payments"}, "web", NoRole},
		{"dana", nil, "web", Operator},
		{"dana", nil, "website", NoRole},
		{"eve", []string{"Platform-Admins"}, "web", NoRole}, // groups match exactly
		{"", nil, "web", NoRole},
	}
	for _, c := range cases {
		if got := p.RoleOn(c.user, c.groups, c.project); got != c.want {
			t.Errorf("%s %v on %s: %s, want %s", c.user, c.groups, c.project, got, c.want)
		}
	}
	if !p.Allows("ann", []string{"platform-admins"}, Administer, "") {
		t.Error("a global admin may read the audit trail")
	}
	if p.Allows("dana", nil, Administer, "") || p.Allows("dana", nil, Operate, "payments-api") {
		t.Error("dana has too much")
	}
	if !p.Allows("dana", nil, ReadAny, "") || p.Allows("zed", nil, ReadAny, "") {
		t.Error("ReadAny")
	}
	scoped, _ := NewPolicy([]Binding{{Group: "a", Role: Admin, Projects: []string{"x"}}})
	if scoped.Allows("u", []string{"a"}, Administer, "") {
		t.Error("an admin of one project is not an admin of everything")
	}
	var none *Policy
	if none.RoleOn("u", nil, "x") != NoRole || (&Policy{}).Allows("u", nil, Read, "x") {
		t.Error("an empty policy grants nothing")
	}
}

func TestPolicyRejectsMistakes(t *testing.T) {
	_, err := NewPolicy([]Binding{
		{Role: Viewer},
		{Group: "g", User: "u", Role: Viewer},
		{Group: "g"},
		{Group: "g", Role: Viewer, Projects: []string{"[bad"}},
	})
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"access[0]: give exactly one", "access[1]: give exactly one", "access[2]: a role is required", `access[3]: project pattern "[bad"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if _, err := ParseRole("owner"); err == nil {
		t.Error("owner is not a role")
	}
}

func TestSealer(t *testing.T) {
	s, err := NewSealer(NewKey(), "login")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal(map[string]string{"a": "b"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := s.Open(sealed, &out); err != nil || out["a"] != "b" {
		t.Fatalf("%v %v", out, err)
	}
	other, _ := NewSealer(NewKey(), "login")
	if other.Open(sealed, &out) == nil {
		t.Error("opened with another key")
	}
	key := NewKey()
	a, _ := NewSealer(key, "login")
	b, _ := NewSealer(key, "something else")
	sealed, _ = a.Seal("x")
	if b.Open(sealed, &out) == nil {
		t.Error("opened for another purpose")
	}
	tampered := []byte(sealed)
	tampered[len(tampered)-3] ^= 1
	if a.Open(string(tampered), new(string)) == nil {
		t.Error("opened a tampered value")
	}
}

func TestClaims(t *testing.T) {
	c := NewOIDC(OIDCOptions{})
	for _, tc := range []struct {
		name   string
		claims map[string]any
		user   string
		groups []string
	}{
		{"preferred_username first", map[string]any{"preferred_username": "ann", "email": "a@x", "sub": "1", "groups": []any{"b", "a", "a"}}, "ann", []string{"a", "b"}},
		{"then email", map[string]any{"email": "a@x", "sub": "1", "groups": "solo"}, "a@x", []string{"solo"}},
		{"not an unverified email", map[string]any{"email": "a@x", "email_verified": false, "sub": "1"}, "1", []string{}},
		{"a verified email", map[string]any{"email": "a@x", "email_verified": true, "sub": "1"}, "a@x", []string{}},
		{"then sub", map[string]any{"sub": "1", "groups": []any{"ok", 7, "bad\nname", ""}}, "1", []string{"ok"}},
	} {
		got, err := c.claims("sub", tc.claims)
		if err != nil || got.Username != tc.user || !slices.Equal(got.Groups, tc.groups) {
			t.Errorf("%s: %+v %v", tc.name, got, err)
		}
	}
	if _, err := c.claims("s", map[string]any{"preferred_username": "x\x00y"}); err == nil {
		t.Error("a username with a control character was accepted")
	}
	custom := NewOIDC(OIDCOptions{UsernameClaim: "upn", GroupsClaim: "roles"})
	if got, _ := custom.claims("s", map[string]any{"upn": "u@corp", "preferred_username": "p", "roles": []any{"r"}}); got.Username != "u@corp" || got.Groups[0] != "r" {
		t.Errorf("custom claims: %+v", got)
	}
}

// A binding with environments grants its role on deployments there only;
// on the project otherwise as usual.
func TestRoleOnEnvironment(t *testing.T) {
	p, err := NewPolicy([]Binding{
		{Group: "sre", Role: Operator, Environments: []string{"prod"}},
		{Group: "dev", Role: Operator, Projects: []string{"shop"}, Environments: []string{"staging", "dev-*"}},
		{Group: "all", Role: Viewer},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		group, project, env string
		want                Role
	}{
		{"sre", "shop", "prod", Operator},
		{"sre", "shop", "staging", NoRole},
		{"dev", "shop", "staging", Operator},
		{"dev", "shop", "dev-7", Operator},
		{"dev", "shop", "prod", NoRole},
		{"dev", "other", "staging", NoRole},
		{"all", "shop", "prod", Viewer},
	} {
		if got := p.RoleOnEnvironment("u", []string{tc.group}, tc.project, tc.env); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
	if p.RoleOn("u", []string{"dev"}, "shop") != Operator {
		t.Error("environments took the project role away")
	}
	if _, err := NewPolicy([]Binding{{Group: "x", Role: Viewer, Environments: []string{"["}}}); err == nil {
		t.Error("a bad environment pattern was accepted")
	}
}
