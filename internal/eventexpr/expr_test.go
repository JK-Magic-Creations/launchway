package eventexpr

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func body(t *testing.T, s string) map[string]any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"body": v}
}

func TestExpressionsOverABody(t *testing.T) {
	vars := body(t, `{"action":"released","release":{"tag":"v1.4.0","draft":false,"assets":[{"name":"a.zip"},{"name":"b.zip"}],"n":3},"env":"prod","labels":["ship","docs"]}`)
	for expr, want := range map[string]any{
		`body.action == "released"`:                                true,
		`body.release.tag startsWith "v1." && !body.release.draft`: true,
		`body.release.tag glob "v2.*"`:                             false,
		`body.env in ["prod", "stage"]`:                            true,
		`body.labels contains "ship"`:                              true,
		`body.release.n >= 3 && body.release.n < 10`:               true,
		`body.release.assets[1].name`:                              "b.zip",
		`body.nothing.here == null`:                                true,
		`body.nothing.here == "x"`:                                 false,
		`default(body.missing, "dev")`:                             "dev",
		`upper(body.env)`:                                          "PROD",
		`len(body.release.assets)`:                                 float64(2),
		`join(body.labels, ",")`:                                   "ship,docs",
		`body.release.n == "3"`:                                    true,
		`(body.env == "prod" || body.env == "stage") && body.action != "deleted"`: true,
	} {
		e, err := Parse(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		got, err := e.Eval(vars)
		if err != nil || got != want {
			t.Errorf("%s = %#v, %v; want %#v", expr, got, err, want)
		}
	}
}

func TestFiltersMustAnswerTrueOrFalse(t *testing.T) {
	vars := body(t, `{"a":"x"}`)
	e, _ := Parse(`body.a`)
	if _, err := e.Bool(vars); err == nil || !strings.Contains(err.Error(), "not true or false") {
		t.Fatalf("%v", err)
	}
	e, _ = Parse(`body.a == "x"`)
	if ok, err := e.Bool(vars); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	e, _ = Parse(`body.a && true`)
	if _, err := e.Bool(vars); err == nil {
		t.Fatal("text was accepted as a condition")
	}
	e, _ = Parse(`nosuch.a == 1`)
	if _, err := e.Bool(vars); err == nil || !strings.Contains(err.Error(), "there is body") {
		t.Fatalf("%v", err)
	}
}

func TestTextOfValues(t *testing.T) {
	vars := body(t, `{"n":3,"f":1.5,"b":true,"l":[1,"a"],"s":"hi"}`)
	for expr, want := range map[string]string{`body.n`: "3", `body.f`: "1.5", `body.b`: "true", `body.l`: `[1,"a"]`, `body.s`: "hi", `body.none`: "", `"lit"`: "lit"} {
		e, err := Parse(expr)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := e.Text(vars); err != nil || got != want {
			t.Errorf("%s = %q %v, want %q", expr, got, err, want)
		}
	}
}

func TestSyntaxErrorsSayWhere(t *testing.T) {
	for expr, want := range map[string]string{
		``:                               "empty",
		`body.a ==`:                      "ends too soon",
		`body.a == "x`:                   "not closed",
		`foo(1)`:                         "no function foo",
		`(body.a`:                        "expected )",
		`body.a @ 1`:                     "unexpected",
		`body.`:                          "field name",
		strings.Repeat("a", MaxLength+1): "longer than",
		strings.Repeat("(", 40) + "1" + strings.Repeat(")", 40): "too deeply",
	} {
		_, err := Parse(expr)
		if err == nil || !errors.Is(err, ErrSyntax) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want a syntax error with %q", expr, err, want)
		}
	}
}

func TestSchemaChecksBodiesAndNeverFetches(t *testing.T) {
	s, err := CompileSchema(`{"type":"object","required":["version"],"properties":{"version":{"type":"string","pattern":"^v[0-9]+$"},"n":{"type":"integer","minimum":1}},"additionalProperties":false}`)
	if err != nil || s == nil {
		t.Fatal(err)
	}
	if p := s.Check([]byte(`{"version":"v3","n":2}`)); p != nil {
		t.Fatalf("%v", p)
	}
	p := s.Check([]byte(`{"version":"3","n":0,"extra":1}`))
	if len(p) < 3 {
		t.Fatalf("%v", p)
	}
	if p := s.Check([]byte(`not json`)); len(p) != 1 || !strings.Contains(p[0], "not JSON") {
		t.Fatalf("%v", p)
	}
	if _, err := CompileSchema(`{"$ref":"https://example.test/s.json"}`); err == nil || !strings.Contains(err.Error(), "may not refer") {
		t.Fatalf("%v", err)
	}
	if s, err := CompileSchema("  "); s != nil || err != nil {
		t.Fatal("an empty text is no schema")
	}
	if _, err := CompileSchema(`{"type":`); err == nil {
		t.Fatal("broken JSON accepted")
	}
	var nilSchema *Schema
	if nilSchema.Check([]byte("{}")) != nil {
		t.Fatal("no schema checks nothing")
	}
}
