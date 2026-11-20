package coverage

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMergeUnitesLineData(t *testing.T) {
	// One package's tests cover line 1 and branch 0 of a.c, another's line
	// 2 and branch 1; b.c is only in the second.
	a := mustParse(t, "SF:a.c\nFN:1,f\nFNDA:1,f\nFN:5,g\nFNDA:0,g\nDA:1,1\nDA:2,0\nDA:3,0\nBRDA:2,0,0,1\nBRDA:2,0,1,0\nend_of_record\n", Limits{})
	b := mustParse(t, "SF:a.c\nFN:5,g\nFNDA:0,g\nDA:1,0\nDA:2,4\nDA:3,0\nBRDA:2,0,0,0\nBRDA:2,0,1,1\nend_of_record\nSF:b.c\nDA:1,1\nend_of_record\n", Limits{})
	m := Merge([]*Report{a, nil, b}, Limits{})
	checkReport(t, m, Limits{})
	if m.Format != FormatLCOV || m.FileCount != 2 || m.Warning != "" {
		t.Errorf("%+v", m)
	}
	if f := m.Files[0]; f.Lines != c(2, 3) || f.Branches != c(2, 2) || f.Functions != c(1, 2) {
		t.Errorf("a.c %+v", f)
	}
	if m.Totals.Lines != c(3, 4) {
		t.Errorf("totals %+v", m.Totals)
	}
	// The inputs are unchanged, and merging again gives the same.
	if a.Totals.Lines != c(1, 3) || b.Totals.Lines != c(2, 4) {
		t.Errorf("inputs changed: %+v %+v", a.Totals, b.Totals)
	}
	if again := Merge([]*Report{m, a}, Limits{}); again.Totals != m.Totals {
		t.Errorf("merging again: %+v", again.Totals)
	}
}

func TestMergeGoBlocks(t *testing.T) {
	a := mustParse(t, "mode: set\np/x.go:1.1,2.2 3 0\np/x.go:3.1,4.2 1 1\n", Limits{})
	b := mustParse(t, "mode: set\np/x.go:1.1,2.2 3 1\np/x.go:3.1,4.2 1 0\n", Limits{})
	m := Merge([]*Report{a, b}, Limits{})
	if m.Unit != UnitStatements || m.Totals.Lines != c(4, 4) {
		t.Errorf("%+v", m)
	}
}

func TestMergeSumsCounters(t *testing.T) {
	a := mustParse(t, `{"x.js":{"lines":{"total":10,"covered":4}},"y.js":{"lines":{"total":2,"covered":2}}}`, Limits{})
	b := mustParse(t, `{"x.js":{"lines":{"total":10,"covered":6}}}`, Limits{})
	m := Merge([]*Report{a, b}, Limits{})
	checkReport(t, m, Limits{})
	if m.Files[0].Lines != c(10, 20) || m.Totals.Lines != c(12, 22) {
		t.Errorf("%+v", m)
	}
	if !strings.Contains(m.Warning, "1 files appear in several reports without line data to combine; their counters were added together") {
		t.Errorf("warning %q", m.Warning)
	}
	// Mixed formats and units.
	g := mustParse(t, "mode: set\nx.js:1.1,2.2 3 1\n", Limits{})
	m = Merge([]*Report{a, g}, Limits{})
	if m.Format != FormatMixed || m.Unit != UnitMixed || !strings.Contains(m.Warning, "different units") {
		t.Errorf("%+v", m)
	}
}

func TestMergeBoundedAndTruncated(t *testing.T) {
	a, _ := Parse(context.Background(), genLCOV(300), Limits{MaxFiles: 10})
	b, _ := Parse(context.Background(), []byte(strings.ReplaceAll(string(genLCOV(300)), "src/", "lib/")), Limits{MaxFiles: 10})
	m := Merge([]*Report{a, b}, Limits{MaxFiles: 50})
	checkReport(t, m, Limits{MaxFiles: 50})
	if len(m.Files) != 50 || m.FileCount != 600 || !m.Truncated || m.Totals.Lines != c(600, 1200) {
		t.Errorf("%d files, count %d, totals %+v", len(m.Files), m.FileCount, m.Totals)
	}
	// Reports that went through JSON lose their line data; files beyond
	// those listed still count in the totals.
	var rt Report
	j, _ := json.Marshal(a)
	if err := json.Unmarshal(j, &rt); err != nil {
		t.Fatal(err)
	}
	m = Merge([]*Report{&rt, b}, Limits{MaxFiles: 50})
	if m.FileCount != 600 || m.Totals.Lines != c(600, 1200) || m.Totals.Functions != c(300, 600) {
		t.Errorf("count %d totals %+v", m.FileCount, m.Totals)
	}
	onlyTotal := mustParse(t, `{"total":{"lines":{"total":8,"covered":2}}}`, Limits{})
	if m := Merge([]*Report{onlyTotal, b}, Limits{}); m.Totals.Lines != c(302, 608) || m.FileCount != 300 {
		t.Errorf("only total: %+v", m.Totals)
	}
	if e := Merge(nil, Limits{}); e.Files == nil || e.FileCount != 0 {
		t.Errorf("%+v", e)
	}
}

func FuzzParse(f *testing.F) {
	_ = filepath.WalkDir("testdata", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, err := os.ReadFile(p); err == nil {
				f.Add(b)
			}
		}
		return nil
	})
	for _, s := range []string{
		"",
		"<coverage/>",
		"<!DOCTYPE x [<!ENTITY a \"&b;\">]><coverage><packages><package><classes><class filename=\"&a;\"><lines><line number=\"1\" hits=\"1\" condition-coverage=\"(1/2)\"/>",
		"<report><package name=\"p\"><class name=\"C\" sourcefilename=\"A\"><method name=\"m\"><counter type=\"METHOD\" covered=\"1\"/></method></class><sourcefile name=\"A\"><line nr=\"1\" ci=\"1\"/></sourcefile>",
		"<coverage clover=\"1\"><project><file name=\"a\"><line num=\"1\" type=\"cond\" truecount=\"1\"/><metrics statements=\"9\"/>",
		"SF:a\nDA:1,1\nBRDA:1,0,0,-\nFN:1,f\nFNDA:1,f\nLF:9\nend_of_record\n",
		"mode: set\na.go:1.1,2.2 1 1\n",
		`{"total":{"lines":{"total":1,"covered":1}},"a":{"lines":{"total":-1,"covered":9}}}`,
	} {
		f.Add([]byte(s))
	}
	lim := Limits{MaxFiles: 5}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := Parse(context.Background(), data, lim)
		if err != nil {
			msg := err.Error()
			if !strings.HasPrefix(msg, "not a coverage report: ") && !strings.HasPrefix(msg, "parsing ") {
				t.Errorf("error = %q", msg)
			}
			if len(msg) > 500 || !utf8.ValidString(msg) {
				t.Errorf("error is %d bytes: %q", len(msg), msg)
			}
			return
		}
		checkReport(t, r, lim)
		m := Merge([]*Report{r, r}, lim)
		checkReport(t, m, lim)
		if m.FileCount != r.FileCount {
			t.Errorf("merge with itself: %d files, was %d", m.FileCount, r.FileCount)
		}
	})
}
