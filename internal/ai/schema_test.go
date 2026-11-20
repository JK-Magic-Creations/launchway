package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckSchema(t *testing.T) {
	good := []string{
		`{"type":"object"}`,
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","title":"t","description":"d","additionalProperties":false,
		  "properties":{"n":{"type":"integer","minimum":0,"maximum":10},"tags":{"type":"array","items":{"type":"string","minLength":1},"maxItems":5},
		  "maybe":{"type":["string","null"]},"kind":{"enum":["a",1,true,null]},"o":{"type":"object","properties":{"x":{"type":"boolean"}}}},
		  "required":["n"]}`,
	}
	for _, s := range good {
		if err := CheckSchema(json.RawMessage(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := map[string]string{
		`[]`:                              "must be a JSON object",
		`{"type":"array"}`:                "must describe an object at the top",
		`{`:                               "not valid JSON",
		`{"type":"object","pattern":"x"}`: `keyword "pattern" is not supported`,
		`{"type":"object","properties":{"a":{"type":"string","format":"email"}}}`:            `schema $.a: keyword "format"`,
		`{"type":"object","additionalProperties":true}`:                                      "additionalProperties may only be false",
		`{"type":"object","additionalProperties":{"type":"string"}}`:                         "additionalProperties may only be false",
		`{"type":"object","required":["a"]}`:                                                 `required property "a" is not among the properties`,
		`{"type":"object","properties":{"a":{"type":"strin"}}}`:                              `type "strin" is not one of`,
		`{"type":"object","properties":{"a":{"type":"string","minimum":1}}}`:                 "minimum needs type number or integer",
		`{"type":"object","properties":{"a":{"type":"string","minLength":-1}}}`:              "minLength must be a whole number",
		`{"type":"object","properties":{"a":{"type":"string","minLength":5,"maxLength":2}}}`: "a minimum is above its maximum",
		`{"type":"object","properties":{"a":{"enum":[]}}}`:                                   "enum must be a non-empty list",
		`{"type":"object","properties":{"a":{"enum":[{"x":1}]}}}`:                            "enum values must be",
		`{"type":"object","properties":{"a":{"type":"string","items":{}}}}`:                  "items needs type array",
		`{"type":"object","properties":{"a":{"$schema":"x"}}}`:                               "$schema belongs only at the top",
	}
	for s, want := range bad {
		err := CheckSchema(json.RawMessage(s))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want %q", s, err, want)
		}
	}
	huge := `{"type":"object","description":"` + strings.Repeat("x", MaxSchemaBytes) + `"}`
	if err := CheckSchema(json.RawMessage(huge)); err == nil || !strings.Contains(err.Error(), "over the limit") {
		t.Errorf("huge schema: %v", err)
	}
	deep := `{"type":"object","properties":{"a":` + strings.Repeat(`{"type":"array","items":`, 40) + `{}` + strings.Repeat("}", 40) + `}}`
	if err := CheckSchema(json.RawMessage(deep)); err == nil || !strings.Contains(err.Error(), "nested more than") {
		t.Errorf("deep schema: %v", err)
	}
}

func TestValidate(t *testing.T) {
	s, err := parseSchema(json.RawMessage(`{"type":"object","properties":{
		"n":{"type":"integer","minimum":0,"maximum":10},
		"f":{"type":"number"},
		"s":{"type":"string","minLength":2,"maxLength":3},
		"tags":{"type":"array","items":{"type":"string","enum":["a","b"]},"minItems":1,"maxItems":2},
		"maybe":{"type":["string","null"]},
		"o":{"type":"object","properties":{"x":{"type":"boolean"}},"required":["x"]},
		"odd key":{"type":"string"}},
		"required":["n"]}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		`{"n":1}`:                        "",
		`{"n":1.0,"f":2}`:                "", // 1.0 is an integer, and an integer is a number
		`{"n":5,"s":"héé","maybe":null}`: "", // characters, not bytes
		`{"n":1,"tags":["a"],"o":{"x":true},"odd key":"v"}`: "",
		`{}`:                           `$: missing required property "n"`,
		`{"n":1.5}`:                    "$.n: must be an integer, not a number",
		`{"n":"1"}`:                    "$.n: must be an integer, not a string",
		`{"n":11}`:                     "$.n: must be at most 10, not 11",
		`{"n":-1}`:                     "$.n: must be at least 0, not -1",
		`{"n":1,"s":"x"}`:              "$.s: must be at least 2 characters, not 1",
		`{"n":1,"s":"xxxx"}`:           "$.s: must be at most 3 characters, not 4",
		`{"n":1,"tags":[]}`:            "$.tags: must have at least 1 items, not 0",
		`{"n":1,"tags":["a","c"]}`:     "$.tags[1]: must be one of a, b",
		`{"n":1,"maybe":3}`:            "$.maybe: must be a string or null, not an integer",
		`{"n":1,"o":{}}`:               `$.o: missing required property "x"`,
		`{"n":1,"o":{"x":true,"y":1}}`: `$.o: unexpected property "y"`,
		`{"n":1,"odd key":2}`:          `$["odd key"]: must be a string, not an integer`,
	}
	for doc, want := range cases {
		_, err := parseAnswer(s, doc)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", doc, err)
		case want != "" && (err == nil || err.Error() != want):
			t.Errorf("%s: error %v, want %q", doc, err, want)
		}
	}
	for _, text := range []string{"```json\n{\"n\":2}\n```", "```\n{\"n\":2}```", "  {\"n\":2}\n", "<think>\nsure\n</think>\n{\"n\":2}"} {
		got, err := parseAnswer(s, text)
		if err != nil || string(got) != `{"n":2}` {
			t.Errorf("%q: %s %v", text, got, err)
		}
	}
	if _, err := parseAnswer(s, `{"n":1} {"n":2}`); err == nil {
		t.Error("accepted two JSON values")
	}
}

func TestWireSchema(t *testing.T) {
	raw := json.RawMessage(`{"$schema":"x","type":"object","properties":{"a":{"type":"string","maxLength":5,"description":"A."},
		"o":{"type":"object","properties":{"b":{"type":"integer","minimum":1}}}},"required":["a","o"]}`)
	var got map[string]any
	if err := json.Unmarshal(wireSchema(raw), &got); err != nil {
		t.Fatal(err)
	}
	if _, has := got["$schema"]; has || got["additionalProperties"] != false {
		t.Errorf("top %v", got)
	}
	props := got["properties"].(map[string]any)
	a := props["a"].(map[string]any)
	if _, has := a["maxLength"]; has || a["description"] != "A. (maxLength 5)" {
		t.Errorf("a %v", a)
	}
	o := props["o"].(map[string]any)
	if o["additionalProperties"] != false || len(o["required"].([]any)) != 0 {
		t.Errorf("o %v", o)
	}
	s, _ := parseSchema(raw)
	if s.strict() {
		t.Error("strict although o.b is optional")
	}
	s, _ = parseSchema(json.RawMessage(`{"type":"object","properties":{"a":{"type":"array","items":{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}}},"required":["a"]}`))
	if !s.strict() {
		t.Error("not strict although every property is required")
	}
}
