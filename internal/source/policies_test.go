package source

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

const orgPolicies = `policies:
  - name: prod-tests
    mode: enforce
    scope: { environments: [prod] }
    rules:
      tests: { min: 1 }
`

// A source pinned to a tag loads; a malformed file, an unknown rule, a
// branch or a missing version is refused with the file and line; an
// unreachable repository is recorded as such, keeping what was loaded.
func TestPolicySources(t *testing.T) {
	dir := t.TempDir()
	repo := testlib.New(t, filepath.Join(dir, "org"), map[string]string{"policies/org.yaml": orgPolicies, "README": "x"})
	v1 := repo.Head()
	repo.Tag("v1", v1)
	bad := repo.Commit(map[string]string{"policies/team.yaml": "policies:\n  - name: x\n    mode: enforce\n    rules:\n      no_fridays: {}\n"}, "bad")
	repo.Tag("v2", bad)
	repo.Branch("next", bad)
	sc := engine.PolicySourceConfig{Name: "org", Library: "org-policies", Version: "v1", Path: "policies", Refresh: time.Minute, StaleAfter: time.Hour}
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Policy: engine.PolicyOptions{Sources: []engine.PolicySourceConfig{sc}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	m, err := NewManagerWithLibraries(eng, filepath.Join(dir, "data"), nil, []Library{{Name: "org-policies", URL: repo.Bare}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	l := m.LoadPolicySource(ctx, sc)
	if l.Refused != nil || l.Unreachable != nil || l.Commit != v1 || len(l.Policies) != 1 || l.Policies[0].Version != v1 ||
		l.Policies[0].File != "policies/org.yaml" || l.Policies[0].Source != "org" {
		t.Fatalf("%+v", l)
	}
	if err := m.RefreshPolicySource(ctx, sc); err != nil {
		t.Fatal(err)
	}
	o, err := eng.PolicyOverview(nil, 1)
	if err != nil || len(o.Sources) != 1 || o.Sources[0].Error != "" || o.Sources[0].Version != v1 || len(o.Sources[0].Policies) != 1 {
		t.Fatalf("%+v %v", o, err)
	}

	for _, c := range []struct{ version, want string }{
		{"v2", `policies/team.yaml:5: policies[0].rules.no_fridays: unknown rule "no_fridays"`},
		{"next", "next is a branch of org-policies; pin policies to a tag or a full commit id"},
		{"v9", `the library org-policies has no tag or commit "v9"`},
	} {
		x := sc
		x.Version = c.version
		l := m.LoadPolicySource(ctx, x)
		if l.Refused == nil || !strings.Contains(l.Refused.Error(), c.want) {
			t.Fatalf("%s: %+v", c.version, l)
		}
	}
	x := sc
	x.Path = "nothing"
	if l := m.LoadPolicySource(ctx, x); l.Refused == nil || !strings.Contains(l.Refused.Error(), "there are no policy files (*.yaml) under nothing/") {
		t.Fatalf("%+v", l)
	}

	// The repository goes away: the source is unreachable, and the version
	// loaded before stays in use.
	m2, err := NewManagerWithLibraries(eng, filepath.Join(dir, "data2"), nil, []Library{{Name: "org-policies", URL: filepath.Join(dir, "gone.git")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if l := m2.LoadPolicySource(ctx, sc); l.Unreachable == nil || l.Refused != nil {
		t.Fatalf("%+v", l)
	}
	if err := m2.RefreshPolicySource(ctx, sc); err != nil {
		t.Fatal(err)
	}
	o, _ = eng.PolicyOverview(nil, 1)
	if s := o.Sources[0]; s.Error != "" || s.FetchError == "" || s.Version != v1 {
		t.Fatalf("%+v", s)
	}
}
