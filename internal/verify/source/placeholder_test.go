package source

import (
	"strings"
	"testing"
)

func TestRewritePromQL(t *testing.T) {
	cases := []struct {
		name, text string
		params     map[string]string
		want       string
	}{
		{"label value", `sum(rate(http_requests_total{namespace="{{ns}}"}[1m]))`, map[string]string{"ns": "shop"},
			`sum(rate(http_requests_total{namespace="shop"}[1m]))`},
		{"inside a longer value", `up{pod=~"web-{{hash}}-.*"}`, map[string]string{"hash": "abc"},
			`up{pod=~"web-abc-.*"}`},
		{"regexp value is quoted", `up{pod=~"{{p}}"}`, map[string]string{"p": ".*"},
			`up{pod=~"\\.\\*"}`},
		{"negative regexp", `up{pod!~"{{p}}"}`, map[string]string{"p": "a|b"},
			`up{pod!~"a\\|b"}`},
		{"not equal", `up{pod!="{{p}}"}`, map[string]string{"p": "x"}, `up{pod!="x"}`},
		{"single quotes", `up{job='{{j}}'}`, map[string]string{"j": `it's "q"`}, `up{job='it\'s "q"'}`},
		{"breaking out of the string", `up{job="{{j}}"}`, map[string]string{"j": `"} or vector(1) #`},
			`up{job="\"} or vector(1) #"}`},
		{"adding a matcher", `up{job="{{j}}"}`, map[string]string{"j": `x", job=~".*`},
			`up{job="x\", job=~\".*"}`},
		{"newline and backslash", `up{job="{{j}}"}`, map[string]string{"j": "a\nb\\c\td\x01"},
			`up{job="a\nb\\c\td\u0001"}`},
		{"comment ignored", "up{job=\"{{j}}\"} # {{other}} \"\n", map[string]string{"j": "a"},
			"up{job=\"a\"} # {{other}} \"\n"},
		{"repeated", `up{a="{{x}}",b="{{x}}{{y}}"}`, map[string]string{"x": "1", "y": "2"}, `up{a="1",b="12"}`},
		{"other braces left alone", `up{a="{{ x }}"}`, nil, `up{a="{{ x }}"}`},
		{"quoted label name", `{"http.status"="{{c}}"}`, map[string]string{"c": "500"}, `{"http.status"="500"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			decl := map[string]bool{}
			for k := range c.params {
				decl[k] = true
			}
			got, err := rewriteQL(Prometheus, c.text, c.params, decl)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestRewriteLogQL(t *testing.T) {
	cases := []struct {
		name, text string
		params     map[string]string
		want       string
	}{
		{"line filter", `{app="shop"} |= "{{id}}"`, map[string]string{"id": `a"b`}, `{app="shop"} |= "a\"b"`},
		{"negative line filter", `{app="shop"} != "{{id}}"`, map[string]string{"id": "x"}, `{app="shop"} != "x"`},
		{"regexp line filter", `{app="shop"} |~ "{{id}}"`, map[string]string{"id": ".*"}, `{app="shop"} |~ "\\.\\*"`},
		{"negative regexp line filter", `{app="shop"} !~ "err{{id}}"`, map[string]string{"id": "(x)"}, `{app="shop"} !~ "err\\(x\\)"`},
		{"or line filter", `{app="shop"} |~ "a" or "{{id}}"`, map[string]string{"id": "."}, `{app="shop"} |~ "a" or "\\."`},
		{"label filter", `{app="shop"} | json | level="{{lvl}}"`, map[string]string{"lvl": "error"}, `{app="shop"} | json | level="error"`},
		{"label filter regexp", `{app="shop"} | logfmt | level=~"{{lvl}}"`, map[string]string{"lvl": "a|b"}, `{app="shop"} | logfmt | level=~"a\\|b"`},
		{"selector", `{app="{{app}}", ns=~"{{ns}}"}`, map[string]string{"app": "x", "ns": "p.d"}, `{app="x", ns=~"p\\.d"}`},
		{"regexp stage", `{app="a"} | regexp "(?P<x>{{w}})"`, map[string]string{"w": "a+"}, `{app="a"} | regexp "(?P<x>a\\+)"`},
		{"template left alone", `{app="{{a}}"} | line_format "{{.msg}}"`, map[string]string{"a": "x"}, `{app="x"} | line_format "{{.msg}}"`},
		{"metric", `sum(count_over_time({app="{{a}}"} |= "error" [1m]))`, map[string]string{"a": "x"}, `sum(count_over_time({app="x"} |= "error" [1m]))`},
		{"newline", `{app="a"} |= "{{v}}"`, map[string]string{"v": "a\nb"}, `{app="a"} |= "a\nb"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			decl := map[string]bool{}
			for k := range c.params {
				decl[k] = true
			}
			got, err := rewriteQL(Loki, c.text, c.params, decl)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestCheckQueryRefuses(t *testing.T) {
	cases := []struct {
		typ, text string
		declared  []string
		want      string
	}{
		{Prometheus, `up{namespace={{ns}}}`, []string{"ns"}, `{{ns}} at line 1 column 14 must be inside quotes, as a label value: namespace="{{ns}}"`},
		{Prometheus, `up + {{n}}`, []string{"n"}, "must be inside quotes"},
		{Prometheus, `label_replace(up, "dst", "{{v}}", "src", ".*")`, []string{"v"}, "function argument"},
		{Prometheus, "up{a=`{{v}}`}", []string{"v"}, "raw string"},
		{Prometheus, `up{a="{{v}}"}`, nil, "{{v}} is not a declared parameter"},
		{Prometheus, `up{a="{{v}}`, []string{"v"}, "not closed"},
		{Prometheus, "up{a=\"{{v}}\n\"}", []string{"v"}, "past the end of the line"},
		{Prometheus, `up{"{{v}}"="x"}`, []string{"v"}, "label matcher's value"},
		{Prometheus, "", nil, "empty"},
		{Prometheus, strings.Repeat("u", MaxQueryBytes+1), nil, "at most 8192"},
		{Prometheus, `up{a="{{v}}"}`, []string{"Bad"}, "parameter name"},
		{Loki, `{app="a"} | line_format "{{v}}"`, []string{"v"}, "line_format template"},
		{Loki, `{app="a"} | label_format x="{{v}}"`, []string{"v"}, "label_format template"},
		{Loki, `{app="a"} | pattern "<_> {{v}}"`, []string{"v"}, "pattern expression"},
		{Loki, `{app="a"} |> "{{v}}"`, []string{"v"}, "pattern line filter"},
		{Loki, `{app="a"} | json x="{{v}}"`, []string{"v"}, "json parser"},
		{Loki, `{app='{{v}}'}`, []string{"v"}, "single quotes"},
		{Loki, `{app="a"} |= ip("{{v}}")`, []string{"v"}, "function argument"},
		{Loki, "{app=\"a\"} |= `{{v}}`", []string{"v"}, "raw string"},
		{Elasticsearch, `{"term": {"a": {{v}}}}`, []string{"v"}, "inside JSON strings"},
		{ClickHouse, `SELECT 1`, nil, "statement's name"},
		{"mysql", `x`, nil, "unknown source type"},
	}
	for _, c := range cases {
		err := CheckQuery(c.typ, c.text, c.declared)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %q: got %v, want an error containing %q", c.typ, c.text, err, c.want)
		}
	}
}

func TestCheckQueryAccepts(t *testing.T) {
	for _, c := range []struct{ typ, text string }{
		{Prometheus, `sum(rate(http_requests_total{code=~"5..",pod_template_hash="{{hash}}"}[1m])) / sum(rate(http_requests_total{pod_template_hash="{{hash}}"}[1m]))`},
		{Prometheus, "up # a comment with {{unknown}} and \" quote\n"},
		{Loki, `{namespace="{{ns}}"} |= "error" | json | line_format "{{.message}}"`},
		{Loki, "{app=\"a\"} |~ `(?i)error`"},
		{Elasticsearch, `{"bool": {"filter": [{"term": {"kubernetes.labels.pod-template-hash": "{{hash}}"}}]}}`},
		{Postgres, "errors_per_minute"},
	} {
		if err := CheckQuery(c.typ, c.text, []string{"hash", "ns"}); err != nil {
			t.Errorf("%s %q: %v", c.typ, c.text, err)
		}
	}
}

func TestEscapeQL(t *testing.T) {
	for in, want := range map[string]string{
		`plain`: `plain`, `"`: `\"`, `'`: `'`, `\`: `\\`, string(rune(0x2028)): `\` + "u2028", "\U000e0001": `\U000e0001`, "é": "é",
	} {
		got, err := escapeQL(in, '"')
		if err != nil || got != want {
			t.Errorf("escapeQL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := escapeQL("\xff", '"'); err == nil {
		t.Error("invalid UTF-8 was accepted")
	}
}
