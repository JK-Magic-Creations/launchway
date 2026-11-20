package source

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/verify"
)

// httpBackend runs operator statements against any HTTP service that
// answers with JSON. For a GET the pipeline's parameters, and start and
// end (Unix seconds), go in the URL's query string, encoded; for a POST
// they also replace {{name}} placeholders inside the body's JSON string
// values, after the body is parsed, so a value cannot change its
// structure. Value, a dotted path into the answer, picks out the numbers or
// lines.
type httpBackend struct {
	stmts map[string]httpStatement
}

type httpStatement struct {
	Statement
	body any // the parsed body template, for a POST
}

func newHTTPJSON(c Config) (*httpBackend, error) {
	b := &httpBackend{stmts: map[string]httpStatement{}}
	for name, st := range c.Statements {
		if st.SQL != "" {
			return nil, fmt.Errorf("statement %s: sql is for clickhouse and postgres sources", name)
		}
		st.Method = strings.ToUpper(st.Method)
		if st.Method == "" {
			st.Method = http.MethodGet
		}
		if st.Method != http.MethodGet && st.Method != http.MethodPost {
			return nil, fmt.Errorf("statement %s: method must be GET or POST", name)
		}
		u, err := url.Parse(st.Path)
		if err != nil || !strings.HasPrefix(st.Path, "/") || strings.HasPrefix(st.Path, "//") || u.Host != "" ||
			strings.Contains(st.Path, "..") || u.Fragment != "" || strings.Contains(st.Path, "{{") {
			return nil, fmt.Errorf("statement %s: path must be a path on the source, starting with /, without .. or placeholders", name)
		}
		if st.Value == "" {
			return nil, fmt.Errorf("statement %s: value is missing: the dotted path to the numbers or lines in the answer, such as data.result", name)
		}
		for p, typ := range st.Params {
			if !httpParamType[typ] {
				return nil, fmt.Errorf("statement %s: parameter %s has type %q; http parameters are string", name, p, typ)
			}
		}
		hs := httpStatement{Statement: st}
		if st.Body != "" {
			if st.Method != http.MethodPost {
				return nil, fmt.Errorf("statement %s: a body needs method POST", name)
			}
			dec := json.NewDecoder(strings.NewReader(st.Body))
			dec.UseNumber()
			if err := dec.Decode(&hs.body); err != nil {
				return nil, fmt.Errorf("statement %s: body is not JSON (placeholders go inside JSON strings): %v", name, err)
			}
			decl := map[string]bool{"start": true, "end": true}
			for p := range st.Params {
				decl[p] = true
			}
			if _, err := fillJSON(hs.body, nil, decl); err != nil {
				return nil, fmt.Errorf("statement %s: body: %v", name, err)
			}
		}
		b.stmts[name] = hs
	}
	return b, nil
}

// fillJSON replaces placeholders in the string values of a parsed JSON
// document. With values nil it only checks them.
func fillJSON(v any, values map[string]string, declared map[string]bool) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if placeholderRE.MatchString(k) {
				return nil, fmt.Errorf("a placeholder is in the key %q; placeholders may only be in string values", k)
			}
			n, err := fillJSON(e, values, declared)
			if err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			n, err := fillJSON(e, values, declared)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	case string:
		return substitute(x, values, declared, func(v string) (string, error) { return v, nil })
	}
	return v, nil
}

func (b *httpBackend) query(ctx context.Context, s *src, q *Query, cred credential) (Result, error) {
	st := b.stmts[q.Statement]
	vals := map[string]string{"start": strconv.FormatInt(q.Start.Unix(), 10), "end": strconv.FormatInt(q.End.Unix(), 10)}
	qs := url.Values{"start": {vals["start"]}, "end": {vals["end"]}}
	for p := range st.Params {
		vals[p] = q.Params[p]
		qs.Set(p, q.Params[p])
	}
	base, _ := url.Parse(joinURL(s.cfg.URL, st.Path))
	merged := base.Query()
	for k, v := range qs {
		merged[k] = v
	}
	base.RawQuery = merged.Encode()
	var body []byte
	ct := ""
	if st.Method == http.MethodPost {
		ct = "application/json"
		body = []byte("{}")
		if st.body != nil {
			filled, err := fillJSON(st.body, vals, nil)
			if err != nil {
				return Result{}, errorf("%v", err)
			}
			body, _ = json.Marshal(filled)
		}
	}
	r, err := s.send(ctx, cred, st.Method, base.String(), body, ct, nil)
	if err != nil {
		return Result{}, err
	}
	if r.status < 200 || r.status > 299 {
		return Result{}, statusError(r, "")
	}
	if ct := r.header.Get("Content-Type"); !strings.Contains(strings.ToLower(ct), "json") {
		if ct == "" {
			ct = "no content type"
		}
		return Result{}, errorf("returned something that is not JSON (%s): %q", ct, snippet(r.body))
	}
	var doc any
	if err := decodeJSON(r, &doc); err != nil {
		return Result{}, err
	}
	v, err := jsonPath(doc, st.Value)
	if err != nil {
		return Result{}, err
	}
	if q.Kind == Logs {
		arr, ok := v.([]any)
		if !ok {
			return Result{}, errorf("%s in the answer is not an array of lines", st.Value)
		}
		var lines []string
		for _, e := range arr {
			switch x := e.(type) {
			case string:
				lines = append(lines, x)
			case map[string]any:
				if m, ok := x["message"].(string); ok {
					lines = append(lines, m)
				}
			}
		}
		return Result{Lines: lines}, nil
	}
	pts, err := jsonPoints(v, q.Start, q.End)
	return Result{Points: pts}, err
}

// jsonPath follows a dotted path; a number indexes an array.
func jsonPath(doc any, p string) (any, error) {
	cur := doc
	for _, part := range strings.Split(p, ".") {
		switch x := cur.(type) {
		case map[string]any:
			v, ok := x[part]
			if !ok {
				return nil, errorf("the answer has no %s (value: %s)", part, p)
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return nil, errorf("the answer has no element %s at %s", part, p)
			}
			cur = x[i]
		default:
			return nil, errorf("the answer has no %s (value: %s)", part, p)
		}
	}
	return cur, nil
}

// jsonPoints reads a number (one point at end), an array of numbers
// (spread evenly over the window, the last at end) or an array of
// [time, value] pairs (time in Unix seconds or milliseconds).
func jsonPoints(v any, start, end time.Time) ([]verify.Point, error) {
	num := func(x any) (float64, bool, error) {
		switch n := x.(type) {
		case json.Number:
			f, err := n.Float64()
			return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0), err
		case string:
			f, err := strconv.ParseFloat(n, 64)
			if err != nil {
				return 0, false, fmt.Errorf("%q is not a number", n)
			}
			return f, !math.IsNaN(f) && !math.IsInf(f, 0), nil
		case nil:
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("%v is not a number", x)
	}
	if arr, ok := v.([]any); ok {
		var pts []verify.Point
		span := end.Sub(start)
		for i, e := range arr {
			if pair, ok := e.([]any); ok {
				if len(pair) != 2 {
					return nil, errorf("a [time, value] pair has %d elements", len(pair))
				}
				t, ok1, err1 := num(pair[0])
				f, ok2, err2 := num(pair[1])
				if err1 != nil || err2 != nil {
					return nil, errorf("a [time, value] pair is not two numbers")
				}
				if ok1 && ok2 {
					pts = append(pts, verify.Point{T: unixToMilli(t), V: f})
				}
				continue
			}
			f, ok, err := num(e)
			if err != nil {
				return nil, errorf("the answer: %v", err)
			}
			if ok {
				t := start.Add(span * time.Duration(i+1) / time.Duration(len(arr)))
				pts = append(pts, verify.Point{T: t.UnixMilli(), V: f})
			}
		}
		return pts, nil
	}
	f, ok, err := num(v)
	if err != nil {
		return nil, errorf("the answer: %v", err)
	}
	if !ok {
		return nil, nil
	}
	return []verify.Point{{T: end.UnixMilli(), V: f}}, nil
}
