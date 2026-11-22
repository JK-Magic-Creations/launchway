package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImportPipelineRoute(t *testing.T) {
	s := &Server{}
	post := func(body string) (*httptest.ResponseRecorder, ImportResponse) {
		w := httptest.NewRecorder()
		s.importPipeline(w, httptest.NewRequest(http.MethodPost, "/api/v1/import/pipeline", strings.NewReader(body)))
		var out ImportResponse
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}
	file := `{"files":[{"path":".github/workflows/ci.yml","content":"on: push\njobs:\n  t:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/setup-go@v5\n        with: {go-version: '1.22'}\n      - run: go test ./...\n"}]}`
	w, out := post(file)
	if w.Code != 200 || len(out.Plans) != 1 {
		t.Fatalf("status %d, plans %d: %s", w.Code, len(out.Plans), w.Body)
	}
	p := out.Plans[0]
	if !strings.Contains(p.Pipeline, "launchway/go-test") || len(p.Findings) == 0 {
		t.Errorf("expected the go-test template and findings:\n%s", p.Pipeline)
	}
	// Nothing found is an answer, not an error.
	w, out = post(`{"files":[{"path":"README.md","content":"hi"}]}`)
	if w.Code != 200 || out.Message == "" || len(out.Plans) != 0 {
		t.Errorf("nothing found: status %d %+v", w.Code, out)
	}
	if w, _ = post(`not json`); w.Code != 400 {
		t.Errorf("bad json: status %d", w.Code)
	}
	if w, _ = post(`{"format":"nope","files":[{"path":"a","content":"b"}]}`); w.Code != 400 {
		t.Errorf("unknown format: status %d", w.Code)
	}
	huge := `{"files":[{"path":"a","content":"` + strings.Repeat("x", importBodyLimit) + `"}]}`
	if w, _ = post(huge); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("huge body: status %d", w.Code)
	}
}
