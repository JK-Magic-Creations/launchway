package sbom

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func mustParse(t testing.TB, s string, lim Limits) *Report {
	t.Helper()
	return parseBytes(t, []byte(s), lim)
}

func bom(components string) string {
	return `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[` + components + `]}`
}

func slow(d time.Duration) time.Duration {
	if raceEnabled {
		return 10 * d
	}
	return d
}

func TestContentThatIsNotAnSBOM(t *testing.T) {
	random := make([]byte, 4096)
	rng := rand.New(rand.NewPCG(3, 4))
	for i := range random {
		random[i] = byte(rng.UintN(256))
	}
	random[0] = 0x8f
	for _, tc := range []struct{ name, in, want string }{
		{"empty file", "", "not a CycloneDX SBOM: the file is empty"},
		{"only white space", "\n \t", "not a CycloneDX SBOM: the file holds only white space"},
		{"plain text", "hello", `not a CycloneDX SBOM: it is not JSON (it starts with "hello")`},
		{"html", "<html><body>404</body></html>", `not a CycloneDX SBOM: it is XML or HTML (it starts with "<html><bo…"); only CycloneDX JSON is read`},
		{"cyclonedx xml", `<?xml version="1.0"?><bom xmlns="http://cyclonedx.org/schema/bom/1.5"/>`, "not a CycloneDX SBOM: it is XML or HTML"},
		{"random binary", string(random), "not a CycloneDX SBOM: it is not JSON"},
		{"json list", `[{"bomFormat":"CycloneDX"}]`, "not a CycloneDX SBOM: the JSON is a list; a CycloneDX SBOM is an object"},
		{"spdx", `{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","packages":[]}`, "not a CycloneDX SBOM: this is an SPDX document; only CycloneDX JSON is read"},
		{"spdx after components", `{"components":[{"name":"a"}],"spdxVersion":"SPDX-2.3"}`, "not a CycloneDX SBOM: this is an SPDX document"},
		{"another format", `{"bomFormat":"SWID","components":[]}`, `not a CycloneDX SBOM: its bomFormat is "SWID", not "CycloneDX"`},
		{"format as a number", `{"bomFormat":5}`, `not a CycloneDX SBOM: its bomFormat is "5", not "CycloneDX"`},
		{"no format", `{"specVersion":"1.5","components":[{"name":"a"}]}`, `not a CycloneDX SBOM: the JSON object has no bomFormat "CycloneDX"`},
		{"sarif", `{"version":"2.1.0","runs":[]}`, `not a CycloneDX SBOM: the JSON object has no bomFormat "CycloneDX"`},
		{"broken", "{\"specVersion\":\n\"1.5\" \"x\"}", "not a CycloneDX SBOM: the JSON could not be read (line 2: expected ',' or '}' after an object member, found '\"')"},
		{"cut before the format", `{"components":[{"name":"a"}`, "not a CycloneDX SBOM: the file ends before it says it is CycloneDX (it has no bomFormat)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(context.Background(), []byte(tc.in), Limits{})
			if !errors.Is(err, ErrNotSBOM) {
				t.Fatalf("err = %v, want ErrNotSBOM", err)
			}
			if !strings.HasPrefix(err.Error(), "not a CycloneDX SBOM: ") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q\nwant it to contain %q", err, tc.want)
			}
			checkString(t, "error", err.Error(), 400)
		})
	}
}

func TestFormatAndVersion(t *testing.T) {
	for _, tc := range []struct{ in, spec, warning string }{
		{`{"bomFormat":"cyclonedx","specVersion":"1.2"}`, "1.2", ""},
		{"\xEF\xBB\xBF {\"bomFormat\":\" CycloneDX \",\"specVersion\":\"1.7\",\"components\":[]}\n", "1.7", ""},
		{`{"bomFormat":"CycloneDX","specVersion":"2.0"}`, "2.0", `specVersion "2.0" is not one this reader knows (1.2 to 1.7); it was read anyway`},
		{`{"bomFormat":"CycloneDX","specVersion":1.4}`, "1.4", ""},
		{`{"bomFormat":"CycloneDX"}`, "", "the SBOM does not give its specVersion; it was read as CycloneDX 1.7"},
		{`{"bomFormat":"CycloneDX","specVersion":"1.5"} trailing`, "1.5", "there is more data after the end of the SBOM (line 1); it was ignored"},
	} {
		r := mustParse(t, tc.in, Limits{})
		if r.SpecVersion != tc.spec || r.Warning != tc.warning || r.Total != 0 {
			t.Errorf("%q: spec %q, warning %q", tc.in, r.SpecVersion, r.Warning)
		}
	}
}

func TestOddComponents(t *testing.T) {
	r := mustParse(t, bom(strings.Join([]string{
		`{"name":"a","version":1.2,"type":["x"],"licenses":"MIT"}`,
		`{"name":"","group":"g"}`,
		`{"name":"b","name":"c"}`,
		`"just a string"`,
		`{"name":"d","licenses":[{"license":{"id":""}},{"license":{"name":"  "}},{"expression":""},{},5,{"license":"MIT"}]}`,
		`{"name":"e","components":{"name":"not a list"}}`,
		`{"name":"f","components":[{"name":"g"},7,{"name":"h"}]}`,
		`{"name":12}`,
	}, ",")), Limits{})
	if got := names(r); got != "a,d,e,f,g,h,12" {
		t.Errorf("names = %s", got)
	}
	if c := r.Components[0]; c.Version != "1.2" || c.Type != "" || c.Licenses != nil {
		t.Errorf("a = %+v", c)
	}
	if r.Warning != "3 components could not be read and were skipped" || r.Licenses[NoLicence] != 7 {
		t.Errorf("warning %q, licences %v", r.Warning, r.Licenses)
	}
}

func TestDirtyText(t *testing.T) {
	r := mustParse(t, bom("{\"group\":\"o\\u0000rg\",\"name\":\"li\\u001b[31mb\\n\\u202eevil\",\"version\":\"1.0\\r\\n\",\"purl\":\"pkg:npm/x\\u0007@1\",\"licenses\":[{\"license\":{\"id\":\"MIT\\u0000\\u001b[0m\"}}],\"type\":\"lib\xffrary\"}"), Limits{})
	c := r.Components[0]
	if c.Name != "org/lib evil" || c.Version != "1.0" || c.PURL != "pkg:npm/x@1" || c.Licenses[0] != "MIT" || c.Type != "lib�rary" {
		t.Errorf("component = %+v", c)
	}
}

func TestDeepNesting(t *testing.T) {
	const depth = 100000
	var sb strings.Builder
	for range depth {
		sb.WriteString(`{"name":"n","components":[`)
	}
	for range depth {
		sb.WriteString(`]}`)
	}
	start := time.Now()
	r := mustParse(t, bom(`{"name":"first"},`+sb.String()+`,{"name":"last"}`), Limits{})
	if d := time.Since(start); d > slow(2*time.Second) {
		t.Errorf("took %s", d)
	}
	if names(r) != "first,last" || r.Warning != "1 components were nested more than 64 deep and skipped" {
		t.Errorf("names %s, warning %q", names(r), r.Warning)
	}
	// Within the limit, every level is read.
	sb.Reset()
	for range 30 {
		sb.WriteString(`{"name":"n","components":[`)
	}
	for range 30 {
		sb.WriteString(`]}`)
	}
	if r := mustParse(t, bom(sb.String()), Limits{}); r.Total != 30 {
		t.Errorf("total %d", r.Total)
	}
	// Unclosed nesting is a file cut short.
	r = mustParse(t, `{"bomFormat":"CycloneDX","components":[{"name":"a"},`+strings.Repeat("[", depth), Limits{})
	if r.Total != 1 || !strings.HasPrefix(r.Warning, "the file ends part-way through; components after that are missing") {
		t.Errorf("total %d, warning %q", r.Total, r.Warning)
	}
}

func TestHugeStrings(t *testing.T) {
	big := strings.Repeat("x€", 1<<20)
	r := mustParse(t, bom(`{"name":"`+big+`","group":"`+big+`","version":"`+big+`","purl":"`+big+`","type":"`+big+`","licenses":[{"license":{"id":"`+big+`"}}]}`), Limits{})
	c := r.Components[0]
	if len(c.Name) > 300 || len(c.Version) > 200 || len(c.PURL) > 1000 || len(c.Type) > 50 || len(c.Licenses[0]) > 200 || !strings.HasSuffix(c.Name, "…") {
		t.Errorf("sizes %d %d %d %d %d", len(c.Name), len(c.Version), len(c.PURL), len(c.Type), len(c.Licenses[0]))
	}
}

func manyComponents(n, licences int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"bomFormat":"CycloneDX","specVersion":"1.6","components":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"type":"library","name":"pkg%d","version":"1.%d.0","purl":"pkg:npm/pkg%d@1.%d.0","licenses":[{"license":{"id":"LIC-%d"}}]}`, i, i%100, i, i%100, i%licences)
	}
	b.WriteString("]}")
	return b.Bytes()
}

func TestManyComponents(t *testing.T) {
	n := 1_000_000
	if raceEnabled || testing.Short() {
		n = 100_000
	}
	data := manyComponents(n, 1000)
	start := time.Now()
	r := parseBytes(t, data, Limits{MaxComponents: 500, Timeout: 10 * time.Minute})
	d := time.Since(start)
	t.Logf("%d components, %.1f MB, parsed in %s", r.Total, float64(len(data))/1e6, d)
	if r.Total != n || len(r.Components) != 500 || !r.Truncated || r.Components[499].Name != "pkg499" {
		t.Errorf("total %d, %d kept, truncated %v", r.Total, len(r.Components), r.Truncated)
	}
	// 1000 different licences: the first 200 are counted by name.
	if len(r.Licenses) != MaxLicenceKeys+1 || r.Licenses["LIC-0"] != n/1000 || r.Licenses[OtherLicences] != n*800/1000 {
		t.Errorf("%d licence keys, LIC-0 %d, other %d", len(r.Licenses), r.Licenses["LIC-0"], r.Licenses[OtherLicences])
	}
	if !strings.Contains(r.Warning, "more than 200 different licences") {
		t.Errorf("warning %q", r.Warning)
	}
	if d > slow(30*time.Second) {
		t.Errorf("took %s", d)
	}
}

func TestFileCutShort(t *testing.T) {
	full := readSample(t, "nested.cdx.json")
	whole := parseSample(t, "nested.cdx.json")
	// The bomFormat comes last in this file, so cut a copy that has it first.
	moved := append([]byte(`{"bomFormat":"CycloneDX",`), full[1:]...)
	start := bytes.Index(moved, []byte(`"components"`))
	prev := 0
	for cut := start + 15; cut < bytes.Index(moved, []byte(`"metadata"`)); cut += 37 {
		r, err := Parse(context.Background(), moved[:cut], Limits{})
		if err != nil {
			t.Fatalf("cut at %d: %v", cut, err)
		}
		checkReport(t, r, Limits{})
		if r.Warning != "the file ends part-way through; components after that are missing" {
			t.Fatalf("cut at %d: warning %q", cut, r.Warning)
		}
		if r.Total < prev || r.Total > whole.Total {
			t.Fatalf("cut at %d: %d components after %d", cut, r.Total, prev)
		}
		prev = r.Total
		for i, c := range r.Components {
			if c.Name != whole.Components[i].Name {
				t.Fatalf("cut at %d: component %d is %q", cut, i, c.Name)
			}
		}
	}
	if prev < 5 {
		t.Errorf("only %d components read before the last cut", prev)
	}
	r := mustParse(t, `{"bomFormat":"CycloneDX","components":[{"name":"a"},{"name" "b"}]}`, Limits{})
	if r.Total != 1 || !regexp.MustCompile(`^the JSON is broken at line 1 \(expected ':' after an object key, found '"'\); components after that are missing$`).MatchString(r.Warning) {
		t.Errorf("total %d, warning %q", r.Total, r.Warning)
	}
}

func TestTimeout(t *testing.T) {
	data := manyComponents(300_000, 10)
	start := time.Now()
	r, err := Parse(context.Background(), data, Limits{Timeout: 20 * time.Millisecond})
	if d := time.Since(start); d > slow(time.Second) {
		t.Errorf("took %s", d)
	}
	if err != nil {
		if !strings.HasPrefix(err.Error(), "parsing stopped after 20ms before any components were read") {
			t.Errorf("err = %v", err)
		}
	} else if !regexp.MustCompile(`^parsing stopped after 20ms; components after line \d+ are missing$`).MatchString(r.Warning) || r.Total == 300_000 {
		t.Errorf("warning %q, total %d", r.Warning, r.Total)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Parse(ctx, data, Limits{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestMemoryIsBounded(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation")
	}
	const size = 20 << 20
	var huge, many bytes.Buffer
	huge.WriteString(`{"bomFormat":"CycloneDX","components":[{"name":"`)
	for huge.Len() < size {
		huge.WriteString("abcdefgh12345678")
	}
	huge.WriteString(`"}]}`)
	many.WriteString(`{"bomFormat":"CycloneDX","components":[`)
	for i := 0; many.Len() < size; i++ {
		if i > 0 {
			many.WriteByte(',')
		}
		fmt.Fprintf(&many, `{"name":"%s","hashes":[%s],"properties":[%s]}`, strings.Repeat("n", 800), strings.Repeat(`{"alg":"SHA-1","content":"x"},`, 20)+`{}`, strings.Repeat(`{"name":"a","value":"b"},`, 20)+`{}`)
	}
	many.WriteString("]}")
	for name, data := range map[string][]byte{"one huge name": huge.Bytes(), "many components": many.Bytes()} {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		r, err := Parse(context.Background(), data, Limits{MaxComponents: 100, Timeout: time.Minute})
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err)
		}
		used := after.TotalAlloc - before.TotalAlloc
		t.Logf("%s: %.1f MB of input, %.1f MB allocated, %d components", name, float64(len(data))/1e6, float64(used)/1e6, r.Total)
		if used > 3*uint64(len(data)) || r.Total == 0 {
			t.Errorf("%s: %d bytes allocated for %d bytes of input, %d components", name, used, len(data), r.Total)
		}
	}
}
