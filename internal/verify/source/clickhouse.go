package source

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/verify"
)

// chBackend runs operator statements on ClickHouse over its HTTP interface.
//
// Values are sent as ClickHouse query parameters (param_<name> in the URL,
// {name:Type} in the SQL), which the server parses as the declared type:
// they never become SQL text. Every query is sent with readonly=1, which
// refuses writes, DDL, temporary tables, table functions that write and
// changing settings from the SQL; the limits the runner sets in the URL
// (max_execution_time, max_result_rows, max_result_bytes) still apply, as
// the tests check against a real server. readonly=2 would let a query
// write files through INSERT INTO FUNCTION file(), so it is not used.
// Operators should still give the source a ClickHouse user whose profile
// is read-only.
type chBackend struct {
	stmts map[string]Statement
}

var chVersionRE = regexp.MustCompile(`\s*\(version [^()]*(\([^()]*\))?\)\s*$`)

func newClickHouse(c Config) (*chBackend, error) {
	for name, st := range c.Statements {
		if st.Path != "" || st.Method != "" || st.Body != "" || st.Value != "" {
			return nil, fmt.Errorf("statement %s: path, method, body and value are for http sources", name)
		}
		if strings.TrimSpace(st.SQL) == "" {
			return nil, fmt.Errorf("statement %s: sql is missing", name)
		}
		t, err := lexSQL(ClickHouse, st.SQL)
		if err != nil {
			return nil, fmt.Errorf("statement %s: %v", name, err)
		}
		if err := t.checkSelect(); err != nil {
			return nil, fmt.Errorf("statement %s: %v", name, err)
		}
		for p, typ := range st.Params {
			if !chTypeRE.MatchString(typ) {
				return nil, fmt.Errorf("statement %s: parameter %s has type %q, which is not a ClickHouse type such as String, UInt64 or DateTime64(3)", name, p, typ)
			}
		}
		used := map[string]bool{}
		for _, m := range chParamRE.FindAllStringSubmatch(t.code, -1) {
			p, typ := m[1], m[2]
			used[p] = true
			if p == "start" || p == "end" {
				continue
			}
			decl, ok := st.Params[p]
			if !ok {
				return nil, fmt.Errorf("statement %s: the SQL uses {%s:%s}, but params does not declare %s", name, p, typ, p)
			}
			if !strings.EqualFold(strings.ReplaceAll(decl, " ", ""), strings.ReplaceAll(typ, " ", "")) {
				return nil, fmt.Errorf("statement %s: the SQL reads %s as %s, but params declares it %s", name, p, typ, decl)
			}
		}
		for p := range st.Params {
			if !used[p] {
				return nil, fmt.Errorf("statement %s: parameter %s is declared but the SQL does not use it as {%s:%s}", name, p, p, st.Params[p])
			}
		}
		for _, b := range t.strings {
			if placeholderRE.MatchString(b) || chParamRE.MatchString(b) {
				return nil, fmt.Errorf("statement %s: a parameter is inside a quoted string, where ClickHouse does not replace it; write it bare, such as WHERE service = {service:String}", name)
			}
		}
		if placeholderRE.MatchString(t.code) {
			return nil, fmt.Errorf("statement %s: ClickHouse statements use ClickHouse parameters such as {service:String}, not {{service}}", name)
		}
	}
	return &chBackend{stmts: c.Statements}, nil
}

func (b *chBackend) query(ctx context.Context, s *src, q *Query, cred credential) (Result, error) {
	st := b.stmts[q.Statement]
	limit := q.MaxLines
	if q.Kind == Metric {
		limit = s.limits.MaxPoints
	}
	v := url.Values{
		"readonly":             {"1"},
		"max_execution_time":   {strconv.FormatFloat(s.timeout.Seconds(), 'f', -1, 64)},
		"max_result_rows":      {strconv.Itoa(limit + 1)},
		"max_result_bytes":     {strconv.Itoa(s.limits.MaxBytes)},
		"result_overflow_mode": {"break"},
		// One JSON array per row, after a row of column names; times as
		// Unix seconds, whatever the server's time zone.
		"default_format":                          {"JSONCompactEachRowWithNames"},
		"date_time_output_format":                 {"unix_timestamp"},
		"output_format_json_quote_64bit_integers": {"0"},
		// The window, as Unix seconds with milliseconds, which a
		// DateTime64(3) parameter reads the same in any time zone.
		"param_start": {unixSeconds(q.Start)},
		"param_end":   {unixSeconds(q.End)},
	}
	for p := range st.Params {
		v.Set("param_"+p, q.Params[p])
	}
	h := http.Header{}
	if cred.user != "" || cred.secret != "" {
		h.Set("X-ClickHouse-User", cred.user)
		h.Set("X-ClickHouse-Key", cred.secret)
	}
	r, err := s.send(ctx, cred, http.MethodPost, joinURL(s.cfg.URL, "/")+"?"+v.Encode(), []byte(st.SQL), "text/plain; charset=utf-8", h)
	if err != nil {
		return Result{}, err
	}
	if r.status != http.StatusOK {
		msg := chVersionRE.ReplaceAllString(snippet(r.body), "")
		switch r.header.Get("X-ClickHouse-Exception-Code") {
		case "159", "160": // TIMEOUT_EXCEEDED, TOO_SLOW
			return Result{}, unavailablef("the source did not finish the query within %s: %s", s.timeout, msg)
		case "164": // READONLY
			return Result{}, errorf("the server refused the statement because health queries are read-only: %s", msg)
		case "457": // BAD_QUERY_PARAMETER
			return Result{}, errorf("a parameter's value does not fit its type: %s", msg)
		}
		return Result{}, statusError(r, msg)
	}
	cols, rows, err := chRows(r.body)
	if err != nil {
		return Result{}, err
	}
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	idx := map[string]int{}
	for i, c := range cols {
		idx[c] = i
	}
	if q.Kind == Logs {
		lines, err := rowsToLines(cols, rows, func(c json.RawMessage) (string, bool) {
			var s string
			return s, json.Unmarshal(c, &s) == nil
		}, chTime)
		return Result{Lines: lines, Truncated: truncated}, err
	}
	pts, err := rowsToPoints(cols, rows, q.End, chNumber, chTime)
	return Result{Points: pts, Truncated: truncated}, err
}

// chRows reads JSONCompactEachRowWithNames: a line of column names, then a
// JSON array per row. An error the server hit after it began answering
// comes as text in the body.
func chRows(body []byte) ([]string, [][]json.RawMessage, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), len(body)+1)
	var cols []string
	var rows [][]json.RawMessage
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if cols == nil {
			if err := json.Unmarshal(line, &cols); err != nil {
				return nil, nil, chStreamError(line)
			}
			continue
		}
		var row []json.RawMessage
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, nil, chStreamError(line)
		}
		if len(row) != len(cols) {
			return nil, nil, errorf("returned a row of %d columns under %d names", len(row), len(cols))
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, nil, errorf("returned an answer that could not be read: %v", err)
	}
	if cols == nil {
		return nil, nil, errorf("returned no column names: is the statement a SELECT?")
	}
	return cols, rows, nil
}

func chStreamError(line []byte) error {
	s := string(line)
	if strings.Contains(s, "Exception") || strings.HasPrefix(s, "Code:") {
		return errorf("the query failed while the answer was being sent: %s", chVersionRE.ReplaceAllString(snippet(line), ""))
	}
	return errorf("returned something that is not JSON: %q", snippet(line))
}

// chNumber reads a value cell: a number, a number in a string (64-bit
// integers may be quoted), or null.
func chNumber(c json.RawMessage) (float64, bool, error) {
	var x any
	if err := json.Unmarshal(c, &x); err != nil {
		return 0, false, err
	}
	switch v := x.(type) {
	case nil:
		return 0, false, nil
	case float64:
		return v, true, nil
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false, fmt.Errorf("%q is not a number", v)
		}
		return f, !math.IsNaN(f) && !math.IsInf(f, 0), nil
	case bool:
		if v {
			return 1, true, nil
		}
		return 0, true, nil
	}
	return 0, false, fmt.Errorf("%s is not a number", snippet(c))
}

// chTime reads a time cell: Unix seconds (a number, or a string with a
// fraction, as date_time_output_format=unix_timestamp writes them).
func chTime(c json.RawMessage) (int64, error) {
	f, ok, err := chNumber(c)
	if err != nil || !ok {
		return 0, fmt.Errorf("the t column holds %s, not a time", snippet(c))
	}
	return unixToMilli(f), nil
}

// unixToMilli reads Unix seconds or, when the number is too large to be
// seconds, milliseconds.
func unixToMilli(f float64) int64 {
	if math.Abs(f) > 1e11 {
		return int64(math.Round(f))
	}
	return int64(math.Round(f * 1000))
}

// rowsToPoints makes points of rows with columns t and value, or of one row
// with one column (a point at end).
func rowsToPoints(cols []string, rows [][]json.RawMessage, end time.Time,
	num func(json.RawMessage) (float64, bool, error), tm func(json.RawMessage) (int64, error)) ([]verify.Point, error) {
	ti, vi := -1, -1
	for i, c := range cols {
		switch strings.ToLower(c) {
		case "t":
			ti = i
		case "value":
			vi = i
		}
	}
	if vi < 0 && len(cols) == 1 {
		vi = 0
	}
	if vi < 0 {
		return nil, errorf("a metric statement must return columns t and value, or a single value column; it returned %s", strings.Join(cols, ", "))
	}
	if ti < 0 && len(rows) > 1 {
		return nil, errorf("a metric statement without a t column must return one row; it returned %d", len(rows))
	}
	var pts []verify.Point
	for _, row := range rows {
		f, ok, err := num(row[vi])
		if err != nil {
			return nil, errorf("the value column: %v", err)
		}
		if !ok {
			continue
		}
		t := end.UnixMilli()
		if ti >= 0 {
			if t, err = tm(row[ti]); err != nil {
				return nil, errorf("%v", err)
			}
		}
		pts = append(pts, verify.Point{T: t, V: f})
	}
	return pts, nil
}

// rowsToLines makes log lines of rows with a message column, in the order
// of a t column when there is one.
func rowsToLines(cols []string, rows [][]json.RawMessage, str func(json.RawMessage) (string, bool), tm func(json.RawMessage) (int64, error)) ([]string, error) {
	mi, ti := -1, -1
	for i, c := range cols {
		switch strings.ToLower(c) {
		case "message":
			mi = i
		case "t":
			ti = i
		}
	}
	if mi < 0 {
		return nil, errorf("a logs statement must return a message column; it returned %s", strings.Join(cols, ", "))
	}
	type entry struct {
		t    int64
		line string
	}
	all := make([]entry, 0, len(rows))
	for _, row := range rows {
		line, ok := str(row[mi])
		if !ok {
			continue // a null message
		}
		var t int64
		if ti >= 0 {
			var err error
			if t, err = tm(row[ti]); err != nil {
				return nil, errorf("%v", err)
			}
		}
		all = append(all, entry{t, line})
	}
	if ti >= 0 {
		sort.SliceStable(all, func(i, j int) bool { return all[i].t < all[j].t })
	}
	lines := make([]string, len(all))
	for i, e := range all {
		lines[i] = e.line
	}
	return lines, nil
}
