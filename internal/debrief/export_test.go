package debrief

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sample is a Debrief with every part filled, for the export tests.
func sample() *Report {
	main := RunRef{ID: "r0", Number: 41, Ref: "refs/heads/main", Branch: "main", SHA: "aaaa1111bbbb2222", CreatedAt: 1_760_000_000_000}
	r := &Report{
		Version: Version, Generated: 1_763_700_000_000,
		Install: Brand{Name: "Launchway"},
		Project: "storefront", Result: "failed",
		Run:     RunRef{ID: "r1", Number: 42, Ref: "refs/heads/fix-cart", Branch: "fix-cart", SHA: "c0ffee1234567890", CreatedAt: 1_763_600_000_000, Actor: "ana", Link: "https://ci.example.test/runs/r1"},
		Summary: Summary{Total: 240, Passed: 228, Failed: 3, Errored: 1, Skipped: 8, Flaky: 2, Quarantined: 1, DurationMs: 184_000, PassRate: 98.3},
		Change: &Change{Base: main, NewlyFailing: 2, NewlyPassing: 1, TotalDelta: 6, DurationDelta: 4200,
			Slower: []Slower{{TestName{"k1", "cart", "TestCheckoutTotals"}, 900, 3100}}},
		Failures: []Failure{
			{TestName: TestName{"k1", "cart", "TestCheckoutTotals"}, File: "cart/checkout_test.go", Line: 88, Status: Failed, DurationMs: 3100,
				Message: "checkout_test.go:88: got 1999, want 2099", Details: "goroutine 7 [running]:\ncart.TestCheckoutTotals(0xc000)\n\tcart/checkout_test.go:88 +0x1f4\ntesting.tRunner(0xc0001, 0x7a)\n\ttesting/testing.go:1792 +0xf4",
				Diff: NewDiff("2099", "1999"), NewHere: true, FirstFailedIn: &RunRef{Number: 42, SHA: "c0ffee1234567890"}, FailingOnBase: "no",
				Output: "loading fixtures\ncart total computed\n"},
			{TestName: TestName{"k2", "ui/login", "renders the form"}, Status: Failed, DurationMs: 800, Variant: "firefox",
				Message: "expected:<Sign in\nEmail\nPassword> but was:<Sign in\nUsername\nPassword>",
				Diff:    NewDiff("Sign in\nEmail\nPassword", "Sign in\nUsername\nPassword"), FailingOnBase: "yes", FirstFailedIn: &RunRef{Number: 39, SHA: "0123456789abcdef"}},
			{TestName: TestName{"k3", "api", "TestTimeout é 日本語"}, Status: Errored, DurationMs: 30000, Message: "context deadline exceeded after 30s", FailingOnBase: "unknown"},
		},
		Flaky:       []FlakyCase{{TestName: TestName{"k4", "ui/search", "debounces input"}, Score: 25, Reason: "passed and failed on the same commit", ThisRun: "passed on retry", FirstSeen: 1_763_000_000_000, Commits: 12, SameCommit: 3}},
		Quarantined: []Quarantined{{TestName: TestName{"k5", "db", "TestSlowMigration"}, Owner: "bo", Expires: "2026-12-15", Reason: "network in CI", Status: Failed}},
		Slowest:     []SlowCase{{TestName{"k3", "api", "TestTimeout"}, 30000, 400}, {TestName{"k1", "cart", "TestCheckoutTotals"}, 3100, 900}},
		Suites: []Suite{
			{Name: "cart", Total: 60, Passed: 59, Failed: 1, DurationMs: 9000},
			{Name: "cart/pricing", Depth: 1, Total: 20, Passed: 20, DurationMs: 2000},
			{Name: "ui/login", Total: 12, Passed: 11, Failed: 1, DurationMs: 40000},
		},
		Matrix: &Matrix{Columns: []string{"chromium", "firefox"}, Rows: []MatrixRow{{TestName{"k2", "ui/login", "renders the form"}, []string{Passed, Failed}}}},
		Coverage: &Coverage{Lines: Ratio{8200, 10000, 82}, Branches: Ratio{300, 500, 60}, Functions: Ratio{90, 100, 90},
			Change: &ChangeCoverage{Covered: 12, Total: 20, Percent: 60, Advisory: "60% of the changed lines are covered; the advised floor is 80%.",
				Files: []FileCoverage{{Path: "cart/checkout.go", Covered: 12, Total: 20, Percent: 60, Missed: "40-47"}}},
			Least: []FileCoverage{{Path: "cart/legacy.go", Covered: 5, Total: 50, Percent: 10, Missed: "1-20, 30-60"}}, Files: 120},
		Notes: []string{"The report shows the first 3 of 4 failures."},
	}
	for i := 0; i < 14; i++ {
		pr := 100.0
		if i%4 == 3 {
			pr = 97 + float64(i)/10
		}
		r.Trend = append(r.Trend, TrendPoint{Number: 29 + i, SHA: fmt.Sprintf("%08x", i*7919+1), Total: 220 + i, PassRate: pr, DurationMs: int64(170000 + i*900), Flaky: i % 3})
	}
	return r
}

func TestHTMLIsSelfContainedAndAccessible(t *testing.T) {
	out := string(HTML(sample()))
	for _, bad := range []string{"<script", "http://", "src=\"http", "href=\"//", "@import", "url(http"} {
		text := out
		if bad == "http://" {
			text = strings.ReplaceAll(out, "http://www.w3.org", "")
		}
		if strings.Contains(text, bad) {
			t.Errorf("the HTML mentions %q: it must need nothing outside the file", bad)
		}
	}
	for _, want := range []string{"<html lang=\"en\"", "prefers-color-scheme:dark", "Skip to the report", "TestCheckoutTotals", "got 1999, want 2099",
		"First failed in", "Also failing on the default branch", "newly failing", "Quarantined tests", "passed on retry", "Show the trend as a table", "<caption>", "aria-labelledby"} {
		if !strings.Contains(out, want) {
			t.Errorf("the HTML lacks %q", want)
		}
	}
	// Markup in a test's own text never becomes markup.
	r := sample()
	r.Failures[0].Message = "<img src=x onerror=alert(1)>"
	if o := string(HTML(r)); strings.Contains(o, "<img src=x") {
		t.Error("a failure message was written as markup")
	}
	// An empty report still reads.
	e := string(HTML(&Report{Project: "p", Install: Brand{}}))
	if !strings.Contains(e, "No test results were collected") {
		t.Error("an empty Debrief should say there are no results")
	}
}

func TestPDFHasPagesFontsAndText(t *testing.T) {
	b, err := PDF(sample())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "d.pdf")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := os.Getenv("DEBRIEF_OUT"); out != "" {
		_ = os.MkdirAll(out, 0o755)
		_ = os.WriteFile(filepath.Join(out, "debrief.pdf"), b, 0o644)
		_ = os.WriteFile(filepath.Join(out, "debrief.html"), HTML(sample()), 0o644)
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("poppler's pdftotext is not installed; the page, font and text checks are skipped")
	}
	txt, err := exec.Command("pdftotext", "-layout", path, "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	s := string(txt)
	for _, want := range []string{"Debrief", "Contents", "Executive summary", "TestCheckoutTotals", "got 1999, want 2099", "Page 2 of", "Least covered files", "storefront"} {
		if !strings.Contains(s, want) {
			t.Errorf("text extracted from the PDF lacks %q", want)
		}
	}
	info, _ := exec.Command("pdfinfo", path).Output()
	if !strings.Contains(string(info), "Pages:") || !strings.Contains(string(info), "Title:") {
		t.Errorf("pdfinfo: %s", info)
	}
	fonts, _ := exec.Command("pdffonts", path).Output()
	for _, line := range strings.Split(strings.TrimSpace(string(fonts)), "\n")[2:] {
		if !strings.Contains(line, "yes") { // the emb column
			t.Errorf("a font is not embedded: %s", line)
		}
	}
}

func TestPDFWithHugeAndHostileText(t *testing.T) {
	r := sample()
	long := strings.Repeat("A", 5000) + " " + strings.Repeat("é日\u0000x\n", 300)
	for i := 0; i < 80; i++ {
		r.Failures = append(r.Failures, Failure{TestName: TestName{fmt.Sprint(i), "s", strings.Repeat("N", 300)}, Status: Failed, Message: long, Details: long, Output: long})
	}
	b, err := PDF(r)
	if err != nil || len(b) == 0 {
		t.Fatal(err)
	}
	if len(b) > 6<<20 {
		t.Errorf("the PDF of a report with 80 long failures is %d bytes", len(b))
	}
}

func TestJUnitIsWellFormed(t *testing.T) {
	var buf bytes.Buffer
	cases := []Case{
		{Suite: "a", Name: "one", Status: Passed, DurationMs: 5},
		{Suite: "a", Name: "two \x01<&>", Status: Failed, Message: "boom\nmore", Details: "trace"},
		{Suite: "b", Name: "three", Status: Passed, Flaky: true, Message: "first try failed"},
		{Suite: "b", Name: "four", Status: Failed, Quarantined: true, Message: "excused"},
		{Suite: "", Name: "five", Status: Skipped},
	}
	if err := JUnit(&buf, sample(), cases); err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(&buf)
	var starts []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok {
			starts = append(starts, se.Name.Local)
		}
	}
	joined := strings.Join(starts, " ")
	for _, want := range []string{"testsuites", "testsuite", "failure", "flakyFailure", "skipped"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no <%s> in %s", want, joined)
		}
	}
	if n := strings.Count(joined, "testsuite "); n != 3 {
		t.Errorf("%d suites, want 3: %s", n, joined)
	}
}
