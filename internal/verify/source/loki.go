package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
)

// lokiBackend reads Loki's range query endpoint, /loki/api/v1/query_range.
// A log query returns streams, which become lines; a metric query (with
// rate, count_over_time and the like) returns a matrix, which becomes
// points. Loki's tenant is chosen with an X-Scope-OrgID header in the
// source's headers.
type lokiBackend struct{}

// lokiServerMaxLines is Loki's default max_entries_limit_per_query: a
// request for more lines than that is refused.
const lokiServerMaxLines = 5000

func (lokiBackend) query(ctx context.Context, s *src, q *Query, cred credential) (Result, error) {
	text, err := prepareText(Loki, q)
	if err != nil {
		return Result{}, err
	}
	v := url.Values{
		"query": {text},
		"start": {strconv.FormatInt(q.Start.UnixNano(), 10)},
		"end":   {strconv.FormatInt(q.End.UnixNano(), 10)},
	}
	if q.Kind == Metric {
		v.Set("step", strconv.FormatFloat(q.Step.Seconds(), 'f', -1, 64))
	} else {
		// Oldest first, so that a cut keeps the start of the window. Ask
		// for one more line than wanted, to know whether there were more,
		// unless that is more than Loki gives by default.
		limit := q.MaxLines + 1
		if limit > lokiServerMaxLines {
			limit = q.MaxLines
		}
		v.Set("limit", strconv.Itoa(limit))
		v.Set("direction", "forward")
	}
	r, err := s.send(ctx, cred, http.MethodGet, joinURL(s.cfg.URL, "/loki/api/v1/query_range")+"?"+v.Encode(), nil, "", nil)
	if err != nil {
		return Result{}, err
	}
	if r.status != http.StatusOK {
		// Loki's errors are plain text; some proxies answer in JSON.
		var a promAnswer
		if json.Unmarshal(r.body, &a) == nil && a.Error != "" {
			return Result{}, statusError(r, a.Error)
		}
		return Result{}, statusError(r, "")
	}
	var a promAnswer
	if err := decodeJSON(r, &a); err != nil {
		return Result{}, err
	}
	if a.Status != "success" {
		return Result{}, errorf("the query failed: %s", a.Error)
	}
	switch a.Data.ResultType {
	case "matrix":
		if q.Kind != Metric {
			return Result{}, errorf("the query is a metric query (it returned numbers); a logs query selects lines, such as {app=\"shop\"} |= \"error\"")
		}
		pts, err := oneSeries(a.Data.Result)
		return Result{Points: pts}, err
	case "streams":
		if q.Kind != Logs {
			return Result{}, errorf("the query returned log lines; a metric query needs a function such as sum(rate({app=\"shop\"} |= \"error\" [1m]))")
		}
		lines, more, err := lokiLines(a.Data.Result, q.MaxLines)
		if err != nil {
			return Result{}, err
		}
		if limit, _ := strconv.Atoi(v.Get("limit")); len(lines) >= limit && limit == q.MaxLines {
			more = true // the limit was reached: there may be more
		}
		return Result{Lines: lines, Truncated: more}, nil
	}
	return Result{}, errorf("returned a %q result, not streams or a matrix", a.Data.ResultType)
}

// lokiLines merges streams into lines, oldest first, at most max.
func lokiLines(raw json.RawMessage, max int) ([]string, bool, error) {
	var streams []struct {
		Values [][]any `json:"values"`
	}
	if err := json.Unmarshal(raw, &streams); err != nil {
		return nil, false, errorf("returned streams that could not be read: %v", err)
	}
	type entry struct {
		t    int64
		line string
	}
	var all []entry
	for _, st := range streams {
		for _, v := range st.Values {
			if len(v) < 2 {
				return nil, false, errorf("returned a log entry that is not [time, line]")
			}
			ts, ok1 := v[0].(string)
			line, ok2 := v[1].(string)
			t, err := strconv.ParseInt(ts, 10, 64)
			if !ok1 || !ok2 || err != nil {
				return nil, false, errorf("returned a log entry that is not [\"nanoseconds\", \"line\"]")
			}
			all = append(all, entry{t, line})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].t < all[j].t })
	more := len(all) > max
	if more {
		all = all[:max]
	}
	lines := make([]string, len(all))
	for i, e := range all {
		lines[i] = e.line
	}
	return lines, more, nil
}
