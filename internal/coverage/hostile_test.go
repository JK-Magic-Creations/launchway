package coverage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// checkString checks that s is clean text within max bytes.
func checkString(t *testing.T, what, s string, max int) {
	t.Helper()
	if len(s) > max {
		t.Errorf("%s is %d bytes, more than %d", what, len(s), max)
	}
	if !utf8.ValidString(s) {
		t.Errorf("%s is not valid UTF-8: %q", what, s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r >= ctrlBase && r < ctrlBase+0x20) {
			t.Errorf("%s holds control character %U: %q", what, r, s)
			return
		}
	}
}

func checkCounter(t *testing.T, what string, c Counter) {
	t.Helper()
	if c.Total < 0 || c.Covered < 0 || c.Covered > c.Total {
		t.Errorf("%s = %+v", what, c)
	}
	if p, ok := c.Percent(); ok && (p < 0 || p > 100 || math.IsNaN(p)) {
		t.Errorf("%s percent %v", what, p)
	}
}

// checkReport checks the invariants every report keeps.
func checkReport(t *testing.T, r *Report, lim Limits) {
	t.Helper()
	lim = lim.withDefaults()
	if r.Files == nil {
		t.Error("Files is nil")
	}
	if len(r.Files) > lim.MaxFiles || len(r.Files) > r.FileCount {
		t.Errorf("%d files listed, %d seen, max %d", len(r.Files), r.FileCount, lim.MaxFiles)
	}
	if r.Truncated != (len(r.Files) < r.FileCount) {
		t.Errorf("truncated %v with %d of %d files", r.Truncated, len(r.Files), r.FileCount)
	}
	if !sort.SliceIsSorted(r.Files, func(i, j int) bool { return r.Files[i].Path < r.Files[j].Path }) {
		t.Error("files are not sorted")
	}
	checkCounter(t, "total lines", r.Totals.Lines)
	checkCounter(t, "total branches", r.Totals.Branches)
	checkCounter(t, "total functions", r.Totals.Functions)
	var sum Totals
	for i, f := range r.Files {
		if i > 0 && f.Path == r.Files[i-1].Path {
			t.Errorf("file %q listed twice", f.Path)
		}
		checkString(t, "path", f.Path, lim.MaxPathBytes)
		checkString(t, "package", f.Package, lim.MaxPathBytes)
		if f.Path == "" {
			t.Error("empty path")
		}
		checkCounter(t, f.Path+" lines", f.Lines)
		checkCounter(t, f.Path+" branches", f.Branches)
		checkCounter(t, f.Path+" functions", f.Functions)
		sum = sum.add(Totals{f.Lines, f.Branches, f.Functions})
	}
	if !r.Truncated && r.FileCount > 0 && sum != r.Totals {
		t.Errorf("totals %+v, files add up to %+v", r.Totals, sum)
	}
	checkString(t, "warning", r.Warning, 2000)
	if len(r.Sources) > MaxSources {
		t.Errorf("%d sources", len(r.Sources))
	}
	for _, s := range r.Sources {
		checkString(t, "source", s, lim.MaxPathBytes)
	}
	if r.Unit != UnitLines && r.Unit != UnitStatements && r.Unit != UnitMixed {
		t.Errorf("unit %q", r.Unit)
	}
}

func parseString(t *testing.T, s string, lim Limits) (*Report, error) {
	t.Helper()
	r, err := Parse(context.Background(), []byte(s), lim)
	if err == nil {
		checkReport(t, r, lim)
	}
	return r, err
}

func mustParse(t *testing.T, s string, lim Limits) *Report {
	t.Helper()
	r, err := parseString(t, s, lim)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r
}

func TestContentThatIsNotAReport(t *testing.T) {
	random := make([]byte, 4096)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range random {
		random[i] = byte(rng.UintN(256))
	}
	random[0] = 0x8f
	junit, _ := os.ReadFile(filepath.Join("..", "testreport", "testdata", "pytest.xml"))
	if len(junit) == 0 {
		junit = []byte(`<?xml version="1.0"?><testsuites><testsuite name="x"><testcase name="a"/></testsuite></testsuites>`)
	}
	sarif := `{"$schema":"https://json.schemastore.org/sarif-2.1.0.json","version":"2.1.0","runs":[{"tool":{"driver":{"name":"semgrep"}},"results":[]}]}`
	for _, tc := range []struct{ name, in, want string }{
		{"empty file", "", "not a coverage report: the file is empty"},
		{"only white space", " \n\t\n", "not a coverage report: the file holds only white space"},
		{"plain text", "hello world, this is a log", `not a coverage report: the format is not recognised (it starts with "hello wor…"); known coverage formats are`},
		{"junit xml", string(junit), "not a coverage report: the XML's root element is <testsuites>; known coverage formats are Cobertura, JaCoCo and Clover XML, LCOV, Go coverprofiles and Istanbul json-summary"},
		{"html page", "<!DOCTYPE html>\n<html><head><title>502</title></head></html>", "not a coverage report: the XML's root element is <html>;"},
		{"sarif", sarif, "not a coverage report: the file is JSON, but not an Istanbul json-summary"},
		{"package.json", `{"name": "shop", "version": "1.0.0"}`, "not a coverage report: the file is JSON, but not an Istanbul json-summary"},
		{"empty json object", `{}`, "not a coverage report: the file is JSON, but not an Istanbul json-summary"},
		{"json array", `[1,2]`, "not a coverage report: the file is a JSON array"},
		{"broken json", `{"total": {"lines": `, "not a coverage report: the JSON could not be read (the file ends part-way through"},
		{"random binary", string(random), "not a coverage report: the format is not recognised (it starts with"},
		{"broken xml", "<<<>>>", "not a coverage report: the XML could not be read (line 1"},
		{"xml with no root", "<?xml version=\"1.0\"?>\n<!-- nothing -->\n", "not a coverage report: the XML has no root element"},
		{"utf-16 xml", `<?xml version="1.0" encoding="UTF-16"?><coverage/>`, "not a coverage report: the XML is in encoding UTF-16, which is not supported"},
		{"go test -json", `{"Action":"run","Package":"p","Test":"T"}` + "\n", "not a coverage report: the file is JSON, but not an Istanbul json-summary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseString(t, tc.in, Limits{})
			if !errors.Is(err, ErrNotReport) {
				t.Fatalf("err = %v, want ErrNotReport", err)
			}
			if !strings.HasPrefix(err.Error(), "not a coverage report: ") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q\nwant it to contain %q", err, tc.want)
			}
			checkString(t, "error", err.Error(), 400)
		})
	}
}

func TestDetection(t *testing.T) {
	for _, tc := range []struct{ in, format string }{
		{`<coverage line-rate="1"><packages/></coverage>`, FormatCobertura},
		{`<coverage><sources><source>x</source></sources></coverage>`, FormatCobertura},
		{`<coverage/>`, FormatCobertura},
		{`<coverage generated="1"/>`, FormatClover},
		{`<coverage generated="1"><project/></coverage>`, FormatClover},
		{`<coverage clover="4.4.1"></coverage>`, FormatClover},
		{`<?xml version="1.0"?><!DOCTYPE report PUBLIC "-//JACOCO//DTD Report 1.1//EN" "report.dtd"><report name="x"/>`, FormatJaCoCo},
		{"TN:\nSF:a.c\nDA:1,1\nend_of_record\n", FormatLCOV},
		{"SF:a.c\nDA:1,1\nend_of_record", FormatLCOV},
		{"mode: set\n", FormatGo},
		{"\xEF\xBB\xBF\n mode: count\na/b.go:1.1,2.2 1 1\n", FormatGo},
		{`{"total":{"lines":{"total":0,"covered":0,"pct":"Unknown"}}}`, FormatIstanbul},
	} {
		r := mustParse(t, tc.in, Limits{})
		if r.Format != tc.format {
			t.Errorf("%q: format %q, want %q", tc.in, r.Format, tc.format)
		}
	}
}

func TestCountsWrittenInFilesAreNotTrusted(t *testing.T) {
	// Cobertura's rates and valid/covered attributes are ignored.
	r := mustParse(t, `<coverage line-rate="1.0" lines-valid="999" lines-covered="999" branch-rate="1"><packages><package name="p" line-rate="1"><classes>
<class name="A" filename="a.py" line-rate="1"><lines><line number="1" hits="1"/><line number="2" hits="0"/><line number="2" hits="0"/></lines></class>
<class name="B" filename="a.py"><lines><line number="2" hits="3"/><line number="3" hits="0" branch="true" condition-coverage="100% (1/4)"/></lines></class>
</classes></package></packages></coverage>`, Limits{})
	if r.Totals.Lines != c(2, 3) || r.Totals.Branches != c(1, 4) || r.FileCount != 1 {
		t.Errorf("%+v", r)
	}
	// LCOV's LF/LH lose to DA lines.
	r = mustParse(t, "SF:a\nDA:1,1\nDA:2,0\nLF:100\nLH:100\nBRF:9\nBRH:9\nFNF:3\nFNH:3\nend_of_record\n", Limits{})
	if r.Totals.Lines != c(1, 2) || r.Totals.Branches != c(9, 9) || r.Totals.Functions != c(3, 3) {
		t.Errorf("%+v", r.Totals)
	}
	// JaCoCo's sourcefile counters lose to line data.
	r = mustParse(t, `<report name="x"><package name="p"><sourcefile name="A.java"><line nr="1" mi="0" ci="1" mb="1" cb="1"/><line nr="2" mi="2" ci="0"/>
<counter type="LINE" missed="0" covered="50"/><counter type="BRANCH" missed="0" covered="50"/><counter type="METHOD" missed="0" covered="7"/></sourcefile></package></report>`, Limits{})
	if r.Totals.Lines != c(1, 2) || r.Totals.Branches != c(1, 2) || r.Totals.Functions != c(7, 7) {
		t.Errorf("%+v", r.Totals)
	}
	// Clover metrics lose to lines; OpenClover cond lines are two branches.
	r = mustParse(t, `<coverage clover="4"><project><package name="p"><file name="A.java" path="src/A.java"><metrics statements="50" coveredstatements="50" conditionals="9" coveredconditionals="9" methods="2" coveredmethods="2"/>
<line num="1" type="method" name="f" count="1"/><line num="2" type="stmt" count="0"/><line num="3" type="cond" truecount="4" falsecount="0"/></file></package></project></coverage>`, Limits{})
	if r.Totals.Lines != c(0, 1) || r.Totals.Branches != c(1, 2) || r.Totals.Functions != c(1, 1) || r.Files[0].Package != "p" {
		t.Errorf("%+v", r)
	}
	// Istanbul's "total" is ignored when there are files.
	r = mustParse(t, `{"total":{"lines":{"total":1000,"covered":1000,"pct":100}},"a.js":{"lines":{"total":4,"covered":1,"pct":100}},"b.js":{"lines":{"total":2,"covered":2}}}`, Limits{})
	if r.Totals.Lines != c(3, 6) || r.FileCount != 2 {
		t.Errorf("%+v", r)
	}
}

func TestHugeAndBadNumbers(t *testing.T) {
	big := "99999999999999999999999999"
	for _, in := range []string{
		`<coverage line-rate="NaN"><packages><package><classes><class filename="a"><lines>
<line number="1" hits="` + big + `" branch="true" condition-coverage="x (` + big + `/` + big + `)"/>
<line number="2" hits="-5" branch="true" condition-coverage="(5/-3)"/><line number="-1" hits="1"/><line number="x" hits="1"/>
<line number="3" hits="NaN" condition-coverage="(9/2)"/><line number="4" hits="1e400"/></lines></class>
<class filename="b"><lines><line number="1" hits="1" condition-coverage="(` + big + `/` + big + `)"/></lines></class></classes></package></packages></coverage>`,
		"SF:a\nDA:1," + big + "\nDA:2,-1\nDA:-3,1\nDA:x,y\nBRDA:1,0,0," + big + "\nLF:-1\nLH:" + big + "\nend_of_record\nSF:b\nLF:" + big + "\nLH:" + big + "\nend_of_record\nSF:c\nLF:" + big + "\nLH:5\nend_of_record\n",
		"mode: set\na.go:1.1,2.2 " + big + " 1\nb.go:1.1,2.2 " + big + " " + big + "\nb.go:1.1,2.2 -4 NaN\nc.go:1.1,2.2 3 -1\n",
		`{"a":{"lines":{"total":` + big + `,"covered":` + big + `},"branches":{"total":-4,"covered":7},"functions":{"total":"NaN","covered":1e999}},"b":{"lines":{"total":` + big + `,"covered":1}}}`,
		`<report><package name="p"><sourcefile name="A"><counter type="LINE" missed="` + big + `" covered="` + big + `"/></sourcefile><sourcefile name="B"><counter type="LINE" missed="-1" covered="` + big + `"/></sourcefile></package></report>`,
		`<coverage clover="1"><project><file name="a"><metrics statements="` + big + `" coveredstatements="` + big + `" conditionals="-1" coveredconditionals="3"/></file><file name="b"><metrics statements="` + big + `" coveredstatements="1"/></file></project></coverage>`,
	} {
		r := mustParse(t, in, Limits{})
		if r.Totals.Lines.Total != math.MaxInt64 && r.Totals.Branches.Total != math.MaxInt64 {
			t.Errorf("%.40q: totals %+v", in, r.Totals)
		}
	}
}

func TestBadCoberturaValues(t *testing.T) {
	r := mustParse(t, `<coverage line-rate="NaN"><packages><package><classes><class filename="a"><lines>
<line number="1" hits="99999999999999999999" branch="true" condition-coverage="(1/2)"/><line number="2" hits="-5" condition-coverage="(5/-3)"/>
<line number="-1" hits="1"/><line number="x" hits="1"/><line number="3" hits="NaN" condition-coverage="(9/2)"/><line number="4" hits="0.2"/>
</lines></class></classes></package></packages></coverage>`, Limits{})
	if r.Totals.Lines != c(2, 4) || r.Totals.Branches != c(3, 4) {
		t.Errorf("%+v", r.Totals)
	}
}

func TestDeepNesting(t *testing.T) {
	const depth = 100000
	in := `<coverage line-rate="1"><packages><package name="p"><classes><class filename="a"><lines><line number="1" hits="1"/></lines></class>` +
		strings.Repeat("<x>", depth) + strings.Repeat("</x>", depth) + `</classes></package></packages></coverage>`
	r := mustParse(t, in, Limits{})
	if r.Totals.Lines != c(1, 1) || !strings.Contains(r.Warning, "more than 64 deep") {
		t.Errorf("%+v", r)
	}
	_, err := parseString(t, strings.Repeat("<a>", depth), Limits{MaxDepth: 10})
	if !errors.Is(err, ErrNotReport) {
		t.Errorf("err = %v", err)
	}
	_, err = parseString(t, `{"a":`+strings.Repeat("[", depth), Limits{})
	if err == nil || err.Error() != "not a coverage report: the JSON nests more than 64 deep" {
		t.Errorf("err = %v", err)
	}
	r = mustParse(t, `{"a.js":{"lines":{"total":2,"covered":1}},"b":`+strings.Repeat(`{"x":`, depth), Limits{})
	if r.Totals.Lines != c(1, 2) || !strings.Contains(r.Warning, "the JSON nests more than 64 deep") {
		t.Errorf("%+v", r)
	}
}

func TestBillionLaughsIsNotExpanded(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("<?xml version=\"1.0\"?>\n<!DOCTYPE coverage [\n <!ENTITY lol \"lol\">\n")
	for i := 1; i <= 9; i++ {
		prev := "lol"
		if i > 1 {
			prev = fmt.Sprintf("lol%d", i-1)
		}
		fmt.Fprintf(&sb, " <!ENTITY lol%d \"%s\">\n", i, strings.Repeat("&"+prev+";", 10))
	}
	sb.WriteString("]>\n<coverage line-rate=\"1\"><packages><package name=\"&lol9;\"><classes><class filename=\"a&lol9;.py\"><lines><line number=\"1\" hits=\"&lol9;\"/></lines></class></classes></package></packages></coverage>\n")
	r := mustParse(t, sb.String(), Limits{})
	if r.Files[0].Path != "a&lol9;.py" || r.Files[0].Package != "&lol9;" || r.Totals.Lines != c(0, 1) {
		t.Errorf("%+v", r.Files)
	}
}

func TestExternalEntityIsNeverRead(t *testing.T) {
	secret := "TOP-SECRET-" + fmt.Sprint(time.Now().UnixNano())
	path := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{
		fmt.Sprintf(`<?xml version="1.0"?>
<!DOCTYPE report [ <!ENTITY xxe SYSTEM "file://%s"> <!ENTITY %% remote SYSTEM "file://%s"> %%remote; ]>
<report name="&xxe;"><package name="&xxe;"><sourcefile name="&xxe;"><line nr="1" mi="0" ci="1"/></sourcefile></package></report>`, path, path),
		fmt.Sprintf(`<!DOCTYPE coverage SYSTEM "file://%s"><coverage clover="1"><project><file name="&xxe;"><line num="1" type="stmt" count="1"/></file></project></coverage>`, path),
	} {
		r := mustParse(t, in, Limits{})
		if dump := fmt.Sprintf("%+v", r); strings.Contains(dump, "TOP-SECRET") {
			t.Fatalf("the file's content leaked: %s", dump)
		}
		if !strings.Contains(r.Files[0].Path, "&xxe;") {
			t.Errorf("path = %q", r.Files[0].Path)
		}
	}
}

func TestDirtyStringsAreCleaned(t *testing.T) {
	r := mustParse(t, "SF:src/\x1b[31ma\x00b\xff\xfe.js\nDA:1,1\nend_of_record\n", Limits{})
	if r.Files[0].Path != "src/ab\uFFFD\uFFFD.js" {
		t.Errorf("path %q", r.Files[0].Path)
	}
	r = mustParse(t, "<coverage line-rate=\"1\"><sources><source>/w&#27;[1m\x01ork\n</source></sources><packages><package name=\"p&#1;\"><classes><class filename=\"a\x02\xc3.py\"><lines/></class></classes></package></packages></coverage>", Limits{})
	if r.Files[0].Path != "a\uFFFD.py" || r.Files[0].Package != "p" || r.Sources[0] != "/work" {
		t.Errorf("%+v", r)
	}
	long := strings.Repeat("€", 5000)
	r = mustParse(t, "SF:"+long+"\nDA:1,1\nend_of_record\n", Limits{MaxPathBytes: 100})
	if !strings.HasSuffix(r.Files[0].Path, "…") || len(r.Files[0].Path) > 100 {
		t.Errorf("path %q", r.Files[0].Path)
	}
}

func TestLongLinesAreSkipped(t *testing.T) {
	in := "SF:a\nDA:1,1\nDA:2," + strings.Repeat("9", 2<<20) + "\nSF:" + strings.Repeat("x", 2<<20) + "\nDA:3,1\nSF:b\nDA:1,0\nend_of_record\n"
	r := mustParse(t, in, Limits{})
	if r.FileCount != 2 || r.Totals.Lines != c(1, 2) || !strings.Contains(r.Warning, "2 lines longer than 1 MiB were skipped (the first is line 3)") {
		t.Errorf("%+v", r)
	}
	r = mustParse(t, "mode: set\na.go:1.1,1.5 1 1\n"+strings.Repeat("y", 2<<20)+"\nbroken line\nb.go:1.1 1 1\n", Limits{})
	if r.FileCount != 1 || !strings.Contains(r.Warning, "2 lines could not be read (the first is line 4)") {
		t.Errorf("%+v", r)
	}
}

func genLCOV(files int) []byte {
	var sb strings.Builder
	for i := range files {
		fmt.Fprintf(&sb, "SF:src/f%07d.js\nFN:1,f\nFNDA:%d,f\nDA:1,1\nDA:2,0\nBRDA:2,0,0,1\nBRDA:2,0,1,0\nend_of_record\n", i, i%2)
	}
	return []byte(sb.String())
}

func TestManyFilesRespectMaxFiles(t *testing.T) {
	r, err := Parse(context.Background(), genLCOV(5000), Limits{MaxFiles: 100, Timeout: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	checkReport(t, r, Limits{MaxFiles: 100})
	if len(r.Files) != 100 || r.FileCount != 5000 || !r.Truncated {
		t.Fatalf("%d files, count %d", len(r.Files), r.FileCount)
	}
	if r.Totals.Lines != c(5000, 10000) || r.Totals.Branches != c(5000, 10000) || r.Totals.Functions != c(2500, 5000) {
		t.Errorf("totals %+v", r.Totals)
	}
	if r.Files[99].Path != "src/f0000099.js" {
		t.Errorf("last listed %q", r.Files[99].Path)
	}
}

func TestTimeLimit(t *testing.T) {
	data := genLCOV(200000)
	// A limit of 1ns has always passed by the first check (after 1000
	// lines or tokens, or 1 MiB of XML), however fast the machine: the
	// files read by then come back with a warning.
	r, err := Parse(context.Background(), data, Limits{Timeout: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	checkReport(t, r, Limits{})
	if r.FileCount != 125 || r.Warning != "parsing stopped after 1ns; coverage after line 999 is missing" {
		t.Errorf("count %d warning %q", r.FileCount, r.Warning)
	}
	// Before any coverage was read, it is an error.
	_, err = Parse(context.Background(), []byte("mode: set\n"+strings.Repeat("\n", 5000)), Limits{Timeout: time.Nanosecond})
	if err == nil || err.Error() != "parsing stopped after 1ns before any coverage was read" {
		t.Errorf("err = %v", err)
	}
	big := append([]byte("<coverage line-rate=\"1\">"), make([]byte, 0)...)
	big = append(big, []byte(strings.Repeat("<!-- padding padding padding -->", 100000))...)
	if _, err := Parse(context.Background(), big, Limits{Timeout: time.Nanosecond}); err == nil || !strings.HasPrefix(err.Error(), "parsing stopped after 1ns") {
		t.Errorf("err = %v", err)
	}
	for name, d := range map[string][]byte{
		"lcov":     data,
		"go":       []byte("mode: set\n" + strings.Repeat("a.go:1.1,2.2 1 1\n", 10000)),
		"istanbul": []byte(`{` + strings.Repeat(`"a.js":{"lines":{"total":1,"covered":1}},`, 10000) + `"z":{}}`),
		"xml":      big,
	} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Parse(ctx, d, Limits{})
		if !errors.Is(err, context.Canceled) || err.Error() != "parsing was cancelled before any coverage was read: context canceled" {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// TestCancelPartWay cancels the context from inside the parse (on a file
// path being cleaned is not possible, so a context that ends after a set
// number of Err calls is used): what was read comes back with a warning.
func TestCancelPartWay(t *testing.T) {
	ctx := &countdownCtx{Context: context.Background(), left: 3}
	r, err := Parse(ctx, genLCOV(50000), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	checkReport(t, r, Limits{})
	if r.FileCount == 0 || r.FileCount == 50000 || !regexp.MustCompile(`^parsing was cancelled; coverage after line \d+ is missing$`).MatchString(r.Warning) {
		t.Errorf("count %d warning %q", r.FileCount, r.Warning)
	}
}

// countdownCtx is a context that is cancelled after Err has been asked a
// number of times.
type countdownCtx struct {
	context.Context
	left int
}

func (c *countdownCtx) Err() error {
	if c.left <= 0 {
		return context.Canceled
	}
	c.left--
	return nil
}

func TestBrokenFilesGivePartialResults(t *testing.T) {
	b, _ := os.ReadFile(filepath.Join("testdata", "real", "jacoco", "maven", "jacoco.xml"))
	cut := b[:len(b)*3/4]
	r := mustParse(t, string(cut), Limits{})
	if r.FileCount != 1 || !strings.Contains(r.Warning, "the file ends part-way through") {
		t.Errorf("%+v", r)
	}
	r = mustParse(t, `{"a.js":{"lines":{"total":2,"covered":1}},"b.js":{"lines":`, Limits{})
	if r.FileCount != 1 || !strings.Contains(r.Warning, "the file ends part-way through") {
		t.Errorf("%+v", r)
	}
}

func TestLatin1(t *testing.T) {
	r := mustParse(t, "<?xml version='1.0' encoding='ISO-8859-1'?><coverage line-rate='1'><packages><package><classes><class filename='caf\xe9.py'><lines><line number='1' hits='1'/></lines></class></classes></package></packages></coverage>", Limits{})
	if r.Files[0].Path != "café.py" {
		t.Errorf("path %q", r.Files[0].Path)
	}
}

func TestLCOVDetails(t *testing.T) {
	// Two records for one file merge; lcov 2 FN with an end line, FNA,
	// exception blocks and "-" for never-reached branches.
	r := mustParse(t, `TN:t1
SF:lib/a.c
FN:3,9,main
FN:12,helper
FNDA:0,helper
DA:3,1
DA:4,0
BRDA:4,e0,0,-
BRDA:4,e0,1,2
end_of_record
TN:t2
SF:lib/a.c
FNL:0,3,9
FNA:0,5,helper
DA:4,7
DA:5,0
BRDA:4,e0,0,0
end_of_record
`, Limits{})
	f := r.Files[0]
	if f.Lines != c(2, 3) || f.Branches != c(1, 2) || f.Functions != c(1, 2) || f.Package != "lib" {
		t.Errorf("%+v", f)
	}
}

func TestGoDuplicateBlocks(t *testing.T) {
	r := mustParse(t, `mode: count
a/b/c.go:1.1,3.2 2 0
a/b/c.go:4.1,5.2 1 0
mode: count
a/b/c.go:1.1,3.2 2 5
a/b/c.go:4.1,5.2 1 0
C:\proj\x.go:1.1,1.2 1 1
`, Limits{})
	if r.Unit != UnitStatements || r.Files[0].Path != `C:\proj\x.go` || r.Files[1].Lines != c(2, 3) || r.Files[1].Package != "a/b" {
		t.Errorf("%+v", r)
	}
}

func TestCoberturaMethodsOnly(t *testing.T) {
	// A class whose lines are only listed under its methods.
	r := mustParse(t, `<coverage line-rate="0"><packages><package name="P"><classes><class name="C" filename="C.cs"><methods>
<method name="A" signature="()"><lines><line number="1" hits="1"/><line number="2" hits="0" branch="true" condition-coverage="50% (1/2)"/></lines></method>
<method name="B" signature="()"><lines><line number="5" hits="0"/></lines></method>
</methods><lines/></class></classes></package></packages></coverage>`, Limits{})
	f := r.Files[0]
	if f.Lines != c(1, 3) || f.Branches != c(1, 2) || f.Functions != c(1, 2) || f.Package != "P" {
		t.Errorf("%+v", f)
	}
}

func TestPercent(t *testing.T) {
	if _, ok := (Counter{}).Percent(); ok {
		t.Error("percent of nothing")
	}
	if p, ok := c(1, 4).Percent(); !ok || p != 25 {
		t.Errorf("%v %v", p, ok)
	}
	if p, _ := c(math.MaxInt64, math.MaxInt64).Percent(); p != 100 {
		t.Errorf("%v", p)
	}
}

func TestLargeInputParsesQuickly(t *testing.T) {
	data := genLCOV(100000) // about 9 MB
	start := time.Now()
	r, err := Parse(context.Background(), data, Limits{})
	if err != nil || r.Warning != "" {
		t.Fatalf("%v %q", err, r.Warning)
	}
	t.Logf("%.1f MB, %d files, parsed in %s", float64(len(data))/1e6, r.FileCount, time.Since(start))
}
