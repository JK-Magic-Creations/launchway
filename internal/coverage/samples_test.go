package coverage

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// The files under testdata/real were written by the real tools, run in
// containers: pytest-cov 7.1.0 (coverage.py 7.17.3), Jest 30.5.2, Vitest
// 5.1.1 (v8 provider), JaCoCo 0.8.16 through Maven and Gradle, Go 1.27.4.

func parseFile(t *testing.T, name string) *Report {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(context.Background(), b, Limits{})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	checkReport(t, r, Limits{})
	if r.Warning != "" {
		t.Errorf("%s: warning %q", name, r.Warning)
	}
	return r
}

func c(cov, tot int64) Counter { return Counter{Covered: cov, Total: tot} }

func TestRealSamples(t *testing.T) {
	for _, tc := range []struct {
		file, format, unit   string
		lines, branches, fns Counter
		files                []string
	}{
		// coverage.py's own root attributes: lines-valid 16 lines-covered
		// 11, branches-valid 8 branches-covered 4 (checked again below).
		{"real/pytest-cov/coverage.xml", FormatCobertura, UnitLines, c(11, 16), c(4, 8), c(0, 0), []string{"__init__.py"}},
		// Jest's text summary: lines 70% (7/10), branches 66.66% (4/6), functions 75% (3/4).
		{"real/jest/cobertura-coverage.xml", FormatCobertura, UnitLines, c(7, 10), c(4, 6), c(3, 4), []string{"src/cart.js"}},
		{"real/jest/lcov.info", FormatLCOV, UnitLines, c(7, 10), c(4, 6), c(3, 4), []string{"src/cart.js"}},
		{"real/jest/clover.xml", FormatClover, UnitLines, c(7, 10), c(4, 6), c(3, 4), []string{"/work/src/cart.js"}},
		{"real/jest/coverage-summary.json", FormatIstanbul, UnitLines, c(7, 10), c(4, 6), c(3, 4), []string{"/work/src/cart.js"}},
		// Vitest: lines 71.42% (5/7), branches 50% (3/6), functions 75% (3/4).
		{"real/vitest-v8/cobertura-coverage.xml", FormatCobertura, UnitLines, c(5, 7), c(3, 6), c(3, 4), []string{"src/cart.js"}},
		{"real/vitest-v8/lcov.info", FormatLCOV, UnitLines, c(5, 7), c(3, 6), c(3, 4), []string{"src/cart.js"}},
		{"real/vitest-v8/coverage-summary.json", FormatIstanbul, UnitLines, c(5, 7), c(3, 6), c(3, 4), []string{"/work/src/cart.js"}},
		// JaCoCo's report counters: LINE 2 missed 15 covered, BRANCH 3/7, METHOD 1/4.
		{"real/jacoco/maven/jacoco.xml", FormatJaCoCo, UnitLines, c(15, 17), c(7, 10), c(4, 5), []string{"com/example/cart/Cart.java"}},
		{"real/jacoco/gradle/jacocoTestReport.xml", FormatJaCoCo, UnitLines, c(15, 17), c(7, 10), c(4, 5), []string{"com/example/cart/Cart.java"}},
		// go tool cover -func: 46.7% of statements (7/15).
		{"real/go/cover-set.out", FormatGo, UnitStatements, c(7, 15), c(0, 0), c(0, 0), []string{"example.com/covdemo/mathx/mathx.go", "example.com/covdemo/strx/strx.go"}},
		{"real/go/cover-atomic.out", FormatGo, UnitStatements, c(7, 15), c(0, 0), c(0, 0), []string{"example.com/covdemo/mathx/mathx.go", "example.com/covdemo/strx/strx.go"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			r := parseFile(t, tc.file)
			if r.Format != tc.format || r.Unit != tc.unit {
				t.Errorf("format %q unit %q", r.Format, r.Unit)
			}
			got := r.Totals
			if got.Lines != tc.lines || got.Branches != tc.branches || got.Functions != tc.fns {
				t.Errorf("totals %+v, want lines %v branches %v functions %v", got, tc.lines, tc.branches, tc.fns)
			}
			if r.FileCount != len(tc.files) || len(r.Files) != len(tc.files) {
				t.Fatalf("files %+v", r.Files)
			}
			for i, f := range r.Files {
				if f.Path != tc.files[i] {
					t.Errorf("file %d = %q, want %q", i, f.Path, tc.files[i])
				}
			}
		})
	}
}

// TestRealSamplesAgreeWithTheirTools compares the computed totals with the
// totals the tools wrote into the files (which Parse itself ignores).
func TestRealSamplesAgreeWithTheirTools(t *testing.T) {
	attrInt := func(t *testing.T, b []byte, re string) int64 {
		t.Helper()
		m := regexp.MustCompile(re).FindSubmatch(b)
		if m == nil {
			t.Fatalf("no match for %s", re)
		}
		v, _ := strconv.ParseInt(string(m[1]), 10, 64)
		return v
	}
	for _, name := range []string{"real/pytest-cov/coverage.xml", "real/jest/cobertura-coverage.xml", "real/vitest-v8/cobertura-coverage.xml"} {
		b, _ := os.ReadFile(filepath.Join("testdata", name))
		r := parseFile(t, name)
		want := Totals{
			Lines:    c(attrInt(t, b, `<coverage[^>]* lines-covered="(\d+)"`), attrInt(t, b, `<coverage[^>]* lines-valid="(\d+)"`)),
			Branches: c(attrInt(t, b, `<coverage[^>]* branches-covered="(\d+)"`), attrInt(t, b, `<coverage[^>]* branches-valid="(\d+)"`)),
		}
		if r.Totals.Lines != want.Lines || r.Totals.Branches != want.Branches {
			t.Errorf("%s: %+v, the tool says %+v", name, r.Totals, want)
		}
	}
	for _, name := range []string{"real/jacoco/maven/jacoco.xml", "real/jacoco/gradle/jacocoTestReport.xml"} {
		b, _ := os.ReadFile(filepath.Join("testdata", name))
		r := parseFile(t, name)
		report := func(typ string) Counter {
			re := `<counter type="` + typ + `" missed="(\d+)" covered="(\d+)"/>(?:<counter[^>]*/>)*\s*</report>`
			m := regexp.MustCompile(re).FindSubmatch(b)
			if m == nil {
				t.Fatalf("%s: no report %s counter", name, typ)
			}
			mi, _ := strconv.ParseInt(string(m[1]), 10, 64)
			cv, _ := strconv.ParseInt(string(m[2]), 10, 64)
			return c(cv, mi+cv)
		}
		want := Totals{Lines: report("LINE"), Branches: report("BRANCH"), Functions: report("METHOD")}
		if r.Totals != want {
			t.Errorf("%s: %+v, JaCoCo says %+v", name, r.Totals, want)
		}
	}
	for _, name := range []string{"real/jest/clover.xml"} {
		b, _ := os.ReadFile(filepath.Join("testdata", name))
		r := parseFile(t, name)
		want := Totals{
			Lines:     c(attrInt(t, b, `<project[^>]*>\s*<metrics statements="\d+" coveredstatements="(\d+)"`), attrInt(t, b, `<project[^>]*>\s*<metrics statements="(\d+)"`)),
			Branches:  c(attrInt(t, b, `<project[^>]*>\s*<metrics[^>]* coveredconditionals="(\d+)"`), attrInt(t, b, `<project[^>]*>\s*<metrics[^>]* conditionals="(\d+)"`)),
			Functions: c(attrInt(t, b, `<project[^>]*>\s*<metrics[^>]* coveredmethods="(\d+)"`), attrInt(t, b, `<project[^>]*>\s*<metrics[^>]* methods="(\d+)"`)),
		}
		if r.Totals != want {
			t.Errorf("%s: %+v, Clover metrics say %+v", name, r.Totals, want)
		}
	}
	for _, pair := range [][2]string{{"real/jest/lcov.info", "real/jest/coverage-summary.json"}, {"real/vitest-v8/lcov.info", "real/vitest-v8/coverage-summary.json"}} {
		a, b := parseFile(t, pair[0]), parseFile(t, pair[1])
		if a.Totals != b.Totals {
			t.Errorf("%s %+v vs %s %+v", pair[0], a.Totals, pair[1], b.Totals)
		}
	}
}

func TestRealSamplesNotSupported(t *testing.T) {
	for name, want := range map[string]string{
		"real/jest/coverage-final.json":      "not a coverage report: the file is Istanbul's detailed coverage-final.json",
		"real/vitest-v8/coverage-final.json": "not a coverage report: the file is Istanbul's detailed coverage-final.json",
		"real/pytest-cov/coverage.json":      "not a coverage report: coverage.py's JSON report is not supported",
	} {
		b, _ := os.ReadFile(filepath.Join("testdata", name))
		_, err := Parse(context.Background(), b, Limits{})
		if err == nil || !regexp.MustCompile("^"+regexp.QuoteMeta(want)).MatchString(err.Error()) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPackages(t *testing.T) {
	r := parseFile(t, "real/jacoco/maven/jacoco.xml")
	if r.Files[0].Package != "com/example/cart" {
		t.Errorf("package %q", r.Files[0].Package)
	}
	r = parseFile(t, "real/go/cover-set.out")
	if r.Files[0].Package != "example.com/covdemo/mathx" || r.Files[0].Lines != c(5, 8) {
		t.Errorf("file %+v", r.Files[0])
	}
	r = parseFile(t, "real/pytest-cov/coverage.xml")
	if r.Files[0].Package != "." || len(r.Sources) != 1 || r.Sources[0] != "/work/calc" {
		t.Errorf("file %+v sources %q", r.Files[0], r.Sources)
	}
	r = parseFile(t, "real/jest/lcov.info")
	if r.Files[0].Package != "src" {
		t.Errorf("package %q", r.Files[0].Package)
	}
}
