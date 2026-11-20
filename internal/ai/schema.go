package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxSchemaBytes bounds a step's JSON schema.
const MaxSchemaBytes = 16 << 10

// maxSchemaDepth bounds how deeply schemas and answers may nest.
const maxSchemaDepth = 32

// The keywords a schema may use. Everything else is refused, so that a
// schema never silently promises a check that is not made.
var schemaKeywords = []string{
	"$schema", "additionalProperties", "description", "enum", "items",
	"maxItems", "maxLength", "maximum", "minItems", "minLength", "minimum",
	"properties", "required", "title", "type",
}

// constraintKeywords are checked here but not sent to the provider: hosted
// structured-output modes refuse them. Their meaning is added to the
// description instead, so the model still knows about them.
var constraintKeywords = []string{"minLength", "maxLength", "minimum", "maximum", "minItems", "maxItems"}

var schemaTypes = []string{"array", "boolean", "integer", "null", "number", "object", "string"}

// schema is a checked JSON schema, ready to validate answers.
type schema struct {
	types      []string // empty: any type
	properties map[string]*schema
	required   []string
	enum       []any
	items      *schema
	minLength  *int
	maxLength  *int
	minItems   *int
	maxItems   *int
	minimum    *float64
	maximum    *float64
}

// CheckSchema reports whether raw is a schema this package can use: a JSON
// object of at most 16 KiB, describing an object at the top, using only the
// keywords type, properties, required, additionalProperties, enum, items,
// minLength, maxLength, minimum, maximum, minItems, maxItems, description,
// title and $schema.
//
// Objects are closed: additionalProperties may only be false, and an object
// that does not say is treated as if it did. Model output never carries keys
// the pipeline author did not name, and hosted structured-output modes
// require it anyway, so it is added to the schema sent to the provider
// rather than asked of every author.
func CheckSchema(raw json.RawMessage) error {
	_, err := parseSchema(raw)
	return err
}

func parseSchema(raw json.RawMessage) (*schema, error) {
	if len(raw) > MaxSchemaBytes {
		return nil, fmt.Errorf("the schema is %d bytes, over the limit of %d", len(raw), MaxSchemaBytes)
	}
	v, err := decodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("the schema is not valid JSON: %v", err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the schema must be a JSON object")
	}
	s, err := compileSchema(obj, "$", 0)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(s.types, []string{"object"}) {
		return nil, fmt.Errorf("the schema must describe an object at the top (type: object)")
	}
	return s, nil
}

func compileSchema(obj map[string]any, at string, depth int) (*schema, error) {
	if depth > maxSchemaDepth {
		return nil, fmt.Errorf("schema %s: nested more than %d levels deep", at, maxSchemaDepth)
	}
	for k := range obj {
		if !slices.Contains(schemaKeywords, k) {
			return nil, fmt.Errorf("schema %s: keyword %q is not supported (supported: %s)", at, k, strings.Join(schemaKeywords, ", "))
		}
		if k == "$schema" && depth > 0 {
			return nil, fmt.Errorf("schema %s: $schema belongs only at the top", at)
		}
	}
	s := &schema{}
	switch t := obj["type"].(type) {
	case nil:
		if _, ok := obj["type"]; ok {
			return nil, fmt.Errorf("schema %s: type must be a type name or a list of them", at)
		}
	case string:
		s.types = []string{t}
	case []any:
		for _, e := range t {
			name, ok := e.(string)
			if !ok || slices.Contains(s.types, name) {
				return nil, fmt.Errorf("schema %s: type must list distinct type names", at)
			}
			s.types = append(s.types, name)
		}
		if len(s.types) == 0 {
			return nil, fmt.Errorf("schema %s: type must not be an empty list", at)
		}
	default:
		return nil, fmt.Errorf("schema %s: type must be a type name or a list of them", at)
	}
	for _, t := range s.types {
		if !slices.Contains(schemaTypes, t) {
			return nil, fmt.Errorf("schema %s: type %q is not one of %s", at, t, strings.Join(schemaTypes, ", "))
		}
	}
	for _, k := range []string{"description", "title", "$schema"} {
		if v, ok := obj[k]; ok {
			if _, isStr := v.(string); !isStr {
				return nil, fmt.Errorf("schema %s: %s must be text", at, k)
			}
		}
	}
	if v, ok := obj["additionalProperties"]; ok && v != false {
		return nil, fmt.Errorf("schema %s: additionalProperties may only be false; objects are always closed", at)
	}
	if v, ok := obj["properties"]; ok {
		props, isObj := v.(map[string]any)
		if !isObj {
			return nil, fmt.Errorf("schema %s: properties must be an object", at)
		}
		s.properties = map[string]*schema{}
		for name, pv := range props {
			po, isObj := pv.(map[string]any)
			if !isObj {
				return nil, fmt.Errorf("schema %s: must be a schema object", propPath(at, name))
			}
			ps, err := compileSchema(po, propPath(at, name), depth+1)
			if err != nil {
				return nil, err
			}
			s.properties[name] = ps
		}
	}
	if v, ok := obj["required"]; ok {
		list, isList := v.([]any)
		if !isList {
			return nil, fmt.Errorf("schema %s: required must be a list of property names", at)
		}
		for _, e := range list {
			name, isStr := e.(string)
			if !isStr || slices.Contains(s.required, name) {
				return nil, fmt.Errorf("schema %s: required must list distinct property names", at)
			}
			if _, known := s.properties[name]; !known {
				return nil, fmt.Errorf("schema %s: required property %q is not among the properties", at, name)
			}
			s.required = append(s.required, name)
		}
	}
	if (s.properties != nil || s.required != nil) && !s.allows("object") {
		return nil, fmt.Errorf("schema %s: properties and required need type object", at)
	}
	if v, ok := obj["items"]; ok {
		io, isObj := v.(map[string]any)
		if !isObj {
			return nil, fmt.Errorf("schema %s: items must be one schema object", at)
		}
		if !s.allows("array") {
			return nil, fmt.Errorf("schema %s: items needs type array", at)
		}
		is, err := compileSchema(io, at+"[]", depth+1)
		if err != nil {
			return nil, err
		}
		s.items = is
	}
	if v, ok := obj["enum"]; ok {
		list, isList := v.([]any)
		if !isList || len(list) == 0 {
			return nil, fmt.Errorf("schema %s: enum must be a non-empty list", at)
		}
		for _, e := range list {
			switch e.(type) {
			case map[string]any, []any:
				return nil, fmt.Errorf("schema %s: enum values must be text, numbers, true, false or null", at)
			}
		}
		s.enum = list
	}
	var err error
	if s.minLength, err = schemaCount(obj, "minLength", at, "string"); err != nil {
		return nil, err
	}
	if s.maxLength, err = schemaCount(obj, "maxLength", at, "string"); err != nil {
		return nil, err
	}
	if s.minItems, err = schemaCount(obj, "minItems", at, "array"); err != nil {
		return nil, err
	}
	if s.maxItems, err = schemaCount(obj, "maxItems", at, "array"); err != nil {
		return nil, err
	}
	for _, k := range []string{"minimum", "maximum"} {
		v, ok := obj[k]
		if !ok {
			continue
		}
		f, isNum := jsonFloat(v)
		if !isNum {
			return nil, fmt.Errorf("schema %s: %s must be a number", at, k)
		}
		if !s.allows("number") && !s.allows("integer") {
			return nil, fmt.Errorf("schema %s: %s needs type number or integer", at, k)
		}
		if k == "minimum" {
			s.minimum = &f
		} else {
			s.maximum = &f
		}
	}
	if s.minLength != nil && s.maxLength != nil && *s.minLength > *s.maxLength ||
		s.minItems != nil && s.maxItems != nil && *s.minItems > *s.maxItems ||
		s.minimum != nil && s.maximum != nil && *s.minimum > *s.maximum {
		return nil, fmt.Errorf("schema %s: a minimum is above its maximum", at)
	}
	return s, nil
}

func schemaCount(obj map[string]any, k, at, needs string) (*int, error) {
	v, ok := obj[k]
	if !ok {
		return nil, nil
	}
	f, isNum := jsonFloat(v)
	if !isNum || f < 0 || f != math.Trunc(f) || f > 1<<30 {
		return nil, fmt.Errorf("schema %s: %s must be a whole number, 0 or more", at, k)
	}
	if v := obj["type"]; v != nil && v != needs {
		if list, isList := v.([]any); !isList || !slices.Contains(list, any(needs)) {
			return nil, fmt.Errorf("schema %s: %s needs type %s", at, k, needs)
		}
	}
	n := int(f)
	return &n, nil
}

// allows reports whether the schema admits type t (an empty list admits all).
func (s *schema) allows(t string) bool {
	return len(s.types) == 0 || slices.Contains(s.types, t)
}

// strict reports whether every object lists all its properties as required,
// which OpenAI's strict structured-output mode needs.
func (s *schema) strict() bool {
	if s.properties != nil && len(s.required) != len(s.properties) {
		return false
	}
	for _, p := range s.properties {
		if !p.strict() {
			return false
		}
	}
	return s.items == nil || s.items.strict()
}

// validate checks an answer against the schema and returns the first
// problem, naming where it is, such as "$.risk: must be one of low, high".
func (s *schema) validate(v any, at string, depth int) error {
	if depth > maxSchemaDepth {
		return fmt.Errorf("%s: nested more than %d levels deep", at, maxSchemaDepth)
	}
	t := jsonType(v)
	if len(s.types) > 0 && !slices.Contains(s.types, t) && !(t == "integer" && slices.Contains(s.types, "number")) {
		return fmt.Errorf("%s: must be %s, not %s", at, typeList(s.types), article(t))
	}
	if s.enum != nil && !slices.ContainsFunc(s.enum, func(e any) bool { return jsonEqual(e, v) }) {
		return fmt.Errorf("%s: must be one of %s", at, enumList(s.enum))
	}
	switch x := v.(type) {
	case string:
		n := utf8.RuneCountInString(x)
		if s.minLength != nil && n < *s.minLength {
			return fmt.Errorf("%s: must be at least %d characters, not %d", at, *s.minLength, n)
		}
		if s.maxLength != nil && n > *s.maxLength {
			return fmt.Errorf("%s: must be at most %d characters, not %d", at, *s.maxLength, n)
		}
	case json.Number:
		f, _ := x.Float64()
		if s.minimum != nil && f < *s.minimum {
			return fmt.Errorf("%s: must be at least %v, not %s", at, *s.minimum, x)
		}
		if s.maximum != nil && f > *s.maximum {
			return fmt.Errorf("%s: must be at most %v, not %s", at, *s.maximum, x)
		}
	case []any:
		if s.minItems != nil && len(x) < *s.minItems {
			return fmt.Errorf("%s: must have at least %d items, not %d", at, *s.minItems, len(x))
		}
		if s.maxItems != nil && len(x) > *s.maxItems {
			return fmt.Errorf("%s: must have at most %d items, not %d", at, *s.maxItems, len(x))
		}
		if s.items != nil {
			for i, e := range x {
				if err := s.items.validate(e, fmt.Sprintf("%s[%d]", at, i), depth+1); err != nil {
					return err
				}
			}
		}
	case map[string]any:
		for _, name := range s.required {
			if _, ok := x[name]; !ok {
				return fmt.Errorf("%s: missing required property %q", at, name)
			}
		}
		names := make([]string, 0, len(x))
		for name := range x {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			ps, ok := s.properties[name]
			if !ok {
				return fmt.Errorf("%s: unexpected property %q", at, name)
			}
			if err := ps.validate(x[name], propPath(at, name), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// wireSchema is the schema as sent to a provider: every object closed with
// additionalProperties false, and the constraint keywords that hosted
// structured-output modes refuse moved into the description. The answer is
// still checked against the full schema here.
func wireSchema(raw json.RawMessage) json.RawMessage {
	v, err := decodeJSON(raw)
	if err != nil {
		return raw
	}
	out, err := json.Marshal(wireNode(v, true))
	if err != nil {
		return raw
	}
	return out
}

func wireNode(v any, top bool) any {
	obj, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	var notes []string
	for k, e := range obj {
		switch {
		case k == "$schema" && top:
			// Not needed by any provider, and some refuse it.
		case slices.Contains(constraintKeywords, k):
			notes = append(notes, k+" "+fmt.Sprint(e))
		case k == "properties":
			props := map[string]any{}
			for name, p := range e.(map[string]any) {
				props[name] = wireNode(p, false)
			}
			out[k] = props
		case k == "items":
			out[k] = wireNode(e, false)
		default:
			out[k] = e
		}
	}
	if _, has := obj["properties"]; has || obj["type"] == "object" {
		out["additionalProperties"] = false
		if _, has := out["required"]; !has {
			out["required"] = []any{}
		}
	}
	if len(notes) > 0 {
		sort.Strings(notes)
		d, _ := out["description"].(string)
		if d != "" {
			d += " "
		}
		out["description"] = d + "(" + strings.Join(notes, ", ") + ")"
	}
	return out
}

// parseAnswer finds the JSON object in a model's text, checks it against
// the schema, and returns it compacted.
func parseAnswer(s *schema, text string) (json.RawMessage, error) {
	body := []byte(answerJSON(text))
	v, err := decodeJSON(body)
	if err != nil {
		return nil, fmt.Errorf("the answer is not JSON (%v)", err)
	}
	if err := s.validate(v, "$", 0); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// answerJSON strips what models commonly wrap JSON in: a reasoning section
// in <think> tags, and a ```json fence.
func answerJSON(text string) string {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "<think>") {
		if i := strings.Index(t, "</think>"); i >= 0 {
			t = strings.TrimSpace(t[i+len("</think>"):])
		}
	}
	if strings.HasPrefix(t, "```") {
		if i := strings.IndexByte(t, '\n'); i >= 0 {
			t = t[i+1:]
		} else {
			t = strings.TrimPrefix(t, "```")
		}
		t = strings.TrimSpace(t)
		t = strings.TrimSuffix(t, "```")
		t = strings.TrimSpace(t)
	}
	return t
}

// decodeJSON reads exactly one JSON value, keeping numbers exact.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("there is more after the JSON value")
	}
	if _, err := dec.Token(); err == nil {
		return nil, fmt.Errorf("there is more after the JSON value")
	}
	return v, nil
}

func jsonType(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		if _, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
			return "integer"
		}
		if f, err := x.Float64(); err == nil && f == math.Trunc(f) {
			return "integer" // a whole number such as 1.0 or 1e3, as JSON Schema counts it
		}
		return "number"
	case []any:
		return "array"
	default:
		return "object"
	}
}

func jsonFloat(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

func jsonEqual(a, b any) bool {
	fa, aNum := jsonFloat(a)
	fb, bNum := jsonFloat(b)
	if aNum || bNum {
		return aNum && bNum && fa == fb
	}
	return a == b
}

func enumList(vals []any) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		if s, ok := v.(string); ok {
			parts[i] = s
		} else {
			b, _ := json.Marshal(v)
			parts[i] = string(b)
		}
	}
	return strings.Join(parts, ", ")
}

func typeList(types []string) string {
	parts := make([]string, len(types))
	for i, t := range types {
		parts[i] = article(t)
	}
	return strings.Join(parts, " or ")
}

func article(t string) string {
	switch t {
	case "null":
		return "null"
	case "array", "integer", "object":
		return "an " + t
	}
	return "a " + t
}

func propPath(at, name string) string {
	ok := name != ""
	for i, r := range name {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			ok = false
			break
		}
	}
	if ok {
		return at + "." + name
	}
	q, _ := json.Marshal(name)
	return at + "[" + string(q) + "]"
}
