package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// The Insights pages: charts per Flight, per Squadron and for the installation,
// worked out from the product's own database. What a person sees is limited to the
// Flights they may read, whatever scope they ask for: a Flight they may not read is
// "not found", a Squadron counts only the Flights in it they may read, and the
// installation counts only the Flights they may read.

func (s *Server) insightsRoutes() []route {
	return []route{
		{"GET /api/v1/insights", signedIn, "", s.insights},
	}
}

func (s *Server) insights(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := identityFrom(r)
	now := s.Now()
	to, err := parseTime(q.Get("to"), now)
	if err != nil {
		s.fail(w, err)
		return
	}
	from, err := parseTime(q.Get("from"), to.Add(-30*24*time.Hour))
	if err != nil {
		s.fail(w, err)
		return
	}
	in := engine.InsightsQuery{From: from, To: to, Branch: q.Get("branch")}
	if b := q.Get("bucket"); b != "" {
		d, ok := map[string]time.Duration{"hour": time.Hour, "day": 24 * time.Hour, "week": 7 * 24 * time.Hour, "month": 30 * 24 * time.Hour}[b]
		if !ok {
			s.fail(w, fmt.Errorf("%w: bucket is hour, day, week or month", engine.ErrInvalid))
			return
		}
		in.Bucket = d
	}
	scope, name := "installation", ""
	switch {
	case q.Get("flight") != "" && q.Get("squadron") != "":
		s.fail(w, fmt.Errorf("%w: ask for a Flight or a Squadron, not both", engine.ErrInvalid))
		return
	case q.Get("flight") != "":
		scope, name = "flight", q.Get("flight")
		if !s.authorize(id, auth.Read, name) {
			writeErr(w, http.StatusNotFound, "project "+name+" not found")
			return
		}
		in.Projects = []string{name}
	case q.Get("squadron") != "":
		scope, name = "squadron", q.Get("squadron")
		by, lay, _, err := s.squadronData(r)
		if err != nil {
			s.fail(w, err)
			return
		}
		found := false
		if lay != nil {
			for _, u := range lay.Units {
				found = found || u.Name == name
			}
		}
		if !found {
			writeErr(w, http.StatusNotFound, fmt.Sprintf("There is no Squadron called %s.", name))
			return
		}
		in.Projects = []string{}
		for _, f := range by[name] { // only the Flights this person may see
			in.Projects = append(in.Projects, f.Name)
		}
	default:
		only, err := s.visibleProjects(id)
		if err != nil {
			s.fail(w, err)
			return
		}
		in.Projects = only // nil: every Flight
	}
	out, err := s.Engine.Insights(in)
	if err != nil {
		s.fail(w, err)
		return
	}
	if q.Get("format") == "csv" {
		writeInsightsCSV(w, scope, name, out)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "name": name, "insights": out})
}

// writeInsightsCSV writes the series of an Insights answer as a spreadsheet file.
func writeInsightsCSV(w http.ResponseWriter, scope, name string, in *engine.Insights) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="insights-`+scope+`.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"start", "runs", "succeeded", "failed", "success_rate", "duration_p50_s", "duration_p95_s", "queue_wait_p50_s", "queue_wait_p95_s", "tests_total", "tests_failed"})
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
	for _, p := range in.Series {
		_ = cw.Write([]string{time.UnixMilli(p.At).UTC().Format(time.RFC3339), strconv.Itoa(p.Runs), strconv.Itoa(p.Succeeded), strconv.Itoa(p.Failed),
			f(p.SuccessRate), f(p.DurationP50), f(p.DurationP95), f(p.QueueWaitP50), f(p.QueueWaitP95),
			strconv.FormatInt(p.TestsTotal, 10), strconv.FormatInt(p.TestsFailed, 10)})
	}
	cw.Flush()
}
