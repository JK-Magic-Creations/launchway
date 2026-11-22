package executor

import (
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// A custom step's sandbox runs inside the step's own image (an ordinary step compiled from the manifest) and, on
// Kubernetes, in the helper image (sh, tar, xargs, chmod, find, du, sleep: busybox has them). What the Station does
// to set it up is Go in the Station's own process: it runs no program that the Station image (which has Git and an
// SSH client and no shell) might lack. This pins that: the files that build the sandbox import nothing that starts a
// program, so a need for one cannot slip in without this test, and the image self-check, being looked at.
func TestTheSandboxStartsNoProgramOfItsOwn(t *testing.T) {
	fset := token.NewFileSet()
	for _, name := range []string{"sandbox.go", "container_sandbox.go", "kubernetes_sandbox.go"} {
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			if path == "os/exec" || path == "syscall" {
				t.Errorf("%s imports %s: a program the Station image may not have would be needed; add it to the image and to `launchway self-check`", name, path)
			}
		}
	}
}
