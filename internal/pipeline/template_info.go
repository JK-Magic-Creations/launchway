package pipeline

// Describing a template for someone choosing one.

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// TemplateInfo describes a template for someone choosing one: what it is
// for, the parameters a use passes in with:, and the outputs it gives.
type TemplateInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Params      []*Param `json:"params"`
	Outputs     []string `json:"outputs"`
	// Problems are problems in the template file itself, if any: a use of
	// it would report them too.
	Problems Problems `json:"problems,omitempty"`
}

// DescribeTemplate reads what a template takes and gives, without the
// values of a use. The rest of the file is checked as a use with every
// parameter at its default would check it.
func DescribeTemplate(data []byte, name string) TemplateInfo {
	info := TemplateInfo{Name: name, Params: []*Param{}, Outputs: []string{}}
	if len(data) > MaxFileBytes {
		info.Problems = Problems{{Message: fmt.Sprintf("the template is larger than %d KiB", MaxFileBytes>>10)}}
		return info
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		info.Problems = Problems{syntaxProblem(err)}
		return info
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		info.Problems = Problems{{Message: "the template is empty or not a mapping"}}
		return info
	}
	ps := &parser{}
	ps.eachKey(doc.Content[0], "", templateKeys, func(k, v *yaml.Node) {
		switch k.Value {
		case "description":
			info.Description = strings.TrimSpace(ps.str(v, "description"))
		case "params":
			if params := ps.parseParams(v); params != nil {
				info.Params = params
			}
		case "outputs":
			if v.Kind == yaml.MappingNode {
				for i := 0; i+1 < len(v.Content); i += 2 {
					info.Outputs = append(info.Outputs, v.Content[i].Value)
				}
			}
		}
	})
	info.Problems = ps.problems
	info.markSecretParams(doc.Content[0])
	if len(info.Problems) == 0 {
		// A parameter without a default gets a value of its type.
		with := map[string]string{}
		for _, p := range info.Params {
			if p.Default == nil {
				with[p.Name] = sampleValue(p)
			}
		}
		_, info.Problems = readTemplate(data, name, with, nil, 0, func(int, int, string, string) {}, DefaultMaxLoopMembers, "", nil)
	}
	return info
}

// sampleValue is a value of a parameter's type, for checking a template
// without a use.
func sampleValue(p *Param) string {
	if len(p.Options) > 0 {
		return p.Options[0]
	}
	switch p.Type {
	case paramTypeNumber:
		return "1"
	case paramTypeBoolean:
		return "true"
	case paramTypeList:
		return "[]"
	case paramTypeEnvironment:
		return "dev"
	case paramTypeApplication:
		return "app"
	}
	return "x"
}
