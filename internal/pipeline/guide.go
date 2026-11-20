package pipeline

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

// The pipeline reference and worked examples, as people and agents read
// them (the MCP server serves them). A test checks that every example is a
// valid pipeline and that the reference names every key the parser knows.

//go:embed guide/reference.md guide/examples/*.yaml
var guide embed.FS

// Reference returns the pipeline format reference, in Markdown.
func Reference() string {
	data, _ := guide.ReadFile("guide/reference.md")
	return string(data)
}

// Example is a worked example pipeline.
type Example struct {
	Name    string // the file name without .yaml
	Summary string // its first comment line(s)
	Text    string
}

// Examples returns the worked examples, by name.
func Examples() []Example {
	var out []Example
	entries, _ := fs.ReadDir(guide, "guide/examples")
	for _, e := range entries {
		data, err := guide.ReadFile("guide/examples/" + e.Name())
		if err != nil {
			continue
		}
		text := string(data)
		var summary []string
		for _, line := range strings.Split(text, "\n") {
			c, ok := strings.CutPrefix(line, "#")
			if !ok {
				break
			}
			summary = append(summary, strings.TrimSpace(c))
		}
		out = append(out, Example{Name: strings.TrimSuffix(e.Name(), ".yaml"), Summary: strings.Join(summary, " "), Text: text})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
