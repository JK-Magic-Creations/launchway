// Package gomod reads Go modules: go.mod for what is required and which of
// it is only indirect, go.sum for the lock.
package gomod

import (
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
)

// Parser reads go.mod files.
type Parser struct{}

// Ecosystem is "go".
func (Parser) Ecosystem() string { return depupdate.Go }

// Detect matches every go.mod.
func (Parser) Detect(p string) bool { return depupdate.Base(p) == "go.mod" }

// Parse lists the required modules. The version in go.mod is the version in
// use (minimal version selection), so Current and Constraint are the same.
// A replace directive with a local path or a different module marks the
// dependency as replaced: it is listed but not updated.
func (Parser) Parse(f depupdate.File) ([]depupdate.Dep, []depupdate.Note, error) {
	_, hasSum := f.Sibling("go.sum")
	lock := ""
	if hasSum {
		lock = join(depupdate.Dir(f.Path), "go.sum")
	}
	var deps []depupdate.Dep
	var notes []depupdate.Note
	replaced := map[string]bool{}
	type pending struct {
		dep depupdate.Dep
	}
	var req []pending
	block := ""
	for i, raw := range strings.Split(string(f.Data), "\n") {
		line := strings.TrimSpace(raw)
		comment := ""
		if j := strings.Index(line, "//"); j >= 0 {
			comment = strings.TrimSpace(line[j+2:])
			line = strings.TrimSpace(line[:j])
		}
		if line == "" {
			continue
		}
		if block != "" {
			if line == ")" {
				block = ""
				continue
			}
			line = block + " " + line
		} else if strings.HasSuffix(line, "(") {
			block = strings.TrimSpace(strings.TrimSuffix(line, "("))
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "require":
			if len(fields) < 3 {
				return nil, nil, fmt.Errorf("go.mod line %d: a require needs a module and a version", i+1)
			}
			d := depupdate.Dep{Ecosystem: depupdate.Go, Name: fields[1], Current: fields[2], Constraint: fields[2],
				Direct: !strings.HasPrefix(comment, "indirect"), Group: depupdate.GroupProd, File: f.Path, Line: i + 1, LockFile: lock}
			req = append(req, pending{d})
		case "replace":
			if k := indexOf(fields, "=>"); k > 1 {
				replaced[fields[1]] = true
				notes = append(notes, depupdate.Note{File: f.Path, Message: fmt.Sprintf("%s is replaced in go.mod and is not updated", fields[1])})
			}
		case "tool":
			if len(fields) > 1 {
				// a tool is a package path; its module is matched below
				notes = append(notes, depupdate.Note{File: f.Path, Message: "tool directive: " + fields[1]})
			}
		}
	}
	for _, p := range req {
		if replaced[p.dep.Name] {
			continue
		}
		deps = append(deps, p.dep)
	}
	return deps, filterTool(notes), nil
}

func filterTool(n []depupdate.Note) []depupdate.Note {
	var out []depupdate.Note
	for _, x := range n {
		if !strings.HasPrefix(x.Message, "tool directive") {
			out = append(out, x)
		}
	}
	return out
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}
