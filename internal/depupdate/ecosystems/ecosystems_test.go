package ecosystems

import (
	"testing"
	"testing/fstest"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
)

const goMod = `module example.com/app

go 1.27

require (
	github.com/google/uuid v1.6.0
	golang.org/x/text v0.20.0 // indirect
	example.com/old v1.0.0
)

require github.com/stretchr/testify v1.9.0

replace example.com/old => ../old
`

const pkgJSON = `{
  "name": "web",
  "dependencies": {
    "react": "^18.2.0",
    "left-pad": "1.3.0"
  },
  "devDependencies": {
    "vite": "~5.1.0"
  }
}
`

const pkgLock = `{
  "name": "web",
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "web"},
    "node_modules/react": {"version": "18.2.0"},
    "node_modules/left-pad": {"version": "1.3.0"},
    "node_modules/vite": {"version": "5.1.4", "dev": true},
    "node_modules/loose-envify": {"version": "1.4.0"},
    "node_modules/vite/node_modules/esbuild": {"version": "0.19.0", "dev": true}
  }
}`

const dockerfile = `ARG NODE_IMAGE=docker.io/library/node:24.22.3-alpine3.23
ARG GO_IMAGE=docker.io/library/golang:1.27.4-alpine3.23@sha256:aaaa

FROM ${NODE_IMAGE} AS web
RUN true
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
FROM web AS again
FROM debian:12
FROM scratch
`

const pipeline = `steps:
  - name: test
    image: docker.io/library/golang:1.27
  - name: web
    image: "docker.io/library/node:24-alpine" # pinned
  - name: build
    image:
      name: x
`

const workflowYML = `jobs:
  a:
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-go@v5
      - uses: ./local
      - uses: org/repo/.github/workflows/x.yml@v1
`

const requirements = `# pinned
requests==2.28.1 \
    --hash=sha256:aaaa \
    --hash=sha256:bbbb
Flask[async]>=2.0,<3  # loose
django ; python_version > "3.8"
mypkg @ git+https://example.com/x.git
-r base.txt
`

const pyproject = `[project]
name = "x"
dependencies = [
  "httpx>=0.27",
  "pydantic==2.5.0",
]

[project.optional-dependencies]
cli = ["click~=8.1"]

[dependency-groups]
dev = ["pytest>=8"]

[tool.poetry.dependencies]
python = "^3.11"
rich = "^13.0"
local = { path = "../local" }
`

const uvLock = `version = 1

[[package]]
name = "httpx"
version = "0.27.2"

[[package]]
name = "pydantic"
version = "2.5.0"

[[package]]
name = "Click"
version = "8.1.7"
`

func TestDiscover(t *testing.T) {
	fsys := fstest.MapFS{
		"go.mod":                          {Data: []byte(goMod)},
		"go.sum":                          {Data: []byte("x")},
		"web/package.json":                {Data: []byte(pkgJSON)},
		"web/package-lock.json":           {Data: []byte(pkgLock)},
		"web/yarn.lock":                   {Data: []byte("")},
		"web/node_modules/x/package.json": {Data: []byte(`{"dependencies":{"no":"1"}}`)},
		"Dockerfile":                      {Data: []byte(dockerfile)},
		".launchway/pipeline.yaml":        {Data: []byte(pipeline)},
		".github/workflows/ci.yml":        {Data: []byte(workflowYML)},
		"py/requirements.txt":             {Data: []byte(requirements)},
		"py2/pyproject.toml":              {Data: []byte(pyproject)},
		"py2/uv.lock":                     {Data: []byte(uvLock)},
		"scripts/lib.sh":                  {Data: []byte("TRIVY_IMAGE='ghcr.io/aquasecurity/trivy:0.76.2'\nGL='ghcr.io/g/gl:v8.30.1@sha256:b109'\nGL_IMAGE=\"ghcr.io/g/gl:v8.30.1@sha256:b109\"\n")},
	}
	res, err := depupdate.Discover(fsys, Parsers([]string{"scripts/*.sh"}), depupdate.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("errors: %+v", res.Errors)
	}
	got := map[string]depupdate.Dep{}
	for _, d := range res.Deps {
		got[d.Ecosystem+"|"+d.File+"|"+d.Name] = d
	}
	want := []struct {
		key        string
		cur        string
		constraint string
		direct     bool
		group      string
		line       int
	}{
		{"go|go.mod|github.com/google/uuid", "v1.6.0", "v1.6.0", true, "prod", 6},
		{"go|go.mod|golang.org/x/text", "v0.20.0", "v0.20.0", false, "prod", 7},
		{"go|go.mod|github.com/stretchr/testify", "v1.9.0", "v1.9.0", true, "prod", 11},
		{"npm|web/package.json|react", "18.2.0", "^18.2.0", true, "prod", 4},
		{"npm|web/package.json|left-pad", "1.3.0", "1.3.0", true, "prod", 5},
		{"npm|web/package.json|vite", "5.1.4", "~5.1.0", true, "dev", 8},
		{"npm|web/package-lock.json|loose-envify", "1.4.0", "", false, "prod", 0},
		{"npm|web/package-lock.json|esbuild", "0.19.0", "", false, "dev", 0},
		{"container|Dockerfile|docker.io/library/node", "24.22.3-alpine3.23", "24.22.3-alpine3.23", true, "prod", 1},
		{"container|Dockerfile|docker.io/library/golang", "1.27.4-alpine3.23", "1.27.4-alpine3.23", true, "prod", 2},
		{"container|Dockerfile|debian", "12", "12", true, "prod", 8},
		{"container|.launchway/pipeline.yaml|docker.io/library/golang", "1.27", "1.27", true, "tool", 3},
		{"container|.launchway/pipeline.yaml|docker.io/library/node", "24-alpine", "24-alpine", true, "tool", 5},
		{"workflow|.github/workflows/ci.yml|actions/checkout", "v7.0.1", "3d3c42e5aac5ba805825da76410c181273ba90b1", true, "tool", 4},
		{"workflow|.github/workflows/ci.yml|actions/setup-go", "v5", "v5", true, "tool", 5},
		{"workflow|.github/workflows/ci.yml|org/repo", "v1", "v1", true, "tool", 7},
		{"python|py/requirements.txt|requests", "2.28.1", "==2.28.1", true, "prod", 2},
		{"python|py/requirements.txt|flask", "", ">=2.0,<3", true, "prod", 5},
		{"python|py2/pyproject.toml|httpx", "0.27.2", ">=0.27", true, "prod", 3},
		{"python|py2/pyproject.toml|pydantic", "2.5.0", "==2.5.0", true, "prod", 3},
		{"python|py2/pyproject.toml|click", "8.1.7", "~=8.1", true, "opt", 0},
		{"python|py2/pyproject.toml|pytest", "", ">=8", true, "dev", 0},
		{"python|py2/pyproject.toml|rich", "", "^13.0", true, "prod", 0},
		{"container|scripts/lib.sh|ghcr.io/aquasecurity/trivy", "0.76.2", "0.76.2", true, "tool", 1},
	}
	for _, w := range want {
		d, ok := got[w.key]
		if !ok {
			t.Errorf("missing %s (have %v)", w.key, keys(got))
			continue
		}
		if d.Current != w.cur || d.Constraint != w.constraint || d.Direct != w.direct {
			t.Errorf("%s: current %q constraint %q direct %v", w.key, d.Current, d.Constraint, d.Direct)
		}
		if w.line != 0 && d.Line != w.line {
			t.Errorf("%s: line %d, want %d", w.key, d.Line, w.line)
		}
	}
	for _, absent := range []string{"go|go.mod|example.com/old", "python|py/requirements.txt|mypkg", "python|py/requirements.txt|django"} {
		if _, ok := got[absent]; ok && absent != "python|py/requirements.txt|django" {
			t.Errorf("%s should not be listed", absent)
		}
	}
	for k := range got {
		if len(k) > 12 && k[:12] == "npm|web/node" {
			t.Errorf("node_modules was read: %s", k)
		}
	}
	if d := got["container|Dockerfile|docker.io/library/golang"]; d.Digest != "sha256:aaaa" {
		t.Errorf("digest: %+v", d)
	}
	foundYarn := false
	for _, n := range res.Notes {
		if n.File == "web/yarn.lock" {
			foundYarn = true
		}
	}
	if !foundYarn {
		t.Errorf("yarn.lock note missing: %+v", res.Notes)
	}
}

func TestAvailableAndComing(t *testing.T) {
	for _, c := range depupdate.Coming() {
		for _, a := range depupdate.Available() {
			if c == a {
				t.Errorf("%s is both", c)
			}
		}
	}
	if depupdate.IsAvailable("cargo") || !depupdate.IsAvailable("go") {
		t.Error("availability")
	}
}

func keys(m map[string]depupdate.Dep) []string {
	var o []string
	for k := range m {
		if len(k) > 6 && k[:6] == "python" {
			o = append(o, k)
		}
	}
	return o
}
