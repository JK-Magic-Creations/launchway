package plan

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/osv"
	"github.com/jk-magic-creations/launchway/internal/depupdate/source"
	"github.com/jk-magic-creations/launchway/internal/depupdate/version"
)

var now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) // a Saturday

// fake is a Source with canned releases; times are days before now.
type fake struct {
	eco  string
	rels map[string][]source.Release
}

func (f fake) Ecosystem() string { return f.eco }
func (f fake) Versions(_ context.Context, n string) ([]source.Release, error) {
	r, ok := f.rels[n]
	if !ok {
		return nil, source.ErrNotFound
	}
	out := make([]source.Release, len(r))
	for i, x := range r {
		out[i] = source.Release{Version: x.Version, Module: x.Module, Yanked: x.Yanked, Deprecated: x.Deprecated}
	}
	return out, nil
}
func (f fake) Detail(_ context.Context, n, v string) (source.Release, error) {
	for _, r := range f.rels[n] {
		if r.Version == v {
			return r, nil
		}
	}
	return source.Release{}, source.ErrNotFound
}

func rel(v string, ageDays int) source.Release {
	return source.Release{Version: v, Time: now.Add(-time.Duration(ageDays) * 24 * time.Hour)}
}

func defaults(t *testing.T, over ...Layer) Policy {
	t.Helper()
	p, err := Merge(append([]Layer{{Level: "defaults", Partial: Defaults()}}, over...)...)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func resolver(p Policy) *Resolver {
	return &Resolver{Policy: p, Now: func() time.Time { return now }, Sources: map[string]source.Source{
		"go": fake{"go", map[string][]source.Release{
			"example.com/lib": {rel("v1.4.0", 400), rel("v1.4.2", 100), rel("v1.5.0", 50), rel("v1.6.0-rc.1", 5), rel("v0.0.0-20260101000000-abcdef123456", 3), {Version: "v2.0.0", Module: "example.com/lib/v2"}},
			"example.com/two": {rel("v1.0.0", 300), rel("v1.0.1", 1)}, // only a day old
			"example.com/old": {rel("v1.0.0", 300), rel("v1.0.1", 20), rel("v1.1.0", 10)},
		}},
		"npm": fake{"npm", map[string][]source.Release{
			"vite":     {rel("5.1.4", 300), rel("5.1.5", 30), rel("5.2.0", 20), rel("6.0.0", 10)},
			"left-pad": {rel("1.3.0", 900), rel("1.3.1", 40), {Version: "1.4.0", Deprecated: "use native"}},
		}},
		"container": fake{"container", map[string][]source.Release{
			"docker.io/library/node": {rel("22-alpine3.23", 90), rel("24.22.3-alpine3.23", 60), rel("24.23.0-alpine3.23", 30), rel("24.23.0-bookworm", 30), rel("25.0.0-rc1-alpine3.23", 2)},
		}},
		"python": fake{"python", map[string][]source.Release{
			"requests": {rel("2.28.1", 800), rel("2.31.0", 300), {Version: "2.32.0", Yanked: true}, rel("3.0.0rc1", 5)},
		}},
	}}
}

func dep(eco, name, cur, file string) depupdate.Dep {
	return depupdate.Dep{Ecosystem: eco, Name: name, Current: cur, Constraint: cur, Direct: true, File: file, Line: 3, Group: "prod"}
}

func byName(us []Update, n string) Update {
	for _, u := range us {
		if u.Dep.Name == n {
			return u
		}
	}
	return Update{}
}

func TestResolveRoutine(t *testing.T) {
	p := defaults(t)
	deps := []depupdate.Dep{
		dep("go", "example.com/lib", "v1.4.2", "go.mod"),
		dep("go", "example.com/two", "v1.0.0", "go.mod"),
		dep("go", "example.com/old", "v1.0.0", "go.mod"),
		dep("npm", "vite", "5.1.4", "web/package.json"),
		dep("npm", "left-pad", "1.3.0", "web/package.json"),
		dep("container", "docker.io/library/node", "24.22.3-alpine3.23", "Dockerfile"),
		dep("python", "requests", "2.28.1", "requirements.txt"),
		{Ecosystem: "go", Name: "indirect", Current: "v1.0.0", Direct: false, File: "go.mod"},
	}
	ups := resolver(p).Resolve(context.Background(), deps, nil)
	type want struct{ status, target string }
	for n, w := range map[string]want{
		"example.com/lib":        {StatusUpdate, "v1.5.0"}, // rc and pseudo skipped; v2 is a different module
		"example.com/two":        {StatusHeld, ""},         // 1 day old, cooldown 3
		"example.com/old":        {StatusUpdate, "v1.1.0"}, // 10 days old, past the cooldown
		"vite":                   {StatusUpdate, "5.2.0"},
		"left-pad":               {StatusUpdate, "1.3.1"},              // 1.4.0 deprecated
		"docker.io/library/node": {StatusUpdate, "24.23.0-alpine3.23"}, // not the bookworm variant, not the rc
		"requests":               {StatusUpdate, "2.31.0"},             // 2.32 yanked, 3.0rc1 pre
		"indirect":               {StatusTransitive, ""},
	} {
		u := byName(ups, n)
		if u.Status != w.status || u.Target != w.target {
			t.Errorf("%s: %s %q (%s), want %s %q", n, u.Status, u.Target, u.Reason, w.status, w.target)
		}
	}
	if u := byName(ups, "example.com/lib"); u.Kind != version.KindMinor || !strings.Contains(u.Note, "v2.0.0") {
		t.Errorf("lib: %+v", u)
	}
	if u := byName(ups, "example.com/two"); !u.HeldUntil.Equal(rel("v1.0.1", 1).Time.Add(72 * time.Hour)) {
		t.Errorf("held until %v", u.HeldUntil)
	}
	if u := byName(ups, "vite"); u.Kind != version.KindMinor || u.Major == nil || u.Major.Target != "6.0.0" || u.AgeDays(now) != 20 {
		t.Errorf("vite: %+v", u)
	}
	// the in-major update is grouped, the major is its own pull request
	ds := Plan(Input{Policy: p, Now: now, Immediate: true, Updates: []Update{byName(ups, "vite")}})
	keys := map[string]bool{}
	for _, d := range ds {
		keys[d.Key] = true
	}
	if !keys["npm-minor-and-patch-web"] || !keys["npm-vite-major-web"] || len(keys) != 2 {
		t.Errorf("proposals: %v", keys)
	}
}

func TestCooldownFallsBackToAnOlderVersion(t *testing.T) {
	p := defaults(t)
	r := resolver(p)
	r.Sources["go"] = fake{"go", map[string][]source.Release{"x/y": {rel("v1.0.0", 100), rel("v1.1.0", 30), rel("v1.2.0", 1)}}}
	u := r.Resolve(context.Background(), []depupdate.Dep{dep("go", "x/y", "v1.0.0", "go.mod")}, nil)[0]
	if u.Status != StatusUpdate || u.Target != "v1.1.0" {
		t.Errorf("want the newest version past the wait: %+v", u)
	}
	p.CooldownDays = 0
	r.Policy = p
	if u := r.Resolve(context.Background(), []depupdate.Dep{dep("go", "x/y", "v1.0.0", "go.mod")}, nil)[0]; u.Target != "v1.2.0" {
		t.Errorf("no wait: %+v", u)
	}
}

func vuln(id, name, fixed string) osv.Match {
	v, _ := osv.Raw([]byte(fmt.Sprintf(`{"id":%q,"database_specific":{"severity":"HIGH"},"affected":[{"package":{"ecosystem":"Go","name":%q},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":%q}]}]}]}`, id, name, fixed)))
	return osv.Match{ID: id, Vuln: v, Severity: "high", Score: 8, FixedIn: fixed}
}

func TestSecurityUpdateIgnoresWaitAndPicksTheLowestFix(t *testing.T) {
	p := defaults(t)
	r := resolver(p)
	r.Sources["go"] = fake{"go", map[string][]source.Release{"x/y": {rel("v1.0.0", 100), rel("v1.0.5", 60), rel("v1.0.6", 1), rel("v1.1.0", 1)}}}
	d := dep("go", "x/y", "v1.0.0", "go.mod")
	m := vuln("GHSA-1", "x/y", "1.0.5")
	m.Dep = d
	u := r.Resolve(context.Background(), []depupdate.Dep{d}, []osv.Match{m})[0]
	if u.Status != StatusSecurity || u.Target != "v1.0.5" || !u.Security || !strings.Contains(u.Reason, "GHSA-1") {
		t.Errorf("%+v", u)
	}
	// the only fix is brand new: still proposed, the wait does not apply to fixes
	r.Sources["go"] = fake{"go", map[string][]source.Release{"x/y": {rel("v1.0.0", 100), rel("v1.0.5", 0)}}}
	if u := r.Resolve(context.Background(), []depupdate.Dep{d}, []osv.Match{m})[0]; u.Status != StatusSecurity || u.Target != "v1.0.5" {
		t.Errorf("%+v", u)
	}
	// vulnerable, nothing fixed
	m2 := vuln("GHSA-2", "x/y", "")
	m2.FixedIn, m2.Dep = "", d
	r.Sources["go"] = fake{"go", map[string][]source.Release{"x/y": {rel("v1.0.0", 100)}}}
	if u := r.Resolve(context.Background(), []depupdate.Dep{d}, []osv.Match{m2})[0]; u.Status != StatusVulnerable || u.Target != "" {
		t.Errorf("%+v", u)
	}
}

func TestPolicyRules(t *testing.T) {
	major := 5
	p := defaults(t, Layer{Level: "repository", Partial: Partial{
		Ignore:      &[]Ignore{{Name: "left-pad", Reason: "pinned on purpose"}, {Name: "example.com/*", Kinds: []string{"minor"}}},
		Constraints: &[]Constraint{{Name: "vite", Major: &major}},
	}})
	ups := resolver(p).Resolve(context.Background(), []depupdate.Dep{
		dep("npm", "left-pad", "1.3.0", "web/package.json"), dep("npm", "vite", "5.1.4", "web/package.json"), dep("go", "example.com/lib", "v1.4.2", "go.mod"),
	}, nil)
	if u := byName(ups, "left-pad"); u.Status != StatusIgnored || !strings.Contains(u.Reason, "pinned on purpose") {
		t.Errorf("%+v", u)
	}
	if u := byName(ups, "vite"); u.Target != "5.2.0" {
		t.Errorf("stay on major 5: %+v", u)
	}
	if u := byName(ups, "example.com/lib"); u.Status != StatusIgnored {
		t.Errorf("minor ignored: %+v", u)
	}
	// a person closed it: ignore this major
	r := resolver(defaults(t))
	r.Closed = map[string]bool{"npm|vite|major-6": true}
	if u := r.Resolve(context.Background(), []depupdate.Dep{dep("npm", "vite", "5.1.4", "web/package.json")}, nil)[0]; u.Target != "5.2.0" {
		t.Errorf("ignored major: %+v", u)
	}
	r.Closed["npm|vite|*"] = true
	if u := r.Resolve(context.Background(), []depupdate.Dep{dep("npm", "vite", "5.1.4", "web/package.json")}, nil)[0]; u.Status != StatusIgnored {
		t.Errorf("ignored dependency: %+v", u)
	}
}

func TestDigestRefresh(t *testing.T) {
	r := resolver(defaults(t))
	old := "sha256:" + strings.Repeat("1", 64)
	nu := "sha256:" + strings.Repeat("2", 64)
	r.Sources["container"] = fake{"container", map[string][]source.Release{"ghcr.io/x/tool": {{Version: "v8.30.1", Digest: nu, Time: now.Add(-10 * 24 * time.Hour)}}}}
	d := dep("container", "ghcr.io/x/tool", "v8.30.1", "scripts/security/lib.sh")
	d.Digest = old
	u := r.Resolve(context.Background(), []depupdate.Dep{d}, nil)[0]
	if u.Status != StatusUpdate || u.Digest != nu || u.Target != "v8.30.1" || u.Kind != version.KindOther {
		t.Errorf("%+v", u)
	}
	d.Digest = nu
	if u := r.Resolve(context.Background(), []depupdate.Dep{d}, nil)[0]; u.Status != StatusUpToDate {
		t.Errorf("same digest: %+v", u)
	}
}

func TestGrouping(t *testing.T) {
	p := defaults(t, Layer{Level: "repository", Partial: Partial{Groups: &[]Group{
		{Name: "go-minor-and-patch", Ecosystem: "go"},
		{Name: "web", Ecosystem: "npm", Directory: "web"},
	}}})
	up := func(eco, name, file string, k version.Kind, sec bool) Update {
		return Update{Dep: dep(eco, name, "1.0.0", file), Status: StatusUpdate, Target: "1.1.0", Kind: k, Security: sec}
	}
	in := Input{Policy: p, Now: now, Immediate: true, Updates: []Update{
		up("go", "a", "go.mod", version.KindMinor, false), up("go", "b", "go.mod", version.KindPatch, false), up("go", "c", "go.mod", version.KindMajor, false),
		up("npm", "d", "web/package.json", version.KindMinor, false), up("npm", "e", "web/package.json", version.KindPatch, false),
		up("npm", "f", "docs/package.json", version.KindMinor, false), // no group for docs: its own
		up("go", "g", "go.mod", version.KindPatch, true),
	}}
	ds := Plan(in)
	keys := map[string]int{}
	for _, d := range ds {
		keys[d.Key] = len(d.Updates)
	}
	want := map[string]int{"go-go-minor-and-patch": 2, "go-c": 1, "npm-web-web": 2, "npm-f-docs": 1, "go-security-g": 1}
	for k, n := range want {
		if keys[k] != n {
			t.Errorf("%s: %d updates, want %d (all: %v)", k, keys[k], n, keys)
		}
	}
	if len(keys) != len(want) {
		t.Errorf("unexpected proposals: %v", keys)
	}
	if ds[0].Key != "go-security-g" || ds[0].Action != ActionOpen || !strings.Contains(ds[0].Branch, "launchway/update/go-security-g") {
		t.Errorf("security first: %+v", ds[0])
	}
}

func TestLimitsAndOrder(t *testing.T) {
	p := defaults(t, Layer{Level: "repository", Partial: Partial{Limits: &Limits{MaxOpen: 3, MaxPerRun: 2}, Groups: &[]Group{}}})
	var ups []Update
	for i := 0; i < 8; i++ { // the burst: eight updates
		ups = append(ups, Update{Dep: dep("go", fmt.Sprintf("m%d", i), "v1.0.0", "go.mod"), Status: StatusUpdate, Target: "v1.0.1", Kind: version.KindPatch})
	}
	ds := Plan(Input{Policy: p, Now: now, Immediate: true, Updates: ups})
	opened, queued := 0, 0
	for _, d := range ds {
		switch d.Action {
		case ActionOpen:
			opened++
		case ActionQueue:
			queued++
			if !strings.Contains(d.Why, "limit") {
				t.Errorf("a queued proposal must say why: %q", d.Why)
			}
		}
	}
	if opened != 2 || queued != 6 {
		t.Errorf("opened %d queued %d", opened, queued)
	}
	// two already open, per-run 2: only one more fits under max_open 3
	open := []Open{{Key: "go-m0", Number: 1, Targets: map[string]string{"m0": "v1.0.1"}}, {Key: "go-m1", Number: 2, Targets: map[string]string{"m1": "v1.0.1"}}}
	ds = Plan(Input{Policy: p, Now: now, Immediate: true, Updates: ups, Open: open})
	counts := map[string]int{}
	for _, d := range ds {
		counts[d.Action]++
	}
	if counts[ActionKeep] != 2 || counts[ActionOpen] != 1 || counts[ActionQueue] != 5 {
		t.Errorf("%v", counts)
	}
	// the limit never stops a security fix, but one run's cap does
	sec := []Update{}
	for i := 0; i < 3; i++ {
		sec = append(sec, Update{Dep: dep("go", fmt.Sprintf("s%d", i), "v1.0.0", "go.mod"), Status: StatusSecurity, Target: "v1.0.1", Kind: version.KindPatch, Security: true})
	}
	ds = Plan(Input{Policy: p, Now: now, Updates: sec, Open: []Open{{Key: "go-x1"}, {Key: "go-x2"}, {Key: "go-x3"}, {Key: "go-x4"}}})
	counts = map[string]int{}
	for _, d := range ds {
		counts[d.Action]++
	}
	if counts[ActionOpen] != 2 || counts[ActionQueue] != 1 || counts[ActionClose] != 4 {
		t.Errorf("security: %v", counts)
	}
}

func TestPerEcosystemLimitAndSwitches(t *testing.T) {
	p := defaults(t, Layer{Level: "repository", Partial: Partial{Limits: &Limits{MaxOpen: 10, MaxPerRun: 10, MaxOpenPerEcosystem: 2}, Groups: &[]Group{},
		Ecosystems: &map[string]Ecosystem{"go": {Enabled: true}, "npm": {Enabled: false}}}})
	var ups []Update
	for i := 0; i < 4; i++ {
		ups = append(ups, Update{Dep: dep("go", fmt.Sprintf("m%d", i), "v1.0.0", "go.mod"), Status: StatusUpdate, Target: "v1.0.1", Kind: version.KindPatch})
	}
	ups = append(ups, Update{Dep: dep("npm", "n", "1.0.0", "package.json"), Status: StatusUpdate, Target: "1.0.1", Kind: version.KindPatch})
	counts := map[string]int{}
	for _, d := range Plan(Input{Policy: p, Now: now, Immediate: true, Updates: ups}) {
		counts[d.Action]++
		if d.Ecosystem == "npm" && !strings.Contains(d.Why, "switched off") {
			t.Errorf("npm: %q", d.Why)
		}
	}
	if counts[ActionOpen] != 2 || counts[ActionQueue] != 3 {
		t.Errorf("%v", counts)
	}
}

func TestUpdateInPlaceAndClose(t *testing.T) {
	p := defaults(t, Layer{Level: "repository", Partial: Partial{Groups: &[]Group{}}})
	u := Update{Dep: dep("go", "m", "v1.0.0", "go.mod"), Status: StatusUpdate, Target: "v1.2.0", Kind: version.KindMinor}
	ds := Plan(Input{Policy: p, Now: now, Immediate: true, Updates: []Update{u}, Open: []Open{
		{Key: "go-m", Number: 7, Targets: map[string]string{"m": "v1.1.0"}},       // a newer version appeared
		{Key: "go-gone", Number: 8, Targets: map[string]string{"gone": "v1.0.1"}}, // dependency removed
	}})
	got := map[string]Decision{}
	for _, d := range ds {
		got[d.Key] = d
	}
	if got["go-m"].Action != ActionUpdate || got["go-m"].Number != 7 {
		t.Errorf("%+v", got["go-m"])
	}
	if got["go-gone"].Action != ActionClose || got["go-gone"].Number != 8 || got["go-gone"].Why == "" {
		t.Errorf("%+v", got["go-gone"])
	}
	ds = Plan(Input{Policy: p, Now: now, Immediate: true, Updates: []Update{u}, Open: []Open{{Key: "go-m", Number: 7, Targets: map[string]string{"m": "v1.2.0"}}}})
	if ds[0].Action != ActionKeep {
		t.Errorf("%+v", ds[0])
	}
}

func TestSchedule(t *testing.T) {
	p := defaults(t, Layer{Level: "repository", Partial: Partial{Groups: &[]Group{}}}) // weekly, monday 06:00 UTC
	u := Update{Dep: dep("go", "m", "v1.0.0", "go.mod"), Status: StatusUpdate, Target: "v1.0.1", Kind: version.KindPatch}
	sec := Update{Dep: dep("go", "s", "v1.0.0", "go.mod"), Status: StatusSecurity, Target: "v1.0.1", Kind: version.KindPatch, Security: true}
	// Saturday, last run on Monday 06:30: not due; routine waits, security does not
	monday := time.Date(2026, 11, 16, 6, 30, 0, 0, time.UTC)
	ds := Plan(Input{Policy: p, Now: now, LastRun: monday, Updates: []Update{u, sec}})
	if ds[0].Key != "go-security-s" || ds[0].Action != ActionOpen || ds[1].Action != ActionQueue || !strings.Contains(ds[1].Why, "every monday at 06:00 UTC") {
		t.Errorf("%+v", ds)
	}
	// last run the previous week: due
	if ds := Plan(Input{Policy: p, Now: now, LastRun: monday.AddDate(0, 0, -7), Updates: []Update{u}}); ds[0].Action != ActionOpen {
		t.Errorf("%+v", ds[0])
	}
	// never ran: due
	if !Due(now, p.Schedule, time.Time{}) {
		t.Error("never run is due")
	}
	// timezones: Monday 06:00 in Chicago is 12:00 UTC
	chi := Schedule{Interval: "weekly", Day: "monday", Hour: 6, Timezone: "America/Chicago"}
	before := time.Date(2026, 11, 23, 11, 0, 0, 0, time.UTC)
	after := time.Date(2026, 11, 23, 12, 30, 0, 0, time.UTC)
	last := time.Date(2026, 11, 16, 13, 0, 0, 0, time.UTC)
	if Due(before, chi, last) || !Due(after, chi, last) {
		t.Error("timezone")
	}
	if !Due(time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC), Schedule{Interval: "daily", Hour: 6}, time.Date(2026, 11, 21, 7, 0, 0, 0, time.UTC)) {
		t.Error("daily")
	}
	if Due(time.Date(2026, 11, 22, 5, 0, 0, 0, time.UTC), Schedule{Interval: "daily", Hour: 6}, time.Date(2026, 11, 21, 7, 0, 0, 0, time.UTC)) {
		t.Error("daily, before the hour")
	}
	if !Due(time.Date(2026, 12, 1, 7, 0, 0, 0, time.UTC), Schedule{Interval: "monthly", Hour: 6}, time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)) {
		t.Error("monthly")
	}
}

func TestMergeAndLocks(t *testing.T) {
	file, err := ParseFile([]byte(`version: 1
cooldown_days: 0
auto_merge: {enabled: true, kinds: [patch], groups: [dev]}
limits: {max_open: 9, max_per_run: 9}
labels: [deps]
`))
	if err != nil {
		t.Fatal(err)
	}
	inst := Layer{Level: "instance", Partial: Partial{Lock: []string{"auto_merge", "cooldown_days"}, AutoMerge: &AutoMerge{Enabled: false}, CooldownDays: ptr(7)}}
	p, err := Merge(Layer{Level: "defaults", Partial: Defaults()}, inst, file)
	if err != nil {
		t.Fatal(err)
	}
	if p.AutoMerge.Enabled || p.CooldownDays != 7 || p.Limits.MaxOpen != 9 || p.Labels[0] != "deps" {
		t.Errorf("%+v", p)
	}
	if p.Locked["auto_merge"] != "instance" || len(p.Ignored) != 2 || !strings.Contains(strings.Join(p.Ignored, "|"), "locked by the instance level") {
		t.Errorf("locked %v ignored %v", p.Locked, p.Ignored)
	}
	// defaults
	d := defaults(t)
	if !d.Enabled || !d.Security || !d.Routine || d.CooldownDays != 3 || d.Limits.MaxOpen != 3 || d.AutoMerge.Enabled || d.Schedule.Interval != "weekly" {
		t.Errorf("defaults: %+v", d)
	}
	// a level cannot lock what is not a setting
	if _, err := Merge(Layer{Level: "x", Partial: Partial{Lock: []string{"nonsense"}}}); err == nil {
		t.Error("unknown lock key")
	}
}

func TestParseFileErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no version":     "cooldown_days: 1\n",
		"unknown key":    "version: 1\ncooldwn_days: 1\n",
		"lock in a file": "version: 1\nlock: [limits]\n",
		"bad yaml":       "version: 1\nlimits: [\n",
	} {
		if _, err := ParseFile([]byte(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	bad, _ := ParseFile([]byte("version: 1\nschedule: {interval: hourly, hour: 30}\ngroups: [{name: x, kinds: [huge]}]\necosystems: {cargo: {enabled: true}}\n"))
	_, err := Merge(Layer{Level: "defaults", Partial: Defaults()}, bad)
	if err == nil || !strings.Contains(err.Error(), "hourly") || !strings.Contains(err.Error(), "cargo") || !strings.Contains(err.Error(), "huge") {
		t.Errorf("validation names every problem: %v", err)
	}
}

func TestGlob(t *testing.T) {
	for _, c := range []struct {
		p, n string
		ok   bool
	}{{"*", "x", true}, {"golang.org/x/*", "golang.org/x/text", true}, {"golang.org/x/*", "golang.org/y/text", false}, {"@types/*", "@types/node", true}, {"*-dev", "left-dev", true}, {"a*c*e", "abcde", true}, {"a*c*e", "abde", false}, {"React", "react", true}, {"exact", "exactly", false}} {
		if Glob(c.p, c.n) != c.ok {
			t.Errorf("%s ~ %s = %v", c.p, c.n, !c.ok)
		}
	}
}
