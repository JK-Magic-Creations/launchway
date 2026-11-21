package testreport

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A failed Go test case always says why: its own output, or what its subtests
// and the package printed, or the package's output when the package failed with
// no test to blame. These are real `go test -json` streams from Go 1.27.4.

// One failing test, from the run that failed on git 2.47: the output has a line
// a server process printed and the test's own failure line, and the package fails.
func TestGoTestRealFailingTestCarriesItsOutput(t *testing.T) {
	r := parseSample(t, "go-test-real-failing-test.json")
	wantCounts(t, r, Counts{Total: 1, Failed: 1})
	c := findCase(t, r, "TestQuotas")
	if !strings.HasPrefix(c.Message, "storage_test.go:502: clone from http://127.0.0.1:38809") {
		t.Errorf("message = %q", c.Message)
	}
	for _, want := range []string{
		"server rep1: git update-ref: fatal: multiple updates for 'refs/heads/main'",
		"storage_test.go:502: clone from",
	} {
		if !strings.Contains(c.Output, want) {
			t.Errorf("the output lacks %q:\n%s", want, c.Output)
		}
	}
	if strings.Contains(c.Output, "=== RUN") || strings.Contains(c.Output, "--- FAIL") {
		t.Errorf("framework lines are in the output:\n%s", c.Output)
	}
}

// Failures of every kind in one stream: a test that panicked, a failing test with a
// child process that wrote the reason to stderr, a parent of failed subtests, a
// package whose TestMain exited and a package that does not compile.
func TestGoTestRealFailureKindsAllSayWhy(t *testing.T) {
	r := parseSample(t, "go-test-real-1.27-failures.json")
	wantCounts(t, r, Counts{Total: 9, Passed: 2, Failed: 5, Errored: 2})
	for _, c := range r.Cases {
		if (c.Status == Failed || c.Status == Errored) && (c.Message == "" || c.Output == "") {
			t.Errorf("%s %s has no message or output: %+v", c.Suite, c.Name, c)
		}
	}
	if c := findCase(t, r, "TestChildSaysWhy"); !strings.Contains(c.Output, "child: fatal: the reason is here") {
		t.Errorf("a child process's line is lost: %+v", c)
	}
	c := findCase(t, r, "TestParent")
	if c.Message != "2 subtests failed: one: s_test.go:19: one broke: 1" ||
		!strings.Contains(c.Output, "three: s_test.go:21: three broke") {
		t.Errorf("the parent test does not name its failed subtests: %+v", c)
	}
	if c := findCase(t, r, "TestBoom"); !strings.Contains(c.Output, "panic: assignment to entry in nil map") {
		t.Errorf("the panic is not in the output: %+v", c)
	}
	if c := findCase(t, r, "[package failed]"); !strings.Contains(c.Output, "cannot reach the database") || !strings.Contains(c.Message, "cannot reach the database") {
		t.Errorf("a package that failed before its first test: %+v", c)
	}
	if c := findCase(t, r, "[build failed]"); !strings.Contains(c.Message, "undefined: undefinedName") {
		t.Errorf("a package that does not build: %+v", c)
	}
}

func goLines(lines ...string) []byte { return []byte(strings.Join(lines, "\n") + "\n") }

func ev(action, test, output string) string {
	s := fmt.Sprintf(`{"Time":"2026-11-21T08:40:00Z","Action":%q,"Package":"example.com/p"`, action)
	if test != "" {
		s += fmt.Sprintf(`,"Test":%q`, test)
	}
	if output != "" {
		s += fmt.Sprintf(`,"Output":%q`, output)
	}
	return s + "}"
}

// Output the package printed while one test ran, with no test named (a subprocess
// or a goroutine that outlived its test), is shown for that test when it printed nothing.
func TestGoTestPackageOutputDuringAFailedTestIsShown(t *testing.T) {
	data := goLines(
		ev("run", "TestA", ""),
		ev("output", "TestA", "=== RUN   TestA\n"),
		ev("output", "", "server: git update-ref: fatal: the reason\n"),
		ev("output", "TestA", "--- FAIL: TestA (0.01s)\n"),
		ev("fail", "TestA", ""),
		ev("output", "", "FAIL\n"),
		ev("fail", "", ""),
	)
	r, err := Parse(context.Background(), data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	c := findCase(t, r, "TestA")
	if c.Status != Failed || c.Output != "server: git update-ref: fatal: the reason" || c.Message != "server: git update-ref: fatal: the reason" {
		t.Errorf("TestA = %+v", c)
	}
}

// With many tests running at once nobody can say which printed a stray line, so it
// is not guessed.
func TestGoTestPackageOutputIsNotGuessedForManyParallelTests(t *testing.T) {
	var lines []string
	for i := 0; i < maxLooseTests+1; i++ {
		lines = append(lines, ev("run", fmt.Sprintf("TestP%d", i), ""))
	}
	lines = append(lines, ev("output", "", "who printed this\n"))
	for i := 0; i < maxLooseTests+1; i++ {
		lines = append(lines, ev("fail", fmt.Sprintf("TestP%d", i), ""))
	}
	r, err := Parse(context.Background(), goLines(lines...), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if c := findCase(t, r, "TestP0"); c.Output != "" {
		t.Errorf("an output was guessed: %+v", c)
	}
}

// A failed test's output is bounded, and it is the end that is kept.
func TestGoTestFailedOutputKeepsTheTail(t *testing.T) {
	lines := []string{ev("run", "TestBig", "")}
	for i := 0; i < 2000; i++ {
		lines = append(lines, ev("output", "TestBig", fmt.Sprintf("    big_test.go:%d: line number %d of the output\n", i+1, i)))
	}
	lines = append(lines, ev("output", "TestBig", "    big_test.go:9999: the last line is the reason\n"), ev("fail", "TestBig", ""), ev("fail", "", ""))
	r, err := Parse(context.Background(), goLines(lines...), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	c := findCase(t, r, "TestBig")
	if len(c.Output) > DefaultLimits().MaxOutputBytes+200 {
		t.Errorf("the output is %d bytes", len(c.Output))
	}
	if !strings.HasSuffix(c.Output, "the last line is the reason") || strings.Contains(c.Output, "line number 0 of") {
		t.Errorf("the tail was not kept: %q", c.Output[:min(len(c.Output), 200)])
	}
}
