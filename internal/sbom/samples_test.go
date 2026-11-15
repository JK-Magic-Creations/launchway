package sbom

import (
	"context"
	"maps"
	"os"
	"path/filepath"
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
	return b
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

// checkReport checks the promises every report keeps, whatever the input.
func checkReport(t testing.TB, r *Report, lim Limits) {
	t.Helper()
	lim = lim.withDefaults()
	if r.Format != FormatCycloneDX || r.Components == nil || r.Licenses == nil {
		t.Errorf("format %q, nil lists %v %v", r.Format, r.Components == nil, r.Licenses == nil)
	}
	if len(r.Components) > lim.MaxComponents || len(r.Components) > r.Total || r.Truncated != (len(r.Components) < r.Total) {
		t.Errorf("%d components kept of %d, truncated %v", len(r.Components), r.Total, r.Truncated)
	}
	checkString(t, "SpecVersion", r.SpecVersion, 20)
	checkString(t, "Warning", r.Warning, maxWarnBytes)
	// Every component is counted once under "(none)" or once per licence,
	// so the counts add up to at least the total.
	sum, keys := 0, 0
	for k, n := range r.Licenses {
		checkString(t, "licence key", k, maxLicenceBytes)
		if n <= 0 || k == "" {
			t.Errorf("licence %q has count %d", k, n)
		}
		sum += n
		if k != NoLicence && k != OtherLicences {
			keys++
		}
	}
	if keys > MaxLicenceKeys || sum < r.Total || r.Licenses[NoLicence] > r.Total {
		t.Errorf("%d licence keys, counts add to %d for %d components", keys, sum, r.Total)
	}
	for i, c := range r.Components {
		checkString(t, "Name", c.Name, lim.MaxNameBytes)
		checkString(t, "Version", c.Version, maxVersionBytes)
		checkString(t, "Type", c.Type, maxTypeBytes)
		checkString(t, "PURL", c.PURL, maxPURLBytes)
		if c.Name == "" {
			t.Errorf("component %d has no name", i)
		}
		if len(c.Licenses) > maxLicences {
			t.Errorf("component %d has %d licences", i, len(c.Licenses))
		}
		for _, l := range c.Licenses {
			checkString(t, "licence", l, maxLicenceBytes)
			if _, ok := r.Licenses[l]; !ok && r.Licenses[OtherLicences] == 0 {
				t.Errorf("licence %q is not counted", l)
			}
		}
	}
}

func checkString(t testing.TB, field, s string, max int) {
	t.Helper()
	if len(s) > max {
		t.Errorf("%s is %d bytes, over %d", field, len(s), max)
	}
	if !utf8.ValidString(s) || s != strings.TrimSpace(s) {
		t.Errorf("%s is not valid UTF-8 or has white space at an end: %q", field, s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) {
			t.Errorf("%s holds control character %U: %q", field, r, s)
			return
		}
	}
}

func names(r *Report) string {
	var n []string
	for _, c := range r.Components {
		n = append(n, c.Name)
	}
	return strings.Join(n, ",")
}

func TestSyftSamples(t *testing.T) {
	r := parseSample(t, "sbom-licences.cdx.json")
	if r.SpecVersion != "1.7" || r.Total != 6 || names(r) != "certifi,charset-normalizer,idna,requests,urllib3,/workspace/requirements.txt" {
		t.Errorf("spec %q, total %d, names %s", r.SpecVersion, r.Total, names(r))
	}
	want := map[string]int{"MPL-2.0": 1, "MIT": 2, "BSD-3-Clause": 1, "Apache-2.0": 1, NoLicence: 1}
	if !maps.Equal(r.Licenses, want) {
		t.Errorf("licences = %v", r.Licenses)
	}
	c := r.Components[0]
	if c.Name != "certifi" || c.Version != "2026.10.23" || c.Type != "library" || c.PURL != "pkg:pypi/certifi@2026.10.23" || !slices.Equal(c.Licenses, []string{"MPL-2.0"}) {
		t.Errorf("certifi = %+v", c)
	}
	if c := r.Components[5]; c.Type != "file" || c.Licenses != nil || c.Version != "" {
		t.Errorf("file component = %+v", c)
	}

	r = parseSample(t, "sbom-py.cdx.json")
	if r.Total != 5 || !maps.Equal(r.Licenses, map[string]int{"BSD-3-Clause": 1, "GPL-2.0-or-later": 1, "MIT": 1, "Apache 2.0": 1, NoLicence: 1}) {
		t.Errorf("total %d, licences %v", r.Total, r.Licenses)
	}
	if c := r.Components[3]; c.Name != "requests" || c.Version != "2.19.1" || !slices.Equal(c.Licenses, []string{"Apache 2.0"}) {
		t.Errorf("requests = %+v", c)
	}

	r = parseSample(t, "sbom-small.cdx.json")
	if r.Total != 3 || r.Components[0].Name != "golang.org/x/net" || r.Components[0].Version != "v0.0.0-20210226172049-e18ecbb05110" || r.Licenses["BSD-3-Clause"] != 2 {
		t.Errorf("report = %+v", r)
	}
}

func TestNestedComponents(t *testing.T) {
	r := parseSample(t, "nested.cdx.json")
	if r.SpecVersion != "1.5" || r.Total != 7 {
		t.Errorf("spec %q, total %d", r.SpecVersion, r.Total)
	}
	// Parents before their children, nameless components left out (but
	// not their children), metadata.component not counted.
	if got := names(r); got != "com.example/shop,org.apache.commons/commons-text,inner-shaded,no-version-here,@angular/core,child-of-nameless,alpine" {
		t.Errorf("names = %s", got)
	}
	shop := r.Components[0]
	if shop.Type != "application" || shop.Version != "2.4.0" || shop.PURL != "pkg:maven/com.example/shop@2.4.0" || !slices.Equal(shop.Licenses, []string{"Apache-2.0 OR MIT"}) {
		t.Errorf("shop = %+v", shop)
	}
	if c := r.Components[3]; !slices.Equal(c.Licenses, []string{"Custom Licence 1.0", "MIT"}) || c.Version != "" {
		t.Errorf("no-version-here = %+v", c)
	}
	want := map[string]int{"Apache-2.0 OR MIT": 1, "Apache-2.0": 1, "Custom Licence 1.0": 1, "MIT": 2, "ISC": 1, NoLicence: 2}
	if !maps.Equal(r.Licenses, want) {
		t.Errorf("licences = %v", r.Licenses)
	}
}
