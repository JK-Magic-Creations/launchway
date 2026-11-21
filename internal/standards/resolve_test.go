package standards

import (
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/findings"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

var goRepo = []string{"go.mod", "main.go", "Dockerfile", ".launchway/pipeline.yaml"}

func ptr[T any](v T) *T { return &v }

func checkOf(e Effective, id string) EffectiveCheck {
	for _, c := range e.Checks {
		if c.ID == id {
			return c
		}
	}
	panic("no check " + id)
}

func TestDefaultsAreEverythingOnWithTheDefaultGates(t *testing.T) {
	e := Resolve(ResolveInput{Files: goRepo, Now: testNow})
	if e.Off.V || e.FailOn.V != findings.High || !e.RequireFix.V || e.DefaultBranch.V != Advise || e.Incomplete.V != IncompletePass {
		t.Fatalf("%+v", e)
	}
	for _, id := range []string{"secrets", "vuln-deps", "vuln-reach-go", "sast-go", "dockerfile", "pipeline-lint", "license", "sbom", "misconfig"} {
		c := checkOf(e, id)
		if !c.Active || c.Reason != "" || c.State.From.Kind != "default" {
			t.Errorf("%s: %+v", id, c)
		}
	}
	if checkOf(e, "secrets").State.V != Block || checkOf(e, "vuln-deps").State.V != Block || checkOf(e, "sast-go").State.V != Advise {
		t.Error("the default states are not the documented ones")
	}
	if c := checkOf(e, "sast-python"); c.Active || !strings.Contains(c.Reason, "nothing in the repository") {
		t.Errorf("%+v", c)
	}
	if c := checkOf(e, "image-scan"); c.Active || !strings.Contains(c.Reason, "not available yet") {
		t.Errorf("%+v", c)
	}
	if len(e.Deny.V) == 0 {
		t.Error("no default denied licenses")
	}
}

func TestNearestLevelWinsAndShowsWhatItOverrides(t *testing.T) {
	e := Resolve(ResolveInput{Files: goRepo, Now: testNow, Levels: []LevelDoc{
		{Level: LevelOrganization, Scope: "acme", Settings: Settings{Checks: map[string]State{"sast-go": Block}}},
		{Level: LevelProject, Scope: "shop", Settings: Settings{Checks: map[string]State{"sast-go": Advise}}},
	}})
	c := checkOf(e, "sast-go").State
	if c.V != Advise || c.From.Level != LevelProject || c.Locked != nil {
		t.Fatalf("%+v", c)
	}
	if len(c.Chain) != 3 || c.Chain[0].Display != "advise" || !c.Chain[1].Overridden || c.Chain[1].Source.Level != LevelOrganization || !c.Chain[2].Overridden {
		t.Errorf("chain: %+v", c.Chain)
	}
}

func TestALockHoldsAgainstEveryLevelBelowAndTheFile(t *testing.T) {
	wing := LevelDoc{Level: LevelOrganization, Scope: "acme", Settings: Settings{
		Checks: map[string]State{"secrets": Block, "sast-go": Block},
		Gate:   GateSettings{FailOn: findings.Medium, RequireFix: ptr(false)},
		Locks: map[string]string{CheckKey("secrets"): "the security team requires it", CheckKey("sast-go"): "policy 12",
			KeyFailOn: "policy 12", KeyRequireFix: "policy 12", KeyOff: "never off", KeyDeny: "legal", KeyIgnore: "no exemptions here"},
		Licenses: LicenseSettings{Deny: &[]string{"GPL-3.0"}},
	}}
	project := LevelDoc{Level: LevelProject, Scope: "shop", Settings: Settings{
		Off: ptr(true), OffReason: "we do not want this now",
		Checks:   map[string]State{"secrets": Off, "sast-go": Advise},
		Gate:     GateSettings{FailOn: findings.Critical, RequireFix: ptr(true)},
		Licenses: LicenseSettings{Deny: &[]string{}},
		Ignore:   []Ignore{{Rule: "G401", Reason: "we checked it twice", Expires: "2027-01-01"}},
	}}
	file := Settings{Checks: map[string]State{"secrets": Advise}, Gate: GateSettings{FailOn: findings.Low}}
	e := Resolve(ResolveInput{Files: goRepo, Now: testNow, Levels: []LevelDoc{wing, project}, File: &file})

	if e.Off.V || e.Off.Locked == nil {
		t.Errorf("the off switch was turned on past a lock: %+v", e.Off)
	}
	s := checkOf(e, "secrets").State
	if s.V != Block || s.Locked == nil || s.Locked.Level != LevelOrganization || s.Locked.Reason != "the security team requires it" || !strings.Contains(s.Locked.Who, "Wing") {
		t.Errorf("secrets: %+v", s)
	}
	if checkOf(e, "sast-go").State.V != Block {
		t.Error("sast-go was weakened below its lock")
	}
	// The file asks for a stricter threshold than the project (low is stricter than critical)
	// but the lock set medium; stricter is allowed, weaker is not.
	if e.FailOn.V != findings.Low {
		t.Errorf("fail_on %s: a stricter value than the lock must count", e.FailOn.V)
	}
	if e.RequireFix.V {
		t.Error("require_fix was relaxed past the lock")
	}
	if !subset([]string{"GPL-3.0"}, e.Deny.V) {
		t.Errorf("the denied list lost a locked entry: %v", e.Deny.V)
	}
	if len(e.Exemptions) != 0 {
		t.Errorf("exemptions count though they are locked above: %+v", e.Exemptions)
	}
	joined := strings.Join(e.Notices, "\n")
	for _, want := range []string{"the state of the check \"Secrets in the repository's history\"", "locked by the Wing", "the security team requires it", "the switch that turns Standing orders off", "exemptions"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no notice mentioning %q in:\n%s", want, joined)
		}
	}
}

func TestOneOffSwitchPerLevelAndExemptionsAdd(t *testing.T) {
	proj := LevelDoc{Level: LevelProject, Scope: "shop", Settings: Settings{Off: ptr(true), OffReason: "evaluating another tool",
		Ignore: []Ignore{{Rule: "CVE-1", Reason: "not reachable here", Expires: "2027-03-01"}, {Rule: "CVE-2", Reason: "old and expired", Expires: "2026-01-01"}}}}
	e := Resolve(ResolveInput{Files: goRepo, Now: testNow, Levels: []LevelDoc{proj}})
	if !e.Off.V || e.OffReason != "evaluating another tool" {
		t.Fatalf("%+v %q", e.Off, e.OffReason)
	}
	for _, c := range e.Checks {
		if c.Active {
			t.Errorf("%s is active while Standing orders are off", c.ID)
		}
	}
	if len(e.Exemptions) != 2 || e.Exemptions[0].Expired || !e.Exemptions[1].Expired {
		t.Errorf("%+v", e.Exemptions)
	}
	p := PolicyOf(e, testNow)
	if len(p.Exemptions) != 1 || len(p.Checks) != 0 {
		t.Errorf("policy %+v", p)
	}
}

func TestResolveIsDeterministic(t *testing.T) {
	in := ResolveInput{Files: goRepo, Now: testNow, Levels: []LevelDoc{{Level: LevelUnit, Scope: "web", Settings: Settings{Checks: map[string]State{"shell": Off}}}}}
	a, b := Resolve(in), Resolve(in)
	if len(a.Checks) != len(b.Checks) || a.Notices == nil {
		t.Fatal("not stable")
	}
	for i := range a.Checks {
		if a.Checks[i].State.V != b.Checks[i].State.V || a.Checks[i].Active != b.Checks[i].Active {
			t.Fatal("not stable")
		}
	}
}

func TestSettingsProblemsArePlain(t *testing.T) {
	bad := Settings{
		Checks:   map[string]State{"sast-golang": Advise, "sbom": Block, "secrets": "loud"},
		Gate:     GateSettings{FailOn: "severe", DefaultBranch: "maybe", Incomplete: "shrug"},
		Licenses: LicenseSettings{Allow: &[]string{"MIT", "evil; rm -rf /"}},
		Off:      ptr(true),
		Ignore:   []Ignore{{}, {Rule: "x", Reason: "short", Expires: "2099-01-01"}, {Rule: "x", Reason: "a good long reason", Expires: "tomorrow"}},
		Locks:    map[string]string{"nonsense": "a reason that is long enough"},
	}
	ps := strings.Join(bad.Problems(testNow, true), "\n")
	for _, want := range []string{`"sast-golang" is not a check`, `can only advise`, `"loud" is not a state`, `"severe" is not a severity`, `gate.default_branch`, `gate.incomplete`,
		`"evil; rm -rf /" is not a license name`, "needs a reason", "say what to ignore", "a reason is needed", "more than 365 days", `"tomorrow" is not a date`, `"nonsense" is not something that can be locked`} {
		if !strings.Contains(ps, want) {
			t.Errorf("no problem mentioning %q in:\n%s", want, ps)
		}
	}
	if ps := (Settings{Locks: map[string]string{KeyOff: "a good enough reason"}}).Problems(testNow, false); len(ps) != 1 || !strings.Contains(ps[0], "not from the repository") {
		t.Errorf("a lock in the repository file: %v", ps)
	}
}

func TestParseFilePlainMessagesAndDidYouMean(t *testing.T) {
	good := `
version: 1
checks:
  secrets: block
  sast-go: { state: advise }
  shell: off
gate:
  fail_on: critical
  require_fix: false
  default_branch: block
licenses:
  allow: [MIT, Apache-2.0]
  deny: [GPL-3.0-only]
ignore:
  - rule: G401
    path: "legacy/**"
    reason: "legacy hashing of non-secrets"
    expires: 2027-01-15
    owner: team-web
`
	s, ps := ParseFile([]byte(good), testNow)
	if len(ps) != 0 {
		t.Fatalf("%v", ps)
	}
	if s.Checks["secrets"] != Block || s.Checks["sast-go"] != Advise || s.Checks["shell"] != Off || s.Gate.FailOn != findings.Critical || *s.Gate.RequireFix || s.Gate.DefaultBranch != Block ||
		len(*s.Licenses.Allow) != 2 || len(s.Ignore) != 1 || s.Ignore[0].Status != StatusAccepted || s.Ignore[0].Owner != "team-web" {
		t.Fatalf("%+v", s)
	}
	if round, ps := ParseFile(ExportFile(s), testNow); len(ps) != 0 || round.Gate.FailOn != s.Gate.FailOn || len(round.Ignore) != 1 || round.Ignore[0].Reason != s.Ignore[0].Reason {
		t.Errorf("export does not read back: %v\n%s", ps, ExportFile(s))
	}

	_, ps = ParseFile([]byte("chekcs:\n  secrets: block\nchecks:\n  sast-golang: advise\n  secrets: loud\nignore:\n  - rule: x\n    reason: why not\n"), testNow)
	all := strings.Join(ps, "\n")
	for _, want := range []string{`line 1: "chekcs" is not a setting. Did you mean "checks"?`, `"sast-golang" is not a check. Did you mean "sast-go"?`, `"loud" is not a state`, "a reason is needed", "an end date is needed"} {
		if !strings.Contains(all, want) {
			t.Errorf("no message %q in:\n%s", want, all)
		}
	}
	if _, ps := ParseFile([]byte("checks: [unclosed"), testNow); len(ps) != 1 || !strings.Contains(ps[0], "not valid YAML") {
		t.Errorf("%v", ps)
	}
	if s, ps := ParseFile(nil, testNow); len(ps) != 0 || len(s.Checks) != 0 {
		t.Errorf("an empty file: %v", ps)
	}
	if _, ps := ParseFile([]byte(strings.Repeat("#", MaxFileBytes+1)), testNow); len(ps) != 1 {
		t.Errorf("%v", ps)
	}
	if _, ps := ParseFile([]byte("- a\n- b\n"), testNow); len(ps) != 1 || !strings.Contains(ps[0], "list of settings") {
		t.Errorf("%v", ps)
	}
}

func fnd(check, rule string, sev findings.Severity, fp string) Finding {
	k := KindVulnerability
	if c, ok := CheckByID(check); ok {
		k = c.Kind
	}
	return Finding{Check: check, Tool: "t", Rule: rule, Severity: sev, Kind: k, Fingerprint: fp, FixedIn: "9.9.9", File: "go.mod", Package: "p"}
}

func testPolicy(states map[string]State) Policy {
	p := Policy{FailOn: findings.High, RequireFix: true}
	for id, st := range states {
		c, _ := CheckByID(id)
		p.Checks = append(p.Checks, PolicyCheck{ID: id, State: st, Kind: c.Kind, Title: c.Title})
	}
	return p
}

func TestGateBlocksOnlyWhatTheChangeIntroduces(t *testing.T) {
	old := fnd("vuln-deps", "CVE-OLD", findings.Critical, "old")
	fresh := fnd("vuln-deps", "CVE-NEW", findings.High, "new")
	low := fnd("vuln-deps", "CVE-LOW", findings.Low, "low")
	in := JudgeInput{Policy: testPolicy(map[string]State{"vuln-deps": Block}), Findings: []Finding{old, fresh, low},
		Ran: map[string]bool{"vuln-deps": true}, Baseline: map[string]bool{"old": true}, HasBaseline: true, Now: testNow}
	v := Judge(in)
	if !v.Blocked || len(v.Gating) != 1 || v.Gating[0].Rule != "CVE-NEW" || v.New != 2 || v.Existing != 1 {
		t.Fatalf("%+v", v)
	}
	if !strings.Contains(v.Summary, "introduces 1 finding") || !strings.Contains(v.Summary, "1 existing finding") {
		t.Errorf("%s", v.Summary)
	}
	// Without the new one the change passes, whatever the backlog holds.
	in.Findings = []Finding{old, low}
	if v := Judge(in); v.Blocked || !strings.HasPrefix(v.Summary, "Passed") {
		t.Errorf("%+v", v)
	}
	// An advising check never blocks.
	in.Findings, in.Policy = []Finding{fresh}, testPolicy(map[string]State{"vuln-deps": Advise})
	if v := Judge(in); v.Blocked {
		t.Errorf("an advising check blocked: %+v", v)
	}
}

func TestGateDefaultBranchShowsBacklogWithoutFailing(t *testing.T) {
	f := fnd("secrets", "github-pat", findings.High, "s")
	in := JudgeInput{Policy: testPolicy(map[string]State{"secrets": Block}), DefaultBranch: true, Findings: []Finding{f}, Ran: map[string]bool{"secrets": true}, Now: testNow}
	if v := Judge(in); v.Blocked || v.Existing != 1 || !strings.Contains(v.Summary, "does not fail the run") {
		t.Errorf("%+v", v)
	}
	in.Policy.DefaultBranchBlocks = true
	if v := Judge(in); !v.Blocked || len(v.Gating) != 1 {
		t.Errorf("the Standing orders ask the default branch to be clean: %+v", v)
	}
}

func TestGateNeverComparesWithoutABaseline(t *testing.T) {
	in := JudgeInput{Policy: testPolicy(map[string]State{"secrets": Block}), Findings: []Finding{fnd("secrets", "r", findings.Critical, "x")},
		Ran: map[string]bool{"secrets": true}, Now: testNow}
	v := Judge(in)
	if v.Blocked || !v.NoBaseline || v.Counts[StandingUnscored] != 1 || !strings.Contains(v.Summary, "has not been inspected yet") {
		t.Errorf("%+v", v)
	}
}

func TestGateDecisionsAndExemptionsExpire(t *testing.T) {
	f := fnd("vuln-deps", "CVE-1", findings.Critical, "fp1")
	base := JudgeInput{Policy: testPolicy(map[string]State{"vuln-deps": Block}), Findings: []Finding{f}, Ran: map[string]bool{"vuln-deps": true}, HasBaseline: true, Baseline: map[string]bool{}, Now: testNow}
	if !Judge(base).Blocked {
		t.Fatal("a new critical did not block")
	}
	dec := Decision{Fingerprint: "fp1", Status: StatusAccepted, Reason: "not reachable", By: "ada", Expires: testNow.Add(24 * time.Hour)}
	base.Decisions = map[string]Decision{"fp1": dec}
	if v := Judge(base); v.Blocked || v.Accepted != 1 || v.Counts[StandingAccepted] != 1 {
		t.Errorf("an accepted risk still blocks: %+v", v)
	}
	// False positives are counted apart.
	dec.Status = StatusFalsePositive
	base.Decisions["fp1"] = dec
	if v := Judge(base); v.Counts[StandingFalse] != 1 {
		t.Errorf("%+v", v.Counts)
	}
	// After the end date it blocks again.
	base.Now = testNow.Add(48 * time.Hour)
	if !Judge(base).Blocked {
		t.Error("an expired acceptance still holds")
	}
	// The repository's own list works the same way and expires too.
	base.Decisions = nil
	base.Now = testNow
	base.Policy.Exemptions = []Ignore{{Rule: "CVE-1", Package: "p", Reason: "documented in the repository", Expires: "2026-12-01"}}
	if v := Judge(base); v.Blocked || !strings.Contains(v.Counts2(), "accepted") {
		t.Errorf("%+v", v)
	}
	base.Now = time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	if !Judge(base).Blocked {
		t.Error("an expired exemption of the file still holds")
	}
}

// Counts2 is a test helper: the standings present, as text.
func (v Verdict) Counts2() string {
	var s []string
	for k, n := range v.Counts {
		if n > 0 {
			s = append(s, k)
		}
	}
	return strings.Join(s, ",")
}

func TestGateIncompleteIsNeverTakenForClean(t *testing.T) {
	p := testPolicy(map[string]State{"secrets": Block, "sast-go": Advise})
	in := JudgeInput{Policy: p, Ran: map[string]bool{"sast-go": true}, HasBaseline: true, Now: testNow}
	v := Judge(in)
	if v.Blocked || len(v.Incomplete) != 1 || v.Incomplete[0] != "secrets" || !strings.Contains(v.Summary, "Did not finish: secrets") {
		t.Errorf("%+v", v)
	}
	p.IncompleteFails = true
	in.Policy = p
	if v := Judge(in); !v.Blocked || !strings.Contains(v.Summary, "did not run") {
		t.Errorf("%+v", v)
	}
}

func TestGateFixAndReachabilityRules(t *testing.T) {
	p := testPolicy(map[string]State{"vuln-deps": Block, "vuln-reach-go": Block})
	nofix := fnd("vuln-deps", "CVE-9", findings.Critical, "a")
	nofix.FixedIn = ""
	if p.Eligible(nofix) {
		t.Error("a vulnerability with no fix available blocks")
	}
	p.RequireFix = false
	if !p.Eligible(nofix) {
		t.Error("with require_fix off, a vulnerability without a fix should block")
	}
	reach := Finding{Check: "vuln-reach-go", Tool: "govulncheck", Kind: KindVulnerability, Severity: findings.High, Reachable: "yes"}
	p.RequireFix = true
	if !p.Eligible(reach) {
		t.Error("a reachable vulnerability blocks even where the report names no fixed version")
	}
	reach.Reachable, reach.Severity = "no", findings.High
	if p.Eligible(reach) {
		t.Error("an unreachable vulnerability blocks")
	}
	sec := fnd("secrets", "r", findings.High, "z")
	if p.Eligible(sec) {
		t.Error("a check that is not in the policy blocks")
	}
}

func TestInspectionStepsAreInjectedInParallelWithAGate(t *testing.T) {
	pl, err := pipeline.Parse([]byte("pipeline: app\non: { push: { branches: [main] } }\nsteps:\n  build:\n    image: docker.io/library/alpine:3.22\n    run: echo hi\n"))
	if err != nil {
		t.Fatal(err)
	}
	e := Resolve(ResolveInput{Files: goRepo, Now: testNow, Levels: []LevelDoc{{Level: LevelProject, Scope: "app", Settings: Settings{Checks: map[string]State{"sast-go": Off}}}}})
	q, inj, err := Apply(pl, InjectInput{Effective: e, Policy: PolicyOf(e, testNow), Since: "abc123"})
	if err != nil || q == pl {
		t.Fatalf("%v", err)
	}
	if len(pl.Steps) != 1 {
		t.Fatal("the original pipeline was changed")
	}
	gate := q.Step(GateStepID)
	if gate == nil || !gate.IsGate() || len(gate.Needs) != len(inj.Steps)-1 || gate.Gate == nil || q.Requires != pipeline.SchemaStandings {
		t.Fatalf("%+v requires %d", gate, q.Requires)
	}
	ids := map[string]bool{}
	for _, s := range inj.Steps {
		ids[s.ID] = true
		if s.IsGate() {
			continue
		}
		if len(s.Needs) != 0 || len(s.Secrets) != 0 || s.Added == nil || s.Added.By != pipeline.AddedByStandingOrders || s.Added.Why == "" || s.OnFailure != "continue" {
			t.Errorf("%s: %+v", s.ID, s)
		}
		if !digestRe.MatchString(s.Image) {
			t.Errorf("%s: image %s is not pinned", s.ID, s.Image)
		}
		if s.Timeout <= 0 || s.Reports == nil {
			t.Errorf("%s has no time budget or reports", s.ID)
		}
	}
	for _, want := range []string{"inspect-secrets", "inspect-vuln-deps", "inspect-vuln-reach-go", "inspect-dockerfile", "inspect-pipeline-lint", "inspect-license", "inspect-sbom"} {
		if !ids[want] {
			t.Errorf("no step %s among %v", want, ids)
		}
	}
	if ids["inspect-sast-go"] {
		t.Error("a check that is off was added")
	}
	if s := q.Step("inspect-secrets"); s.Env["LAUNCHWAY_STANDING_SINCE"] != "abc123" {
		t.Errorf("%v", s.Env)
	}
	if s := q.Step("inspect-license"); !strings.Contains(s.Env["STANDING_LICENSE_DENY"], "AGPL-3.0") {
		t.Errorf("%v", s.Env)
	}
	if s := q.Step("inspect-vuln-reach-go"); len(s.Cache) != 1 || s.Cache[0].Key.Files[0] != "**/go.sum" {
		t.Errorf("%+v", s.Cache)
	}
	// The same pipeline with Standing orders off, or a step of its own of that name, changes less.
	off := Resolve(ResolveInput{Files: goRepo, Now: testNow, Levels: []LevelDoc{{Level: LevelProject, Settings: Settings{Off: ptr(true), OffReason: "evaluating something else"}}}})
	if q2, _, _ := Apply(pl, InjectInput{Effective: off, Policy: PolicyOf(off, testNow)}); q2 != pl {
		t.Error("steps were added while Standing orders are off")
	}
	own, perr := pipeline.Parse([]byte("pipeline: app\non: { push: { branches: [main] } }\nsteps:\n  inspect-secrets:\n    image: docker.io/library/alpine:3.22\n    run: ./my-scan.sh\n"))
	if perr != nil {
		t.Fatal(perr)
	}
	q3, inj3, _ := Apply(own, InjectInput{Effective: e, Policy: PolicyOf(e, testNow)})
	if q3.Step("inspect-secrets").Image != "docker.io/library/alpine:3.22" || len(inj3.Provided) != 1 || inj3.Provided[0] != "secrets" || !contains(q3.Step(GateStepID).Needs, "inspect-secrets") {
		t.Errorf("a step of the team's own with the same name was replaced: %+v", inj3.Provided)
	}
}
