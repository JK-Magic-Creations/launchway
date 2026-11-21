package eventexpr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// MaxSchema is the longest JSON Schema an event source may carry.
const MaxSchema = 32 << 10

// noLoader refuses every address a schema's $ref might name: a schema is
// checked with what it carries, and never fetches anything.
type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, errors.New("a schema may not refer to another address (" + url + "): put what it needs inside it")
}

// Schema is a compiled JSON Schema for the body of an event.
type Schema struct{ s *jsonschema.Schema }

// CompileSchema reads a JSON Schema (the draft 2020-12 vocabulary) given as
// JSON text. An empty text is no schema (nil, no error).
func CompileSchema(text string) (*Schema, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	if len(text) > MaxSchema {
		return nil, fmt.Errorf("the schema is longer than %d bytes", MaxSchema)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(text)))
	if err != nil {
		return nil, fmt.Errorf("the schema is not JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(noLoader{})
	if err := c.AddResource("event-source.json", doc); err != nil {
		return nil, fmt.Errorf("the schema cannot be used: %v", err)
	}
	s, err := c.Compile("event-source.json")
	if err != nil {
		return nil, fmt.Errorf("the schema cannot be used: %v", err)
	}
	return &Schema{s}, nil
}

// Check returns what is wrong with a body, one problem to a line, or nil.
func (s *Schema) Check(body []byte) []string {
	if s == nil {
		return nil
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return []string{"the body is not JSON: " + err.Error()}
	}
	err = s.s.Validate(v)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []string{err.Error()}
	}
	var out []string
	var walk func(*jsonschema.OutputUnit)
	out0 := ve.DetailedOutput()
	walk = func(u *jsonschema.OutputUnit) {
		if u.Error != nil && len(u.Errors) == 0 {
			where := u.InstanceLocation
			if where == "" {
				where = "the body"
			}
			out = append(out, fmt.Sprintf("%s: %s", where, u.Error.String()))
		}
		for i := range u.Errors {
			walk(&u.Errors[i])
		}
	}
	walk(out0)
	if len(out) == 0 {
		out = []string{err.Error()}
	}
	if len(out) > 10 {
		out = append(out[:10], fmt.Sprintf("… and %d more", len(out)-10))
	}
	return out
}

// Parse reads a JSON body into the values expressions see. Numbers are
// float64s.
func ParseBody(body []byte) (any, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more than one JSON value")
	}
	return v, nil
}
