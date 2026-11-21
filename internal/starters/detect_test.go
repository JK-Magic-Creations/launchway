package starters_test

import (
	"reflect"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/starters"
)

func TestDetect(t *testing.T) {
	files := func(names ...string) []string { return names }
	contents := map[string]string{
		"go.mod":          "module x\n\ngo 1.26.1\n",
		".python-version": "3.12.4\n",
		".nvmrc":          "v20.11.0\n",
		"package.json":    `{"engines":{"node":">=18"}}`,
		"pom.xml":         "<project><properties><maven.compiler.release>17</maven.compiler.release></properties></project>",
	}
	read := func(n string) string { return contents[n] }
	for _, tc := range []struct {
		name   string
		files  []string
		flight string
		with   map[string]string
		why    string
	}{
		{"go service", files("go.mod", "main.go", "Dockerfile"), "go-service", map[string]string{"go_version": "1.26"}, "go.mod was found (and a Dockerfile)"},
		{"go library", files("go.mod", "x.go"), "go-library", map[string]string{"go_version": "1.26", "test_versions": `["1.26"]`}, "go.mod was found"},
		{"python library", files("pyproject.toml", ".python-version"), "python-library", map[string]string{"python_version": "3.12", "test_versions": `["3.12"]`}, "pyproject.toml was found"},
		{"python with uv", files("uv.lock", "pyproject.toml", "Dockerfile"), "python-service", map[string]string{}, "uv.lock was found (and a Dockerfile)"},
		{"node app, version from .nvmrc", files("package.json", ".nvmrc", "Dockerfile"), "node-app", map[string]string{"node_version": "20"}, "package.json was found (and a Dockerfile)"},
		{"maven", files("pom.xml"), "java-maven", map[string]string{"java_version": "17"}, "pom.xml was found"},
		{"gradle with image", files("build.gradle.kts", "Dockerfile"), "java-gradle", map[string]string{"image": "true"}, "build.gradle.kts was found (and a Dockerfile)"},
		{"chart", files("Chart.yaml", "values.yaml"), "helm-chart", nil, "Chart.yaml was found"},
		{"image only", files("Dockerfile", "README.md"), "container-image", nil, "Dockerfile was found and nothing else that builds the project"},
		{"site", files("index.html", "site.css"), "static-site", nil, "index.html was found and nothing else that builds the project"},
		{"a subdirectory's go.mod is not the root's", files("tools/go.mod", "README.md"), "", nil, ""},
	} {
		d := starters.Detect(tc.files, read)
		if tc.flight == "" {
			if d != nil {
				t.Errorf("%s: %+v, want nothing", tc.name, d)
			}
			continue
		}
		if d == nil || d.Flight != tc.flight || d.Because != tc.why {
			t.Errorf("%s: %+v, want %s because %s", tc.name, d, tc.flight, tc.why)
			continue
		}
		if len(tc.with) != 0 && !reflect.DeepEqual(d.With, tc.with) {
			t.Errorf("%s: with %v, want %v", tc.name, d.With, tc.with)
		}
	}
	// Whatever is detected is a flight plan of the library.
	for _, f := range []string{"go-service", "go-library", "python-service", "python-library", "node-app", "node-library", "java-maven", "java-gradle", "helm-chart", "container-image", "static-site"} {
		if _, ok := starters.Template(f); !ok {
			t.Errorf("detection can choose %s, which the library does not have", f)
		}
	}
}
