package stepkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// JSONList writes a list of text as the canonical JSON array a list value has.
func JSONList(items []string) string {
	if items == nil {
		items = []string{}
	}
	b, _ := json.Marshal(items)
	return string(b)
}

// ParseJSONList reads a list value: a JSON array of strings, or, for a person
// writing one by hand, comma-separated text.
func ParseJSONList(v string) ([]string, error) {
	t := bytes.TrimSpace([]byte(v))
	if len(t) > 0 && t[0] == '[' {
		var items []string
		dec := json.NewDecoder(bytes.NewReader(t))
		if err := dec.Decode(&items); err != nil {
			return nil, errors.New("write it as [\"a\", \"b\"]")
		}
		if dec.More() {
			return nil, errors.New("there is text after the list")
		}
		if len(items) > 500 {
			return nil, errors.New("at most 500 entries")
		}
		return items, nil
	}
	if len(t) == 0 {
		return []string{}, nil
	}
	var items []string
	start := 0
	for i := 0; i <= len(t); i++ {
		if i == len(t) || t[i] == ',' {
			items = append(items, string(bytes.TrimSpace(t[start:i])))
			start = i + 1
		}
	}
	return items, nil
}

// ParseJSONRecord reads a record value: one JSON object.
func ParseJSONRecord(v string) (map[string]any, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader([]byte(v)))
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, errors.New("it is not a JSON object")
	}
	if dec.More() {
		return nil, errors.New("there is text after the object")
	}
	return m, nil
}

func canonJSON(v any) (string, string) {
	if _, ok := v.(map[string]any); !ok {
		return "", "a record is written as { key: value }"
	}
	b, err := json.Marshal(v) // keys sorted by Marshal
	if err != nil {
		return "", "that is not a record"
	}
	return string(b), ""
}

// TypedInput turns the text of a `with` value into its typed JSON value.
func TypedInput(typ, v string) (any, error) {
	switch typ {
	case TypeNumber:
		var f json.Number
		if err := json.Unmarshal([]byte(trim(v)), &f); err != nil {
			return nil, fmt.Errorf("%q is not a number", v)
		}
		return f, nil
	case TypeBoolean:
		return v == "true", nil
	case TypeList:
		return ParseJSONList(v)
	case TypeRecord:
		return ParseJSONRecord(v)
	default:
		return v, nil
	}
}

func trim(s string) string {
	b := bytes.TrimSpace([]byte(s))
	return string(b)
}

// BuildInputs makes the input document a step receives, from the text of the
// values the use gave (and the defaults for those it did not). The names of
// secret inputs map to the environment variable the Station put the secret
// in; a secret that was not given is left out.
func (m *Manifest) BuildInputs(values map[string]string, secretEnv map[string]string) ([]byte, error) {
	doc := map[string]any{}
	for _, in := range m.Inputs {
		if in.Type == TypeSecret {
			if env, ok := secretEnv[in.Name]; ok {
				doc[in.Name] = map[string]string{"secret": env}
			}
			continue
		}
		v, ok := values[in.Name]
		if !ok && in.Default != nil {
			v, ok = *in.Default, true
		}
		if !ok {
			continue
		}
		tv, err := TypedInput(in.Type, v)
		if err != nil {
			return nil, fmt.Errorf("input %s: %w", in.Name, err)
		}
		doc[in.Name] = tv
	}
	return json.Marshal(map[string]any{"contract": ContractVersion, "inputs": doc})
}

// ScalarEnv gives the LAUNCHWAY_WITH_ variables of the scalar inputs.
func (m *Manifest) ScalarEnv(values map[string]string) map[string]string {
	out := map[string]string{}
	for _, in := range m.Inputs {
		if in.Type != TypeString && in.Type != TypeNumber && in.Type != TypeBoolean {
			continue
		}
		v, ok := values[in.Name]
		if !ok && in.Default != nil {
			v, ok = *in.Default, true
		}
		if ok {
			out[EnvWithPrefix+EnvPart(in.Name)] = v
		}
	}
	return out
}

// EnvPart upper-cases a name for use inside a variable name.
func EnvPart(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = c - 32
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		default:
			b[i] = '_'
		}
	}
	return string(b)
}

// SortedKeys returns the keys of m in order.
func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// OutputsManifest is a manifest that has only the outputs a step declares,
// for a Station that was handed the types and not the manifest.
func OutputsManifest(name string, types map[string]string) *Manifest {
	m := &Manifest{Name: name}
	for _, k := range SortedKeys(types) {
		m.Outputs = append(m.Outputs, &Output{Name: k, Type: types[k]})
	}
	return m
}
