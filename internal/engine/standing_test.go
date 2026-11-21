package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/standards"
)

// osvSarif is what osv-scanner writes for a package with a fix: the message
// names the package and version, the rule's help ends with the table of
// fixed versions.
func osvSarif(rules ...string) ReportFile {
	var res []string
	var defs []string
	for _, r := range rules {
		res = append(res, fmt.Sprintf(`{"ruleId":%q,"level":"warning","message":{"text":"Package 'example.org/lib@1.0.0' is vulnerable to '%s'."},
			"locations":[{"physicalLocation":{"artifactLocation":{"uri":"go.mod"}}}]}`, r, r))
		defs = append(defs, fmt.Sprintf(`{"id":%q,"properties":{"security-severity":"7.5"},"shortDescription":{"text":"x"},
			"help":{"text":"Details.\n\n### Fixed Versions\n\n| Vulnerability ID | Package Name | Fixed Version |\n| --- | --- | --- |\n| %s | example.org/lib | 1.2.0 |\n"}}`, r, r))
	}
	return ReportFile{Path: ".standing/vuln-deps.sarif", Data: []byte(fmt.Sprintf(
		`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"osv-scanner","rules":[%s]}},"results":[%s]}]}`, strings.Join(defs, ","), strings.Join(res, ",")))}
}

func leakSarif(file string) ReportFile {
	return ReportFile{Path: ".standing/secrets.sarif", Data: []byte(fmt.Sprintf(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"gitleaks","rules":[{"id":"github-pat"}]}},
		"results":[{"ruleId":"github-pat","message":{"text":"github-pat has detected secret for file %s."},"locations":[{"physicalLocation":{"artifactLocation":{"uri":%q},"region":{"startLine":3}}}]}]}]}`, file, file))}
}

// inspectedRun creates a run whose pipeline has the Inspection added by the
// server, runs its steps with the files each check "found", and returns the
// run and the gate's final status.
func inspectedRun(t *testing.T, e *Engine, defaultBranch bool, found map[string]ReportFile) (string, *RunState) {
	t.Helper()
	pl := parse(t, "pipeline: demo\non: { push: { branches: [main] }, pull_request: {} }\nsteps:\n  build: {image: x, run: build}\n")
	off := map[string]standards.State{"license": standards.Off, "sbom": standards.Off, "vuln-reach-go": standards.Off, "sast-go": standards.Off}
	eff := standards.Resolve(standards.ResolveInput{Files: []string{"go.mod"}, Now: e.now(), Levels: []standards.LevelDoc{{Level: standards.LevelProject, Scope: "demo", Settings: standards.Settings{Checks: off}}}})
	pol := standards.PolicyOf(eff, e.now())
	pol.DefaultBranchRun = defaultBranch
	q, _, err := standards.Apply(pl, standards.InjectInput{Effective: eff, Policy: pol})
	if err != nil {
		t.Fatal(err)
	}
	trig := Trigger{Event: "pull_request", Ref: "refs/pull/7/head", PullRequest: 7, PullRequestInto: "main"}
	if defaultBranch {
		trig = Trigger{Event: "push", Ref: "refs/heads/main", Branch: "main"}
	}
	run, err := e.CreateRun(NewRun{Project: "demo", Pipeline: q, Trigger: trig, Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		j, err := e.Lease(t.Context(), runner, 0)
		if err != nil {
			t.Fatal(err)
		}
		if j == nil {
			break
		}
		if _, err := e.Start(j.LeaseID); err != nil {
			t.Fatal(err)
		}
		zero := 0
		res := Result{Outcome: "succeeded", ExitCode: &zero}
		if f, ok := found[strings.TrimPrefix(j.StepID, "inspect-")]; ok {
			res.Findings = &CollectedReports{Files: []ReportFile{f}}
		} else if strings.HasPrefix(j.StepID, "inspect-") {
			res.Findings = &CollectedReports{Files: []ReportFile{{Path: ".standing/x.sarif", Data: []byte(`{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"empty"}},"results":[]}]}`)}}}
		}
		if err := e.Finish(j.LeaseID, res); err != nil {
			t.Fatal(err)
		}
	}
	st, err := e.runState(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run.ID, st
}

// gateReason is the sentence the verdict step finished with.
func gateReason(t *testing.T, e *Engine, run string) string {
	t.Helper()
	evs, err := e.Events(run)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Type == StepFinished && ev.Data.Step == "inspection" {
			return ev.Data.Reason
		}
	}
	return ""
}

func TestGateIsSettledByTheServerAndMeasuresAgainstTheDefaultBranch(t *testing.T) {
	e, clk := newEngine(t)
	// The first run on the default branch: its findings are the backlog, and it does not fail.
	main1, st := inspectedRun(t, e, true, map[string]ReportFile{"vuln-deps": osvSarif("CVE-OLD"), "secrets": leakSarif("config/a.env")})
	if g := st.Steps["inspection"]; g == nil || g.Status != StepSucceeded {
		t.Fatalf("the verdict of the default branch's own run: %+v", g)
	}
	list, err := e.StandingFindings("demo", StandingQuery{})
	if err != nil || list.Open != 2 || list.BySeverity["high"] != 2 {
		t.Fatalf("%+v %v", list, err)
	}
	old := list.Findings[0]
	for _, f := range list.Findings {
		if f.Rule == "CVE-OLD" {
			old = f
			if f.Package != "example.org/lib" || f.Version != "1.0.0" || f.FixedIn != "1.2.0" || f.Kind != "vulnerability" || f.FirstRun != 1 {
				t.Errorf("%+v", f)
			}
		}
	}

	// A change that adds nothing passes, whatever the backlog holds.
	_, st = inspectedRun(t, e, false, map[string]ReportFile{"vuln-deps": osvSarif("CVE-OLD"), "secrets": leakSarif("config/a.env")})
	if g := st.Steps["inspection"]; g.Status != StepSucceeded {
		t.Fatalf("a change that introduces nothing failed: %+v", g)
	}

	// A change that adds a fixable high finding is blocked: the step fails with the reason.
	pr2, st := inspectedRun(t, e, false, map[string]ReportFile{"vuln-deps": osvSarif("CVE-OLD", "CVE-NEW"), "secrets": leakSarif("config/a.env")})
	g := st.Steps["inspection"]
	if g.Status != StepFailed || !strings.Contains(gateReason(t, e, pr2), "introduces 1 finding") {
		t.Fatalf("%+v %q", g, gateReason(t, e, pr2))
	}
	ins, err := e.RunInspection(pr2, InspectionQuery{})
	if err != nil || !ins.Blocked || ins.Counts["new"] != 1 || ins.Counts["existing"] != 2 || len(ins.Findings) != 3 || ins.Findings[0].Rule != "CVE-NEW" || !ins.Findings[0].Gating {
		t.Fatalf("%+v %v", ins, err)
	}
	if len(ins.Checks) != 2 || !ins.Checks[0].Ran || ins.Checks[0].ID == "" {
		t.Errorf("checks %+v", ins.Checks)
	}
	newFP := ins.Findings[0].Fingerprint

	// Accepting the risk needs a reason and an end date, and then the same change passes.
	if _, err := e.DecideFinding("demo", newFP, standards.StatusAccepted, "short", "ada", clk.Now().Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Errorf("a decision without a real reason: %v", err)
	}
	if _, err := e.DecideFinding("demo", newFP, standards.StatusAccepted, "fixed in the next release train", "ada", clk.Now().AddDate(2, 0, 0)); err == nil {
		t.Error("a decision that lasts two years was accepted")
	}
	if _, err := e.DecideFinding("demo", newFP, standards.StatusAccepted, "fixed in the next release train", "ada", clk.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, st = inspectedRun(t, e, false, map[string]ReportFile{"vuln-deps": osvSarif("CVE-OLD", "CVE-NEW"), "secrets": leakSarif("config/a.env")})
	if g := st.Steps["inspection"]; g.Status != StepSucceeded {
		t.Fatalf("an accepted risk still blocks: %+v", g)
	}
	// It expires: the same change is blocked again.
	clk.Add(72 * time.Hour)
	_, st = inspectedRun(t, e, false, map[string]ReportFile{"vuln-deps": osvSarif("CVE-OLD", "CVE-NEW"), "secrets": leakSarif("config/a.env")})
	if g := st.Steps["inspection"]; g.Status != StepFailed {
		t.Fatalf("an expired acceptance still holds: %+v", g)
	}
	// And a decision can be ended by hand.
	if _, err := e.DecideFinding("demo", newFP, standards.StatusFalsePositive, "the scanner is wrong here", "ada", clk.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.ReopenFinding("demo", newFP, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := e.ReopenFinding("demo", newFP, "bob"); err == nil {
		t.Error("reopened twice")
	}

	// The default branch fixes the old finding: it is recorded as fixed, with the time it took.
	clk.Add(24 * time.Hour)
	main2, _ := inspectedRun(t, e, true, map[string]ReportFile{"vuln-deps": osvSarif(), "secrets": leakSarif("config/a.env")})
	list, _ = e.StandingFindings("demo", StandingQuery{Status: "all"})
	var fixed, open int
	for _, f := range list.Findings {
		switch f.Status {
		case "fixed":
			fixed++
			if f.Fingerprint != old.Fingerprint && f.Rule == "CVE-OLD" {
				t.Error("a different fingerprint for the same finding")
			}
		case "open":
			open++
		}
	}
	if fixed != 1 || open != 1 {
		t.Fatalf("fixed %d open %d: %+v", fixed, open, list.Findings)
	}
	h, err := e.StandingHistory("demo", 30)
	if err != nil || h.FixedCount != 1 || h.MeanDaysToFix < 3 || len(h.Points) < 1 || h.Open != 1 {
		t.Fatalf("%+v %v", h, err)
	}
	_ = main1
	_ = main2
}

func TestGateWithoutABaselineDoesNotBlockAndSaysSo(t *testing.T) {
	e, _ := newEngine(t)
	pr, st := inspectedRun(t, e, false, map[string]ReportFile{"vuln-deps": osvSarif("CVE-X"), "secrets": leakSarif("a.env")})
	if g := st.Steps["inspection"]; g.Status != StepSucceeded || !strings.Contains(gateReason(t, e, pr), "has not been inspected yet") {
		t.Fatalf("%+v %q", g, gateReason(t, e, pr))
	}
	ins, _ := e.RunInspection(pr, InspectionQuery{})
	if !ins.NoBaseline || ins.Counts["unscored"] != 2 {
		t.Errorf("%+v", ins)
	}
}

func TestStandingSettingsAreVersioned(t *testing.T) {
	e, _ := newEngine(t)
	if d, err := e.StandingSettings("project", "demo"); err != nil || d != nil {
		t.Fatalf("%v %v", d, err)
	}
	_, a, err := e.SetStandingSettings("project", "demo", []byte(`{"checks":{"shell":"off"}}`), 0, "ada")
	if err != nil || a.Version != 1 {
		t.Fatalf("%+v %v", a, err)
	}
	if _, _, err := e.SetStandingSettings("project", "demo", []byte(`{}`), 0, "bob"); err == nil || !strings.Contains(err.Error(), "changed since you read") {
		t.Fatalf("a stale write was accepted: %v", err)
	}
	b, a2, err := e.SetStandingSettings("project", "demo", []byte(`{"checks":{"shell":"advise"}}`), 1, "bob")
	if err != nil || a2.Version != 2 || b.Version != 1 {
		t.Fatalf("%+v %+v %v", b, a2, err)
	}
	all, _ := e.AllStandingSettings()
	if len(all) != 1 || all[0].UpdatedBy != "bob" {
		t.Errorf("%+v", all)
	}
}
