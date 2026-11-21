package gomod

import (
	"fmt"
	"strings"
)

// Command is what must run, in the module's directory and inside the pinned
// toolchain image, to move one dependency to a version and bring go.sum with
// it. It is never run on the server.
type Command struct {
	Dir  string
	Args [][]string
}

// UpdateCommands returns the toolchain steps for one or more updates:
// "go get module@version" for each, then "go mod tidy". Both write go.mod and
// go.sum; nothing here edits them by hand.
func UpdateCommands(dir string, updates map[string]string, names []string) Command {
	c := Command{Dir: dir}
	for _, n := range names {
		c.Args = append(c.Args, []string{"go", "get", n + "@" + updates[n]})
	}
	c.Args = append(c.Args, []string{"go", "mod", "tidy"})
	return c
}

// EditGoMod changes a required version in go.mod text directly. It is used
// for the preview and as the fallback to show the exact diff; the real change
// is made by UpdateCommands.
func EditGoMod(src, module, to string) (string, error) {
	lines := strings.Split(src, "\n")
	for i, raw := range lines {
		t := strings.TrimSpace(raw)
		t = strings.TrimPrefix(t, "require ")
		f := strings.Fields(t)
		if len(f) >= 2 && f[0] == module && strings.HasPrefix(f[1], "v") {
			lines[i] = strings.Replace(raw, f[1], to, 1)
			return strings.Join(lines, "\n"), nil
		}
	}
	return src, fmt.Errorf("%s is not required in go.mod", module)
}
