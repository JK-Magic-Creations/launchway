package release

import (
	"strings"
	"testing"
	"time"
)

func TestSemverOrderingFollowsTheSpecification(t *testing.T) {
	// The ordering example of semver.org section 11.
	want := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	got := append([]string(nil), want...)
	for i, j := 0, len(got)-1; i < j; i, j = i+1, j-1 {
		got[i], got[j] = got[j], got[i]
	}
	Sort(SchemeSemver, got)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("order\n got %v\nwant %v", got, want)
	}
	if Compare(SchemeSemver, "1.0.0+a", "1.0.0+b") != 0 {
		t.Error("build metadata must not affect the order")
	}
	if Compare(SchemeSemver, "1.10.0", "1.9.0") <= 0 {
		t.Error("1.10.0 is after 1.9.0")
	}
}

func TestVersionValidation(t *testing.T) {
	for _, bad := range []string{"", "1", "1.2", "v1.2.3", "01.2.3", "1.2.3-", "1.2.3-01", "1.2.3+", "a.b.c", "1.2.3 "} {
		if _, err := CheckVersion(SchemeSemver, bad); err == nil && bad != "1.2.3 " {
			t.Errorf("%q should be refused", bad)
		}
	}
	if v, err := CheckVersion(SchemeSemver, "1.2.3-rc.1+build.5"); err != nil || v != "1.2.3-rc.1+build.5" {
		t.Errorf("a pre-release with build metadata: %q %v", v, err)
	}
	if _, err := CheckVersion(SchemeCalver, "2026.11.21"); err != nil {
		t.Error(err)
	}
	if _, err := CheckVersion(SchemeCalver, "1.2.3"); err == nil {
		t.Error("a calendar version starts with a year")
	}
	if _, err := CheckVersion(SchemeNatural, "r12"); err != nil {
		t.Error(err)
	}
	if _, err := CheckVersion(SchemeNatural, "r 12"); err == nil {
		t.Error("spaces are refused")
	}
	if Compare(SchemeNatural, "r9", "r10") >= 0 || Compare(SchemeCalver, "2026.11.3", "2026.11.21") >= 0 {
		t.Error("digit runs compare as numbers")
	}
}

func TestMayReleaseNeedsHigherUnlessAllowed(t *testing.T) {
	if err := MayRelease(SchemeSemver, "1.4.0", "1.4.1", false); err != nil {
		t.Error(err)
	}
	if err := MayRelease(SchemeSemver, "1.4.0", "1.3.9", false); err == nil {
		t.Error("lower must be refused")
	}
	if err := MayRelease(SchemeSemver, "1.4.0", "1.3.9", true); err != nil {
		t.Error("lower is allowed when asked for (a maintenance branch)")
	}
	if err := MayRelease(SchemeSemver, "1.4.0", "1.4.0+x", true); err == nil {
		t.Error("the same version is never allowed")
	}
	if err := MayRelease(SchemeCreated, "b", "a", false); err != nil {
		t.Error("the created scheme orders by time")
	}
}

func TestProposalReasoning(t *testing.T) {
	ch := func(title string, labels ...string) Change {
		return Change{Kind: "pull", Ref: 1, Title: title, Labels: labels}
	}
	cases := []struct {
		last    string
		changes []Change
		bump    Bump
		next    string
	}{
		{"1.4.2", []Change{ch("fix: a crash")}, BumpPatch, "1.4.3"},
		{"1.4.2", []Change{ch("fix: a crash"), ch("feat(ui): a page")}, BumpMinor, "1.5.0"},
		{"1.4.2", []Change{ch("feat: a page"), ch("refactor!: the API")}, BumpMajor, "2.0.0"},
		{"1.4.2", []Change{ch("tidy up", "breaking")}, BumpMajor, "2.0.0"},
		{"1.4.2", []Change{{Kind: "commit", Title: "x", Body: "BREAKING CHANGE: the flag is gone"}}, BumpMajor, "2.0.0"},
		{"0.9.0", []Change{ch("feat!: reshape")}, BumpMinor, "0.10.0"},
		{"1.4.2", []Change{ch("improve", "enhancement")}, BumpMinor, "1.5.0"},
	}
	for _, c := range cases {
		p := Propose(SchemeSemver, c.last, c.changes)
		if p.Bump != c.bump || p.Next != c.next || len(p.Reasons) != len(c.changes) || p.Summary == "" {
			t.Errorf("%s %v: got %s %s (%d reasons)", c.last, c.changes, p.Bump, p.Next, len(p.Reasons))
		}
		if p.Reasons[0].Why == "" {
			t.Error("each reason says why")
		}
	}
	if p := Propose(SchemeSemver, "1.0.0", nil); p.Next != "" || len(p.Warnings) == 0 {
		t.Errorf("nothing changed, nothing proposed: %+v", p)
	}
	if p := Propose(SchemeSemver, "", []Change{ch("feat: x")}); p.Next != "0.1.0" {
		t.Errorf("first release: %s", p.Next)
	}
	if p := Propose(SchemeCalver, "2026.11.1", []Change{ch("feat: x")}); p.Next != "" {
		t.Error("a calendar version is not guessed")
	}
	if got := NextPre("1.5.0", "rc", []string{"1.5.0-rc.1", "1.5.0-rc.2", "1.4.0"}); got != "1.5.0-rc.3" {
		t.Errorf("next pre-release: %s", got)
	}
}

func TestNotesAreGroupedAndLinked(t *testing.T) {
	changes := []Change{
		{Kind: "pull", Ref: 12, Title: "feat(web): the matrix", Author: "ann", URL: "https://example.test/pulls/12"},
		{Kind: "pull", Ref: 13, Title: "fix: a crash on start", Author: "bob"},
		{Kind: "pull", Ref: 14, Title: "security: close the hole", Author: "ann", Labels: []string{"security"}},
		{Kind: "pull", Ref: 15, Title: "chore(deps): bump x", Author: "dependabot[bot]", Labels: []string{"dependencies"}},
		{Kind: "commit", SHA: "0123456789abcdef", Title: "reshape the API", Body: "BREAKING CHANGE: gone", Author: "bob"},
		{Kind: "commit", SHA: "fedcba9876543210", Title: "docs: wording"},
	}
	n := Render("1.5.0", changes, "pull requests and commits")
	md := n.Markdown
	order := []string{"### Breaking changes", "### Security", "### Features", "### Fixes", "### Dependencies", "### Other changes", "### Contributors"}
	last := -1
	for _, h := range order {
		i := strings.Index(md, h)
		if i < 0 || i < last {
			t.Fatalf("%s missing or out of order in:\n%s", h, md)
		}
		last = i
	}
	for _, s := range []string{"[#12](https://example.test/pulls/12)", "- the matrix", "(0123456)", "ann, bob"} {
		if !strings.Contains(md, s) {
			t.Errorf("missing %q in:\n%s", s, md)
		}
	}
	if strings.Contains(strings.Join(n.Contributors, ","), "dependabot") {
		t.Error("a bot is not listed as a contributor")
	}
	if strings.Contains(link("x", "javascript:alert(1)"), "javascript") {
		t.Error("only http(s) links")
	}
}

func TestChangelogEntry(t *testing.T) {
	got, err := ChangelogEntry("# Changelog\n\n## 1.0.0\n\n- first\n", "1.1.0", "## 1.1.0\n\n- second\n")
	if err != nil || !strings.Contains(got, "## 1.1.0") || strings.Index(got, "1.1.0") > strings.Index(got, "1.0.0") || !strings.HasPrefix(got, "# Changelog") {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := ChangelogEntry(got, "1.1.0", "## 1.1.0\n"); err == nil {
		t.Error("a version already there is refused")
	}
	if g, _ := ChangelogEntry("", "1.0.0", "x\n"); !strings.HasPrefix(g, "# Changelog") {
		t.Error("a new file gets a title")
	}
}

func TestWindowAndFreeze(t *testing.T) {
	w := Window{Days: []string{"mon", "tue", "wed", "thu"}, From: "09:00", To: "16:00", Zone: "America/Chicago"}
	if err := w.Check(); err != nil {
		t.Fatal(err)
	}
	chi, _ := time.LoadLocation("America/Chicago")
	thu := time.Date(2026, 11, 19, 10, 0, 0, 0, chi) // a Thursday
	if ok, _ := w.Allows(thu); !ok {
		t.Error("Thursday 10:00 is inside")
	}
	fri := time.Date(2026, 11, 20, 10, 0, 0, 0, chi)
	ok, next := w.Allows(fri)
	if ok || next.In(chi).Weekday() != time.Monday || next.In(chi).Hour() != 9 {
		t.Errorf("Friday is outside; next opening Monday 09:00, got %v %v", ok, next.In(chi))
	}
	if ok, _ := w.Allows(time.Date(2026, 11, 19, 16, 0, 0, 0, chi)); ok {
		t.Error("the end is exclusive")
	}
	for _, bad := range []Window{{Days: []string{"funday"}}, {From: "09:00"}, {From: "17:00", To: "09:00"}, {Zone: "Nowhere/Land"}, {From: "9:00", To: "10:00"}} {
		if bad.Check() == nil {
			t.Errorf("%+v should be refused", bad)
		}
	}
	f := Freeze{ID: "f1", Project: "shop", From: thu.UnixMilli(), To: thu.Add(time.Hour).UnixMilli(), Reason: "audit"}
	if !f.Covers("shop", "prod", thu.Add(time.Minute)) || f.Covers("other", "prod", thu.Add(time.Minute)) || f.Covers("shop", "prod", thu.Add(2*time.Hour)) {
		t.Error("a freeze covers its project and period only")
	}
	f.Airfield = "prod"
	if f.Covers("shop", "dev", thu.Add(time.Minute)) {
		t.Error("a freeze of one Airfield leaves the others")
	}
}

func TestChecklist(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	g := Gate{RequireChecks: true, RequireNotes: true, RequireNoBlock: true, SoakMinutes: 60, PreviousAirfield: "staging", Clearances: 2,
		Window: &Window{Days: []string{"thu"}, From: "09:00", To: "17:00"}}
	facts := Facts{Now: now, Project: "shop", Airfield: "prod", State: "published", HasNotes: true, ChecksGreen: true,
		InPrevious: true, PreviousHealthy: true, PreviousSince: now.Add(-2 * time.Hour), Clearances: []string{"ann", "bob"}}
	items := Evaluate(g, facts)
	if !Ready(items) {
		t.Fatalf("everything is met: %+v", items)
	}
	byID := func(items []Item, id string) Item {
		for _, i := range items {
			if i.ID == id {
				return i
			}
		}
		t.Fatalf("no item %s", id)
		return Item{}
	}
	facts.PreviousSince = now.Add(-10 * time.Minute)
	if it := byID(Evaluate(g, facts), "soak"); it.Status != Waiting || !strings.Contains(it.Detail, "ready at") {
		t.Errorf("soak: %+v", it)
	}
	facts.PreviousSince = now.Add(-2 * time.Hour)
	facts.Clearances = []string{"ann"}
	if byID(Evaluate(g, facts), "clearance").Status != Waiting {
		t.Error("one of two clearances waits")
	}
	facts.Clearances = []string{"ann", "bob"}
	facts.Now = now.Add(24 * time.Hour) // Friday
	items = Evaluate(g, facts)
	if byID(items, "window").Status != Waiting || Ready(items) {
		t.Error("outside the window")
	}
	if _, over := Blockers(items); !over {
		t.Error("a window may be overridden")
	}
	facts.Now = now
	facts.Freezes = []Freeze{{Project: "", From: now.Add(-time.Hour).UnixMilli(), To: now.Add(time.Hour).UnixMilli(), Reason: "release freeze"}}
	items = Evaluate(g, facts)
	if byID(items, "freeze").Status != Fail || !strings.Contains(byID(items, "freeze").Detail, "release freeze") {
		t.Error("freeze fails the checklist")
	}
	facts.Freezes = nil
	facts.ChecksGreen, facts.BlockingFindings, facts.HasNotes = false, 3, false
	items = Evaluate(g, facts)
	list, over := Blockers(items)
	if over || len(list) != 3 {
		t.Errorf("checks, notes and findings cannot be overridden: %d %v", len(list), over)
	}
	facts.State = "draft"
	if byID(Evaluate(g, facts), "state").Status != Fail {
		t.Error("a draft is never promoted")
	}
	if (Gate{SoakMinutes: -1}).Check() == nil || (Gate{ClearanceRoles: []string{"viewer"}}).Check() == nil {
		t.Error("gate validation")
	}
}

func TestMeasuresMath(t *testing.T) {
	d0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return d0.Add(time.Duration(h) * time.Hour) }
	ls := []Launch{
		{Project: "a", Airfield: "prod", At: at(10), Merged: at(8)},                                  // lead 2h
		{Project: "a", Airfield: "prod", At: at(30), Merged: at(24)},                                 // lead 6h
		{Project: "a", Airfield: "prod", At: at(50), Merged: at(40), Failed: true, Restored: at(53)}, // lead 10h, restored in 3h
		{Project: "a", Airfield: "prod", At: at(70), Failed: true},                                   // not restored
		{Project: "a", Airfield: "prod", At: at(24 * 30)},                                            // outside
	}
	m := Compute(ls, d0, d0.Add(14*24*time.Hour))
	if m.Launches != 4 || m.Failures != 2 || m.ChangeFailureRate != 0.5 {
		t.Errorf("counts: %+v", m)
	}
	if m.LeadTimeMedianSec != 6*3600 || m.LeadTimeCount != 3 {
		t.Errorf("lead time median should be 6h over 3: %+v", m)
	}
	if m.RestoreMedianSec != 3*3600 || m.RestoreCount != 1 || m.Unrestored != 1 {
		t.Errorf("restore: %+v", m)
	}
	if m.FrequencyPerWeek != 1 { // two good launches in two weeks
		t.Errorf("frequency: %v", m.FrequencyPerWeek)
	}
	if e := Compute(nil, d0, d0.Add(time.Hour)); e.ChangeFailureRate != 0 || e.LeadTimeMedianSec != 0 {
		t.Error("empty period is zero, not NaN")
	}
	s := Series(ls, d0, d0.Add(14*24*time.Hour), 7*24*time.Hour)
	if len(s) != 2 || s[0].Launches != 4 || s[1].Launches != 0 {
		t.Errorf("series: %+v", s)
	}
}
