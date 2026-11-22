package steptest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/jk-magic-creations/launchway/internal/stepkit"
	"github.com/jk-magic-creations/launchway/sdk"
)

// Languages a step can be scaffolded in.
var Languages = []string{"go", "python", "ts", "sh"}

type lang struct {
	toolchain string
	command   string
	kit       string // path below sdk/
	kitName   string // the kit's file name in the step (under kitDir)
	kitDir    string
	files     map[string]string // name → template text
}

var langs = map[string]lang{
	"go": {toolchain: "go@1.25", command: "go run .", kit: "steps/go/lwstep/lwstep.go", kitName: "lwstep.go", kitDir: "lwstep",
		files: map[string]string{"main.go": goMain, "main_test.go": goTest, "go.mod": goMod}},
	"python": {toolchain: "python@3.13", command: "python3 -B main.py", kit: "steps/python/launchway_step.py", kitName: "launchway_step.py",
		files: map[string]string{"main.py": pyMain, "test_main.py": pyTest}},
	"ts": {toolchain: "node@24", command: "node main.ts", kit: "js/src/step.ts", kitName: "launchway-step.ts",
		files: map[string]string{"main.ts": tsMain, "main.test.ts": tsTest}},
	"sh": {toolchain: "sh", command: "sh main.sh", kit: "steps/sh/launchway-step.sh", kitName: "launchway-step.sh",
		files: map[string]string{"main.sh": shMain, "test.sh": shTest}},
}

// Scaffold writes a new step in dir (steps/<name> of the repository at root)
// with a manifest, the kit, the code, a test and a README. It refuses to
// overwrite anything.
func Scaffold(root, name, language string) (string, error) {
	l, ok := langs[language]
	if !ok {
		return "", fmt.Errorf("--lang is one of %s", strings.Join(Languages, ", "))
	}
	if !stepkit.StepNameRe.MatchString(name) {
		return "", fmt.Errorf("%q is not a step name: use lower-case letters, digits and - (at most 63)", name)
	}
	dir := filepath.Join(root, stepkit.StepDir, name)
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("%s exists already", dir)
	}
	kit, err := sdk.Kits.ReadFile(l.kit)
	if err != nil {
		return "", err
	}
	data := struct{ Name, Lang, Toolchain, Command, Kit string }{name, language, l.toolchain, l.command, l.kitName}
	write := func(rel, text string, raw bool) error {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: source files of a repository, meant to be read by the team
			return err
		}
		if !raw {
			t, err := template.New(rel).Delims("[[", "]]").Parse(text)
			if err != nil {
				return err
			}
			var b strings.Builder
			if err := t.Execute(&b, data); err != nil {
				return err
			}
			text = b.String()
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(path, []byte(text), mode) //nolint:gosec // G306,G703: source files of a repository, in the directory the person named
	}
	if err := write("step.yaml", manifestText, false); err != nil {
		return "", err
	}
	if err := write("README.md", readmeText, false); err != nil {
		return "", err
	}
	kitPath := l.kitName
	if l.kitDir != "" {
		kitPath = l.kitDir + "/" + l.kitName
	}
	if err := write(kitPath, string(kit), true); err != nil {
		return "", err
	}
	for rel, text := range l.files {
		if err := write(rel, text, false); err != nil {
			return "", err
		}
	}
	// The step's own scratch directory must not be committed.
	if gi := filepath.Join(root, ".gitignore"); fileExists(filepath.Join(root, ".git")) {
		b, _ := os.ReadFile(gi)
		if !strings.Contains(string(b), ".launchway/step-io") {
			add := "# Launchway: files a step attempt writes while it runs\n.launchway/step-io/\n.launchway/lib/\n"
			if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
				add = "\n" + add
			}
			_ = os.WriteFile(gi, append(b, add...), 0o644) //nolint:gosec // G306,G703: the repository's own .gitignore, which is read by the team
		}
	}
	return dir, nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

const manifestText = `# The manifest of the step "[[.Name]]": what it takes, what it gives, what it may do.
# Reference: docs/reference/step-manifest.md
step: [[.Name]]
contract: 1
description: Greet someone and say how long the greeting is
toolchain: [[.Toolchain]]
command: [[.Command]]

inputs:
  who: { type: string, default: world, description: Whom to greet, pattern: "^[A-Za-z][A-Za-z ]{0,39}$" }
  shout: { type: boolean, default: false, description: Greet in capital letters }

outputs:
  greeting: { type: string, description: The greeting }
  length: { type: number, description: How many characters it has }

# A step that asks for nothing can reach no network and write no file of the
# workspace. List what it needs, and a person who adds the step sees it:
# permissions:
#   network: [registry.npmjs.org]
#   write: [dist]
`

const readmeText = `# [[.Name]]

A Launchway custom step written in [[.Lang]]. It greets someone: the input ` + "`who`" + ` (and ` + "`shout`" + `) in,
the outputs ` + "`greeting`" + ` and ` + "`length`" + ` out, and a summary and a note on the step's page.

## Try it

    launchway step test steps/[[.Name]] --with who=Ada

runs it as a Station would (in its container when Podman or Docker is there, with no network and a
read-only workspace) and checks that it keeps to the step contract.

## Use it

In a pipeline of this repository:

    steps:
      greet:
        uses: ./steps/[[.Name]]
        with: { who: Ada }

To share it, put the ` + "`steps/[[.Name]]`" + ` directory in a library, tag a release, and pipelines use
` + "`uses: <library>/[[.Name]]`" + ` at that tag. ` + "`launchway step validate`" + ` checks the manifest.

The kit ([[.Kit]]) is a copy of the published one; update it by running ` + "`launchway step new`" + ` in a scratch
directory and copying the file.
`
