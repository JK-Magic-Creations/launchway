package graph

import (
	"testing"
)

const pipelineSvc = `pipeline: svc
on: { push: { branches: [main] } }
steps:
  image:
    kind: image
    context: .
    file: Containerfile
    platforms: [linux/amd64, linux/arm64]
    tags:
      - { registry: harbor, repository: apps/svc, tag: latest }
`

const pipelineBase = `pipeline: base
on: { push: { branches: [main] } }
steps:
  image:
    kind: image
    context: .
    file: Containerfile
    platforms: [linux/amd64, linux/arm64]
    tags:
      - { registry: harbor, repository: base/runtime, tag: "1.0" }
`

const k8sSvc = `apiVersion: apps/v1
kind: Deployment
metadata: { name: svc }
spec:
  template:
    spec:
      containers:
        - name: svc
          image: harbor.test/apps/svc:1
          ports: [{ containerPort: 8080 }]
          resources:
            requests: { cpu: 500m, memory: 512Mi }
            limits: { nvidia.com/gpu: 1 }
`

// fixtureRepos is several repositories that depend on each other: a Go
// library, a Go service that uses it and is built on a base image another
// repository builds, an npm UI library and the web app using it, and a
// Python library and application.
func fixtureRepos() *MemRepos {
	m := NewMemRepos()
	m.Push("acme/libgo", "c-libgo-1", map[string]string{
		"go.mod": "module example.test/libgo\n\ngo 1.23\n",
	})
	m.Push("acme/base", "c-base-1", map[string]string{
		"Containerfile":            "FROM debian:12\nRUN true\n",
		".launchway/pipeline.yaml": pipelineBase,
		"CODEOWNERS":               "* @platform-team\n",
	})
	m.Push("acme/svc", "c-svc-1", map[string]string{
		"go.mod":                   "module example.test/svc\n\ngo 1.23\n\nrequire (\n\texample.test/libgo v1.2.0\n\tgithub.com/jackc/pgx/v5 v5.5.0\n)\n",
		"Containerfile":            "FROM golang:1.23 AS build\nRUN true\nFROM harbor.test/base/runtime:1.0\nEXPOSE 8080\nCOPY --from=build /x /x\n",
		".launchway/pipeline.yaml": pipelineSvc,
		"k8s/deploy.yaml":          k8sSvc,
		"CODEOWNERS":               "* @apps-team\n",
	})
	m.Push("acme/ui", "c-ui-1", map[string]string{
		"package.json": `{"name":"@acme/ui","version":"1.0.0"}`,
	})
	m.Push("acme/web", "c-web-1", map[string]string{
		"package.json":                `{"name":"web","dependencies":{"@acme/ui":"^1.0.0"}}`,
		"packages/extra/package.json": `{"name":"@acme/extra","dependencies":{"web":"*"}}`,
	})
	m.Push("acme/pylib", "c-pylib-1", map[string]string{
		"pyproject.toml": "[project]\nname = \"acme_pylib\"\nversion = \"1.0\"\nrequires-python = \">=3.11\"\n",
	})
	m.Push("acme/pyapp", "c-pyapp-1", map[string]string{
		"pyproject.toml": "[project]\nname = \"pyapp\"\nversion = \"1.0\"\ndependencies = [\"acme-pylib>=1.0\"]\n",
	})
	return m
}

func newRunner(t testing.TB, repos Repos) *Runner {
	t.Helper()
	s := newStore(t)
	cfg := Config{RegistryHosts: map[string]string{"harbor": "harbor.test"}}
	return &Runner{Store: s, Repos: repos, Sources: []Source{ManifestSource{Repos: repos}, ContainerSource{Repos: repos, Cfg: cfg}, RequirementsSource{Repos: repos, Cfg: cfg}}}
}
