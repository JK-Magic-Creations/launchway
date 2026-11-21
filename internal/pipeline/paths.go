package pipeline

// Path filters: on.push.paths, on.push.paths_ignore (the same for
// on.pull_request) and a step's when: { changed: [...] }.
//
// Glob rules, which are the same everywhere:
//   - A path is relative to the repository root, written with `/`. A leading
//     `/` in a pattern is ignored.
//   - `*` matches any characters but `/`, `?` one character but `/`, and
//     `[abc]` one of them. `**` as a whole segment matches any number of
//     directories, including none: `src/**` is everything under src, `**/*.md`
//     every Markdown file at any depth. A pattern ending in `/` means
//     everything under that directory.
//   - Case matters: `README.md` is not `readme.md`.
//   - In a list, a pattern starting with `!` takes back what the patterns
//     before it matched: `[docs/**, "!docs/api/**"]`. A list that starts with
//     a `!` pattern begins with every file selected.
//   - A renamed file counts under both its old and new path, and a file that
//     was deleted under the path it had.

import (
	"fmt"
	"path"
	"strings"
)

// ChangeSet is the files a push or a pull request changed. Known is false when
// they cannot be told (the first push of a branch, a force-push that rewrote
// history, too many files, a run started by hand or by a schedule): every
// filter then lets the run, and every step, go ahead, and Why says so.
type ChangeSet struct {
	Known bool     `json:"known"`
	Files []string `json:"files,omitempty"`
	Why   string   `json:"why,omitempty"`
}

// MaxChangedFiles is the most files a ChangeSet lists; with more, the change
// is not known.
const MaxChangedFiles = 1000

// GlobMatch reports whether the file matches the pattern (see above).
func GlobMatch(pattern, file string) bool {
	pattern = strings.TrimPrefix(pattern, "/")
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegments(pat, file []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(file); i++ {
				if matchSegments(pat[1:], file[i:]) {
					return true
				}
			}
			return false
		}
		if len(file) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], file[0]); !ok {
			return false
		}
		pat, file = pat[1:], file[1:]
	}
	return len(file) == 0
}

// CheckGlob says what is wrong with a pattern, or "".
func CheckGlob(pattern string) string {
	p := strings.TrimPrefix(strings.TrimPrefix(pattern, "!"), "/")
	switch {
	case strings.TrimSpace(p) == "":
		return "a path pattern cannot be empty"
	case strings.Contains(p, "\\"):
		return "write paths with / and no backslashes"
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "a path pattern cannot go up with .."
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Sprintf("%q is not a valid pattern (%v)", pattern, err)
		}
	}
	return ""
}

// Selects reports whether the patterns select the file: the last pattern that
// matches it decides (a `!` pattern deselects).
func Selects(patterns []string, file string) bool {
	selected := len(patterns) > 0 && strings.HasPrefix(patterns[0], "!")
	for _, p := range patterns {
		if neg, ok := strings.CutPrefix(p, "!"); ok {
			if GlobMatch(neg, file) {
				selected = false
			}
		} else if GlobMatch(p, file) {
			selected = true
		}
	}
	return selected
}

// PathFilter is a trigger's paths and paths_ignore (only one is set).
type PathFilter struct {
	Paths  []string `json:"paths,omitempty"`
	Ignore []string `json:"paths_ignore,omitempty"`
}

// Active says the trigger filters by path at all.
func (f PathFilter) Active() bool { return len(f.Paths) > 0 || len(f.Ignore) > 0 }

// Decide says whether a change starts a run, and when it does not, why in
// words. An unknown change always runs.
func (f PathFilter) Decide(c *ChangeSet) (run bool, why string) {
	if !f.Active() || c == nil || !c.Known {
		return true, ""
	}
	if len(c.Files) == 0 {
		return true, "" // nothing to judge by (an empty commit): do not guess
	}
	if len(f.Paths) > 0 {
		for _, file := range c.Files {
			if Selects(f.Paths, file) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("no changed file matches paths (%s); changed: %s", strings.Join(f.Paths, ", "), fileList(c.Files))
	}
	for _, file := range c.Files {
		if !Selects(f.Ignore, file) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("every changed file matches paths_ignore (%s); changed: %s", strings.Join(f.Ignore, ", "), fileList(c.Files))
}

func fileList(files []string) string {
	if len(files) <= 3 {
		return strings.Join(files, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(files[:3], ", "), len(files)-3)
}

// ChangedWhy explains why the condition's changed test keeps a step from
// running, or returns "" if it holds, or the change is not known.
func (c *Condition) ChangedWhy(ctx Context) string {
	if c == nil || len(c.Changed) == 0 || ctx.Changed == nil || !ctx.Changed.Known {
		return ""
	}
	for _, file := range ctx.Changed.Files {
		if Selects(c.Changed, file) {
			return ""
		}
	}
	return PathSkipPrefix + "no file under " + describeGlobs(c.Changed) + " changed"
}

// PathSkipPrefix starts the reason of a step skipped by a path rule.
const PathSkipPrefix = "skipped by path rule: "

func describeGlobs(globs []string) string {
	var out []string
	for _, g := range globs {
		if strings.HasPrefix(g, "!") {
			continue
		}
		out = append(out, strings.TrimSuffix(strings.TrimSuffix(g, "**"), "/*"))
	}
	if len(out) == 0 {
		return strings.Join(globs, ", ")
	}
	return strings.Join(out, " or ")
}

// PathDescription lists what a trigger's filter selects, for `launchway validate`.
func (f PathFilter) PathDescription() string {
	switch {
	case len(f.Paths) > 0:
		return "runs only when a changed file matches " + strings.Join(f.Paths, ", ")
	case len(f.Ignore) > 0:
		return "runs unless every changed file matches " + strings.Join(f.Ignore, ", ")
	}
	return "runs for every change"
}

// PathNotes says, in words, which changes start the pipeline and which steps
// depend on the files changed: what `launchway validate` prints.
func (p *Pipeline) PathNotes() []string {
	var out []string
	always := "; the first push of a branch, a force-push, a tag and a change of more than 1000 files always run"
	if f := p.On.PushPaths(); f.Active() {
		out = append(out, "on.push: "+f.PathDescription()+always)
	}
	if f := p.On.PullRequestPaths(); f.Active() {
		out = append(out, "on.pull_request: "+f.PathDescription()+"; compared with the merge base of the target branch")
	}
	for _, s := range p.Steps {
		if s.When != nil && len(s.When.Changed) > 0 {
			out = append(out, fmt.Sprintf("step %s: runs only when a changed file matches %s; otherwise it is skipped (and counts as passed for required checks)", s.ID, strings.Join(s.When.Changed, ", ")))
		}
	}
	return out
}
