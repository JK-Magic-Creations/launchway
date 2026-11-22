package findings

import (
	"math"
	"strings"
	"testing"
)

func TestMask(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"nothing to see here", "nothing to see here"},
		// AWS access key ids, but not a word that only starts like one.
		{"key AKIA" + "2E0A8F3B244C9986 found", "key [masked] found"},
		{"ASIA" + "ZXCVBNMASDFGHJKL in env", "[masked] in env"},
		{"AKIAshort", "AKIAshort"},
		// GitHub, GitLab, Slack, Stripe, Google.
		{"token ghp" + "_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5", "token [masked]"},
		{"gh" + "o_16C7e42F292c6912E7710c838347Ae178B4a and gh" + "s_16C7e42F292c6912E7710c8", "[masked] and [masked]"},
		{"github" + "_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyz", "[masked]"},
		{"use gl" + "pat-xxxxXXXXyyyyYYYY1234 now", "use [masked] now"},
		{"slack xo" + "xb-123456789012-1234567890123-AbCdEf", "slack [masked]"},
		{"stripe sk_li" + "ve_4eC39HqLyjWDarjtT1zdp7dc", "stripe [masked]"},
		{"rk_li" + "ve_abcdefghij0123", "[masked]"},
		{"google AIza" + "SyA1234567890abcdefghijklmnopqrstu", "google [masked]"},
		// JSON Web Tokens.
		{"bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U ok", "bearer [masked] ok"},
		// PEM private keys, closed or running to the end of the text.
		{"key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEow\nabc\n-----END RSA PRIVATE KEY-----\ndone", "key:\n[masked]\ndone"},
		{"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA", "[masked]"},
		{"-----BEGIN PUBLIC KEY----- is fine", "-----BEGIN PUBLIC KEY----- is fine"},
		// Assignments: the name stays, the value goes.
		{"password=hunter2", "password=[masked]"},
		{"Possible hardcoded password: 'Tr0ub4dor&3'", "Possible hardcoded password: [masked]"},
		{`{"client_secret": "abc def"}`, `{"client_secret": [masked]}`},
		{"DB_PASSWD = s3cr3t; next", "DB_PASSWD = [masked]; next"},
		{"token: abc123, user: bob", "token: [masked], user: bob"},
		{"api-key=xyz", "api-key=[masked]"},
		{"a secret was found here", "a secret was found here"},
		{"Passwords must be long", "Passwords must be long"},
		// URLs with credentials in them.
		{"connect to postgres://admin:Sup3rS3cret@db:5432/app", "connect to postgres://admin:[masked]@db:5432/app"},
		{"see https://example.com/a:b@c", "see https://example.com/a:b@c"},
		// Long tokens with letters and digits.
		{"value 0123456789abcdefABCDEF0123456789xyz", "value [masked]"},
		{"Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZg== end", "[masked] end"},
		// Left alone: commit ids and digests, vulnerability ids, paths,
		// long words without digits.
		{"commit 9fceb02d0ae598e95dc970b74767f19372d61af8", "commit 9fceb02d0ae598e95dc970b74767f19372d61af8"},
		{"sha256:5e2b554c1c45d22c9d1aa836828828e320a26011b76c08631ac896cbc3625e3e", "sha256:5e2b554c1c45d22c9d1aa836828828e320a26011b76c08631ac896cbc3625e3e"},
		{"CVE-2024-12345678901234567890123456", "CVE-2024-12345678901234567890123456"},
		{"GHSA-ppp9-7jff-5vj2-aaaa-bbbb-cccc-dddd", "GHSA-ppp9-7jff-5vj2-aaaa-bbbb-cccc-dddd"},
		{"src/main/java/com/example/service/v2/OrderService1.java", "src/main/java/com/example/service/v2/OrderService1.java"},
		{"golang.org/x/net@v0.0.0-20210226172049-e18ecbb05110", "golang.org/x/net@v0.0.0-20210226172049-e18ecbb05110"},
		{"averyveryverylongwordwithoutanydigitsatall", "averyveryverylongwordwithoutanydigitsatall"},
		{"--------------------------------------", "--------------------------------------"},
		{"Package: busybox\nInstalled Version: 1.36.0-r9", "Package: busybox\nInstalled Version: 1.36.0-r9"},
	} {
		if got := Mask(tc.in); got != tc.want {
			t.Errorf("Mask(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

func TestMaskIsLinear(t *testing.T) {
	// Text that matches the hints of every pattern but no pattern, many
	// times over: Go's regular expressions stay linear.
	in := strings.Repeat("password AKIA ghp_ eyJ.eyJ -----BEGIN x PRIVATE KEY----- xox _live_ AIza glpat- ", 5000)
	if got := Mask(in); !strings.HasPrefix(got, "password AKIA") {
		t.Errorf("got %.80q", got)
	}
}

func TestSecretClassification(t *testing.T) {
	for _, tool := range []string{"gitleaks", "Gitleaks", "TruffleHog", "trufflehog-enterprise", "detect-secrets", "GitGuardian ggshield", "Talisman", "secretlint", "kingfisher", "NoseyParker", "whispers"} {
		if !isSecretTool(tool) {
			t.Errorf("%q is not taken as a secret scanner", tool)
		}
	}
	for _, tool := range []string{"semgrep", "CodeQL", "Trivy", "Bandit", ""} {
		if isSecretTool(tool) {
			t.Errorf("%q is taken as a secret scanner", tool)
		}
	}
	for _, s := range []string{"hardcoded_password_string", "aws-access-key", "AWS Access Key", "generic-api-key", "API key in code", "slack-token", "PrivateKey", "private key", "rsa-private-key", "Hard-coded credentials", "external/cwe/cwe-798", "CWE-259: Use of Hard-coded Password", "cwe-321", "SECRET_KEY", "passwd in file", "apikey"} {
		if !mentionsSecret(s) {
			t.Errorf("%q is not taken as a secret rule", s)
		}
	}
	for _, s := range []string{"sql-injection", "external/cwe/cwe-7980", "cwe-79", "B602", "openssl: Incorrect cipher key and IV length processing", "keyboard"} {
		if mentionsSecret(s) {
			t.Errorf("%q is taken as a secret rule", s)
		}
	}
}

func TestSeverityFromScore(t *testing.T) {
	for _, tc := range []struct {
		score float64
		want  Severity
	}{
		{10, Critical}, {9.8, Critical}, {9.0, Critical}, {8.99, High}, {7.0, High}, {6.9, Medium},
		{4.0, Medium}, {3.9, Low}, {0.1, Low}, {0.09, Unknown}, {0, Unknown}, {-1, Unknown},
		{math.NaN(), Unknown}, {math.Inf(1), Critical}, {1e300, Critical},
	} {
		if got := SeverityFromScore(tc.score); got != tc.want {
			t.Errorf("SeverityFromScore(%v) = %s, want %s", tc.score, got, tc.want)
		}
	}
}

func TestSeverityOrder(t *testing.T) {
	result := func(level, props string) string {
		r := `{"ruleId":"r","message":{"text":"m"}`
		if level != "" {
			r += `,"level":` + level
		}
		if props != "" {
			r += `,"properties":` + props
		}
		return r + "}"
	}
	rules := func(rule string) string {
		return `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"x","rules":[` + rule + `]}},"results":[`
	}
	for _, tc := range []struct {
		name, rule, result string
		want               Severity
		level              string
	}{
		{"result score wins over everything", `{"id":"r","properties":{"security-severity":"2.0","tags":["CRITICAL"]}}`, result(`"error"`, `{"security-severity":9.5,"severity":"low"}`), Critical, "error"},
		{"rule score as a string", `{"id":"r","properties":{"security-severity":"7.5"}}`, result(`"note"`, ""), High, "note"},
		{"a zero score falls through", `{"id":"r","properties":{"security-severity":"0"}}`, result(`"error"`, `{"security-severity":"none"}`), High, "error"},
		{"issue_severity", `{"id":"r"}`, result(`"error"`, `{"issue_severity":"LOW"}`), Low, "error"},
		{"severity word moderate", `{"id":"r"}`, result("", `{"severity":"Moderate"}`), Medium, "warning"},
		{"severity word info", `{"id":"r"}`, result("", `{"severity":"info"}`), Low, "warning"},
		{"unknown word falls through", `{"id":"r"}`, result(`"note"`, `{"severity":"spicy"}`), Low, "note"},
		{"rule tag", `{"id":"r","properties":{"tags":["security","high"]}}`, result(`"note"`, ""), High, "note"},
		{"a tag must be exactly the word", `{"id":"r","properties":{"tags":["HIGH CONFIDENCE"]}}`, result(`"note"`, ""), Low, "note"},
		{"rule default level", `{"id":"r","defaultConfiguration":{"level":"error"}}`, result("", ""), High, "error"},
		{"result level wins over the rule's", `{"id":"r","defaultConfiguration":{"level":"error"}}`, result(`"note"`, ""), Low, "note"},
		{"level none", `{"id":"r"}`, result(`"none"`, ""), Unknown, "none"},
		{"a bad level is ignored", `{"id":"r","defaultConfiguration":{"level":"fatal"}}`, result(`"loud"`, ""), Medium, "warning"},
		{"a secret rule is at least high", `{"id":"aws-access-key"}`, result(`"note"`, ""), High, "note"},
		{"a secret rule keeps critical", `{"id":"r","name":"leaked password"}`, result(`"note"`, `{"security-severity":"9.1"}`), Critical, "note"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := rules(tc.rule) + strings.Replace(tc.result, `"ruleId":"r"`, `"ruleId":`+idOf(tc.rule), 1) + `]}]}`
			r := parseBytes(t, []byte(in), Limits{})
			if len(r.Findings) != 1 {
				t.Fatalf("%d findings (warning %q)", len(r.Findings), r.Warning)
			}
			if f := r.Findings[0]; f.Severity != tc.want || f.Level != tc.level {
				t.Errorf("severity %s level %s, want %s %s", f.Severity, f.Level, tc.want, tc.level)
			}
		})
	}
}

// idOf picks the quoted id out of a rule written in a test.
func idOf(rule string) string {
	i := strings.Index(rule, `"id":`) + len(`"id":`)
	j := strings.IndexAny(rule[i+1:], `"`)
	return rule[i : i+j+2]
}

func TestWebURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.com/x?y=1#z":                   "https://example.com/x?y=1#z",
		"HTTP://Example.com":                            "HTTP://Example.com",
		"  https://example.com/  ":                      "https://example.com/",
		"javascript:alert(1)":                           "",
		"JaVaScRiPt:alert(1)":                           "",
		"data:text/html,<b>x</b>":                       "",
		"file:///etc/passwd":                            "",
		"//example.com/x":                               "",
		"/relative/path":                                "",
		"https://":                                      "",
		"https://exa mple.com":                          "",
		"https://example.com/\x00":                      "",
		"https://user:pw@example.com":                   "",
		"https://example.com/é":                         "",
		"ftp://example.com":                             "",
		"https://" + strings.Repeat("a", 3000) + ".com": "",
	} {
		if got := webURL(in); got != want {
			t.Errorf("webURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFilePath(t *testing.T) {
	bases := map[string]string{"SRCROOT": "file:///work/repo/", "OTHER": "https://example.com/"}
	for _, tc := range []struct{ uri, base, want string }{
		{"src/a.go", "", "src/a.go"},
		{"src/a.go", "%SRCROOT%", "src/a.go"},
		{"./src/./b/../a.go", "", "src/a.go"},
		{"file:///work/repo/src/a.go", "", "src/a.go"},
		{"file:///elsewhere/a.go", "", "/elsewhere/a.go"},
		{"file://localhost/etc/hosts", "", "/etc/hosts"},
		{"src/my%20file.go", "", "src/my file.go"},
		{"../../etc/passwd", "", "../../etc/passwd"},
		{"%2e%2e/%2e%2e/etc/passwd", "", "../../etc/passwd"},
		{"a/../../../etc/shadow", "", "../../etc/shadow"},
		{"/abs/./x//y", "", "/abs/x/y"},
		{"bad%zzescape", "", "bad%zzescape"},
		{"line%0Abreak%00nul%1B[31m", "", "line breaknul"},
		{"https://example.com/a/b.js", "", "https://example.com/a/b.js"},
		{"C:\\src\\a.cs", "", "C:\\src\\a.cs"},
		{"", "", ""},
	} {
		if got := filePath(tc.uri, tc.base, bases, 500); got != tc.want {
			t.Errorf("filePath(%q, %q) = %q, want %q", tc.uri, tc.base, got, tc.want)
		}
	}
	if got := filePath(strings.Repeat("dir/", 1000)+"x.go", "", nil, 50); len(got) > 50 || !strings.HasSuffix(got, "…") {
		t.Errorf("long path = %q", got)
	}
}
