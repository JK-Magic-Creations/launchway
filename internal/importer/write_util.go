package importer

import (
	"strings"
)

// trimSlashes drops the slash that ends a directory: Launchway keeps a
// directory whole when it is named without it.
func trimSlashes(l []string) []string {
	out := make([]string, len(l))
	for i, s := range l {
		out[i] = strings.TrimRight(strings.TrimSuffix(strings.TrimSuffix(s, "**"), "*"), "/")
		if strings.HasSuffix(s, "*") && !strings.HasSuffix(s, "/**") && !strings.HasSuffix(s, "/*") {
			out[i] = s // a pattern such as *.xml keeps its star
		}
		if out[i] == "" {
			out[i] = "."
		}
	}
	return out
}

// tidyScript removes the indentation every line shares and the spaces that end lines,
// so the script is written as a readable block.
func tidyScript(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(s, "\n \t"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	minIndent := -1
	for i, l := range lines {
		l = strings.TrimRight(l, " \t")
		lines[i] = l
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if minIndent < 0 || n < minIndent {
			minIndent = n
		}
	}
	// The first line of a quoted block usually starts at the margin: dedent only when every line is indented.
	if minIndent > 0 {
		for i, l := range lines {
			if len(l) >= minIndent {
				lines[i] = l[minIndent:]
			}
		}
	}
	return strings.Join(lines, "\n")
}

// expandFetch turns a fetch of every workspace (Name "*") into one fetch for each
// workspace artifact kept by a job upstream.
func (w *writer) expandFetch(j *Job) []Fetch {
	var out []Fetch
	for _, f := range j.Fetch {
		if f.Name != "*" {
			out = append(out, f)
			continue
		}
		seen := map[string]bool{}
		var walk func(ids []string)
		walk = func(ids []string) {
			for _, id := range ids {
				if seen[id] || w.jobs[id] == nil {
					continue
				}
				seen[id] = true
				for _, a := range w.jobs[id].Artifacts {
					if strings.HasPrefix(a.Name, "workspace-") {
						out = append(out, Fetch{Name: a.Name, Into: f.Into})
					}
				}
				walk(w.jobs[id].Needs)
			}
		}
		walk(j.Needs)
	}
	return out
}
