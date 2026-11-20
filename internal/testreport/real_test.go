package testreport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The files under testdata/real were written by the real tools (versions
// in each directory's name or notes); these tests pin what is read from
// them.

func parseReal(t *testing.T, name, format string) *Report {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "real", name))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(context.Background(), b, Limits{})
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	checkReport(t, r, DefaultLimits())
	if r.Format != format || r.Warning != "" || r.Truncated {
		t.Errorf("%s: format %q (want %q), warning %q, truncated %v", name, r.Format, format, r.Warning, r.Truncated)
	}
	return r
}

// findVariant finds a case by name and variant.
func findVariant(t *testing.T, r *Report, name, variant string) Case {
	t.Helper()
	for _, c := range r.Cases {
		if c.Name == name && c.Variant == variant {
			return c
		}
	}
	t.Fatalf("no case %q in %q", name, variant)
	return Case{}
}

func stepNamed(c Case, name string) *Step {
	for i := range c.Steps {
		if c.Steps[i].Name == name {
			return &c.Steps[i]
		}
	}
	return nil
}

func TestRealSamplesAreDetected(t *testing.T) {
	for name, format := range map[string]string{
		"cucumber-js/cucumber.json":                                          FormatCucumber,
		"cucumber-js/cucumber-junit.xml":                                     FormatJUnit,
		"cypress/mochawesome-merged.json":                                    FormatMochawesome,
		"cypress/mochawesome/mochawesome.json":                               FormatMochawesome,
		"cypress/junit/results-717c174b50e6bec6597e1e8d09583876.xml":         FormatJUnit,
		"dotnet/nunit/nunit3.xml":                                            FormatNUnit,
		"dotnet/nunit/nunit.trx":                                             FormatTRX,
		"dotnet/nunit/xunit-v2.xml":                                          FormatXUnit,
		"dotnet/nunit/junit.xml":                                             FormatJUnit,
		"dotnet/xunit/xunit.trx":                                             FormatTRX,
		"dotnet/xunit/xunit-v2.xml":                                          FormatXUnit,
		"dotnet/xunit/nunit3.xml":                                            FormatNUnit,
		"gradle/test-results/test/TEST-com.example.cart.CartTest.xml":        FormatJUnit,
		"jest/ctrf-report.json":                                              FormatCTRF,
		"jest/jest-junit.xml":                                                FormatJUnit,
		"karate/v1.5.2/karate-reports/tests.users.json":                      FormatCucumber,
		"karate/v1.5.2/karate-reports/tests.users.karate-json.txt":           FormatKarate,
		"karate/v1.5.2/karate-reports/tests.users.xml":                       FormatJUnit,
		"karate/v2.1.4/cucumber-json/users.json":                             FormatCucumber,
		"karate/v2.1.4/junit-xml/users.xml":                                  FormatJUnit,
		"karate/v2.1.4/karate-json/karate-events.jsonl":                      FormatKarate,
		"maven-surefire/surefire-reports/TEST-com.example.cart.CartTest.xml": FormatJUnit,
		"maven-surefire/failsafe-reports/TEST-com.example.cart.CartIT.xml":   FormatJUnit,
		"mocha/mocha-junit.xml":                                              FormatJUnit,
		"phpunit/phpunit-junit.xml":                                          FormatJUnit,
		"playwright/results.json":                                            FormatPlaywright,
		"playwright/junit.xml":                                               FormatJUnit,
		"playwright/ctrf-report.json":                                        FormatCTRF,
		"pytest/junit-xunit1.xml":                                            FormatJUnit,
		"pytest/junit-xunit2.xml":                                            FormatJUnit,
		"robot/output.xml":                                                   FormatRobot,
		"robot/xunit.xml":                                                    FormatJUnit,
		"rspec/rspec-junit.xml":                                              FormatJUnit,
		"tap/bats/bats.tap":                                                  FormatTAP,
		"tap/node-test/node-test.tap":                                        FormatTAP,
		"testng/testng-native/testng-results.xml":                            FormatTestNG,
		"testng/surefire-reports/TEST-com.example.cart.CartNgTest.xml":       FormatJUnit,
		"vitest/vitest-junit.xml":                                            FormatJUnit,
	} {
		t.Run(name, func(t *testing.T) { parseReal(t, name, format) })
	}
	// Files next to the reports that are not test results are refused
	// clearly.
	for name, want := range map[string]string{
		"maven-surefire/failsafe-reports/failsafe-summary.xml": "the XML's root element is <failsafe-summary>; known roots are",
		"karate/v1.5.2/karate-reports/karate-summary-json.txt": `the JSON object has keys "efficiency", "totalTime", "threads", …; known JSON reports are`,
		"vitest/vitest-results.json":                           `the JSON object has keys "numTotalTestSuites"`,
		"cucumber-js/cucumber-messages.ndjson":                 "no go test -json events were found",
	} {
		b, err := os.ReadFile(filepath.Join("testdata", "real", name))
		if err != nil {
			t.Fatal(err)
		}
		_, err = Parse(context.Background(), b, Limits{})
		if err == nil || !strings.HasPrefix(err.Error(), "not a test report: ") || !strings.Contains(err.Error(), want) || len(err.Error()) > 320 {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
}

func TestRealSurefire(t *testing.T) {
	r := parseReal(t, "maven-surefire/surefire-reports/TEST-com.example.cart.CartTest.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 8, Passed: 5, Failed: 1, Errored: 1, Skipped: 1, Flaky: 1})
	c := findCase(t, r, "flakyPassesOnRerun")
	if !c.Flaky || c.Status != Passed || c.Retries != 1 || len(c.Attempts) != 1 || c.Attempts[0].Status != Failed ||
		c.Attempts[0].Message != "fails first time ==> expected: <2> but was: <1>" {
		t.Errorf("flaky case = %+v", c)
	}
	c = findCase(t, r, "tenDiscountIsWrong")
	// One <failure> and two <rerunFailure>: three runs, the last final.
	if c.Status != Failed || c.Retries != 2 || len(c.Attempts) != 2 || c.Message != "TEN discount ==> expected: <25> but was: <20>" {
		t.Errorf("rerun case = %+v", c)
	}
	c = findCase(t, r, "errorNotAssertion")
	if c.Status != Errored || c.Retries != 2 || len(c.Attempts) != 2 || c.Attempts[0].Status != Errored {
		t.Errorf("rerun error case = %+v", c)
	}
	// The suite's properties are the JVM's: not given to every case.
	for _, c := range r.Cases {
		if len(c.Properties) != 0 {
			t.Errorf("%s has properties %v", c.Name, c.Properties)
		}
	}
	r = parseReal(t, "maven-surefire/failsafe-reports/TEST-com.example.cart.CartIT.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 2, Passed: 1, Failed: 1})
}

func TestRealJUnitDialects(t *testing.T) {
	r := parseReal(t, "pytest/junit-xunit2.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 11, Passed: 7, Failed: 1, Errored: 1, Skipped: 2})
	c := findCase(t, r, "test_div_failure")
	if c.Suite != "tests.test_calc" || c.Message != "AssertionError: integer division expected\nassert 2.5 == 3" {
		t.Errorf("pytest case = %+v", c)
	}
	r = parseReal(t, "pytest/junit-xunit1.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 11, Passed: 7, Failed: 1, Errored: 1, Skipped: 2})

	r = parseReal(t, "phpunit/phpunit-junit.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 8, Passed: 3, Failed: 2, Errored: 1, Skipped: 2})
	c = findCase(t, r, "testTenDiscountIsWrong")
	if c.Suite != "CartTest" || c.File != "/work/tests/CartTest.php" || c.Message != "TEN discount" || !strings.Contains(c.Details, "CartTest.php:18") {
		t.Errorf("phpunit case = %+v", c)
	}
	if c := findCase(t, r, `testHalf with data set "three"`); c.Status != Failed || c.Message != "Failed asserting that 1 is identical to 2." {
		t.Errorf("phpunit data set case = %+v", c)
	}

	r = parseReal(t, "rspec/rspec-junit.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 5, Passed: 1, Failed: 2, Skipped: 2})
	c = findCase(t, r, "Cart applies the TEN discount (wrong expectation)")
	if c.File != "./spec/cart_spec.rb" || !strings.HasPrefix(c.Message, "expected: 25") || len(c.Properties) != 1 || c.Properties[0].Name != "seed" {
		t.Errorf("rspec case = %+v", c)
	}

	r = parseReal(t, "gradle/test-results/test/TEST-com.example.cart.CartTest.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 8, Passed: 4, Failed: 3, Skipped: 1})
	r = parseReal(t, "jest/jest-junit.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 7, Passed: 5, Failed: 1, Skipped: 1})
	r = parseReal(t, "vitest/vitest-junit.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 6, Passed: 3, Failed: 1, Skipped: 2})
	r = parseReal(t, "mocha/mocha-junit.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 3, Passed: 2, Failed: 1})
	r = parseReal(t, "cypress/junit/results-717c174b50e6bec6597e1e8d09583876.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 4, Passed: 3, Failed: 1})
	r = parseReal(t, "dotnet/xunit/junit.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 7, Passed: 3, Failed: 3, Skipped: 1})

	// Playwright's JUnit reporter names attachments the Jenkins way.
	r = parseReal(t, "playwright/junit.xml", FormatJUnit)
	wantCounts(t, r, Counts{Total: 12, Passed: 8, Failed: 2, Skipped: 2})
	c = findCase(t, r, "shop › flaky: passes on retry")
	if len(c.Attachments) != 4 || c.Attachments[0].Path != "test-results/shop-shop-flaky-passes-on-retry-chromium/test-failed-1.png" || c.Attachments[0].Name != "test-failed-1.png" {
		t.Errorf("attachments = %+v", c.Attachments)
	}
}

func TestRealCucumberJS(t *testing.T) {
	r := parseReal(t, "cucumber-js/cucumber.json", FormatCucumber)
	wantCounts(t, r, Counts{Total: 6, Passed: 3, Failed: 1, Errored: 1, Skipped: 1})
	c := findCase(t, r, "Wrong total is reported")
	if c.Suite != "Shopping cart" || c.File != "features/cart.feature" || c.Message != "AssertionError [ERR_ASSERTION]: Expected values to be strictly equal:" ||
		!strings.Contains(c.Details, "3 !== 4") || strings.Join(c.Tags, ",") != "shop,regression" {
		t.Errorf("failed scenario = %+v", c)
	}
	if s := stepNamed(c, "the cart has 4 items"); s == nil || s.Status != Failed || s.Keyword != "Then" || !strings.Contains(s.Error, "3 !== 4") {
		t.Errorf("failed step = %+v", s)
	}
	c = findCase(t, r, "Gift wrap is undefined")
	if c.Status != Errored || c.Message != "step is undefined: When I ask for gift wrapping" {
		t.Errorf("undefined scenario = %+v", c)
	}
	if c := findCase(t, r, "Checkout is pending"); c.Status != Skipped || c.Message != "step is pending: Then I can check out" {
		t.Errorf("pending scenario = %+v", c)
	}
	// Outline examples get their line, so they do not share a history.
	a, b := findCase(t, r, "Add several kinds (line 33)"), findCase(t, r, "Add several kinds (line 34)")
	if a.Key() == b.Key() {
		t.Error("outline examples share a key")
	}
	// Embeddings: text decoded, the screenshot decoded to a PNG.
	c = findCase(t, r, "Add a single item")
	if len(c.Attachments) != 3 || string(c.Attachments[0].Data) != "cart now: apple,apple" || !strings.HasPrefix(string(c.Attachments[1].Data), "\x89PNG") || c.Attachments[1].ContentType != "image/png" {
		t.Errorf("attachments = %+v", c.Attachments)
	}
	if s := stepNamed(c, "I take a screenshot"); s == nil || len(s.Attachments) != 2 {
		t.Errorf("screenshot step = %+v", s)
	}
}

func TestRealKarate(t *testing.T) {
	check := func(t *testing.T, r *Report, failed int) {
		t.Helper()
		wantCounts(t, r, Counts{Total: 6, Passed: 6 - failed, Failed: failed})
		c := findCase(t, r, "get a single user")
		if !strings.Contains(c.File, "users.feature") || c.Suite != "Users API" {
			t.Errorf("case = %s / %s / %s", c.Suite, c.Name, c.File)
		}
		s := stepNamed(c, "method get")
		if s == nil || s.HTTP == nil {
			t.Fatalf("no HTTP exchange in %+v", c.Steps)
		}
		h := s.HTTP
		if h.Method != "GET" || h.URL != "http://127.0.0.1:8099/users/1" || h.Status != 200 || h.DurationMs <= 0 ||
			!strings.Contains(h.ResponseBody, `"Ada"`) || len(h.RequestHeaders) < 3 || len(h.ResponseHeaders) < 2 {
			t.Errorf("exchange = %+v", h)
		}
		c = findCase(t, r, "wrong role is reported (fails)")
		if c.Status != Failed || c.Message != "match failed: EQUALS" || !strings.Contains(c.Details, "'admin'") {
			t.Errorf("failed case = %+v", c)
		}
		// Outline examples are told apart by line.
		n := 0
		for _, c := range r.Cases {
			if strings.HasPrefix(c.Name, "lookup user ") && strings.Contains(c.Name, "(line ") {
				n++
			}
		}
		if n != 3 {
			t.Errorf("%d outline examples with a line", n)
		}
	}
	t.Run("1.5.2 karate json", func(t *testing.T) {
		r := parseReal(t, "karate/v1.5.2/karate-reports/tests.users.karate-json.txt", FormatKarate)
		check(t, r, 2)
		c := findCase(t, r, "create a user")
		if s := stepNamed(c, "method post"); s == nil || s.HTTP == nil || s.HTTP.Status != 201 || s.HTTP.RequestBody != `{"name":"Grace","role":"user"}` {
			t.Errorf("POST step = %+v", s)
		}
		if strings.Join(c.Tags, ",") != "smoke,api" {
			t.Errorf("tags = %v", c.Tags)
		}
	})
	t.Run("1.5.2 cucumber json", func(t *testing.T) {
		check(t, parseReal(t, "karate/v1.5.2/karate-reports/tests.users.json", FormatCucumber), 2)
	})
	t.Run("2.1.4 cucumber json", func(t *testing.T) {
		r := parseReal(t, "karate/v2.1.4/cucumber-json/users.json", FormatCucumber)
		check(t, r, 1)
		// Karate 2 logs the exchange as a text/plain embedding and ends it
		// with a summary that is not part of the body.
		c := findCase(t, r, "create a user")
		s := stepNamed(c, "method post")
		if s == nil || s.HTTP == nil || s.HTTP.Status != 201 || s.HTTP.DurationMs != 6 || strings.Contains(s.HTTP.ResponseBody, "201 (6 ms)") || len(s.Attachments) != 1 {
			t.Errorf("POST step = %+v", s)
		}
	})
	t.Run("2.1.4 events", func(t *testing.T) {
		check(t, parseReal(t, "karate/v2.1.4/karate-json/karate-events.jsonl", FormatKarate), 1)
	})
}

func TestRealPlaywright(t *testing.T) {
	r := parseReal(t, "playwright/results.json", FormatPlaywright)
	wantCounts(t, r, Counts{Total: 12, Passed: 8, Failed: 2, Skipped: 2, Flaky: 2})
	if r.DurationMs != 9608 {
		t.Errorf("duration = %d, want stats.duration", r.DurationMs)
	}
	c := findVariant(t, r, "flaky: passes on retry", "firefox")
	if !c.Flaky || c.Retries != 1 || len(c.Attempts) != 1 || c.Attempts[0].Status != Failed || !strings.HasPrefix(c.Attempts[0].Message, "Error: first attempt fails") {
		t.Errorf("flaky = %+v", c)
	}
	// The passing retry kept nothing; the failed try's files are shown.
	if len(c.Attachments) != 4 || c.Attachments[3].Path != "/work/test-results/shop-shop-flaky-passes-on-retry-firefox/trace.zip" || c.Attachments[3].ContentType != "application/zip" {
		t.Errorf("attachments = %+v", c.Attachments)
	}
	c = findVariant(t, r, "shows wrong count (fails)", "chromium")
	if c.Suite != "shop.spec.js › shop" || c.File != "shop.spec.js" || c.Message != "Error: expect(locator).toHaveText(expected) failed" ||
		strings.Contains(c.Details, "\x1b") || c.Retries != 1 || len(c.Attachments) != 4 || !strings.Contains(c.Attachments[0].Path, "-retry1/") {
		t.Errorf("failed = %+v", c)
	}
	if c.Key() == findVariant(t, r, "shows wrong count (fails)", "firefox").Key() {
		t.Error("the two projects share a key")
	}
	c = findVariant(t, r, "adds an item to the cart", "chromium")
	if strings.Join(c.Tags, ",") != "smoke,cart" {
		t.Errorf("tags = %v", c.Tags)
	}
	c = findVariant(t, r, "attaches a note", "firefox")
	if len(c.Attachments) != 1 || string(c.Attachments[0].Data) != "hello from the test" || c.Attachments[0].Name != "note.txt" {
		t.Errorf("inline attachment = %+v", c.Attachments)
	}
}

func TestRealMochawesome(t *testing.T) {
	r := parseReal(t, "cypress/mochawesome-merged.json", FormatMochawesome)
	wantCounts(t, r, Counts{Total: 7, Passed: 4, Failed: 1, Skipped: 2})
	c := findCase(t, r, "shows wrong count (fails)")
	if c.Suite != "shop" || c.File != "cypress/e2e/shop.cy.js" || !strings.HasPrefix(c.Message, "AssertionError: Timed out retrying after 500ms") || !strings.Contains(c.Details, "shop.cy.js:17:39") {
		t.Errorf("failed = %+v", c)
	}
	if c := findCase(t, r, "adds"); c.File != "cypress/e2e/math.cy.js" || c.Suite != "math (second spec)" {
		t.Errorf("second file = %+v", c)
	}
}

func TestRealCTRF(t *testing.T) {
	r := parseReal(t, "jest/ctrf-report.json", FormatCTRF)
	wantCounts(t, r, Counts{Total: 7, Passed: 4, Failed: 1, Skipped: 2, Flaky: 1})
	c := findCase(t, r, "flaky: passes on retry")
	if !c.Flaky || c.Retries != 1 || len(c.Attempts) != 1 || c.Attempts[0].Status != Failed {
		t.Errorf("flaky = %+v", c)
	}
	c = findCase(t, r, "cart applies TEN discount (wrong expectation)")
	if c.Suite != "cart.test.js › cart" || c.File != "/work/src/cart.test.js" || c.Message != "Error: expect(received).toBe(expected) // Object.is equality" || !strings.Contains(c.Details, "cart.test.js:13:33") {
		t.Errorf("failed = %+v", c)
	}
	r = parseReal(t, "playwright/ctrf-report.json", FormatCTRF)
	wantCounts(t, r, Counts{Total: 12, Passed: 8, Failed: 2, Skipped: 2, Flaky: 2})
	if c := r.Cases[0]; strings.Join(c.Tags, ",") != "smoke" || c.Suite != "chromium › shop.spec.js › shop" {
		t.Errorf("first = %+v", c)
	}
}

func TestRealTestNG(t *testing.T) {
	r := parseReal(t, "testng/testng-native/testng-results.xml", FormatTestNG)
	// setUp is a configuration method that passed: not a case.
	wantCounts(t, r, Counts{Total: 6, Passed: 3, Failed: 1, Skipped: 2})
	c := findCase(t, r, "wrongTotal")
	if c.Suite != "com.example.cart.CartNgTest" || c.Message != "doubled base expected [25] but found [20]" || !strings.Contains(c.Details, "java.lang.AssertionError") || strings.Join(c.Tags, ",") != "regression" {
		t.Errorf("failed = %+v", c)
	}
	c = findCase(t, r, "doubles[3, 6]")
	if strings.Join(c.Tags, ",") != "smoke" || len(c.Properties) != 2 || c.Properties[1] != (Property{"param1", "6"}) {
		t.Errorf("data provider row = %+v", c)
	}
	if c := findCase(t, r, "skippedAtRuntime"); c.Status != Skipped || c.Message != "feature flag off" {
		t.Errorf("skipped = %+v", c)
	}
}

func TestRealDotNet(t *testing.T) {
	r := parseReal(t, "dotnet/nunit/nunit3.xml", FormatNUnit)
	wantCounts(t, r, Counts{Total: 8, Passed: 4, Failed: 2, Skipped: 2})
	c := findCase(t, r, "TenDiscountIsWrong")
	if c.Suite != "CartNunit.CartTests" || !strings.HasPrefix(c.Message, "TEN discount\nAssert.That") || strings.Join(c.Tags, ",") != "cart,regression" ||
		len(c.Properties) != 1 || c.Properties[0] != (Property{"Owner", "cart-team"}) || !strings.Contains(c.Details, "NunitTests.cs:line 25") {
		t.Errorf("nunit = %+v", c)
	}
	r = parseReal(t, "dotnet/xunit/nunit3.xml", FormatNUnit)
	if c := findCase(t, r, "HalfDiscount(price: 3, expected: 2)"); c.Suite != "CartXunit.CartTests" || c.Status != Failed {
		t.Errorf("nunit (xunit logger) = %+v", c)
	}

	r = parseReal(t, "dotnet/xunit/xunit-v2.xml", FormatXUnit)
	wantCounts(t, r, Counts{Total: 7, Passed: 3, Failed: 3, Skipped: 1})
	c = findCase(t, r, "TenDiscountIsWrong")
	if c.Suite != "CartXunit.CartTests" || !strings.HasPrefix(c.Message, "Assert.Equal() Failure: Values differ") || strings.Join(c.Tags, ",") != "regression" || c.Properties[0] != (Property{"Owner", "cart-team"}) {
		t.Errorf("xunit = %+v", c)
	}
	if c := findCase(t, r, "HalfDiscount(price: 3, expected: 2)"); c.Status != Failed {
		t.Errorf("theory row = %+v", c)
	}

	r = parseReal(t, "dotnet/xunit/xunit.trx", FormatTRX)
	wantCounts(t, r, Counts{Total: 7, Passed: 3, Failed: 3, Skipped: 1})
	c = findCase(t, r, "TenDiscountIsWrong")
	if c.Suite != "CartXunit.CartTests" || c.DurationMs != 4 || !strings.HasPrefix(c.Message, "Assert.Equal() Failure") || c.Properties[0] != (Property{"owner", "cart-team"}) {
		t.Errorf("trx = %+v", c)
	}
	if c := findCase(t, r, "Currencies"); c.Status != Skipped || c.Message != "waiting for currency support" {
		t.Errorf("trx skipped = %+v", c)
	}
	r = parseReal(t, "dotnet/nunit/nunit.trx", FormatTRX)
	wantCounts(t, r, Counts{Total: 8, Passed: 4, Failed: 2, Skipped: 2})
	if c := findCase(t, r, "Inconclusive"); c.Status != Skipped || c.Message != "environment not ready" {
		t.Errorf("trx inconclusive = %+v", c)
	}
}

func TestRealTAP(t *testing.T) {
	r := parseReal(t, "tap/node-test/node-test.tap", FormatTAP)
	wantCounts(t, r, Counts{Total: 7, Passed: 4, Failed: 2, Skipped: 1})
	c := findCase(t, r, "child throws")
	if c.Suite != "cart › has subtests" || c.Message != "boom in child" || !strings.Contains(c.Details, "cart.test.mjs:20:13") {
		t.Errorf("subtest = %+v", c)
	}
	c = findCase(t, r, "fails an assertion")
	if c.Suite != "cart" || c.Message != "Expected values to be strictly deep-equal:" || c.DurationMs != 1 {
		t.Errorf("failed = %+v", c)
	}
	if c := findCase(t, r, "is skipped"); c.Status != Skipped || c.Message != "not ready" {
		t.Errorf("skip = %+v", c)
	}
	if c := findCase(t, r, "is todo"); c.Status != Passed {
		t.Errorf("passing todo = %+v", c)
	}
	if c := findCase(t, r, "top-level with diagnostic"); c.Suite != "" {
		t.Errorf("top level = %+v", c)
	}

	r = parseReal(t, "tap/bats/bats.tap", FormatTAP)
	wantCounts(t, r, Counts{Total: 3, Passed: 1, Failed: 1, Skipped: 1})
	c = findCase(t, r, "string comparison fails")
	if c.DurationMs != 6 || !strings.Contains(c.Details, `[ "$output" = "goodbye" ]`) {
		t.Errorf("bats failure = %+v", c)
	}
	if c := findCase(t, r, "skipped test"); c.Message != "not implemented" {
		t.Errorf("bats skip = %+v", c)
	}
}

func TestRealRobot(t *testing.T) {
	r := parseReal(t, "robot/output.xml", FormatRobot)
	wantCounts(t, r, Counts{Total: 4, Passed: 2, Failed: 1, Skipped: 1})
	c := findCase(t, r, "Division Fails")
	if c.Suite != "Suite" || c.File != "/work/suite.robot" || c.Message != "integer division expected: 2.5 != 3.0" || strings.Join(c.Tags, ",") != "api,math,regression" {
		t.Errorf("failed = %+v", c)
	}
	s := stepNamed(c, "Should Be Equal As Numbers")
	if s == nil || s.Keyword != "BuiltIn" || s.Status != Failed || s.Error != "integer division expected: 2.5 != 3.0" {
		t.Errorf("failed keyword = %+v", s)
	}
	c = findCase(t, r, "Addition Works")
	if s := stepNamed(c, "Evaluate"); s == nil || s.Depth != 1 || s.Text != "${r} = 5" {
		t.Errorf("nested keyword = %+v", s)
	}
	if c := findCase(t, r, "Not Ready Yet"); c.Status != Skipped || c.Message != "feature flag disabled" {
		t.Errorf("skipped = %+v", c)
	}
}
