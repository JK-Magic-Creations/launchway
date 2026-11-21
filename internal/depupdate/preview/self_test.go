package preview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/ecosystems"
	"github.com/jk-magic-creations/launchway/internal/depupdate/osv"
	"github.com/jk-magic-creations/launchway/internal/depupdate/plan"
	"github.com/jk-magic-creations/launchway/internal/depupdate/source"
	"github.com/jk-magic-creations/launchway/internal/depupdate/version"
)

var now = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) // a Monday after 06:00 Chicago

// registry is a recorded-shape registry: for every dependency the repository
// has, the versions a real registry would list around the one in use. No
// network is used.
type registry struct {
	eco  string
	deps map[string]depupdate.Dep
}

func (r registry) Ecosystem() string { return r.eco }

func (r registry) Versions(_ context.Context, name string) ([]source.Release, error) {
	d, ok := r.deps[name]
	if !ok {
		return nil, source.ErrNotFound
	}
	return r.releases(d), nil
}

func (r registry) Detail(_ context.Context, name, v string) (source.Release, error) {
	d, ok := r.deps[name]
	if !ok {
		return source.Release{}, source.ErrNotFound
	}
	for _, rel := range r.releases(d) {
		if rel.Version == v {
			return rel, nil
		}
	}
	return source.Release{}, source.ErrNotFound
}

func ago(days int) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }

// releases: the current version, a patch (40 days old), a minor (20 days), a
// major (10 days) and a patch published yesterday.
func (r registry) releases(d depupdate.Dep) []source.Release {
	cur := d.Current
	if d.Ecosystem == depupdate.Container || d.Ecosystem == depupdate.Workflow {
		cur = d.Constraint
		if d.Ecosystem == depupdate.Workflow {
			cur = d.Current
		}
	}
	cur = strings.TrimPrefix(cur, "v")
	switch d.Ecosystem {
	case depupdate.Container, depupdate.Workflow:
		t := version.ParseTag(map[bool]string{true: "v", false: ""}[d.Ecosystem == depupdate.Workflow] + cur)
		if !t.OK {
			return nil
		}
		bump := func(i int) string {
			n := append([]uint64(nil), t.Nums...)
			n[i]++
			for j := i + 1; j < len(n); j++ {
				n[j] = 0
			}
			var p []string
			for _, x := range n {
				p = append(p, fmt.Sprint(x))
			}
			return t.Prefix + strings.Join(p, ".") + t.Variant
		}
		rels := []source.Release{{Version: t.Raw, Time: ago(400), Digest: strings.Repeat("a", 40)}}
		last := len(t.Nums) - 1
		rels = append(rels, source.Release{Version: bump(last), Time: ago(40), Digest: strings.Repeat("b", 40)})
		if last >= 1 {
			rels = append(rels, source.Release{Version: bump(1), Time: ago(20), Digest: strings.Repeat("c", 40)})
		}
		if r.eco == "container" && d.Digest != "" {
			for i := range rels {
				rels[i].Digest = "sha256:" + strings.Repeat(string(rune('a'+i)), 64)
			}
		}
		return rels
	}
	v, ok := version.ParseSemver(cur)
	if !ok {
		return nil
	}
	pre := ""
	if strings.HasPrefix(d.Current, "v") {
		pre = "v"
	}
	f := func(a, b, c uint64) string { return fmt.Sprintf("%s%d.%d.%d", pre, a, b, c) }
	major := source.Release{Version: f(v.Major+1, 0, 0), Time: ago(10)}
	if d.Ecosystem == depupdate.Go {
		major.Module = fmt.Sprintf("%s/v%d", strings.TrimSuffix(d.Name, fmt.Sprintf("/v%d", v.Major)), v.Major+1) // a new major is a new module path
	}
	return []source.Release{
		{Version: f(v.Major, v.Minor, v.Patch), Time: ago(400)},
		{Version: f(v.Major, v.Minor, v.Patch+1), Time: ago(40)},
		{Version: f(v.Major, v.Minor+1, 0), Time: ago(20)},
		major,
		{Version: f(v.Major, v.Minor, v.Patch+2), Time: ago(1)},
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".launchway", "updates.yaml")); err != nil {
		t.Fatal("the repository's own .launchway/updates.yaml is missing: " + err.Error())
	}
	return root
}

func TestPreviewOfLaunchwaysOwnRepository(t *testing.T) {
	root := repoRoot(t)
	tree := os.DirFS(root)
	// Discovery first, to build the recorded registries from what is really there.
	pol, err := plan.ParseFile(mustRead(t, filepath.Join(root, plan.FilePath)))
	if err != nil {
		t.Fatal(err)
	}
	merged, err := plan.Merge(plan.Layer{Level: "defaults", Partial: plan.Defaults()}, pol)
	if err != nil {
		t.Fatal(err)
	}
	disc, err := depupdate.Discover(tree, ecosystems.Parsers(merged.ImageFiles), depupdate.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]map[string]depupdate.Dep{}
	for _, d := range disc.Deps {
		if by[d.Ecosystem] == nil {
			by[d.Ecosystem] = map[string]depupdate.Dep{}
		}
		by[d.Ecosystem][d.Name] = d
	}
	srcs := map[string]source.Source{}
	for eco, m := range by {
		srcs[eco] = registry{eco, m}
	}
	// One Go module is affected by a known vulnerability, fixed in its next patch.
	var vulnerable depupdate.Dep
	for _, d := range by["go"] {
		if d.Direct && (vulnerable.Name == "" || d.Name < vulnerable.Name) {
			vulnerable = d
		}
	}
	cur, _ := version.ParseSemver(vulnerable.Current)
	fixed := fmt.Sprintf("%d.%d.%d", cur.Major, cur.Minor, cur.Patch+1)
	adv := fmt.Sprintf(`{"id":"GHSA-test-0001","summary":"Test advisory","database_specific":{"severity":"HIGH"},"affected":[{"package":{"ecosystem":"Go","name":%q},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":%q}]}]}]}`, vulnerable.Name, fixed)
	idx := &osv.DirIndex{FS: fstest.MapFS{"GHSA-test-0001.json": {Data: []byte(adv)}}}

	rep, err := Run(context.Background(), Input{FS: tree, Sources: srcs, OSV: idx, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if rep.FileError != "" {
		t.Fatalf("the repository's settings file is not valid: %s", rep.FileError)
	}
	// The file's settings apply.
	p := rep.Policy
	if p.Limits.MaxOpenPerEcosystem != 2 || p.Limits.MaxPerRun != 3 || p.CooldownDays != 3 || p.Schedule.Interval != "weekly" || p.Ecosystems["python"].Enabled {
		t.Fatalf("policy: %+v", p)
	}
	if len(rep.Errors) != 0 {
		t.Errorf("unreadable files: %+v", rep.Errors)
	}
	// Every ecosystem this repository uses is covered.
	seen := map[string]int{}
	for _, d := range rep.Updates {
		seen[d.Dep.Ecosystem]++
	}
	for _, e := range []string{"go", "npm", "container", "workflow"} {
		if seen[e] == 0 {
			t.Errorf("no %s dependencies found: %v", e, seen)
		}
	}
	if seen["python"] != 0 {
		t.Error("python is switched off in the file")
	}
	// The tool images named in scripts/security are found (and the digest-pinned one is kept by digest).
	var gitleaks bool
	for _, d := range rep.Updates {
		if strings.HasPrefix(d.Dep.File, "scripts/security/") && strings.Contains(d.Dep.Name, "gitleaks") && d.Dep.Digest != "" {
			gitleaks = true
		}
	}
	if !gitleaks {
		t.Error("the gitleaks image pinned by digest in scripts/security/lib.sh was not found")
	}
	// What would be opened.
	var opened, queued, security int
	perEco := map[string]int{}
	keys := map[string]bool{}
	for _, d := range rep.Decisions {
		keys[d.Key] = true
		switch d.Action {
		case plan.ActionOpen:
			opened++
			perEco[d.Ecosystem]++
			if d.Security {
				security++
			}
		case plan.ActionQueue:
			queued++
			if d.Why == "" {
				t.Errorf("%s queued without a reason", d.Key)
			}
		}
	}
	if security != 1 || rep.Decisions[0].Key != "go-security-"+strings.NewReplacer("/", "-", ".", "-").Replace(strings.ToLower(vulnerable.Name)) || rep.Decisions[0].Action != plan.ActionOpen {
		t.Errorf("the vulnerable module's fix must come first: %+v", rep.Decisions[0])
	}
	if opened != p.Limits.MaxPerRun {
		t.Errorf("%d would open; the run limit of %d should be reached with this many updates", opened, p.Limits.MaxPerRun)
	}
	for _, d := range rep.Updates {
		if d.Dep.Ecosystem == "go" && d.Major != nil {
			t.Errorf("%s: a Go major version changes the import path and is never proposed automatically", d.Dep.Name)
		}
	}
	if opened > p.Limits.MaxPerRun {
		t.Errorf("%d opened, the run limit is %d", opened, p.Limits.MaxPerRun)
	}
	for e, n := range perEco {
		if n > 2 {
			t.Errorf("%d %s pull requests would open; the limit per ecosystem is 2", n, e)
		}
	}
	for _, want := range []string{"go-minor-and-patch", "npm-minor-and-patch-web", "npm-minor-and-patch-docs"} {
		if !keys[want] {
			t.Errorf("expected a group proposal %s, have %v", want, sortedKeys(keys))
		}
	}
	if queued == 0 {
		t.Error("with this many updates the limits must queue some")
	}
	// A repository file with a mistake is reported, and the preview still runs.
	broken := fstest.MapFS{plan.FilePath: {Data: []byte("version: 1\ncooldwn_days: 1\n")}, "go.mod": {Data: []byte("module x\n\nrequire a/b v1.0.0\n")}}
	r2, err := Run(context.Background(), Input{FS: broken, Sources: map[string]source.Source{}, Now: now})
	if err != nil || r2.FileError == "" || !r2.Policy.Enabled {
		t.Errorf("broken settings file: %v %q", err, r2.FileError)
	}
	t.Logf("preview: %d dependencies, %d would open, %d queued", len(rep.Updates), opened, queued)
	for _, d := range rep.Decisions {
		t.Logf("%-7s %-45s %d updates  %s", d.Action, d.Key, len(d.Updates), d.Why)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sortedKeys(m map[string]bool) []string {
	var o []string
	for k := range m {
		o = append(o, k)
	}
	sort.Strings(o)
	return o
}
