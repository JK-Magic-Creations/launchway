package stepkit

import "fmt"

// InputInfo, OutputInfo and Info describe a step for a person choosing one,
// and for the step's page: generated from the manifest, never written by hand.
type InputInfo struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Default     *string  `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Options     []string `json:"options,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
}

// OutputInfo is one output of a step.
type OutputInfo struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// Info is what the manifest says, for display.
type Info struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Contract    int          `json:"contract"`
	Image       string       `json:"image"`
	Command     string       `json:"command"`
	Digest      string       `json:"digest"`
	Inputs      []InputInfo  `json:"inputs"`
	Outputs     []OutputInfo `json:"outputs"`
	Network     []string     `json:"network"`
	Write       []string     `json:"write"`
	Secrets     []string     `json:"secrets"`
	// Permissions is what the step may do, one sentence for each kind; Sentence all of it.
	Permissions []Permission `json:"permissions"`
	Sentence    string       `json:"sentence"`
	CPU         string       `json:"cpu,omitempty"`
	Memory      string       `json:"memory,omitempty"`
	Timeout     string       `json:"timeout,omitempty"`
	// Example is a step of a pipeline that uses it, with every required input.
	Example string `json:"example"`
}

// Describe makes the Info of a manifest. uses is how a pipeline names it
// (library/step or ./steps/step).
func (m *Manifest) Describe(uses string) Info {
	info := Info{Name: m.Name, Description: m.Description, Contract: m.Contract, Image: m.Image, Digest: m.Digest,
		Inputs: []InputInfo{}, Outputs: []OutputInfo{}, Network: nonNil(m.Permissions.Network), Write: nonNil(m.Permissions.Write),
		Secrets: nonNil(m.SecretInputs()), Permissions: m.PermissionList(), Sentence: m.Sentence(), CPU: m.CPU, Memory: m.Memory, Timeout: m.Timeout}
	if m.Shell && len(m.Command) == 1 {
		info.Command = m.Command[0]
	} else {
		for i, a := range m.Command {
			if i > 0 {
				info.Command += " "
			}
			info.Command += a
		}
	}
	for _, in := range m.Inputs {
		info.Inputs = append(info.Inputs, InputInfo{Name: in.Name, Type: in.Type, Required: in.Required, Default: in.Default, Description: in.Description,
			Options: in.Options, Pattern: in.Pattern, Min: in.Min, Max: in.Max})
	}
	for _, o := range m.Outputs {
		info.Outputs = append(info.Outputs, OutputInfo{Name: o.Name, Type: o.Type, Description: o.Description})
	}
	info.Example = m.Example(uses)
	return info
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// Example is a pipeline step that uses the step, with its required inputs.
func (m *Manifest) Example(uses string) string {
	s := fmt.Sprintf("steps:\n  %s:\n    uses: %s\n", m.Name, uses)
	var with string
	for _, in := range m.Inputs {
		if !in.Required {
			continue
		}
		switch in.Type {
		case TypeSecret:
			with += fmt.Sprintf("      %s_store: vault\n      %s_key: path/to/%s\n", in.Name, in.Name, in.Name)
		case TypeNumber:
			with += fmt.Sprintf("      %s: 1\n", in.Name)
		case TypeBoolean:
			with += fmt.Sprintf("      %s: true\n", in.Name)
		case TypeList:
			with += fmt.Sprintf("      %s: [a, b]\n", in.Name)
		case TypeRecord:
			with += fmt.Sprintf("      %s: '{\"key\": \"value\"}'\n", in.Name)
		default:
			with += fmt.Sprintf("      %s: value\n", in.Name)
		}
	}
	if with != "" {
		s += "    with:\n" + with
	}
	return s
}
