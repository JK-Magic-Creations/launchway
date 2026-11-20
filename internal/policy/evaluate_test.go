package policy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	digA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var t0 = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC).UnixMilli()

func set(ps ...Policy) Set {
	for i := range ps {
		if ps[i].Mode == "" {
			ps[i].Mode = ModeEnforce
		}
		if ps[i].Name == "" {
			ps[i].Name = "p"
		}
		ps[i].Source = "config"
	}
	return Set{Sources: []Source{{Name: "config", Version: Hash(ps), Policies: ps}}}
}

func runInput(f PipelineFacts) Input {
	return Input{Point: PointRun, Project: "shop", Run: "run_1", RunNumber: 1, Event: "push", Branch: "main", Starter: "dana", Pipeline: &f}
}

func deployInput(e Evidence) Input {
	return Input{Point: PointDeploy, Project: "shop", Run: "run_1", RunNumber: 1, Event: "push", Branch: "main", Starter: "dana",
		Step: "deploy", Action: "deploy", Environment: "prod", Evidence: &e}
}

func imageInput(im ImageStep) Input {
	return Input{Point: PointImage, Project: "shop", Run: "run_1", Event: "push", Branch: "main", Starter: "dana", Step: "image", Action: "push", Image: &im}
}

// one evaluates one policy and returns its one rule's result.
func one(t *testing.T, rules Rules, in Input, ws ...Waiver) (Evaluation, RuleResult) {
	t.Helper()
	ev := Evaluate(set(Policy{Rules: rules}), in, ws, t0)
	if len(ev.Policies) != 1 || len(ev.Policies[0].Rules) != 1 {
		t.Fatalf("evaluation: %+v", ev)
	}
	return ev, ev.Policies[0].Rules[0]
}

func TestRunRules(t *testing.T) {
	libs := func(kinds ...string) PipelineFacts {
		var f PipelineFacts
		for i, k := range kinds {
			f.Libraries = append(f.Libraries, LibraryFact{Name: "lib" + string(rune('a'+i)), Repo: "ci", Version: "v1", RefKind: k, Commit: strings.Repeat("c", 40)})
		}
		return f
	}
	tmpl := PipelineFacts{Templates: []TemplateFact{{Group: "ci", Library: "ci-templates", Template: "scan", Version: "v3.2", RefKind: "tag"}}}
	branchTmpl := PipelineFacts{Templates: []TemplateFact{{Group: "ci", Library: "ci-templates", Template: "scan", Version: "v3.x", RefKind: "branch"}}}
	images := PipelineFacts{Images: []ImageRef{{Where: "step test", Image: "golang:1.27"}, {Where: "container py", Image: "harbor.example.com/base/py@" + digA}}}
	cases := []struct {
		name  string
		rules Rules
		facts PipelineFacts
		event string
		want  string
		found string
	}{
		{"libraries tagged", Rules{LibraryVersions: &LibraryVersions{Pinned: "tag"}}, libs("tag", "commit"), "", ResultPass, "2 libraries, all pinned"},
		{"library on a branch", Rules{LibraryVersions: &LibraryVersions{Pinned: "tag"}}, libs("tag", "branch"), "", ResultFail, "libb at v1 (a branch)"},
		{"commit only", Rules{LibraryVersions: &LibraryVersions{Pinned: "commit"}}, libs("tag"), "", ResultFail, "liba at v1 (a tag)"},
		{"no libraries", Rules{LibraryVersions: &LibraryVersions{Pinned: "tag"}}, PipelineFacts{}, "", ResultPass, "uses no shared library"},
		{"template present", Rules{RequiredTemplates: []RequiredTemplate{{Library: "ci-templates", Template: "scan", Versions: []string{"v3.*"}}}}, tmpl, "", ResultPass, ""},
		{"template missing", Rules{RequiredTemplates: []RequiredTemplate{{Library: "ci-templates", Template: "scan"}}}, PipelineFacts{}, "", ResultFail, "no step uses ci-templates/scan"},
		{"template at a branch", Rules{RequiredTemplates: []RequiredTemplate{{Library: "ci-templates", Template: "scan", Versions: []string{"v3.*"}}}}, branchTmpl, "", ResultFail, "only at a version not allowed: ci at v3.x (a branch)"},
		{"template wrong version", Rules{RequiredTemplates: []RequiredTemplate{{Library: "ci-templates", Template: "scan", Versions: []string{"v4"}}}}, tmpl, "", ResultFail, "not allowed"},
		{"images by digest", Rules{StepImages: &StepImages{Digest: true}}, images, "", ResultFail, "step test: golang:1.27 (not pinned by digest)"},
		{"images registry", Rules{StepImages: &StepImages{Registries: []string{"harbor.example.com/**"}}}, images, "", ResultFail, "golang:1.27 (not from an allowed registry)"},
		{"images docker library", Rules{StepImages: &StepImages{Registries: []string{"harbor.example.com/**", "docker.io/library/*"}}}, images, "", ResultPass, "2 images"},
		{"timeouts", Rules{MaxTimeout: "1h"}, PipelineFacts{Timeouts: []TimeoutFact{{Step: "a", Timeout: 3600000}, {Step: "b", Timeout: 7200000}}}, "", ResultFail, "b: 2h0m0s"},
		{"timeouts ok", Rules{MaxTimeout: "2h"}, PipelineFacts{Timeouts: []TimeoutFact{{Step: "b", Timeout: 7200000}}}, "", ResultPass, ""},
		{"secrets in a pull request", Rules{ForbidSecrets: &ForbidSecrets{Events: []string{"pull_request"}}},
			PipelineFacts{SecretsAllowed: true, Secrets: []SecretFact{{Step: "push", Names: []string{"TOKEN"}}}}, "pull_request", ResultFail, "push (TOKEN)"},
		{"pull request without secrets", Rules{ForbidSecrets: &ForbidSecrets{Events: []string{"pull_request"}}},
			PipelineFacts{SecretsAllowed: false, Secrets: []SecretFact{{Step: "push", Names: []string{"TOKEN"}}}}, "pull_request", ResultPass, "may not read secrets"},
		{"secrets on push", Rules{ForbidSecrets: &ForbidSecrets{Events: []string{"pull_request"}}},
			PipelineFacts{SecretsAllowed: true, Secrets: []SecretFact{{Step: "push", Names: []string{"TOKEN"}}}}, "push", ResultPass, "this is a push run"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := runInput(c.facts)
			if c.event != "" {
				in.Event = c.event
			}
			ev, r := one(t, c.rules, in)
			if r.Verdict != c.want || !strings.Contains(r.Found, c.found) {
				t.Fatalf("got %s %q (want %s %q)", r.Verdict, r.Found, c.want, c.found)
			}
			if r.Want == "" {
				t.Fatal("no want")
			}
			if (c.want == ResultFail) != (ev.Verdict == VerdictBlock) {
				t.Fatalf("evaluation verdict %s", ev.Verdict)
			}
		})
	}
}

func TestDeployRules(t *testing.T) {
	tests := func(total, failed int) StepFact {
		return StepFact{Step: "test", Status: "succeeded", Run: "run_1", Tests: &TestFact{Total: total, Passed: total - failed, Failed: failed}, Declared: []string{"tests"}}
	}
	scan := func(c Counts, tools ...string) StepFact {
		bt := map[string]Counts{}
		if len(tools) > 0 {
			bt[tools[0]] = c
		}
		return StepFact{Step: "scan", Status: "succeeded", Run: "run_1", Findings: &FindingsFact{Counts: c, Tools: tools, ByTool: bt}}
	}
	img := func(signed, scanned bool) StepFact {
		return StepFact{Step: "image", Kind: "image", Status: "succeeded", Run: "run_1", Image: &BuiltImage{Digest: digA, Platforms: []string{digB}, Pushed: true, Signed: signed, Scanned: scanned}}
	}
	approval := func(by string) StepFact {
		return StepFact{Step: "approve-" + by, Kind: "approval", Status: "succeeded", Run: "run_1", Approval: &ApprovalFact{By: by}}
	}
	all := DeployedImages{Digest: true, BuiltByRun: true, Signed: true, Scanned: true}
	cases := []struct {
		name  string
		rules Rules
		ev    Evidence
		want  string
		found string
	}{
		{"tests pass", Rules{Tests: &Tests{Min: 3}}, Evidence{Steps: []StepFact{tests(5, 0)}}, ResultPass, "5 tests from 1 step(s)"},
		{"tests missing", Rules{Tests: &Tests{Min: 1}}, Evidence{Steps: []StepFact{{Step: "build", Status: "succeeded"}}}, ResultFail, "no step before deploy reported test results"},
		{"tests too few", Rules{Tests: &Tests{Min: 10}}, Evidence{Steps: []StepFact{tests(5, 0)}}, ResultFail, "5 tests"},
		{"tests failed", Rules{Tests: &Tests{Min: 1}}, Evidence{Steps: []StepFact{tests(5, 1)}}, ResultFail, "1 failed"},
		{"no scanner", Rules{Findings: &Findings{MaxSeverity: "high"}}, Evidence{}, ResultFail, "no scanner ran"},
		{"findings under", Rules{Findings: &Findings{MaxSeverity: "high"}}, Evidence{Steps: []StepFact{scan(Counts{Total: 2, High: 2}, "Trivy")}}, ResultPass, "2 findings"},
		{"findings over", Rules{Findings: &Findings{MaxSeverity: "medium"}}, Evidence{Steps: []StepFact{scan(Counts{Total: 2, High: 2}, "Trivy")}}, ResultFail, "2 worse than medium"},
		{"none at all", Rules{Findings: &Findings{MaxSeverity: "none"}}, Evidence{Steps: []StepFact{scan(Counts{Total: 1, Unknown: 1}, "Trivy")}}, ResultFail, "1 worse than none"},
		{"unknown is not above low", Rules{Findings: &Findings{MaxSeverity: "low"}}, Evidence{Steps: []StepFact{scan(Counts{Total: 1, Unknown: 1}, "Trivy")}}, ResultPass, ""},
		{"per tool", Rules{Findings: &Findings{PerTool: map[string]string{"trivy": "low"}}}, Evidence{Steps: []StepFact{scan(Counts{Total: 1, Medium: 1}, "Trivy")}}, ResultFail, "1 from trivy worse than low"},
		{"per tool other tool", Rules{Findings: &Findings{PerTool: map[string]string{"semgrep": "low"}}}, Evidence{Steps: []StepFact{scan(Counts{Total: 1, Medium: 1}, "Trivy")}}, ResultPass, ""},
		{"per tool without counts", Rules{Findings: &Findings{PerTool: map[string]string{"trivy": "low"}}},
			Evidence{Steps: []StepFact{{Step: "scan", Status: "succeeded", Findings: &FindingsFact{Counts: Counts{Total: 1, Medium: 1}, Tools: []string{"Trivy"}}}}}, ResultFail, "without counts per tool"},
		{"scanner missing", Rules{Findings: &Findings{Scanners: []string{"gitleaks", "trivy"}}}, Evidence{Steps: []StepFact{scan(Counts{}, "Trivy")}}, ResultFail, "gitleaks did not report"},
		{"sbom", Rules{SBOM: &SBOM{MinComponents: 1}}, Evidence{Steps: []StepFact{{Step: "image", SBOM: &SBOMFact{Components: 40}}}}, ResultPass, "40 components"},
		{"sbom missing", Rules{SBOM: &SBOM{MinComponents: 1}}, Evidence{Steps: []StepFact{tests(1, 0)}}, ResultFail, "no step before deploy made an SBOM"},
		{"sbom empty", Rules{SBOM: &SBOM{MinComponents: 1}}, Evidence{Steps: []StepFact{{Step: "image", SBOM: &SBOMFact{Components: 0}}}}, ResultFail, "the largest SBOM has 0"},
		{"image ok", Rules{DeployedImages: &all}, Evidence{Steps: []StepFact{img(true, true)}, Deployed: []DeployedRef{{File: "a.yaml", Image: "team/shop", Digest: digA}}, Changes: 1}, ResultPass, "1 image reference"},
		{"platform digest", Rules{DeployedImages: &all}, Evidence{Steps: []StepFact{img(true, true)}, Deployed: []DeployedRef{{File: "a.yaml", Image: "team/shop", Digest: digB}}, Changes: 1}, ResultPass, ""},
		{"image by tag", Rules{DeployedImages: &all}, Evidence{Steps: []StepFact{img(true, true)}, Deployed: []DeployedRef{{File: "a.yaml", Image: "team/shop", Tag: "1.2"}}, Changes: 1}, ResultFail, "pinned by tag, not digest"},
		{"image not built here", Rules{DeployedImages: &DeployedImages{BuiltByRun: true}}, Evidence{Deployed: []DeployedRef{{File: "a.yaml", Image: "team/shop", Digest: digA}}, Changes: 1}, ResultFail, "no image step before deploy built this digest"},
		{"unsigned", Rules{DeployedImages: &all}, Evidence{Steps: []StepFact{img(false, true)}, Deployed: []DeployedRef{{File: "a.yaml", Image: "team/shop", Digest: digA}}, Changes: 1}, ResultFail, "not signed"},
		{"unscanned", Rules{DeployedImages: &all}, Evidence{Steps: []StepFact{img(true, false)}, Deployed: []DeployedRef{{File: "a.yaml", Image: "team/shop", Digest: digA}}, Changes: 1}, ResultFail, "not scanned"},
		{"no image written", Rules{DeployedImages: &DeployedImages{Digest: true}}, Evidence{Changes: 2}, ResultFail, "writes no image reference (2 change(s)"},
		{"approved", Rules{Approvals: &Approvals{Min: 1}}, Evidence{Steps: []StepFact{approval("sam")}}, ResultPass, "approved by sam"},
		{"self approved", Rules{Approvals: &Approvals{Min: 1}}, Evidence{Steps: []StepFact{approval("dana")}}, ResultFail, "an approval by the person who started the run does not count"},
		{"two needed", Rules{Approvals: &Approvals{Min: 2}}, Evidence{Steps: []StepFact{approval("sam"), approval("sam")}}, ResultFail, "approved by sam only"},
		{"no approval", Rules{Approvals: &Approvals{Min: 1}}, Evidence{}, ResultFail, "no approval before deploy"},
		{"branch ok", Rules{Refs: &Refs{Branches: []string{"main"}}}, Evidence{}, ResultPass, "branch main"},
		{"branch wrong", Rules{Refs: &Refs{Branches: []string{"release/*"}}}, Evidence{}, ResultFail, "a run of branch main"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, r := one(t, c.rules, deployInput(c.ev))
			if r.Verdict != c.want || !strings.Contains(r.Found, c.found) {
				t.Fatalf("got %s %q (want %s %q); notes %v", r.Verdict, r.Found, c.want, c.found, r.Notes)
			}
			if (c.want == ResultFail) != (ev.Verdict == VerdictBlock) {
				t.Fatalf("evaluation verdict %s", ev.Verdict)
			}
			if c.want == ResultFail && !strings.Contains(ev.Reason(), "policy p (enforce), rule "+r.Rule+": ") {
				t.Fatalf("reason %q", ev.Reason())
			}
		})
	}
}

func TestImagePushRule(t *testing.T) {
	strict := Rules{ImagePush: &ImagePush{Scan: "high", Sign: "runner", SBOM: true, Provenance: true, Registries: []string{"harbor"}}}
	good := ImageStep{Push: true, Registries: []string{"harbor"}, ScanMode: "enforce", ScanFailOn: "medium", Sign: "runner", SBOM: true, Provenance: true}
	cases := []struct {
		name  string
		im    func(ImageStep) ImageStep
		want  string
		found string
	}{
		{"good", func(s ImageStep) ImageStep { return s }, ResultPass, "every safeguard"},
		{"no push", func(s ImageStep) ImageStep { s.Push, s.ScanMode = false, ""; return s }, ResultPass, "pushes nothing"},
		{"report scan", func(s ImageStep) ImageStep { s.ScanMode = "report"; return s }, ResultFail, "only reports"},
		{"lenient scan", func(s ImageStep) ImageStep { s.ScanFailOn = "critical"; return s }, ResultFail, "fails only on critical"},
		{"no scan", func(s ImageStep) ImageStep { s.ScanMode = ""; return s }, ResultFail, "does not scan"},
		{"key sign", func(s ImageStep) ImageStep { s.Sign = "key"; return s }, ResultFail, "not the runner's own key"},
		{"no sign", func(s ImageStep) ImageStep { s.Sign = ""; return s }, ResultFail, "does not sign"},
		{"no sbom", func(s ImageStep) ImageStep { s.SBOM = false; return s }, ResultFail, "no SBOM"},
		{"no provenance", func(s ImageStep) ImageStep { s.Provenance = false; return s }, ResultFail, "no provenance"},
		{"registry", func(s ImageStep) ImageStep { s.Registries = []string{"dockerhub"}; return s }, ResultFail, "pushes to dockerhub"},
		{"cache registry", func(s ImageStep) ImageStep { s.Cache = "dockerhub"; return s }, ResultFail, "pushes to dockerhub"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, r := one(t, strict, imageInput(c.im(good)))
			if r.Verdict != c.want || !strings.Contains(r.Found, c.found) {
				t.Fatalf("got %s %q (want %s %q)", r.Verdict, r.Found, c.want, c.found)
			}
		})
	}
}

// Report mode shows the failure and warns; it never blocks. Enforce blocks.
func TestModes(t *testing.T) {
	in := deployInput(Evidence{})
	rep := Evaluate(set(Policy{Name: "a", Mode: ModeReport, Rules: Rules{Tests: &Tests{Min: 1}}}), in, nil, t0)
	if rep.Verdict != VerdictWarn || rep.Blocked() || !strings.HasPrefix(rep.Reason(), "policy warning: policy a (report), rule tests") {
		t.Fatalf("%s %q", rep.Verdict, rep.Reason())
	}
	both := Evaluate(set(Policy{Name: "a", Mode: ModeReport, Rules: Rules{Tests: &Tests{Min: 1}}},
		Policy{Name: "b", Mode: ModeEnforce, Rules: Rules{SBOM: &SBOM{MinComponents: 1}}}), in, nil, t0)
	if both.Verdict != VerdictBlock || !strings.HasPrefix(both.Reason(), "blocked by policy: policy a (report)") {
		t.Fatalf("%s %q", both.Verdict, both.Reason())
	}
	// A policy without rules at this point is not judged here.
	none := Evaluate(set(Policy{Name: "c", Rules: Rules{MaxTimeout: "1h"}}), in, nil, t0)
	if len(none.Policies) != 0 || none.Verdict != VerdictPass {
		t.Fatalf("%+v", none)
	}
}

func TestScope(t *testing.T) {
	p := Policy{Name: "prod", Scope: Scope{Projects: []string{"pay-*"}, Environments: []string{"prod"}, Branches: []string{"main"}, Events: []string{"push"}},
		Rules: Rules{Tests: &Tests{Min: 1}}}
	cases := []struct {
		project, branch, event, env string
		judged                      bool
	}{
		{"pay-api", "main", "push", "prod", true},
		{"shop", "main", "push", "prod", false},
		{"pay-api", "dev", "push", "prod", false},
		{"pay-api", "", "pull_request", "prod", false},
		{"pay-api", "main", "manual", "prod", false},
		{"pay-api", "main", "push", "staging", false},
	}
	for _, c := range cases {
		in := deployInput(Evidence{})
		in.Project, in.Branch, in.Event, in.Environment = c.project, c.branch, c.event, c.env
		ev := Evaluate(set(p), in, nil, t0)
		if (len(ev.Policies) == 1) != c.judged {
			t.Errorf("%+v: judged %d", c, len(ev.Policies))
		}
	}
}

// A source that could not be loaded fails closed at every point, for the
// projects it covers, and no waiver passes it.
func TestBrokenSourceFailsClosed(t *testing.T) {
	s := Set{Sources: []Source{
		{Name: "config", Policies: []Policy{{Name: "ok", Mode: ModeReport, Source: "config", Rules: Rules{MaxTimeout: "1h"}}}},
		{Name: "org", Ref: "v3", Projects: []string{"shop"}, Error: "policies/a.yaml:4: unknown rule \"x\""},
	}}
	w := []Waiver{{ID: "w1", Project: "shop", Policy: "org", Rule: RuleSource, GrantedAt: t0 - 1, ExpiresAt: t0 + 1}}
	for _, in := range []Input{runInput(PipelineFacts{}), deployInput(Evidence{}), imageInput(ImageStep{Push: true})} {
		ev := Evaluate(s, in, w, t0)
		if !ev.Blocked() {
			t.Fatalf("%s: %+v", in.Point, ev)
		}
		if !strings.Contains(ev.Reason(), `policy org (enforce), rule source: the policies of source org at v3 could not be loaded: policies/a.yaml:4: unknown rule "x"`) {
			t.Fatalf("%q", ev.Reason())
		}
	}
	orgOnly := Set{Sources: s.Sources[1:]}
	if !orgOnly.Governed("shop", "x", "push") || orgOnly.Governed("web", "x", "push") {
		t.Fatal("governed")
	}
	in := runInput(PipelineFacts{})
	in.Project = "web" // not covered by org: only the config policy is judged
	if ev := Evaluate(s, in, nil, t0); ev.Blocked() {
		t.Fatalf("%+v", ev)
	}
}

func TestWaivers(t *testing.T) {
	failing := Rules{Tests: &Tests{Min: 1}}
	w := func(mod func(*Waiver)) Waiver {
		x := Waiver{ID: "wvr_1", Project: "shop", Policy: "p", Rule: RuleTests, Reason: "hotfix", GrantedBy: "sam", GrantedAt: t0 - 1000, ExpiresAt: t0 + 3600000}
		if mod != nil {
			mod(&x)
		}
		return x
	}
	cases := []struct {
		name   string
		waiver Waiver
		in     func(Input) Input
		want   string
		note   string
	}{
		{"applies", w(nil), nil, ResultWaived, ""},
		{"expired", w(func(x *Waiver) { x.ExpiresAt = t0 }), nil, ResultFail, "expired at 2026-11-18 09:00 UTC"},
		{"not yet", w(func(x *Waiver) { x.GrantedAt = t0 + 1 }), nil, ResultFail, ""},
		{"other project", w(func(x *Waiver) { x.Project = "web" }), nil, ResultFail, ""},
		{"other rule", w(func(x *Waiver) { x.Rule = RuleSBOM }), nil, ResultFail, ""},
		{"other policy", w(func(x *Waiver) { x.Policy = "q" }), nil, ResultFail, ""},
		{"this run", w(func(x *Waiver) { x.Run = "run_1" }), nil, ResultWaived, ""},
		{"another run", w(func(x *Waiver) { x.Run = "run_2" }), nil, ResultFail, ""},
		{"this environment", w(func(x *Waiver) { x.Environment = "prod" }), nil, ResultWaived, ""},
		{"another environment", w(func(x *Waiver) { x.Environment = "staging" }), nil, ResultFail, ""},
		{"self, unprotected", w(func(x *Waiver) { x.GrantedBy = "dana" }), nil, ResultWaived, ""},
		{"self, protected", w(func(x *Waiver) { x.GrantedBy = "dana" }), func(in Input) Input { in.Protected = true; return in }, ResultFail,
			"waiver wvr_1 was granted by dana, who started the run; environment prod is protected"},
		{"self, protected, allow_self", w(func(x *Waiver) { x.GrantedBy = "dana" }),
			func(in Input) Input { in.Protected, in.SelfApproval = true, true; return in }, ResultWaived, ""},
		{"other person, protected", w(nil), func(in Input) Input { in.Protected = true; return in }, ResultWaived, ""},
		{"finding waiver is not a rule waiver", w(func(x *Waiver) { x.Finding = "fp1" }), nil, ResultFail, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := deployInput(Evidence{})
			if c.in != nil {
				in = c.in(in)
			}
			ev, r := one(t, failing, in, c.waiver)
			if r.Verdict != c.want {
				t.Fatalf("got %s, want %s; notes %v", r.Verdict, c.want, r.Notes)
			}
			if c.note != "" && !strings.Contains(strings.Join(r.Notes, "|"), c.note) {
				t.Fatalf("notes %v", r.Notes)
			}
			if c.want == ResultWaived {
				if r.Waiver == nil || r.Waiver.ID != "wvr_1" || ev.Verdict != VerdictPass || ev.Policies[0].Verdict != ResultWaived {
					t.Fatalf("%+v %s", r, ev.Verdict)
				}
			}
		})
	}
}

// Waivers of single findings pass the findings rule only when every finding
// that breaks it is listed and waived.
func TestFindingWaivers(t *testing.T) {
	rules := Rules{Findings: &Findings{MaxSeverity: "medium"}}
	ev := Evidence{
		Steps: []StepFact{{Step: "scan", Status: "succeeded", Findings: &FindingsFact{Counts: Counts{Total: 3, High: 2, Low: 1}, Tools: []string{"semgrep"},
			ByTool: map[string]Counts{"semgrep": {Total: 3, High: 2, Low: 1}}}}},
		Findings: []FindingFact{
			{Fingerprint: "fp1", Tool: "semgrep", Rule: "r1", Severity: "high", Step: "scan"},
			{Fingerprint: "fp2", Tool: "semgrep", Rule: "r2", Severity: "high", Step: "scan"},
		},
		FindingsListed: true,
	}
	fw := func(fp string) Waiver {
		return Waiver{ID: "w-" + fp, Project: "shop", Policy: "p", Rule: RuleFindings, Finding: fp, GrantedBy: "sam", GrantedAt: t0 - 1, ExpiresAt: t0 + 1000}
	}
	_, r := one(t, rules, deployInput(ev), fw("fp1"))
	if r.Verdict != ResultFail || !strings.Contains(r.Found, "2 worse than medium (1 of them waived)") {
		t.Fatalf("%s %q", r.Verdict, r.Found)
	}
	e2, r := one(t, rules, deployInput(ev), fw("fp1"), fw("fp2"))
	if r.Verdict != ResultWaived || e2.Verdict != VerdictPass || len(r.Waivers) != 2 {
		t.Fatalf("%s %q %v", r.Verdict, r.Found, r.Waivers)
	}
	// A third high finding that was not listed cannot be waived away.
	ev.Steps[0].Findings.Counts.High, ev.Steps[0].Findings.Counts.Total = 3, 4
	ev.FindingsListed = false
	_, r = one(t, rules, deployInput(ev), fw("fp1"), fw("fp2"))
	if r.Verdict != ResultFail || !strings.Contains(r.Found, "3 worse than medium (2 of them waived)") {
		t.Fatalf("%s %q", r.Verdict, r.Found)
	}
}

// An evaluation is reproducible from what it records: the policies, the
// input, the waivers and the time it was judged at.
func TestEvaluationsReproduceFromTheirRecord(t *testing.T) {
	s := set(Policy{Name: "a", Rules: Rules{Tests: &Tests{Min: 2}, Approvals: &Approvals{Min: 1}, Refs: &Refs{Branches: []string{"main"}}}},
		Policy{Name: "b", Mode: ModeReport, Rules: Rules{Findings: &Findings{MaxSeverity: "high", Scanners: []string{"gitleaks"}}}})
	in := deployInput(Evidence{Steps: []StepFact{
		{Step: "test", Status: "succeeded", Tests: &TestFact{Total: 4, Passed: 4}},
		{Step: "ok", Kind: "approval", Status: "succeeded", Approval: &ApprovalFact{By: "sam"}},
	}})
	ws := []Waiver{{ID: "w2", Project: "shop", Policy: "b", Rule: RuleFindings, GrantedBy: "sam", GrantedAt: t0 - 5, ExpiresAt: t0 + 5}}
	ev := Evaluate(s, in, ws, t0)
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var back Evaluation
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	var ps []Policy
	for _, p := range back.Policies {
		ps = append(ps, p.Policy)
	}
	again := Evaluate(Set{Sources: []Source{{Name: "config", Policies: ps}}}, back.Input, back.Waivers, back.At)
	if !reflect.DeepEqual(again, back) {
		a, _ := json.Marshal(again)
		t.Fatalf("replay differs:\n%s\n%s", b, a)
	}
	if ev.Verdict != VerdictPass || ev.Policies[1].Verdict != ResultWaived {
		t.Fatalf("%s %+v", ev.Verdict, ev.Policies[1])
	}
}

func TestSplitImage(t *testing.T) {
	cases := map[string][2]string{
		"alpine":                           {"docker.io/library/alpine", ""},
		"alpine:3.22":                      {"docker.io/library/alpine", ""},
		"team/app:1":                       {"docker.io/team/app", ""},
		"harbor.example.com/a/b:1@" + digA: {"harbor.example.com/a/b", digA},
		"localhost:5000/x":                 {"localhost:5000/x", ""},
		"localhost/x@" + digA:              {"localhost/x", digA},
	}
	for in, want := range cases {
		repo, dig := splitImage(in)
		if repo != want[0] || dig != want[1] {
			t.Errorf("%s: %s %s", in, repo, dig)
		}
	}
	if !matchRepo([]string{"harbor.example.com/**"}, "harbor.example.com/a/b") || matchRepo([]string{"harbor.example.com/**"}, "harbor.example.com") ||
		!matchRepo([]string{"docker.io/library/*"}, "docker.io/library/alpine") || matchRepo([]string{"docker.io/library/*"}, "docker.io/team/app") {
		t.Fatal("matchRepo")
	}
}

func TestFindingFloor(t *testing.T) {
	ps := []Policy{{Rules: Rules{Findings: &Findings{MaxSeverity: "high", PerTool: map[string]string{"x": "low"}}}}, {Rules: Rules{Tests: &Tests{}}}}
	if f := FindingFloor(ps); f != "low" {
		t.Fatal(f)
	}
	if f := FindingFloor(nil); f != "" {
		t.Fatal(f)
	}
	if !Worse("high", "medium") || Worse("medium", "medium") || !Worse("unknown", "none") || Worse("unknown", "low") {
		t.Fatal("Worse")
	}
}
