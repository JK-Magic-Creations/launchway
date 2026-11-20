package source

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/verify"
)

// promBackend reads a Prometheus-compatible query API: the range query
// endpoint /api/v1/query_range, sent as a POST form so that long queries
// fit. Thanos, Mimir and VictoriaMetrics serve the same endpoint.
type promBackend struct{}

// promAnswer is the API's envelope, also Loki's.
type promAnswer struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

// promSeries is one series of a matrix.
type promSeries struct {
	Metric map[string]string `json:"metric"`
	Values [][2]any          `json:"values"`
}

// prepareText checks a pipeline's PromQL or LogQL and puts its parameter
// values in.
func prepareText(lang string, q *Query) (string, error) {
	// Every placeholder counts as declared for the check of where it is;
	// one without a value is reported when it is filled in.
	declared, names := declaredFor(q)
	if err := CheckQuery(lang, q.Text, names); err != nil {
		return "", errorf("the query is not allowed: %v", err)
	}
	text, err := rewriteQL(lang, q.Text, q.Params, declared)
	if err != nil {
		return "", errorf("the query is not allowed: %v", err)
	}
	return text, nil
}

// declaredFor lists the parameters a query has values for and the
// placeholders its text uses.
func declaredFor(q *Query) (map[string]bool, []string) {
	if q.Params == nil {
		q.Params = map[string]string{} // nil would mean "only check"
	}
	declared := map[string]bool{}
	for k := range q.Params {
		declared[k] = true
	}
	for _, k := range usedPlaceholders(q.Text) {
		declared[k] = true
	}
	names := make([]string, 0, len(declared))
	for k := range declared {
		names = append(names, k)
	}
	return declared, names
}

func (promBackend) query(ctx context.Context, s *src, q *Query, cred credential) (Result, error) {
	if q.Kind != Metric {
		return Result{}, errorf("a prometheus source reads metrics only; read logs from a loki, elasticsearch or clickhouse source")
	}
	text, err := prepareText(Prometheus, q)
	if err != nil {
		return Result{}, err
	}
	form := url.Values{
		"query": {text},
		"start": {unixSeconds(q.Start)},
		"end":   {unixSeconds(q.End)},
		"step":  {strconv.FormatFloat(q.Step.Seconds(), 'f', -1, 64)},
		// The server stops evaluating when the runner stops waiting.
		"timeout": {s.timeout.String()},
		// Two are enough to know there is more than one series.
		"limit": {"2"},
	}
	r, err := s.send(ctx, cred, http.MethodPost, joinURL(s.cfg.URL, "/api/v1/query_range"),
		[]byte(form.Encode()), "application/x-www-form-urlencoded", nil)
	if err != nil {
		return Result{}, err
	}
	var a promAnswer
	if err := decodeJSON(r, &a); err != nil {
		if r.status != http.StatusOK {
			return Result{}, statusError(r, "")
		}
		return Result{}, err
	}
	if r.status != http.StatusOK || a.Status != "success" {
		msg := a.Error
		if a.ErrorType != "" {
			msg = a.ErrorType + ": " + msg
		}
		if r.status == http.StatusOK {
			return Result{}, errorf("the query failed: %s", msg)
		}
		return Result{}, statusError(r, msg)
	}
	if a.Data.ResultType != "matrix" {
		return Result{}, errorf("returned a %s, not the matrix a range query returns", a.Data.ResultType)
	}
	pts, err := oneSeries(a.Data.Result)
	return Result{Points: pts}, err
}

// oneSeries reads the points of a matrix that must have at most one series.
func oneSeries(raw json.RawMessage) ([]verify.Point, error) {
	var series []promSeries
	if err := json.Unmarshal(raw, &series); err != nil {
		return nil, errorf("returned a matrix that could not be read: %v", err)
	}
	switch {
	case len(series) == 0:
		return nil, nil
	case len(series) > 1:
		return nil, errorf("the query returned %s series (%s); a metric query must return exactly one (aggregate with sum() or avg())",
			map[bool]string{true: "more than one", false: strconv.Itoa(len(series))}[len(series) == 2], labelsText(series[:2]))
	}
	pts := make([]verify.Point, 0, len(series[0].Values))
	for _, v := range series[0].Values {
		t, ok1 := v[0].(float64)
		s, ok2 := v[1].(string)
		if !ok1 || !ok2 {
			return nil, errorf("returned a sample that is not [time, \"value\"]")
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, errorf("returned a value that is not a number: %q", s)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		pts = append(pts, verify.Point{T: int64(math.Round(t * 1000)), V: f})
	}
	return pts, nil
}

func labelsText(series []promSeries) string {
	var parts []string
	for _, s := range series {
		var kv []string
		for k, v := range s.Metric {
			kv = append(kv, fmt.Sprintf("%s=%q", k, v))
		}
		sort.Strings(kv)
		parts = append(parts, "{"+strings.Join(kv, ", ")+"}")
	}
	return strings.Join(parts, ", ")
}
