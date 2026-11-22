package stepkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Outputs is a step's outputs checked against its manifest.
type Outputs struct {
	// Values is each output as text, the way later steps receive it: a string
	// as it is, a number or boolean in its JSON form, a list or record as
	// JSON. A secret is "***": its value is never kept.
	Values map[string]string
	// Types is each output's declared type.
	Types map[string]string
	// Secrets are the real values of secret outputs, for masking.
	Secrets []string
}

// ReadOutputs checks the outputs document a step wrote. A passed step must
// write every output it declares; any step may write none at all if it failed.
func (m *Manifest) ReadOutputs(data []byte, passed bool) (*Outputs, []string) {
	out := &Outputs{Values: map[string]string{}, Types: map[string]string{}}
	var problems []string
	for _, o := range m.Outputs {
		out.Types[o.Name] = o.Type
	}
	if len(bytes.TrimSpace(data)) == 0 {
		if passed && len(m.Outputs) > 0 {
			problems = append(problems, fmt.Sprintf("the step declares the outputs %s but wrote none (write them as JSON to $%s)", names(m.Outputs), EnvOutputs))
		}
		return out, problems
	}
	if len(data) > MaxOutputsBytes {
		return out, []string{fmt.Sprintf("the outputs are larger than %d KiB", MaxOutputsBytes>>10)}
	}
	var doc map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return out, []string{"the outputs file is not one JSON object: " + clip(err.Error(), 120)}
	}
	if dec.More() {
		return out, []string{"the outputs file has text after its JSON object"}
	}
	for _, k := range SortedKeys(doc) {
		o := m.Output(k)
		if o == nil {
			problems = append(problems, fmt.Sprintf("the step wrote the output %q, which its manifest does not declare (its outputs are %s)", clip(k, 40), names(m.Outputs)))
			continue
		}
		text, secret, err := outputText(o.Type, doc[k])
		if err != nil {
			problems = append(problems, fmt.Sprintf("the output %s is a %s: %s", k, o.Type, err))
			continue
		}
		if len(text) > MaxOutputValue {
			problems = append(problems, fmt.Sprintf("the output %s is longer than %d bytes", k, MaxOutputValue))
			continue
		}
		if o.Type == TypeSecret {
			out.Secrets = append(out.Secrets, secret)
			text = "***"
		}
		out.Values[k] = text
	}
	if passed {
		var missing []string
		for _, o := range m.Outputs {
			if _, ok := doc[o.Name]; !ok {
				missing = append(missing, o.Name)
			}
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("the step did not write the output %s that its manifest declares", strings.Join(missing, ", ")))
		}
	}
	return out, problems
}

func names(os []*Output) string {
	if len(os) == 0 {
		return "none"
	}
	var n []string
	for _, o := range os {
		n = append(n, o.Name)
	}
	return strings.Join(n, ", ")
}

func outputText(typ string, raw json.RawMessage) (text, secret string, err error) {
	raw = json.RawMessage(bytes.TrimSpace(raw))
	switch typ {
	case TypeString, TypeSecret:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", "", fmt.Errorf("write it as a JSON string")
		}
		if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return "", "", fmt.Errorf("it is not text")
		}
		return s, s, nil
	case TypeNumber:
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil || len(raw) == 0 || raw[0] == '"' {
			return "", "", fmt.Errorf("write it as a JSON number")
		}
		return n.String(), "", nil
	case TypeBoolean:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return "", "", fmt.Errorf("write it as true or false")
		}
		if b {
			return "true", "", nil
		}
		return "false", "", nil
	case TypeList:
		var items []string
		if err := json.Unmarshal(raw, &items); err != nil {
			return "", "", fmt.Errorf("write it as a JSON list of strings")
		}
		return JSONList(items), "", nil
	case TypeRecord:
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil || m == nil {
			return "", "", fmt.Errorf("write it as a JSON object")
		}
		b, _ := json.Marshal(m)
		return string(b), "", nil
	}
	return "", "", fmt.Errorf("unknown type")
}
