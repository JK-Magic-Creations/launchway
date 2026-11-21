package osv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/findings"
)

const goAdvisory = `{
 "id": "GHSA-xxxx-1111-aaaa", "aliases": ["CVE-2026-0001"], "summary": "Path traversal in example.com/lib",
 "modified": "2026-10-01T00:00:00Z",
 "severity": [{"type":"CVSS_V3","score":"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}],
 "affected": [{"package":{"ecosystem":"Go","name":"example.com/lib"},
   "ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.4.2"}]},
             {"type":"SEMVER","events":[{"introduced":"1.5.0"},{"fixed":"1.5.1"}]}]}],
 "references": [{"type":"ADVISORY","url":"https://example.com/advisory/1"}]}`

const npmAdvisory = `{
 "id": "GHSA-yyyy-2222-bbbb", "summary": "ReDoS in left-pad",
 "database_specific": {"severity": "MODERATE"},
 "affected": [{"package":{"ecosystem":"npm","name":"left-pad"},"versions":["1.2.0","1.3.0"]}]}`

const pypiAdvisory = `{
 "id": "PYSEC-2026-1", "summary": "Leak in Requests",
 "database_specific": {"severity": "HIGH"},
 "affected": [{"package":{"ecosystem":"PyPI","name":"Requests"},
   "ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"2.31.0"}]}]}]}`

const withdrawn = `{"id":"GHSA-gone","withdrawn":"2026-01-01T00:00:00Z","affected":[{"package":{"ecosystem":"Go","name":"example.com/lib"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"}]}]}]}`

func mirror() *DirIndex {
	return &DirIndex{FS: fstest.MapFS{
		"go/GHSA-xxxx-1111-aaaa.json":  {Data: []byte(goAdvisory)},
		"npm/GHSA-yyyy-2222-bbbb.json": {Data: []byte(npmAdvisory)},
		"PyPI/PYSEC-2026-1.json":       {Data: []byte(pypiAdvisory)},
		"Go/GHSA-gone.json":            {Data: []byte(withdrawn)},
		"Go/broken.json":               {Data: []byte("{")},
		"Go/readme.txt":                {Data: []byte("x")},
	}}
}

func deps() []depupdate.Dep {
	return []depupdate.Dep{
		{Ecosystem: "go", Name: "example.com/lib", Current: "v1.4.0", Direct: true, File: "go.mod", Line: 6},
		{Ecosystem: "go", Name: "example.com/lib2", Current: "v1.0.0", Direct: true, File: "go.mod", Line: 7},
		{Ecosystem: "npm", Name: "left-pad", Current: "1.3.0", Direct: true, File: "web/package.json", Line: 5},
		{Ecosystem: "python", Name: "requests", Current: "2.28.1", Direct: true, File: "requirements.txt", Line: 1},
		{Ecosystem: "python", Name: "requests", Current: "2.31.0", Direct: true, File: "other/requirements.txt", Line: 1},
		{Ecosystem: "container", Name: "node", Current: "24", Direct: true, File: "Dockerfile", Line: 1},
	}
}

func TestCheckAgainstMirror(t *testing.T) {
	m, err := Check(context.Background(), mirror(), deps())
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 {
		t.Fatalf("%d matches: %+v", len(m), m)
	}
	if m[0].ID != "GHSA-xxxx-1111-aaaa" || m[0].Severity != "critical" || m[0].Score != 9.8 || m[0].FixedIn != "1.4.2" || m[0].Dep.Line != 6 {
		t.Errorf("first: %+v", m[0])
	}
	bySev := map[string]string{}
	for _, x := range m {
		bySev[x.ID] = x.Severity + "/" + x.FixedIn
	}
	if bySev["GHSA-yyyy-2222-bbbb"] != "medium/" || bySev["PYSEC-2026-1"] != "high/2.31.0" {
		t.Errorf("%v", bySev)
	}
}

func TestRangeEdges(t *testing.T) {
	v, _ := Raw([]byte(goAdvisory))
	cases := map[string]bool{"v0.9.0": true, "v1.4.1": true, "v1.4.2": false, "v1.4.9": false, "v1.5.0": true, "v1.5.1": false, "v2.0.0": false}
	for ver, want := range cases {
		if got, _ := Affects(v, "Go", "example.com/lib", ver); got != want {
			t.Errorf("%s affected=%v want %v", ver, got, want)
		}
	}
	// fixed version above the current one: the lowest
	if _, fixed := Affects(v, "Go", "example.com/lib", "v1.5.0"); fixed != "1.5.1" {
		t.Errorf("fixed %q", fixed)
	}
	// last_affected
	la, _ := Raw([]byte(`{"id":"x","affected":[{"package":{"ecosystem":"npm","name":"a"},"ranges":[{"type":"SEMVER","events":[{"introduced":"1.0.0"},{"last_affected":"1.2.0"}]}]}]}`))
	if ok, _ := Affects(la, "npm", "a", "1.2.0"); !ok {
		t.Error("last_affected is inclusive")
	}
	if ok, _ := Affects(la, "npm", "a", "1.2.1"); ok {
		t.Error("after last_affected")
	}
}

func TestSeverityScores(t *testing.T) {
	cases := map[string]float64{
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H": 9.8,
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:C/C:H/I:H/A:H": 10.0,
		"CVSS:3.1/AV:L/AC:L/PR:L/UI:N/S:U/C:H/I:N/A:N": 5.5,
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N": 5.3,
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:N": 0,
	}
	for vec, want := range cases {
		got, ok := cvss3(vec)
		if !ok || got != want {
			t.Errorf("%s = %v, want %v", vec, got, want)
		}
	}
	if _, ok := cvss3("CVSS:3.1/AV:X"); ok {
		t.Error("bad vector")
	}
	if s, _ := Severity(Vuln{}); s != "unknown" {
		t.Error("no severity is unknown")
	}
}

func TestAPIIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/query":
			var q struct {
				Version string
				Package struct{ Name, Ecosystem string }
			}
			_ = json.NewDecoder(r.Body).Decode(&q)
			if q.Package.Name == "example.com/lib" {
				w.Write([]byte(`{"vulns":[{"id":"GHSA-xxxx-1111-aaaa"}]}`))
				return
			}
			w.Write([]byte(`{}`))
		case r.URL.Path == "/v1/vulns/GHSA-xxxx-1111-aaaa":
			w.Write([]byte(goAdvisory))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m, err := Check(context.Background(), &API{Client: srv.Client(), BaseURL: srv.URL}, deps()[:2])
	if err != nil || len(m) != 1 || m[0].FixedIn != "1.4.2" {
		t.Fatalf("%v %+v", err, m)
	}
	if _, err := (&API{Client: srv.Client(), BaseURL: srv.URL + "/nope"}).Lookup(context.Background(), Query{"Go", "a", "v1"}); err == nil {
		t.Error("an error status must fail")
	}
}

func TestSARIFRoundTrip(t *testing.T) {
	m, _ := Check(context.Background(), mirror(), deps())
	doc, err := SARIF(m)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := findings.Parse(context.Background(), doc, findings.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts.Total != 3 || rep.Counts.Critical != 1 || rep.Counts.High != 1 || rep.Counts.Medium != 1 {
		t.Fatalf("counts %+v", rep.Counts)
	}
	f := rep.Findings[0]
	if f.Tool != ToolName || f.File != "go.mod" || f.StartLine != 6 || !strings.Contains(f.Message, "Fixed in 1.4.2") || f.Fingerprint == "" {
		t.Errorf("%+v", f)
	}
	again, _ := SARIF(m)
	rep2, _ := findings.Parse(context.Background(), again, findings.Limits{})
	if rep2.Findings[0].Fingerprint != f.Fingerprint {
		t.Error("fingerprints must be stable between runs")
	}
}
