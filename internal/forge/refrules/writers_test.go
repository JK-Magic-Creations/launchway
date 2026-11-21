package refrules

import (
	"strings"
	"testing"
)

func TestAdmitsOnlyTheFilesARuleNames(t *testing.T) {
	ws := []Writer{{Branch: "main", Projects: []string{"mcp-*"}, Files: []string{"apps/mcp-servers/base.yaml", "apps/*/overlay/**"}}}
	if err := ValidateWriters(ws); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		branch, project string
		files           []string
		want            string // "" admitted
	}{
		{"main", "mcp-weather", []string{"apps/mcp-servers/base.yaml"}, ""},
		{"main", "mcp-weather", []string{"apps/x/overlay/a/b.yaml"}, ""},
		{"main", "mcp-weather", []string{"apps/mcp-servers/base.yaml", "README.md"}, "may not change README.md on main"},
		{"main", "mcp-weather", []string{"apps/x/overlayy/a.yaml"}, "may not change"},
		{"main", "shop", []string{"apps/mcp-servers/base.yaml"}, "no rule of this repository admits a pipeline of project shop"},
		{"develop", "mcp-weather", []string{"apps/mcp-servers/base.yaml"}, "no rule of this repository admits"},
		{"main", "mcp-weather", nil, ""},
	} {
		err := Admits(ws, c.branch, c.project, c.files)
		switch {
		case c.want == "" && err != nil, c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%+v: %v", c, err)
		}
	}
}

func TestWritersAreValidated(t *testing.T) {
	for w, want := range map[string]Writer{
		"no files":    {Branch: "main", Projects: []string{"a"}},
		"every file":  {Branch: "main", Projects: []string{"a"}, Files: []string{"**"}},
		"parent":      {Branch: "main", Projects: []string{"a"}, Files: []string{"../x"}},
		"absolute":    {Branch: "main", Projects: []string{"a"}, Files: []string{"/etc/x"}},
		"bad branch":  {Branch: "refs/heads/main", Projects: []string{"a"}, Files: []string{"x"}},
		"no project":  {Branch: "main", Files: []string{"x"}},
		"bad pattern": {Branch: "main", Projects: []string{"["}, Files: []string{"x"}},
	} {
		if err := ValidateWriters([]Writer{want}); err == nil {
			t.Errorf("%s was accepted", w)
		}
	}
}
