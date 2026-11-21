package standards

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/findings"
)

var testNow = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

func TestToolsArePinnedByDigest(t *testing.T) {
	tools, err := Tools()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) < 9 {
		t.Fatalf("only %d tools", len(tools))
	}
	for _, tl := range tools {
		if !digestRe.MatchString(tl.Image) || tl.License == "" || tl.Home == "" {
			t.Errorf("tool %s is not fully described: %+v", tl.Name, tl)
		}
		if !strings.Contains(tl.Image, tl.Version) && tl.Name != "launchway-pipeline-lint" && tl.Name != "govulncheck" {
			t.Errorf("the image of %s does not carry its version %s: %s", tl.Name, tl.Version, tl.Image)
		}
	}
}

func TestEveryCheckHasATool_AScript_AndSaneBounds(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Checks() {
		if seen[c.ID] {
			t.Errorf("check %s is listed twice", c.ID)
		}
		seen[c.ID] = true
		if _, ok := ToolNamed(c.Tool); !ok {
			t.Errorf("check %s names the tool %q, which is not in tools.yaml", c.ID, c.Tool)
		}
		if c.Available() && (strings.TrimSpace(c.Script) == "" || len(c.Outputs) == 0 || c.Budget < time.Minute || c.Budget > time.Hour) {
			t.Errorf("check %s has no script, no outputs, or a budget out of range: %+v", c.ID, c.Budget)
		}
		if c.Kind.Security() != c.Gates && c.Gates && !c.Kind.Security() && c.Kind != KindLicense {
			t.Errorf("check %s gates though it is not a security or license kind", c.ID)
		}
		if c.Default == Block && !c.Gates {
			t.Errorf("check %s blocks by default but has no gate", c.ID)
		}
		if !c.Restricted {
			t.Errorf("check %s may not run in a restricted run; every check here needs no secret", c.ID)
		}
		if len(c.Detect) == 0 || c.Why == "" || c.Title == "" {
			t.Errorf("check %s is not described", c.ID)
		}
		if c.Ecosystem != Any && !contains(Ecosystems, c.Ecosystem) {
			t.Errorf("check %s has an unknown ecosystem %q", c.ID, c.Ecosystem)
		}
	}
}

// The scripts run with /bin/sh in the tool's image: they must at least be
// valid POSIX shell.
func TestScriptsAreValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	for _, c := range Checks() {
		if !c.Available() {
			continue
		}
		cmd := exec.Command(sh, "-n")
		cmd.Stdin = strings.NewReader(c.Script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("the script of %s is not valid shell: %v\n%s", c.ID, err, out)
		}
	}
}

func TestDetectionByEcosystem(t *testing.T) {
	cases := []struct {
		files []string
		eco   string
		check string
		yes   bool
	}{
		{[]string{"go.mod", "main.go"}, Go, "sast-go", true},
		{[]string{"go.mod"}, Go, "vuln-reach-go", true},
		{[]string{"app/main.py"}, Python, "sast-python", true},
		{[]string{"requirements-dev.txt"}, Python, "vuln-deps", true},
		{[]string{"package-lock.json"}, JavaScript, "vuln-deps", true},
		{[]string{"svc/pom.xml"}, Java, "vuln-deps", true},
		{[]string{"build/Dockerfile.prod"}, Container, "dockerfile", true},
		{[]string{"deploy/Chart.yaml"}, Kubernetes, "k8s-practice", true},
		{[]string{"k8s/app.yaml"}, Kubernetes, "k8s-practice", true},
		{[]string{"infra/main.tf"}, Infra, "misconfig", true},
		{[]string{"scripts/run.sh"}, Shell, "shell", true},
		{[]string{".launchway/pipeline.yaml"}, Any, "pipeline-lint", true},
		{[]string{"README.md"}, Any, "secrets", true},
		{[]string{"README.md"}, Go, "sast-go", false},
		{[]string{"vendor/x/go.mod", "node_modules/a/package-lock.json"}, Go, "vuln-deps", false},
		{[]string{"docs/readme.txt"}, Python, "vuln-deps", false},
	}
	for _, c := range cases {
		ck, _ := CheckByID(c.check)
		why, ok := ck.Applies(c.files)
		if ok != c.yes {
			t.Errorf("%s on %v: applies=%v (%s), want %v", c.check, c.files, ok, why, c.yes)
		}
		if ok && why == "" {
			t.Errorf("%s gives no reason", c.check)
		}
		if c.yes {
			found := false
			for _, d := range Detect(c.files) {
				found = found || d.Ecosystem == c.eco
			}
			if !found {
				t.Errorf("Detect(%v) lacks %s", c.files, c.eco)
			}
		}
	}
}

// recorded reads a real tool output made on 2026-11-21 (testdata/recorded).
func recorded(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "recorded", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseRecorded(t *testing.T, name, check string) []Finding {
	t.Helper()
	data, _, err := Adapt(recorded(t, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	rep, err := findings.Parse(context.Background(), data, findings.Limits{MaxHelpBytes: 16 << 10})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return Normalize(check, rep)
}

func find(fs []Finding, rule string) *Finding {
	for i := range fs {
		if fs[i].Rule == rule {
			return &fs[i]
		}
	}
	return nil
}

func TestNormalizeRealOutputs(t *testing.T) {
	t.Run("osv-scanner: package, version, fixed version, advisories", func(t *testing.T) {
		fs := parseRecorded(t, "osv.sarif", "vuln-deps")
		f := find(fs, "CVE-2023-45288")
		if f == nil {
			t.Fatalf("no CVE-2023-45288 among %d findings", len(fs))
		}
		if f.Package != "golang.org/x/net" || f.Version != "0.17.0" || f.FixedIn != "0.23.0" || f.Kind != KindVulnerability || f.File != "go.mod" {
			t.Errorf("%+v", f)
		}
		if !f.hasAlias("GHSA-4v7x-pqxf-cx7m") || !f.hasAlias("GO-2024-2687") {
			t.Errorf("aliases %v", f.Advisories)
		}
		if f.Severity != findings.Medium {
			t.Errorf("severity %s", f.Severity)
		}
		if !strings.Contains(f.Fix, "0.23.0") {
			t.Errorf("fix text %q", f.Fix)
		}
	})
	t.Run("govulncheck: reachable or not", func(t *testing.T) {
		fs := parseRecorded(t, "govulncheck.sarif", "vuln-reach-go")
		yes, no := 0, 0
		for _, f := range fs {
			switch f.Reachable {
			case "yes":
				yes++
				if f.Severity != findings.High {
					t.Errorf("a reachable finding is %s", f.Severity)
				}
			case "no":
				no++
			}
		}
		if yes == 0 || no == 0 {
			t.Errorf("reachable yes=%d no=%d of %d", yes, no, len(fs))
		}
		if f := find(fs, "GO-2024-3333"); f == nil || f.Package != "golang.org/x/net/html" || f.Reachable != "yes" {
			t.Errorf("%+v", f)
		}
	})
	t.Run("gitleaks: a secret, never its value", func(t *testing.T) {
		fs := parseRecorded(t, "gitleaks.sarif", "secrets")
		if len(fs) != 1 || fs[0].Kind != KindSecret || fs[0].Rule != "github-pat" || fs[0].Severity < findings.High && fs[0].Severity != findings.High {
			t.Fatalf("%+v", fs)
		}
		if strings.Contains(fs[0].Title, "aB3d") || fs[0].File != "go/leak.txt" || fs[0].Line != 1 {
			t.Errorf("%+v", fs[0])
		}
	})
	t.Run("gosec", func(t *testing.T) {
		fs := parseRecorded(t, "gosec.sarif", "sast-go")
		if f := find(fs, "G401"); f == nil || f.Kind != KindCode || f.Severity != findings.Medium || f.File != "main.go" {
			t.Errorf("%+v", f)
		}
		if find(fs, "G204") == nil {
			t.Errorf("no G204 (command from variable) in %v", rules(fs))
		}
	})
	t.Run("ruff: a suggested fix and advice is never alarming", func(t *testing.T) {
		fs := parseRecorded(t, "ruff.sarif", "lint-python")
		f := find(fs, "E401")
		if f == nil || f.Fix != "Split imports" || f.Kind != KindStyle || f.Severity != findings.Low || f.File != "py/app.py" {
			t.Errorf("%+v", f)
		}
	})
	t.Run("hadolint, trivy config, kube-linter", func(t *testing.T) {
		if f := find(parseRecorded(t, "hadolint.sarif", "dockerfile"), "DL3007"); f == nil || f.Kind != KindConfig || f.File != "go/Dockerfile" {
			t.Errorf("hadolint %+v", f)
		}
		tc := parseRecorded(t, "trivy-config.sarif", "misconfig")
		if f := find(tc, "DS-0001"); f == nil || !strings.HasPrefix(f.Title, "Specify a tag") {
			t.Errorf("trivy %+v", f)
		}
		if f := find(parseRecorded(t, "kubelinter.sarif", "k8s-practice"), "latest-tag"); f == nil || f.File != "go/k8s/deploy.yaml" {
			t.Errorf("kube-linter %+v", f)
		}
	})
	t.Run("shellcheck json becomes findings", func(t *testing.T) {
		fs := parseRecorded(t, "shellcheck.json", "shell")
		if len(fs) != 5 || fs[0].Rule != "SC2045" || fs[0].Line != 2 || fs[0].File != "sh/run.sh" || fs[0].Kind != KindStyle {
			t.Errorf("%d: %+v", len(fs), fs)
		}
	})
	t.Run("licenses: denied, not allowed, unknown", func(t *testing.T) {
		fs := parseRecorded(t, "licenses-wrapped.json", "license")
		var denied, notAllowed, unknown int
		for _, f := range fs {
			switch f.Rule {
			case "license-denied":
				denied++
				if f.Severity != findings.High {
					t.Errorf("denied is %s", f.Severity)
				}
			case "license-not-allowed":
				notAllowed++
			case "license-unknown":
				unknown++
				if f.Severity != findings.Low {
					t.Errorf("unknown is %s", f.Severity)
				}
			}
		}
		if denied == 0 || unknown == 0 {
			t.Errorf("denied %d, not allowed %d, unknown %d of %d: %v", denied, notAllowed, unknown, len(fs), rules(fs))
		}
		if f := find(fs, "license-denied"); f.Package != "golang.org/x/net" || f.Version != "0.17.0" || f.File != "go/go.mod" {
			t.Errorf("%+v", f)
		}
	})
}

func rules(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	return out
}

// The fingerprint is the finding's identity across runs. It must not change
// when the code around it moves, and must tell identical findings apart.
func TestFingerprintStaysWhenLinesShift(t *testing.T) {
	orig := recorded(t, "gosec.sarif")
	shifted := []byte(strings.NewReplacer(`"startLine":16`, `"startLine":46`, `"endLine":16`, `"endLine":46`, `"startLine": 16`, `"startLine": 46`).Replace(string(orig)))
	if string(shifted) == string(orig) {
		t.Fatal("the recording has no line 16 to shift")
	}
	read := func(b []byte) []Finding {
		rep, err := findings.Parse(context.Background(), b, findings.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		return Normalize("sast-go", rep)
	}
	a, b := read(orig), read(shifted)
	if len(a) != len(b) || len(a) == 0 {
		t.Fatalf("%d and %d findings", len(a), len(b))
	}
	moved := 0
	for i := range a {
		if a[i].Fingerprint != b[i].Fingerprint {
			t.Errorf("the fingerprint of %s changed when lines shifted", a[i].Rule)
		}
		if a[i].Line != b[i].Line {
			moved++
		}
	}
	if moved == 0 {
		t.Error("no finding moved: the test shifted nothing that matters")
	}
	seen := map[string]bool{}
	for _, f := range a {
		if seen[f.Fingerprint] {
			t.Errorf("two findings share the fingerprint %s (%s)", f.Fingerprint, f.Rule)
		}
		seen[f.Fingerprint] = true
	}
}

func TestIdenticalFindingsInOneFileKeepApart(t *testing.T) {
	seen := map[string]int{}
	f := findings.Finding{Tool: "x", RuleID: "R1", Severity: findings.Low, Message: "Same thing on line 12", File: "a.go", StartLine: 12}
	a := NormalizeOne("sast-go", f, findings.Rule{}, seen)
	f.StartLine, f.Message = 40, "Same thing on line 40"
	b := NormalizeOne("sast-go", f, findings.Rule{}, seen)
	if a.Fingerprint == b.Fingerprint {
		t.Error("two findings of one rule in one file got the same fingerprint")
	}
	// A later run that sees them in the same order gives the same keys.
	seen2 := map[string]int{}
	f.StartLine, f.Message = 15, "Same thing on line 15"
	a2 := NormalizeOne("sast-go", f, findings.Rule{}, seen2)
	if a2.Fingerprint != a.Fingerprint {
		t.Error("the first finding changed its fingerprint when its line number changed")
	}
}

func TestAdaptNativeFormatAndUnknownFormats(t *testing.T) {
	native := `{"format":"launchway-findings/1","tool":"acme-scan","findings":[
	  {"rule":"ACME-1","severity":"high","message":"is vulnerable to ACME-1.","package":"left-pad","version":"1.0.0","fixed_in":"1.3.0","file":"package-lock.json"},
	  {"rule":"ACME-2","severity":"low","message":"A style problem","file":"a.js","line":3}]}`
	out, format, err := Adapt([]byte(native))
	if err != nil || format != FormatNative {
		t.Fatalf("%q %v", format, err)
	}
	rep, err := findings.Parse(context.Background(), out, findings.Limits{})
	if err != nil || len(rep.Findings) != 2 {
		t.Fatalf("%v %v", err, rep)
	}
	fs := Normalize("", rep)
	if fs[0].Tool != "acme-scan" || fs[0].Severity != findings.High || fs[0].Line != 0 || fs[1].Line != 3 || fs[1].Severity != findings.Low {
		t.Errorf("%+v", fs)
	}
	for _, bad := range []string{`{"format":"launchway-findings/1","findings":[]}`, `{"format":"launchway-findings/1","tool":"x","findings":[{"severity":"high"}]}`,
		`{"format":"launchway-findings/1","tool":"x","findings":[{"rule":"r","severity":"awful"}]}`} {
		if _, _, err := Adapt([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	// SARIF and anything else pass through untouched.
	for _, same := range []string{`{"runs":[]}`, `[1,2]`, `not json`, `{"something":"else"}`} {
		out, format, err := Adapt([]byte(same))
		if err != nil || format != "" || string(out) != same {
			t.Errorf("%q changed: %q %q %v", same, out, format, err)
		}
	}
}

func TestGlobMatch(t *testing.T) {
	for pat, want := range map[string]map[string]bool{
		"vendor/**":      {"vendor/a/b.go": true, "src/vendor/a.go": false},
		"**/testdata/**": {"a/testdata/x.json": true, "testdata/x.json": true, "a/b/c.json": false},
		"*.go":           {"main.go": true, "pkg/main.go": false},
		"go.mod":         {"go.mod": true, "x/go.mod": false},
		"**/go.mod":      {"go.mod": true, "x/y/go.mod": true},
	} {
		for p, w := range want {
			if GlobMatch(pat, p) != w {
				t.Errorf("GlobMatch(%q, %q) = %v, want %v", pat, p, !w, w)
			}
		}
	}
}

// The reference page names every check, its tool and the tool's license, so a
// check cannot be added or a tool changed without the page saying so.
func TestReferencePageListsEveryCheckAndTool(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "standing-orders.md"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	for _, c := range Checks() {
		row := ""
		for _, line := range strings.Split(page, "\n") {
			if strings.HasPrefix(line, "| `"+c.ID+"` |") {
				row = line
			}
		}
		if row == "" {
			t.Errorf("the check %s has no row in docs/reference/standing-orders.md", c.ID)
			continue
		}
		tool, _ := ToolNamed(c.Tool)
		if !strings.Contains(row, c.Tool) || !strings.Contains(row, tool.License) && c.Available() {
			t.Errorf("the row of %s should name its tool %s and the license %s: %s", c.ID, c.Tool, tool.License, row)
		}
	}
	for _, tl := range func() []Tool { x, _ := Tools(); return x }() {
		if tl.Name != "launchway-pipeline-lint" && tl.Name != "govulncheck" && !strings.Contains(page, tl.Name) {
			t.Errorf("the tool %s is not named in the reference page", tl.Name)
		}
	}
	if !strings.Contains(page, ProfileVersion) {
		t.Errorf("the reference page does not say it is for profile %s", ProfileVersion)
	}
}
