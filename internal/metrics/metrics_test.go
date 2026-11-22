package metrics

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestExpositionFormat(t *testing.T) {
	r := NewRegistry()
	c := r.New(Desc{Name: "t_total", Kind: Counter, Labels: []string{"a"}, Help: "h"})
	h := r.New(Desc{Name: "t_seconds", Kind: Histogram, Buckets: []float64{1, 10}, Help: "h"})
	c.Inc("x\"y")
	c.Add(-5, "x\"y") // a counter never goes down
	h.Observe(0.5)
	h.Observe(5)
	var b bytes.Buffer
	if err := r.Write(&b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`t_total{a="x\"y"} 1`, `t_seconds_bucket{le="1"} 1`, `t_seconds_bucket{le="10"} 2`, `t_seconds_bucket{le="+Inf"} 2`, "t_seconds_sum 5.5", "t_seconds_count 2", "# TYPE t_seconds histogram"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("exposition lacks %q:\n%s", want, b.String())
		}
	}
}

func TestSeriesCapDropsAndCounts(t *testing.T) {
	r := NewRegistry()
	c := r.New(Desc{Name: "capped_total", Kind: Counter, Labels: []string{"a"}, Cap: 3, Help: "h"})
	for i := 0; i < 10; i++ {
		c.Inc(string(rune('a' + i)))
	}
	if c.SeriesCount() != 3 {
		t.Fatalf("series = %d, want the cap of 3", c.SeriesCount())
	}
	if got := r.byName["launchway_metrics_series_dropped_total"].Value("capped_total"); got != 7 {
		t.Fatalf("dropped = %v, want 7", got)
	}
}

func TestHandlerTokenAndOnlyMetrics(t *testing.T) {
	r := NewRegistry()
	h := Handler(r, "s3cret")
	for _, c := range []struct {
		path, auth string
		want       int
	}{
		{"/metrics", "", 401}, {"/metrics", "Bearer wrong", 401}, {"/metrics", "Bearer s3cret", 200}, {"/api/v1/runs", "Bearer s3cret", 404},
	} {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("%s with %q: %d, want %d", c.path, c.auth, w.Code, c.want)
		}
	}
	w := httptest.NewRecorder()
	Handler(r, "").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != ContentType {
		t.Fatalf("no token: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
}

// A repository, a branch, a person or a run id must never be a label.
func TestNoIdentifyingLabels(t *testing.T) {
	banned := []string{"repository", "repo", "branch", "person", "user", "actor", "run", "run_id", "sha", "ref", "tenant", "email", "path"}
	for _, d := range Default.Descs() {
		if !strings.HasPrefix(d.Name, "launchway_") {
			t.Errorf("%s: metric names start with launchway_", d.Name)
		}
		if d.Help == "" {
			t.Errorf("%s: no help text", d.Name)
		}
		for _, l := range d.Labels {
			for _, b := range banned {
				if l == b {
					t.Errorf("%s: label %q identifies a person or a thing; not allowed", d.Name, l)
				}
			}
		}
		if len(d.Labels) > 3 {
			t.Errorf("%s: more than 3 labels", d.Name)
		}
	}
}

func TestFamily(t *testing.T) {
	for q, want := range map[string]string{"SELECT 1": "select", "  insert into x": "insert", "UPDATE t": "update", "delete from t": "delete", "CREATE TABLE": "other", "": "other"} {
		if got := Family(q); got != want {
			t.Errorf("Family(%q) = %s, want %s", q, got, want)
		}
	}
}

// The reference page is generated from the registry; a metric nobody
// documented makes this fail.
func TestReferencePageMatchesRegistry(t *testing.T) {
	const page = "../../docs/reference/metrics.md"
	want := Markdown(Default)
	if os.Getenv("LAUNCHWAY_UPDATE_DOCS") != "" {
		if err := os.WriteFile(page, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("the metrics reference is missing: %v (LAUNCHWAY_UPDATE_DOCS=1 writes it)", err)
	}
	if string(got) != want {
		t.Fatal("docs/reference/metrics.md does not match the metrics registry: run LAUNCHWAY_UPDATE_DOCS=1 go test ./internal/metrics -run TestReferencePageMatchesRegistry")
	}
}
