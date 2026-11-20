package policy

import (
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const goodFile = `policies:
  - name: prod-evidence
    description: What reaches production must show its tests and scans.
    mode: enforce
    scope: { projects: [shop, "pay-*"], environments: [prod], branches: [main], events: [push, manual] }
    rules:
      tests: { min: 10 }
      findings: { max_severity: medium, per_tool: { Trivy: high }, scanners: [gitleaks] }
      sbom: {}
      deployed_images: { digest: true, built_by_run: true, signed: true, scanned: true }
      approvals: { min: 2 }
      refs: { branches: [main], tags: ["v*"] }
  - name: pipeline-shape
    mode: report
    rules:
      library_versions: { pinned: tag }
      required_templates: [ { library: ci-templates, template: security-scan, versions: ["v3", "v3.*"] } ]
      step_images: { digest: true, registries: ["harbor.example.com/**", docker.io/library/alpine] }
      max_timeout: 2h
      forbid_secrets: { events: [pull_request, draft] }
      image_push: { scan: high, sign: runner, sbom: true, provenance: true, registries: [harbor] }
`

func TestParseAGoodFile(t *testing.T) {
	ps, err := ParseFile([]byte(goodFile), "policies/org.yaml", "org")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 {
		t.Fatalf("%d policies", len(ps))
	}
	p := ps[0]
	if p.Name != "prod-evidence" || p.Mode != ModeEnforce || p.Source != "org" || p.File != "policies/org.yaml" || p.Line != 2 {
		t.Fatalf("%+v", p)
	}
	if got := p.RuleList(); !slices.Equal(got, []string{RuleTests, RuleFindings, RuleSBOM, RuleDeployedImages, RuleApprovals, RuleRefs}) {
		t.Fatalf("rules %v", got)
	}
	if p.Rules.Tests.Min != 10 || p.Rules.SBOM.MinComponents != 1 || p.Rules.Approvals.Min != 2 || p.Rules.Findings.PerTool["Trivy"] != "high" {
		t.Fatalf("%+v", p.Rules)
	}
	if !slices.Equal(p.Points(), []string{PointDeploy}) || !slices.Equal(ps[1].Points(), []string{PointRun, PointImage}) {
		t.Fatalf("points %v %v", p.Points(), ps[1].Points())
	}
	if !p.Governs("pay-api", "main", "push") || p.Governs("pay-api", "dev", "push") || p.Governs("web", "main", "push") ||
		p.Governs("shop", "", "pull_request") || p.Governs("shop", "main", "schedule") {
		t.Fatal("scope")
	}
	if !p.AppliesTo(PointDeploy, "shop", "main", "push", "prod") || p.AppliesTo(PointDeploy, "shop", "main", "push", "staging") ||
		p.AppliesTo(PointRun, "shop", "main", "push", "") {
		t.Fatal("applies")
	}
}

// Every mistake is refused with the file and the line it is on.
func TestParseRefusesMistakesWithTheirLines(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"not yaml", "policies:\n  - name: a\n    mode: b: c\n", "x.yaml:3: not valid YAML: line 3: mapping values are not allowed"},
		{"empty", "", "x.yaml:1: the file is empty"},
		{"top key", "rules: {}\n", `x.yaml:1: rules: unknown key "rules"`},
		{"unknown rule", "policies:\n  - name: a\n    mode: report\n    rules:\n      no_friday_deploys: true\n",
			`x.yaml:5: policies[0].rules.no_friday_deploys: unknown rule "no_friday_deploys"; the rules are library_versions`},
		{"no mode", "policies:\n  - name: a\n    rules: { tests: {} }\n", "x.yaml:2: policies[0]: a policy needs a mode"},
		{"bad mode", "policies:\n  - name: a\n    mode: warn\n    rules: { tests: {} }\n", `x.yaml:3: policies[0].mode: "warn": write report or enforce`},
		{"no name", "policies:\n  - mode: report\n    rules: { tests: {} }\n", "x.yaml:2: policies[0]: a policy needs a name"},
		{"bad name", "policies:\n  - name: Prod_Gate\n    mode: report\n    rules: { tests: {} }\n", `x.yaml:2: policies[0].name: "Prod_Gate"`},
		{"no rules", "policies:\n  - name: a\n    mode: report\n", "x.yaml:2: policies[0]: a policy needs rules"},
		{"empty rules", "policies:\n  - name: a\n    mode: report\n    rules: {}\n", "x.yaml:4: policies[0].rules: a policy needs at least one rule"},
		{"duplicate", "policies:\n  - { name: a, mode: report, rules: { tests: {} } }\n  - { name: a, mode: report, rules: { tests: {} } }\n",
			"x.yaml:3: policies[1].name: policy a is already defined on line 2"},
		{"env scope run rule", "policies:\n  - name: a\n    mode: report\n    scope: { environments: [prod] }\n    rules:\n      max_timeout: 1h\n",
			"x.yaml:6: policies[0].rules.max_timeout: a policy scoped to environments may only have deploy rules"},
		{"bad event", "policies:\n  - name: a\n    mode: report\n    scope: { events: [tag] }\n    rules: { tests: {} }\n",
			`x.yaml:4: policies[0].scope.events: "tag" is not an event`},
		{"bad severity", "policies:\n  - name: a\n    mode: report\n    rules:\n      findings: { max_severity: severe }\n",
			`x.yaml:5: policies[0].rules.findings.max_severity: "severe": write none, low, medium, high or critical`},
		{"bad duration", "policies:\n  - name: a\n    mode: report\n    rules:\n      max_timeout: forever\n", `x.yaml:5: policies[0].rules.max_timeout: "forever"`},
		{"bad bool", "policies:\n  - name: a\n    mode: report\n    rules:\n      deployed_images: { digest: yes please }\n",
			"x.yaml:5: policies[0].rules.deployed_images.digest: must be true or false"},
		{"rule key", "policies:\n  - name: a\n    mode: report\n    rules:\n      tests: { minimum: 3 }\n", `x.yaml:5: policies[0].rules.tests.minimum: unknown key "minimum"`},
		{"empty rule", "policies:\n  - name: a\n    mode: report\n    rules:\n      step_images: {}\n", "x.yaml:5: policies[0].rules.step_images: say digest: true"},
		{"bad pattern", "policies:\n  - name: a\n    mode: report\n    scope: { projects: [\"[\"] }\n    rules: { tests: {} }\n", `"[" is not a valid pattern`},
		{"sign", "policies:\n  - name: a\n    mode: report\n    rules:\n      image_push: { sign: maybe }\n", `x.yaml:5: policies[0].rules.image_push.sign: "maybe"`},
		{"twice", "policies:\n  - name: a\n    mode: report\n    rules:\n      tests: {}\n      tests: {}\n", "x.yaml:6: policies[0].rules.tests: written twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseFile([]byte(c.text), "x.yaml", "org")
			if err == nil {
				t.Fatalf("accepted:\n%s", c.text)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %q, want it to contain %q", err, c.want)
			}
			if _, ok := err.(Problems); !ok {
				t.Fatalf("not Problems: %T", err)
			}
		})
	}
}

// Inline policies in the server's configuration are read from the node, so
// their problems have the configuration file's lines.
func TestParseListFromConfiguration(t *testing.T) {
	var doc yaml.Node
	text := "listen: x\npolicy:\n  policies:\n    - name: a\n      mode: enforce\n      rules: { teste: {} }\n"
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	n := doc.Content[0].Content[3].Content[1]
	_, err := ParseList(n, "launchway.yaml", "policy.policies", "config")
	if err == nil || !strings.Contains(err.Error(), `launchway.yaml:6: policy.policies[0].rules.teste: unknown rule "teste"`) {
		t.Fatalf("%v", err)
	}
}

func TestCheckRefusesTheSameNameFromTwoSources(t *testing.T) {
	a, _ := ParseFile([]byte("policies:\n  - { name: gate, mode: report, rules: { tests: {} } }\n"), "a.yaml", "org")
	b, _ := ParseFile([]byte("policies:\n\n  - { name: gate, mode: enforce, rules: { sbom: {} } }\n"), "b.yaml", "team")
	err := Check(a, b)
	if err == nil || !strings.Contains(err.Error(), "b.yaml:3: gate: policy gate is also defined in org (a.yaml:2)") {
		t.Fatalf("%v", err)
	}
}

func TestEventsMatchThePipelinePackage(t *testing.T) {
	// The policy package keeps its own list of events; this guards it.
	want := []string{"push", "manual", "draft", "pull_request", "schedule", "webhook"}
	if !slices.Equal(Events, want) {
		t.Fatalf("%v", Events)
	}
}
