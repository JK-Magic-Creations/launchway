package ecosystems

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/depupdate/container"
	"github.com/jk-magic-creations/launchway/internal/depupdate/gomod"
	"github.com/jk-magic-creations/launchway/internal/depupdate/npm"
	"github.com/jk-magic-creations/launchway/internal/depupdate/python"
	"github.com/jk-magic-creations/launchway/internal/depupdate/workflow"
)

func TestEditGoMod(t *testing.T) {
	out, err := gomod.EditGoMod(goMod, "github.com/google/uuid", "v1.7.0")
	if err != nil || !strings.Contains(out, "github.com/google/uuid v1.7.0") || !strings.Contains(out, "testify v1.9.0") {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err = gomod.EditGoMod(goMod, "github.com/stretchr/testify", "v1.10.0")
	if err != nil || !strings.Contains(out, "require github.com/stretchr/testify v1.10.0") {
		t.Fatalf("single-line require: %v %s", err, out)
	}
	if _, err = gomod.EditGoMod(goMod, "nope", "v1.0.0"); err == nil {
		t.Error("missing module must fail")
	}
	c := gomod.UpdateCommands("", map[string]string{"a/b": "v1.2.3"}, []string{"a/b"})
	if len(c.Args) != 2 || strings.Join(c.Args[0], " ") != "go get a/b@v1.2.3" || strings.Join(c.Args[1], " ") != "go mod tidy" {
		t.Errorf("commands: %v", c.Args)
	}
}

func TestEditNPM(t *testing.T) {
	out, err := npm.EditManifest(pkgJSON, "react", "18.3.1")
	if err != nil || !strings.Contains(out, `"react": "^18.3.1"`) || !strings.Contains(out, `"vite": "~5.1.0"`) {
		t.Fatalf("%v %s", err, out)
	}
	out, err = npm.EditManifest(pkgJSON, "left-pad", "1.4.0")
	if err != nil || !strings.Contains(out, `"left-pad": "1.4.0"`) {
		t.Fatalf("exact: %v %s", err, out)
	}
	if _, err = npm.EditManifest(`{"dependencies":{"x":">=1 <2"}}`, "x", "1.5.0"); err == nil {
		t.Error("a union range cannot be rewritten")
	}
	if got := strings.Join(npm.LockCommands("web").Args[0], " "); !strings.Contains(got, "--package-lock-only") || !strings.Contains(got, "--ignore-scripts") {
		t.Errorf("lock command: %s", got)
	}
}

func TestEditPython(t *testing.T) {
	out, relock, err := python.EditRequirements(requirements, "requests", "2.31.0")
	if err != nil || !relock || !strings.Contains(out, "requests==2.31.0") || strings.Contains(out, "sha256:aaaa") || !strings.Contains(out, "Flask[async]") {
		t.Fatalf("%v %v\n%s", err, relock, out)
	}
	out, relock, err = python.EditRequirements("a==1.0\nb==2.0\n", "b", "2.1")
	if err != nil || relock || out != "a==1.0\nb==2.1\n" {
		t.Fatalf("plain: %v %v %q", err, relock, out)
	}
	if _, _, err = python.EditRequirements(requirements, "flask", "2.5"); err == nil {
		t.Error("an unpinned requirement is not edited here")
	}
	out, err = python.EditPyproject(pyproject, "pydantic", "2.7.1")
	if err != nil || !strings.Contains(out, `"pydantic==2.7.1"`) {
		t.Fatalf("%v %s", err, out)
	}
	out, err = python.EditPyproject(pyproject, "rich", "13.7.0")
	if err != nil || !strings.Contains(out, `rich = "^13.7.0"`) {
		t.Fatalf("poetry: %v %s", err, out)
	}
	if c, ok := python.LockCommand("py2", "py2/uv.lock", "httpx"); !ok || strings.Join(c.Args[0], " ") != "uv lock --upgrade-package httpx" {
		t.Errorf("%v %v", c, ok)
	}
	if _, ok := python.LockCommand("", "requirements.txt", "x"); ok {
		t.Error("requirements has no lock command")
	}
}

func TestEditContainerAndWorkflow(t *testing.T) {
	out, err := container.Edit(dockerfile, 1, "node:24.22.3-alpine3.23", "node:24.23.0-alpine3.23")
	if err != nil || !strings.HasPrefix(out, "ARG NODE_IMAGE=docker.io/library/node:24.23.0-alpine3.23\n") {
		t.Fatalf("%v %s", err, out)
	}
	if _, err = container.Edit(dockerfile, 3, "node:24", "x"); err == nil {
		t.Error("the wrong line must fail")
	}
	r := container.ParseRef("localhost:5000/team/app:1.2@sha256:abc")
	if r.Repo != "localhost:5000/team/app" || r.Tag != "1.2" || r.Digest != "sha256:abc" || r.String() != "localhost:5000/team/app:1.2@sha256:abc" {
		t.Errorf("%+v", r)
	}
	out2, ok := workflow.Edit(workflowYML, 4, "3d3c42e5aac5ba805825da76410c181273ba90b1", "ffffffffffffffffffffffffffffffffffffffff", "v7.0.2")
	if !ok || !strings.Contains(out2, "checkout@ffffffffffffffffffffffffffffffffffffffff # v7.0.2") {
		t.Fatalf("%v %s", ok, out2)
	}
}
