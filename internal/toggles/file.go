package toggles

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// File is the toggles of a project written as a file (conventionally
// .launchway/toggles.yaml in the repository): the definitions, what each does
// in each environment, and the segments. It is the same model as the API
// speaks, in YAML, so that it can be reviewed in a pull request. Applying it
// is a decision of a person or of a pipeline step, never automatic: Launchway
// shows how the file and the live state differ and changes nothing by itself.
type File struct {
	Toggles  map[string]FileToggle `json:"toggles,omitempty"`
	Segments map[string]Segment    `json:"segments,omitempty"`
}

// FileToggle is one toggle in a File.
type FileToggle struct {
	Type          ValueType        `json:"type"`
	Kind          Kind             `json:"kind,omitempty"`
	Description   string           `json:"description,omitempty"`
	Owner         string           `json:"owner,omitempty"`
	Expires       string           `json:"expires,omitempty"` // a date, 2027-03-01
	ClientVisible bool             `json:"client_visible,omitempty"`
	Tags          []string         `json:"tags,omitempty"`
	Default       any              `json:"default"`
	SafeValue     any              `json:"safe_value,omitempty"`
	Environments  map[string]State `json:"environments,omitempty"`
}

// ParseFile reads a toggles file (YAML, or JSON, which is YAML), refusing
// unknown fields so that a misspelt rule is an error and not a silent no-op,
// and checks every toggle and segment.
func ParseFile(data []byte) (*File, error) {
	var generic any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return nil, fmt.Errorf("the toggles file is not valid YAML: %w", err)
	}
	generic = stringKeys(generic)
	raw, err := json.Marshal(generic)
	if err != nil {
		return nil, fmt.Errorf("the toggles file holds something JSON cannot: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("the toggles file: %w", err)
	}
	return &f, f.Check()
}

// stringKeys turns the maps YAML may give with non-string keys into maps JSON accepts.
func stringKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = stringKeys(e)
		}
		return x
	case map[any]any:
		out := map[string]any{}
		for k, e := range x {
			out[fmt.Sprint(k)] = stringKeys(e)
		}
		return out
	case []any:
		for i, e := range x {
			x[i] = stringKeys(e)
		}
	case time.Time:
		return x.Format("2006-01-02")
	}
	return v
}

// Check checks every toggle and segment of the file.
func (f *File) Check() error {
	for _, k := range f.Keys() {
		t := f.Toggles[k]
		if err := CheckKey(k); err != nil {
			return err
		}
		if !t.Type.Valid() {
			return fmt.Errorf("toggle %s: type is boolean, string, number or json", k)
		}
		if t.Kind != "" && !t.Kind.Valid() {
			return fmt.Errorf("toggle %s: kind is release, kill, experiment, operational or permission", k)
		}
		if t.Expires != "" {
			if _, err := time.Parse("2006-01-02", t.Expires); err != nil {
				return fmt.Errorf("toggle %s: expires is a date like 2027-03-01", k)
			}
		}
		if err := CheckValue(t.Type, t.Default); err != nil {
			return fmt.Errorf("toggle %s: default: %w", k, err)
		}
		for env, st := range t.Environments {
			st := st
			if err := CheckState(t.Type, &st); err != nil {
				return fmt.Errorf("toggle %s, environment %s: %w", k, env, err)
			}
			t.Environments[env] = st
		}
	}
	for n, s := range f.Segments {
		if err := CheckSegment(n, s); err != nil {
			return fmt.Errorf("segment %s: %w", n, err)
		}
	}
	return nil
}

// Keys returns the toggles' keys in order.
func (f *File) Keys() []string {
	keys := make([]string, 0, len(f.Toggles))
	for k := range f.Toggles {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// YAML writes the file, keys in order.
func (f *File) YAML() ([]byte, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(buf.String()) + "\n"), nil
}

// Canonical is a value's canonical JSON, for comparing two states.
func Canonical(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ParseState reads one environment's state from YAML or JSON (the rules file
// of `launchway toggles rules`), refusing unknown fields.
func ParseState(data []byte) (State, error) {
	var generic any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return State{}, fmt.Errorf("the rules file is not valid YAML: %w", err)
	}
	raw, err := json.Marshal(stringKeys(generic))
	if err != nil {
		return State{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var st State
	if err := dec.Decode(&st); err != nil {
		return State{}, fmt.Errorf("the rules file: %w", err)
	}
	return st, nil
}
