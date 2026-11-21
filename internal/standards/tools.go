// Package standards is Launchway's Standing orders for repositories: the
// same scans and best-practice checks Launchway runs on its own code,
// offered to every repository, on by default, and changeable by the team
// that owns the repository within what the levels above allow.
//
// The package holds the versioned profile (which checks exist, what
// triggers them, with which pinned tool), the settings by level and the
// repository file that make the effective profile, the steps the server
// adds to a run (the Inspection), the one findings model every tool is
// normalised into, and the gate that decides what blocks a pull request.
// It runs no tool itself: tools run as ordinary steps in their pinned
// images, on runners, and are never bundled into the server.
package standards

import (
	_ "embed"
	"fmt"
	"regexp"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed tools.yaml
var toolsYAML []byte

// Tool is one scanner or linter and the image it runs from.
type Tool struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version" json:"version"`
	// Image is the reference with a tag and a digest: the digest is what
	// runs.
	Image   string `yaml:"image" json:"image"`
	License string `yaml:"license" json:"license"`
	Home    string `yaml:"home" json:"home"`
	Note    string `yaml:"note,omitempty" json:"note,omitempty"`
}

var digestRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

var (
	toolsOnce sync.Once
	toolList  []Tool
	toolMap   map[string]Tool
	toolsErr  error
)

func loadTools() {
	var f struct {
		Manifest int    `yaml:"manifest"`
		Tools    []Tool `yaml:"tools"`
	}
	if err := yaml.Unmarshal(toolsYAML, &f); err != nil {
		toolsErr = fmt.Errorf("standards: tools.yaml: %w", err)
		return
	}
	toolMap = map[string]Tool{}
	for _, t := range f.Tools {
		switch {
		case t.Name == "" || t.Version == "" || t.License == "":
			toolsErr = fmt.Errorf("standards: tools.yaml: a tool needs a name, a version and a license: %+v", t)
			return
		case !digestRe.MatchString(t.Image):
			toolsErr = fmt.Errorf("standards: tools.yaml: the image of %s is not pinned by digest: %q", t.Name, t.Image)
			return
		}
		if _, dup := toolMap[t.Name]; dup {
			toolsErr = fmt.Errorf("standards: tools.yaml: %s is listed twice", t.Name)
			return
		}
		toolMap[t.Name] = t
	}
	toolList = f.Tools
	sort.Slice(toolList, func(i, j int) bool { return toolList[i].Name < toolList[j].Name })
}

// Tools returns the pinned tools, by name.
func Tools() ([]Tool, error) {
	toolsOnce.Do(loadTools)
	return append([]Tool(nil), toolList...), toolsErr
}

// ToolNamed returns one tool.
func ToolNamed(name string) (Tool, bool) {
	toolsOnce.Do(loadTools)
	t, ok := toolMap[name]
	return t, ok
}
