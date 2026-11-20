package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/verify"
)

// esBackend searches Elasticsearch or OpenSearch with the _search API.
//
// The pipeline's query is query DSL: one query clause, such as
// {"match": {"level": "error"}} or {"bool": {...}}. It is parsed as JSON and
// walked: clauses that run scripts or reach other indices are refused, and
// {{name}} placeholders are replaced inside string values only, after
// parsing, so a value can never change the query's structure. Launchway
// wraps the clause with the window's time filter, the size, sort, fields
// and timeout, so a pipeline cannot ask for aggregations or other fields.
//
// For a logs query the hits' message field becomes lines, oldest first.
// For a metric query the value is the number of matching documents per
// minute over the window, as one point at the window's end.
type esBackend struct {
	indices      []string
	timeField    string
	messageField string
}

func newElastic(c *Config) (*esBackend, error) {
	if len(c.Indices) == 0 {
		return nil, fmt.Errorf("indices is empty; list the index patterns pipelines may search, such as logs-shop-*")
	}
	for _, p := range c.Indices {
		if _, err := path.Match(p, ""); err != nil || p == "" || strings.ContainsAny(p, ",:") {
			return nil, fmt.Errorf("index pattern %q is not a valid pattern of one index", p)
		}
	}
	if c.TimeField == "" {
		c.TimeField = "@timestamp"
	}
	if c.MessageField == "" {
		c.MessageField = "message"
	}
	return &esBackend{indices: c.Indices, timeField: c.TimeField, messageField: c.MessageField}, nil
}

var indexRE = regexp.MustCompile(`^[a-z0-9*][a-z0-9._*+-]*$`)

// checkIndex checks an index a pipeline names against the allow-list.
func (b *esBackend) checkIndex(index string) error {
	switch {
	case index == "":
		return errorf("the query names no index; this source allows %s", strings.Join(b.indices, ", "))
	case len(index) > 255 || !indexRE.MatchString(index):
		return errorf("index %q is not one index name or pattern (lower-case letters, digits, '.', '_', '-', '+' and '*'; no commas, no exclusions, nothing starting with '_' or '-')", index)
	}
	if !matchAny(b.indices, index) {
		return errorf("index %q is not allowed by this source (it allows %s)", index, strings.Join(b.indices, ", "))
	}
	return nil
}

// Clause names that run scripts, read other indices, or hide a query
// from the walk. They are refused anywhere in a query.
var esRefused = map[string]string{
	"script":                      "runs a script",
	"script_score":                "runs a script",
	"script_fields":               "runs a script",
	"scripted_metric":             "runs a script",
	"function_score":              "can run scripts",
	"runtime_mappings":            "defines runtime fields, which run scripts",
	"minimum_should_match_script": "runs a script",
	"wrapper":                     "hides a query from the check",
	"percolate":                   "reads documents from other indices",
	"indexed_shape":               "reads documents from other indices",
	"terms_set":                   "can run scripts",
	"has_child":                   "is a joining query",
	"has_parent":                  "is a joining query",
	"parent_id":                   "is a joining query",
	"more_like_this":              "can read documents from other indices",
	"_index":                      "names another index",
	"aggs":                        "is not a query; Launchway counts matching documents itself",
	"aggregations":                "is not a query; Launchway counts matching documents itself",
}

// Clauses whose string values are parsed as query syntax or patterns, so
// a value could match more than itself.
var esSyntax = map[string]bool{
	"query_string": true, "simple_query_string": true, "wildcard": true, "regexp": true,
	"fuzzy": true, "prefix": true, "match_bool_prefix": true, "intervals": true,
}

// rewriteDSL parses a query clause, checks it and replaces the placeholders
// in its string values. With values nil it only checks.
func rewriteDSL(text string, values map[string]string, declared map[string]bool) (json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		if placeholderRE.MatchString(text) {
			return nil, fmt.Errorf("the query is not JSON (%v); placeholders must be inside JSON strings, such as {\"term\": {\"service\": \"{{service}}\"}}", err)
		}
		return nil, fmt.Errorf("the query is not JSON: %v", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("the query has more than one JSON value")
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the query must be a JSON object with one query clause, such as {\"match\": {\"level\": \"error\"}}")
	}
	if q, ok := obj["query"]; ok && len(obj) == 1 {
		if obj, ok = q.(map[string]any); !ok {
			return nil, fmt.Errorf("query must be an object")
		}
	}
	if len(obj) != 1 {
		var keys []string
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("the query must be one query clause, such as {\"bool\": {...}}; it has %s (Launchway sets size, sort, the time range and the rest)", strings.Join(keys, ", "))
	}
	out, err := walkDSL(obj, "", values, declared, "")
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	return b, err
}

func walkDSL(v any, at string, values map[string]string, declared map[string]bool, syntax string) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if why, bad := esRefused[k]; bad {
				return nil, fmt.Errorf("%s is not allowed in a health query: it %s", where(at, k), why)
			}
			if k == "index" {
				// A terms lookup, {"terms": {"f": {"index": ..., "id": ...}}},
				// reads another index.
				if _, ok := x["id"]; ok {
					return nil, fmt.Errorf("%s is a terms lookup, which reads another index; it is not allowed", where(at, k))
				}
			}
			if m := placeholderRE.FindStringSubmatch(k); m != nil {
				return nil, fmt.Errorf("{{%s}} is in a key at %s; placeholders may only be in string values", m[1], where(at, k))
			}
			s := syntax
			if esSyntax[k] {
				s = k
			}
			nv, err := walkDSL(val, where(at, k), values, declared, s)
			if err != nil {
				return nil, err
			}
			out[k] = nv
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			nv, err := walkDSL(e, fmt.Sprintf("%s[%d]", at, i), values, declared, syntax)
			if err != nil {
				return nil, err
			}
			out[i] = nv
		}
		return out, nil
	case string:
		m := placeholderRE.FindStringSubmatch(x)
		if m == nil {
			return x, nil
		}
		if syntax != "" {
			return nil, fmt.Errorf("{{%s}} is in a %s clause at %s, which reads its value as a pattern or query syntax; use term, terms or match for a parameter", m[1], syntax, at)
		}
		return substitute(x, values, declared, func(v string) (string, error) { return v, nil })
	}
	return v, nil
}

func where(at, k string) string {
	if at == "" {
		return k
	}
	return at + "." + k
}

func (b *esBackend) query(ctx context.Context, s *src, q *Query, cred credential) (Result, error) {
	if err := b.checkIndex(q.Index); err != nil {
		return Result{}, err
	}
	declared, names := declaredFor(q)
	if err := CheckQuery(Elasticsearch, q.Text, names); err != nil {
		return Result{}, errorf("the query is not allowed: %v", err)
	}
	clause, err := rewriteDSL(q.Text, q.Params, declared)
	if err != nil {
		return Result{}, errorf("the query is not allowed: %v", err)
	}
	body := map[string]any{
		"query": map[string]any{"bool": map[string]any{
			"must": []any{clause},
			"filter": []any{map[string]any{"range": map[string]any{b.timeField: map[string]any{
				"gte": q.Start.UnixMilli(), "lt": q.End.UnixMilli(), "format": "epoch_millis"}}}},
		}},
		"timeout":          fmt.Sprintf("%dms", s.timeout.Milliseconds()),
		"track_total_hits": true,
	}
	if q.Kind == Logs {
		body["size"] = q.MaxLines
		body["sort"] = []any{map[string]any{b.timeField: "asc"}}
		body["_source"] = []string{b.messageField}
	} else {
		body["size"] = 0
	}
	data, _ := json.Marshal(body)
	u := joinURL(s.cfg.URL, "/"+url.PathEscape(q.Index)+"/_search") + "?allow_partial_search_results=false&ignore_unavailable=false"
	r, err := s.send(ctx, cred, http.MethodPost, u, data, "application/json", nil)
	if err != nil {
		return Result{}, err
	}
	var a struct {
		TimedOut bool `json:"timed_out"`
		Hits     struct {
			Total struct {
				Value    json.Number `json:"value"`
				Relation string      `json:"relation"`
			} `json:"total"`
			Hits []struct {
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
		Error json.RawMessage `json:"error"`
	}
	if err := decodeJSON(r, &a); err != nil {
		if r.status != http.StatusOK {
			return Result{}, statusError(r, "")
		}
		return Result{}, err
	}
	if r.status != http.StatusOK {
		return Result{}, statusError(r, esReason(a.Error))
	}
	if a.TimedOut {
		return Result{}, unavailablef("the search timed out after %s", s.timeout)
	}
	total, _ := strconv.ParseInt(a.Hits.Total.Value.String(), 10, 64)
	if q.Kind == Metric {
		minutes := q.End.Sub(q.Start).Minutes()
		return Result{Points: []verify.Point{{T: q.End.UnixMilli(), V: float64(total) / minutes}}}, nil
	}
	lines := make([]string, 0, len(a.Hits.Hits))
	for _, h := range a.Hits.Hits {
		if m, ok := fieldOf(h.Source, b.messageField); ok {
			lines = append(lines, m)
		}
	}
	return Result{Lines: lines, Truncated: total > int64(len(a.Hits.Hits))}, nil
}

// fieldOf finds a dotted field in a document's source, written either as
// nested objects or with dots in the name.
func fieldOf(doc map[string]any, field string) (string, bool) {
	if v, ok := doc[field]; ok {
		return fieldText(v)
	}
	head, rest, ok := strings.Cut(field, ".")
	for ok {
		if sub, isMap := doc[head].(map[string]any); isMap {
			if s, found := fieldOf(sub, rest); found {
				return s, true
			}
		}
		var more string
		more, rest, ok = strings.Cut(rest, ".")
		head += "." + more
	}
	return "", false
}

func fieldText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case nil:
		return "", false
	case []any:
		if len(x) > 0 {
			return fieldText(x[0])
		}
		return "", false
	}
	b, _ := json.Marshal(v)
	return string(b), true
}

// esReason finds the reason in an error answer.
func esReason(raw json.RawMessage) string {
	var e struct {
		Type      string `json:"type"`
		Reason    string `json:"reason"`
		RootCause []struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"root_cause"`
	}
	if json.Unmarshal(raw, &e) != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return snippet(bytes.TrimSpace(raw))
	}
	if len(e.RootCause) > 0 && e.RootCause[0].Reason != "" {
		return e.RootCause[0].Type + ": " + e.RootCause[0].Reason
	}
	if e.Reason != "" {
		return e.Type + ": " + e.Reason
	}
	return ""
}
