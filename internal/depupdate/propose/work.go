package propose

import (
	"fmt"
	"path"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/container"
	"github.com/jk-magic-creations/launchway/internal/depupdate/gomod"
	"github.com/jk-magic-creations/launchway/internal/depupdate/npm"
	"github.com/jk-magic-creations/launchway/internal/depupdate/plan"
	"github.com/jk-magic-creations/launchway/internal/depupdate/python"
	"github.com/jk-magic-creations/launchway/internal/depupdate/workflow"
)

// Toolchains are the images lock files are regenerated in. Defaults are
// version tags; an installation should pin them by digest and mirror them
// (the settings key "toolchains").
type Toolchains struct{ Go, Node, UV string }

// Defaults fills what is empty.
func (t Toolchains) Defaults() Toolchains {
	if t.Go == "" {
		t.Go = "docker.io/library/golang:1.27"
	}
	if t.Node == "" {
		t.Node = "docker.io/library/node:24"
	}
	if t.UV == "" {
		t.UV = "ghcr.io/astral-sh/uv:python3.13-bookworm"
	}
	return t
}

// FromPolicy reads the toolchain settings.
func FromPolicy(p plan.Policy) Toolchains {
	return Toolchains{Go: p.Toolchains["go"], Node: p.Toolchains["node"], UV: p.Toolchains["uv"]}.Defaults()
}

// Step is one toolchain run inside a container.
type Step struct {
	Dir      string     // repository-relative directory it runs in ("" is the root)
	Image    string     // the pinned toolchain image
	Commands [][]string // run in order, each stopping the step when it fails
	Allowed  []string   // repository-relative files the step may change
	Why      string     // for the log and the pull request
}

// Work is everything one proposal needs.
type Work struct {
	Edits map[string]string // file -> new text, made in the server (manifests only)
	Steps []Step
	Notes []string // things the pull request must say (a lock that the pipeline must regenerate, ...)
	// Files lists every file the proposal may end up changing.
	Files []string
}

// Reader reads a repository file at the base.
type Reader func(path string) (string, bool)

// Build works out the edits and steps of a proposal. Every update that
// cannot be done safely is an error naming it; the whole proposal is then
// not opened.
func Build(pr plan.Proposal, read Reader, tc Toolchains) (Work, error) {
	tc = tc.Defaults()
	w := Work{Edits: map[string]string{}}
	cur := func(p string) (string, bool) {
		if s, ok := w.Edits[p]; ok {
			return s, true
		}
		return read(p)
	}
	files := map[string]bool{}
	goDirs := map[string][]plan.Update{}
	npmDirs := map[string]bool{}
	uvDirs := map[string]string{}
	for _, u := range pr.Updates {
		d := u.Dep
		dir := depupdate.Dir(d.File)
		switch d.Ecosystem {
		case depupdate.Go:
			goDirs[dir] = append(goDirs[dir], u)
			files[join(dir, "go.mod")] = true
			if d.LockFile != "" {
				files[d.LockFile] = true
			}
		case depupdate.NPM:
			src, ok := cur(d.File)
			if !ok {
				return w, fmt.Errorf("%s cannot be read", d.File)
			}
			out, err := npm.EditManifest(src, d.Name, u.Target)
			if err != nil {
				return w, err
			}
			w.Edits[d.File] = out
			files[d.File] = true
			npmDirs[dir] = true
			if d.LockFile != "" {
				files[d.LockFile] = true
			} else {
				w.Notes = append(w.Notes, fmt.Sprintf("%s has no package-lock.json; none is created.", dir2(dir)))
			}
		case depupdate.Python:
			if err := pythonEdit(&w, files, uvDirs, cur, u); err != nil {
				return w, err
			}
		case depupdate.Container:
			src, ok := cur(d.File)
			if !ok {
				return w, fmt.Errorf("%s cannot be read", d.File)
			}
			oldRef := container.Ref{Repo: shortRepo(src, d), Tag: d.Constraint, Digest: d.Digest}
			newRef := container.Ref{Repo: oldRef.Repo, Tag: u.Target, Digest: ""}
			if d.Digest != "" {
				if u.Digest == "" {
					return w, fmt.Errorf("the digest of %s:%s is not known, so the pinned digest cannot be updated", d.Name, u.Target)
				}
				newRef.Digest = u.Digest
			}
			out, err := container.Edit(src, d.Line, oldRef.String(), newRef.String())
			if err != nil {
				return w, err
			}
			w.Edits[d.File] = out
			files[d.File] = true
		case depupdate.Workflow:
			src, ok := cur(d.File)
			if !ok {
				return w, fmt.Errorf("%s cannot be read", d.File)
			}
			oldRef, newRef, comment := d.Constraint, u.Target, ""
			if d.Digest != "" { // pinned by commit, version in the comment
				newRef, comment = u.Digest, u.Target
			}
			out, ok := workflow.Edit(src, d.Line, oldRef, newRef, comment)
			if !ok {
				return w, fmt.Errorf("%s line %d no longer has %s", d.File, d.Line, d.Name)
			}
			w.Edits[d.File] = out
			files[d.File] = true
		default:
			return w, fmt.Errorf("%s updates are not available yet", d.Ecosystem)
		}
	}
	for dir, us := range goDirs {
		versions := map[string]string{}
		var names []string
		for _, u := range us {
			versions[u.Dep.Name] = u.Target
			names = append(names, u.Dep.Name)
		}
		c := gomod.UpdateCommands(dir, versions, names)
		w.Steps = append(w.Steps, Step{Dir: dir, Image: tc.Go, Commands: c.Args,
			Allowed: []string{join(dir, "go.mod"), join(dir, "go.sum")}, Why: "go get, then go mod tidy: go.mod and go.sum are written by the Go tool"})
	}
	for dir := range npmDirs {
		c := npm.LockCommands(dir)
		w.Steps = append(w.Steps, Step{Dir: dir, Image: tc.Node, Commands: c.Args,
			Allowed: []string{join(dir, "package.json"), join(dir, "package-lock.json")}, Why: "npm writes only package-lock.json, and runs no install scripts"})
		files[join(dir, "package-lock.json")] = true
	}
	for dir, lock := range uvDirs {
		var names []string
		for _, u := range pr.Updates {
			if u.Dep.Ecosystem == depupdate.Python && depupdate.Dir(u.Dep.File) == dir && strings.HasSuffix(u.Dep.LockFile, "uv.lock") {
				names = append(names, u.Dep.Name)
			}
		}
		var cmds [][]string
		for _, n := range names {
			c, _ := python.LockCommand(dir, lock, n)
			cmds = append(cmds, c.Args...)
		}
		w.Steps = append(w.Steps, Step{Dir: dir, Image: tc.UV, Commands: cmds, Allowed: []string{lock}, Why: "uv writes only uv.lock"})
	}
	for f := range files {
		w.Files = append(w.Files, f)
	}
	sortStrings(w.Files)
	return w, nil
}

func pythonEdit(w *Work, files map[string]bool, uvDirs map[string]string, cur func(string) (string, bool), u plan.Update) error {
	d := u.Dep
	src, ok := cur(d.File)
	if !ok {
		return fmt.Errorf("%s cannot be read", d.File)
	}
	dir := depupdate.Dir(d.File)
	files[d.File] = true
	if path.Base(d.File) == "pyproject.toml" {
		out, err := python.EditPyproject(src, d.Name, u.Target)
		if err != nil {
			return err
		}
		w.Edits[d.File] = out
		switch {
		case strings.HasSuffix(d.LockFile, "uv.lock"):
			uvDirs[dir] = d.LockFile
			files[d.LockFile] = true
		case d.LockFile != "":
			w.Notes = append(w.Notes, fmt.Sprintf("%s was not regenerated: the pipeline must run the lock command for it (for Poetry: poetry lock) before merging.", d.LockFile))
		}
		return nil
	}
	out, relock, err := python.EditRequirements(src, d.Name, u.Target)
	if err != nil {
		return err
	}
	w.Edits[d.File] = out
	if relock {
		w.Notes = append(w.Notes, fmt.Sprintf("%s pinned hashes for %s. The old hashes were removed because they no longer match; regenerate them with the pipeline's lock step (pip-compile --generate-hashes) before merging.", d.File, d.Name))
	}
	return nil
}

// shortRepo returns the repository text as it is written on the line (the
// planner knows it normalized, e.g. with docker.io/library/ added only if the
// file had it): the name from discovery is exactly the text, so use it.
func shortRepo(_ string, d depupdate.Dep) string { return d.Name }

func dir2(d string) string {
	if d == "" {
		return "the repository root"
	}
	return d
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
