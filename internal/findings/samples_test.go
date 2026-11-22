package findings

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func readSample(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	// Made-up tokens in the samples are written with @@ inside them, so
	// that no file in the repository holds a string a secret scanner would
	// take for a real credential.
	return bytes.ReplaceAll(b, []byte("@@"), nil)
}

func parseBytes(t testing.TB, data []byte, lim Limits) *Report {
	t.Helper()
	r, err := Parse(context.Background(), data, lim)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	checkReport(t, r, lim)
	return r
}

func parseSample(t testing.TB, name string) *Report {
	t.Helper()
	r := parseBytes(t, readSample(t, name), Limits{})
	if r.Warning != "" || r.Truncated {
		t.Errorf("%s: warning %q, truncated %v", name, r.Warning, r.Truncated)
	}
	return r
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// checkReport checks the promises every report keeps, whatever the input.
func checkReport(t testing.TB, r *Report, lim Limits) {
	t.Helper()
	lim = lim.withDefaults()
	n := r.Counts
	if n.Total != n.Critical+n.High+n.Medium+n.Low+n.Unknown || n.Total < 0 || n.Suppressed < 0 {
		t.Errorf("counts do not add up: %+v", n)
	}
	if len(r.Findings) > lim.MaxFindings || len(r.Findings) > n.Total {
		t.Errorf("%d findings kept, counts %+v, MaxFindings %d", len(r.Findings), n, lim.MaxFindings)
	}
	if len(r.Findings) < n.Total && !r.Truncated {
		t.Errorf("%d of %d findings kept, but not marked truncated", len(r.Findings), n.Total)
	}
	if r.Format != FormatSARIF || r.Findings == nil || r.Rules == nil || r.Tools == nil {
		t.Errorf("format %q, nil lists: %v %v %v", r.Format, r.Findings == nil, r.Rules == nil, r.Tools == nil)
	}
	checkString(t, "Warning", r.Warning, maxWarnBytes, false)
	tools := map[string]bool{}
	for _, tool := range r.Tools {
		checkString(t, "Tools", tool, lim.MaxNameBytes, false)
		if tools[tool] || tool == "" {
			t.Errorf("tool %q is listed twice or empty", tool)
		}
		tools[tool] = true
	}
	fps := map[string]bool{}
	used := map[string]bool{}
	for i, f := range r.Findings {
		checkString(t, "Tool", f.Tool, lim.MaxNameBytes, false)
		checkString(t, "RuleID", f.RuleID, lim.MaxNameBytes, false)
		checkString(t, "Message", f.Message, lim.MaxMessageBytes, true)
		checkString(t, "File", f.File, lim.MaxPathBytes, false)
		if !tools[f.Tool] {
			t.Errorf("finding %d: tool %q is not in Tools", i, f.Tool)
		}
		if f.RuleID == "" {
			t.Errorf("finding %d has no rule id", i)
		}
		if !slices.Contains(Severities, f.Severity) {
			t.Errorf("finding %d: severity %q", i, f.Severity)
		}
		switch f.Level {
		case "error", "warning", "note", "none":
		default:
			t.Errorf("finding %d: level %q", i, f.Level)
		}
		for _, v := range []int{f.StartLine, f.EndLine, f.StartColumn} {
			if v < 0 || v >= 10_000_000 {
				t.Errorf("finding %d: line or column %d", i, v)
			}
		}
		if !hex32.MatchString(f.Fingerprint) || fps[f.Fingerprint] {
			t.Errorf("finding %d: fingerprint %q is malformed or repeated", i, f.Fingerprint)
		}
		fps[f.Fingerprint] = true
		if f.Secret && (f.Severity.rank() < High.rank() || !strings.HasSuffix(f.Message, secretText) && !strings.HasSuffix(f.Message, "…")) {
			t.Errorf("finding %d is a secret, but has severity %s and message %q", i, f.Severity, f.Message)
		}
		used[f.Tool+"\x00"+f.RuleID] = true
	}
	for i, ru := range r.Rules {
		checkString(t, "Rule.Tool", ru.Tool, lim.MaxNameBytes, false)
		checkString(t, "Rule.ID", ru.ID, lim.MaxNameBytes, false)
		checkString(t, "Rule.Name", ru.Name, lim.MaxNameBytes, false)
		checkString(t, "Rule.Short", ru.Short, min(maxDescBytes, lim.MaxHelpBytes), true)
		checkString(t, "Rule.Full", ru.Full, min(maxDescBytes, lim.MaxHelpBytes), true)
		checkString(t, "Rule.Help", ru.Help, lim.MaxHelpBytes, true)
		checkString(t, "Rule.HelpURI", ru.HelpURI, maxURIBytes, false)
		if ru.HelpURI != "" && !strings.HasPrefix(ru.HelpURI, "http://") && !strings.HasPrefix(ru.HelpURI, "https://") {
			t.Errorf("rule %d: help URI %q", i, ru.HelpURI)
		}
		if len(ru.Tags) > maxTags {
			t.Errorf("rule %d has %d tags", i, len(ru.Tags))
		}
		for _, tag := range ru.Tags {
			checkString(t, "Rule.Tags", tag, maxTagBytes, false)
		}
		if i > 0 {
			prev := r.Rules[i-1]
			if prev.Tool > ru.Tool || prev.Tool == ru.Tool && prev.ID >= ru.ID {
				t.Errorf("rules are not sorted: %q/%q before %q/%q", prev.Tool, prev.ID, ru.Tool, ru.ID)
			}
		}
		if !used[ru.Tool+"\x00"+ru.ID] && !ruleUsedBySubID(r, ru) {
			t.Errorf("rule %s/%s is not used by any finding", ru.Tool, ru.ID)
		}
	}
}

func ruleUsedBySubID(r *Report, ru Rule) bool {
	for _, f := range r.Findings {
		if f.Tool == ru.Tool && strings.HasPrefix(f.RuleID, ru.ID+"/") {
			return true
		}
	}
	return false
}

// checkString checks text is valid UTF-8 within max bytes, holds no
// control characters (newlines only where multiline), and has no white
// space at either end.
func checkString(t testing.TB, field, s string, max int, multiline bool) {
	t.Helper()
	if len(s) > max {
		t.Errorf("%s is %d bytes, over %d", field, len(s), max)
	}
	if !utf8.ValidString(s) {
		t.Errorf("%s is not valid UTF-8: %q", field, s)
	}
	if s != strings.TrimSpace(s) {
		t.Errorf("%s has white space at an end: %q", field, s)
	}
	for _, r := range s {
		if (r < 0x20 && !(multiline && r == '\n')) || r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) {
			t.Errorf("%s holds control character %U: %q", field, r, s)
			return
		}
	}
}

func wantCounts(t testing.TB, r *Report, want Counts) {
	t.Helper()
	if r.Counts != want {
		t.Errorf("counts = %+v, want %+v", r.Counts, want)
	}
}

func findOne(t testing.TB, r *Report, rule, file string, line int) Finding {
	t.Helper()
	var got []Finding
	for _, f := range r.Findings {
		if f.RuleID == rule && f.File == file && f.StartLine == line {
			got = append(got, f)
		}
	}
	if len(got) != 1 {
		t.Fatalf("%d findings of %s at %s:%d", len(got), rule, file, line)
	}
	return got[0]
}

func findRule(t testing.TB, r *Report, id string) Rule {
	t.Helper()
	for _, ru := range r.Rules {
		if ru.ID == id {
			return ru
		}
	}
	t.Fatalf("no rule %q", id)
	return Rule{}
}

func fingerprintSet(r *Report) map[string]Finding {
	m := map[string]Finding{}
	for _, f := range r.Findings {
		m[f.Fingerprint] = f
	}
	return m
}

// noSecrets marshals the whole report and checks none of the strings
// appear anywhere in it.
func noSecrets(t testing.TB, r *Report, secrets ...string) {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		if strings.Contains(string(b), s) {
			t.Errorf("the report holds %q", s)
		}
	}
}

func TestBandit(t *testing.T) {
	r := parseSample(t, "bandit.sarif")
	wantCounts(t, r, Counts{Total: 4, High: 1, Medium: 2, Low: 1})
	if !slices.Equal(r.Tools, []string{"Bandit"}) || len(r.Rules) != 4 {
		t.Errorf("tools %v, %d rules", r.Tools, len(r.Rules))
	}
	f := findOne(t, r, "B602", "app.py", 9)
	if f.Severity != High || f.Level != "error" || f.Tool != "Bandit" || f.EndLine != 9 || f.StartColumn != 12 || f.Secret {
		t.Errorf("B602 = %+v", f)
	}
	if f.Message != "subprocess call with shell=True identified, security issue." {
		t.Errorf("message = %q", f.Message)
	}
	// No level, issue_severity MEDIUM: the level defaults to warning.
	if f := findOne(t, r, "B506", "app.py", 13); f.Severity != Medium || f.Level != "warning" {
		t.Errorf("B506 = %+v", f)
	}
	// level note, issue_severity LOW.
	if f := findOne(t, r, "B404", "app.py", 1); f.Severity != Low || f.Level != "note" {
		t.Errorf("B404 = %+v", f)
	}
	ru := findRule(t, r, "B307")
	if ru.Name != "blacklist" || ru.HelpURI != "https://bandit.readthedocs.io/en/1.9.4/blacklists/blacklist_calls.html#b307-eval" || !slices.Equal(ru.Tags, []string{"security", "external/cwe/cwe-78"}) {
		t.Errorf("B307 rule = %+v", ru)
	}
	noSecrets(t, r, "import subprocess", "return eval(expr)", "yaml.load(data)")
}

func TestFixedRunsKeepFingerprints(t *testing.T) {
	for _, tc := range []struct{ before, after string }{
		{"bandit.sarif", "bandit-fixed.sarif"},
		{"gitleaks.sarif", "gitleaks-fixed.sarif"},
		{"govulncheck.sarif", "govulncheck-small.sarif"},
	} {
		t.Run(tc.after, func(t *testing.T) {
			before := fingerprintSet(parseSample(t, tc.before))
			after := parseSample(t, tc.after)
			for _, f := range after.Findings {
				old, ok := before[f.Fingerprint]
				if !ok {
					t.Errorf("finding %s at %s:%d has a new fingerprint", f.RuleID, f.File, f.StartLine)
					continue
				}
				if old.RuleID != f.RuleID || old.File != f.File {
					t.Errorf("fingerprint %s was %s, now %s", f.Fingerprint, old.RuleID, f.RuleID)
				}
			}
			if gone := len(before) - len(after.Findings); gone <= 0 {
				t.Errorf("no findings were fixed between %s and %s", tc.before, tc.after)
			}
		})
	}
	small := parseSample(t, "govulncheck-small.sarif")
	if len(small.Findings) != 2 || small.Findings[0].RuleID != "GO-2026-5970" {
		t.Errorf("govulncheck-small findings = %+v", small.Findings)
	}
}

// dropResults returns the sample with the results at the given indexes of
// the first run removed, everything else as it was.
func dropResults(t testing.TB, data []byte, drop ...int) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	run := doc["runs"].([]any)[0].(map[string]any)
	var keep []any
	for i, r := range run["results"].([]any) {
		if !slices.Contains(drop, i) {
			keep = append(keep, r)
		}
	}
	run["results"] = keep
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFingerprintsSurviveOtherFixes(t *testing.T) {
	for _, name := range []string{"trivy-image.sarif", "trivy-config.sarif", "semgrep.sarif", "hadolint.sarif", "codeql.sarif", "bandit.sarif"} {
		t.Run(name, func(t *testing.T) {
			data := readSample(t, name)
			all := parseSample(t, name)
			again := parseSample(t, name)
			if !slices.EqualFunc(all.Findings, again.Findings, func(a, b Finding) bool { return a == b }) {
				t.Fatal("two parses of the same file differ")
			}
			fewer := parseBytes(t, dropResults(t, data, 0, 2), Limits{})
			if fewer.Counts.Total >= all.Counts.Total {
				t.Fatalf("dropping results left %d of %d", fewer.Counts.Total, all.Counts.Total)
			}
			before := fingerprintSet(all)
			for _, f := range fewer.Findings {
				if old, ok := before[f.Fingerprint]; !ok || old != f {
					t.Errorf("finding %s at %s:%d changed when other results were removed", f.RuleID, f.File, f.StartLine)
				}
			}
		})
	}
}

func TestSemgrep(t *testing.T) {
	data := readSample(t, "semgrep.sarif")
	r := parseSample(t, "semgrep.sarif")
	wantCounts(t, r, Counts{Total: 8, High: 2, Medium: 5, Low: 1})
	if !slices.Equal(r.Tools, []string{"Semgrep OSS"}) || len(r.Rules) != 8 {
		t.Errorf("tools %v, %d rules (only the 8 used ones are kept)", r.Tools, len(r.Rules))
	}
	f := findOne(t, r, "python.lang.security.audit.subprocess-shell-true.subprocess-shell-true", "app.py", 9)
	if f.Severity != High || f.Level != "error" || f.StartColumn != 39 {
		t.Errorf("finding = %+v", f)
	}
	if f := findOne(t, r, "yaml.kubernetes.security.run-as-non-root.run-as-non-root", "deploy/deployment.yaml", 11); f.Severity != Low || f.Level != "note" {
		t.Errorf("finding = %+v", f)
	}
	ru := findRule(t, r, "dockerfile.security.last-user-is-root.last-user-is-root")
	if ru.HelpURI != "https://semgrep.dev/r/dockerfile.security.last-user-is-root.last-user-is-root" ||
		!strings.HasPrefix(ru.Help, "The last user in the container is 'root'.") || strings.Contains(ru.Help, "<a href") || strings.Contains(ru.Help, "####") {
		t.Errorf("rule = %+v", ru)
	}

	// The "requires login" placeholder is not used: replacing it with
	// another value every result shares changes nothing, while real,
	// distinct values do change the fingerprints.
	same := strings.ReplaceAll(string(data), `"requires login"`, `"0000"`)
	r2 := parseBytes(t, []byte(same), Limits{})
	for i := range r.Findings {
		if r2.Findings[i].Fingerprint != r.Findings[i].Fingerprint {
			t.Errorf("finding %d: a shared placeholder changed the fingerprint", i)
		}
	}
	n := 0
	distinct := regexp.MustCompile(`"requires login"`).ReplaceAllStringFunc(string(data), func(string) string {
		n++
		return `"real-` + strings.Repeat("x", n) + `"`
	})
	r3 := parseBytes(t, []byte(distinct), Limits{})
	for i := range r.Findings {
		if r3.Findings[i].Fingerprint == r.Findings[i].Fingerprint {
			t.Errorf("finding %d: real partial fingerprints were not used", i)
		}
	}
}

func TestTrivyImage(t *testing.T) {
	r := parseSample(t, "trivy-image.sarif")
	wantCounts(t, r, Counts{Total: 49, Critical: 3, High: 6, Medium: 26, Low: 14})
	var same []Finding
	for _, f := range r.Findings {
		if f.RuleID == "CVE-2022-48174" {
			same = append(same, f)
		}
		if f.File != "library/alpine" || f.StartLine != 1 {
			t.Errorf("location %s:%d", f.File, f.StartLine)
		}
	}
	if len(same) != 3 {
		t.Fatalf("%d findings of CVE-2022-48174", len(same))
	}
	for i, pkg := range []string{"busybox", "busybox-binsh", "ssl_client"} {
		f := same[i]
		if f.Severity != Critical || !strings.HasPrefix(f.Message, "Package: "+pkg+"\nInstalled Version: 1.36.0-r9\n") {
			t.Errorf("finding %d = %+v", i, f)
		}
	}
	if same[0].Fingerprint == same[1].Fingerprint || same[1].Fingerprint == same[2].Fingerprint {
		t.Error("the same CVE in different packages got one fingerprint")
	}
	ru := findRule(t, r, "CVE-2022-48174")
	if ru.HelpURI != "https://avd.aquasec.com/nvd/cve-2022-48174" || ru.Name != "OsPackageVulnerability" || !slices.Contains(ru.Tags, "CRITICAL") {
		t.Errorf("rule = %+v", ru)
	}
	for _, f := range r.Findings {
		if strings.Contains(f.Message, Masked) {
			t.Errorf("a trivy message was masked: %q", f.Message)
		}
	}
}

func TestTrivyConfig(t *testing.T) {
	r := parseSample(t, "trivy-config.sarif")
	wantCounts(t, r, Counts{Total: 24, Critical: 1, High: 6, Medium: 5, Low: 12})
	f := findOne(t, r, "DS-0010", "Dockerfile", 4)
	if f.Severity != Critical || f.Level != "error" {
		t.Errorf("DS-0010 = %+v", f)
	}
	f = findOne(t, r, "KSV-0118", "deploy/deployment.yaml", 11)
	if f.Severity != High || f.EndLine != 17 {
		t.Errorf("KSV-0118 = %+v", f)
	}
	if ru := findRule(t, r, "DS-0001"); ru.HelpURI != "https://avd.aquasec.com/misconfig/ds-0001" || ru.Short != "':latest' tag used" || strings.Contains(ru.Help, "| --- |") {
		t.Errorf("rule = %+v", ru)
	}
}

func TestHadolint(t *testing.T) {
	// The results come before the tool in this file, and there are no
	// rules at all.
	r := parseSample(t, "hadolint.sarif")
	wantCounts(t, r, Counts{Total: 7, High: 1, Medium: 3, Low: 3})
	if len(r.Rules) != 0 || !slices.Equal(r.Tools, []string{"Hadolint"}) {
		t.Errorf("rules %+v, tools %v", r.Rules, r.Tools)
	}
	if f := findOne(t, r, "DL3004", "Dockerfile", 4); f.Severity != High {
		t.Errorf("DL3004 = %+v", f)
	}
}

func TestGovulncheck(t *testing.T) {
	r := parseSample(t, "govulncheck.sarif")
	wantCounts(t, r, Counts{Total: 33, High: 1, Medium: 14, Low: 18})
	f := r.Findings[0]
	if f.RuleID != "GO-2021-0113" || f.File != "go.mod" || f.StartLine != 1 || f.Severity != High {
		t.Errorf("first finding = %+v", f)
	}
	if ru := findRule(t, r, "GO-2021-0113"); ru.HelpURI != "https://pkg.go.dev/vuln/GO-2021-0113" || !strings.HasPrefix(ru.Help, "Due to improper index calculation") || ru.Full != "Out-of-bounds read in golang.org/x/text/language" {
		t.Errorf("rule = %+v", ru)
	}
}

func TestGitleaks(t *testing.T) {
	r := parseSample(t, "gitleaks.sarif")
	wantCounts(t, r, Counts{Total: 1, High: 1})
	f := r.Findings[0]
	if !f.Secret || f.RuleID != "github-pat" || f.File != "app.py" || f.StartLine != 5 || f.Level != "warning" {
		t.Errorf("finding = %+v", f)
	}
	if f.Message != "Uncovered a GitHub Personal Access Token, potentially leading to unauthorized repository access and sensitive content exposure"+secretText {
		t.Errorf("message = %q", f.Message)
	}
	if len(r.Rules) != 1 || r.Rules[0].ID != "github-pat" {
		t.Errorf("rules = %+v (only the one used is kept)", r.Rules)
	}
	noSecrets(t, r, "REDACTED", "github-pat has detected")
}

func TestCodeQL(t *testing.T) {
	data := readSample(t, "codeql.sarif")
	r := parseSample(t, "codeql.sarif")
	wantCounts(t, r, Counts{Total: 5, Critical: 1, High: 1, Medium: 2, Low: 1, Suppressed: 1})
	if !slices.Equal(r.Tools, []string{"CodeQL"}) {
		t.Errorf("tools %v", r.Tools)
	}

	sql := findOne(t, r, "js/sql-injection", "src/db/orders.js", 42)
	if sql.Severity != High || sql.Level != "error" || sql.StartColumn != 18 || sql.Message != "This query string depends on a [user-provided value](1)." {
		t.Errorf("sql = %+v", sql)
	}
	// Suppressed with status accepted: counted, not kept. Under review: kept.
	if xss := findOne(t, r, "js/reflected-xss", "src/routes/profile.js", 19); xss.Severity != Medium {
		t.Errorf("xss = %+v", xss)
	}
	for _, f := range r.Findings {
		if f.File == "src/routes/search.js" || f.File == "src/util/legacy.js" || f.Message == "No unused variables." {
			t.Errorf("a suppressed, absent or passing result was kept: %+v", f)
		}
	}
	creds := findOne(t, r, "js/hardcoded-credentials", "src/config/admin.js", 3)
	if !creds.Secret || creds.Severity != Critical || creds.Message != "Hard-coded credentials"+secretText {
		t.Errorf("creds = %+v", creds)
	}
	// ruleIndex 0 without a tool component is the driver's first rule.
	if f := findOne(t, r, "js/unused-local-variable", "src/util/format.js", 8); f.Severity != Low || f.Level != "note" {
		t.Errorf("unused = %+v", f)
	}
	nr := findOne(t, r, "(no rule)", "shop.checkout.Total", 0)
	if nr.Severity != Medium || nr.Level != "warning" {
		t.Errorf("no rule = %+v", nr)
	}

	ru := findRule(t, r, "js/sql-injection")
	if ru.Help != "# Database query built from user-controlled sources\nUse parameterised queries." || ru.HelpURI == "" || ru.Full == "" {
		t.Errorf("rule = %+v", ru)
	}
	for _, id := range []string{"js/reflected-xss", "js/hardcoded-credentials"} {
		if ru := findRule(t, r, id); ru.HelpURI != "" {
			t.Errorf("%s kept help URI %q", id, ru.HelpURI)
		}
	}
	noSecrets(t, r, "Sh0pAdm1n", "5f2c8d1a9e7b3c40", "<script>", "SELECT * FROM", "escaped by the template")

	// Moving the code changes the line but not the partial fingerprint;
	// a finding without one gets a new fingerprint.
	moved := strings.Replace(string(data), `"startLine": 42,`, `"startLine": 50,`, 1)
	moved = strings.Replace(moved, `"fullyQualifiedName": "shop.checkout.Total", "kind": "function" } ] }`, `"fullyQualifiedName": "shop.checkout.Total", "kind": "function" } ], "physicalLocation": { "region": { "startLine": 7 } } }`, 1)
	m := parseBytes(t, []byte(moved), Limits{})
	if got := findOne(t, m, "js/sql-injection", "src/db/orders.js", 50); got.Fingerprint != sql.Fingerprint {
		t.Error("moving the code changed a partial fingerprint")
	}
	if got := findOne(t, m, "(no rule)", "shop.checkout.Total", 7); got.Fingerprint == nr.Fingerprint {
		t.Error("a line-based fingerprint did not change with the line")
	}
}

func TestSecretSamples(t *testing.T) {
	r := parseSample(t, "trufflehog.sarif")
	if !slices.Equal(r.Tools, []string{"TruffleHog", "gitleaks"}) {
		t.Errorf("tools = %v", r.Tools)
	}
	wantCounts(t, r, Counts{Total: 4, High: 4})
	want := []struct{ tool, rule, file, msg string }{
		{"TruffleHog", "AWS", "deploy/.env", "AWS access key"},
		{"TruffleHog", "Github", "scripts/release.sh", "GitHub personal access token"},
		{"TruffleHog", "PrivateKey", "keys/deploy_rsa", "PrivateKey"},
		{"gitleaks", "slack-access-token", "/home/ci/repo/config/slack.yaml", "Slack token"},
	}
	for i, w := range want {
		f := r.Findings[i]
		if f.Tool != w.tool || f.RuleID != w.rule || f.File != w.file || f.Message != w.msg+secretText || !f.Secret || f.Severity != High {
			t.Errorf("finding %d = %+v", i, f)
		}
	}
	noSecrets(t, r,
		"AKIA"+"2E0A8F3B244C9986", "wJalrXUtnFEMI", "ghp"+"_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5", "aB3dE5fG7",
		"MIIEowIBAAKCAQEA", "BEGIN RSA", "xoxb-", "AbCdEfGhIjKlMnOpQrStUvWx",
		"9fceb02d0ae598e95dc970b74767f19372d61af8", "dev@example.com", "Found verified")

	b := parseSample(t, "bandit-b105.sarif")
	wantCounts(t, b, Counts{Total: 2, High: 1, Medium: 1})
	f := findOne(t, b, "B105", "settings.py", 4)
	if !f.Secret || f.Severity != High || f.Message != "hardcoded_password_string"+secretText {
		t.Errorf("B105 = %+v", f)
	}
	if f := findOne(t, b, "B608", "settings.py", 9); f.Secret || f.Severity != Medium {
		t.Errorf("B608 = %+v", f)
	}
	noSecrets(t, b, "Tr0ub4dor", "xyzzy", "DB_PASSWORD", "SELECT * FROM users")

	// The same file from a tool that is not a secret scanner, with rules
	// that do not mention secrets: the messages are still masked.
	plain := strings.NewReplacer(`"TruffleHog"`, `"LintAll"`, `"gitleaks"`, `"LintMore"`,
		`"AWS access key"`, `"Thing one"`, `"GitHub personal access token"`, `"Thing two"`,
		`"PrivateKey"`, `"Thing3"`, `"Slack token"`, `"Thing four"`, `"slack-access-token"`, `"thing-four"`).Replace(string(readSample(t, "trufflehog.sarif")))
	p := parseBytes(t, []byte(plain), Limits{})
	for _, f := range p.Findings {
		if f.Secret {
			t.Errorf("finding %s is judged a secret", f.RuleID)
		}
		if !strings.Contains(f.Message, Masked) {
			t.Errorf("message not masked: %q", f.Message)
		}
	}
	noSecrets(t, p, "AKIA"+"2E0A8F3B244C9986", "wJalrXUtnFEMI", "ghp"+"_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5", "MIIEowIBAAKCAQEA", "xoxb-123", "AbCdEfGhIjKlMnOpQrStUvWx")
	if f := p.Findings[0]; f.Message != "Found verified AWS result: [masked] / [masked] in deploy/.env" {
		t.Errorf("masked message = %q", f.Message)
	}
}
