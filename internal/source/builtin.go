package source

// The library that ships with Launchway (internal/starters): flight plans
// and step templates a pipeline uses as launchway/<name> with no
// registration. It has one version, Launchway's own, recorded in a run as a
// content hash where a Git library has a commit.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/starters"
)

// isBuiltin reports whether a pipeline's library is the one that ships with
// Launchway: the implicit entry, or one written with that repo and version.
func isBuiltin(l *pipeline.Library) bool {
	return l.Repo == starters.Name && l.Version == starters.Version
}

// builtinTemplate reads a template of the built-in library.
func builtinTemplate(name string) ([]byte, error) {
	if data, ok := starters.Template(name); ok {
		return data, nil
	}
	return nil, pipeline.ErrNoTemplate
}

// builtinProblem is a problem on one line of the pipeline file.
func builtinProblem(line int, path, msg string) pipeline.Problem {
	return pipeline.Problem{Line: line, Path: path, Message: msg}
}

// StarterKinds name what a template of the built-in library is.
const (
	KindFlightPlan   = "flight plan"
	KindStepTemplate = "step template"
)

// builtinInfo describes every template of the built-in library for the
// gallery: its parameters, a file that uses it, and what that file runs.
func builtinInfo() []pipeline.TemplateInfo {
	var out []pipeline.TemplateInfo
	for _, name := range starters.Templates() {
		data, _ := starters.Template(name)
		info := pipeline.DescribeTemplate(data, name)
		info.Kind = KindStepTemplate
		if strings.HasPrefix(info.Description, "Flight plan:") {
			info.Kind = KindFlightPlan
		}
		info.Example = StarterExample(name, info.Kind == KindFlightPlan)
		if p, err := ExpandBuiltin(info.Example); err == nil {
			info.Graph = graphOf(p)
		}
		out = append(out, info)
	}
	return out
}

// StarterExample is a pipeline file that uses a starter.
func StarterExample(name string, flight bool) string {
	if flight {
		return "pipeline: my-project\non:\n  push: { branches: [main] }\n  pull_request: {}\nsupersede: true\nsteps:\n  ci:\n    uses: launchway/" + name + "\n"
	}
	return "pipeline: my-project\non:\n  push: { branches: [main] }\nsteps:\n  " + strings.TrimSuffix(name, "-check") + ":\n    uses: launchway/" + name + "\n"
}

// ExpandBuiltin parses a pipeline that uses only the built-in library and
// expands it as a run would, without a server: for "show me what this does".
func ExpandBuiltin(text string) (*pipeline.Pipeline, error) {
	p, err := pipeline.Parse([]byte(text))
	if err != nil {
		return nil, err
	}
	if p, err = pipeline.Bind(p, pipeline.PreviewValues(p), pipeline.BindOptions{Preview: true}); err != nil {
		return nil, err
	}
	for _, l := range p.Libraries {
		if !isBuiltin(l) {
			return nil, fmt.Errorf("the pipeline uses the library %s, which comes from a server", l.Name)
		}
		l.Commit, l.RefKind = starters.Commit(), "builtin"
	}
	out, _, err := pipeline.ExpandWith(p, func(_ *pipeline.Library, name string) ([]byte, error) { return builtinTemplate(name) }, nil)
	return out, plainBuiltin(err)
}

func graphOf(p *pipeline.Pipeline) []pipeline.TemplateStep {
	var out []pipeline.TemplateStep
	for _, s := range p.Steps {
		out = append(out, pipeline.TemplateStep{ID: s.ID, Description: s.Description, Needs: s.Needs, Kind: s.Kind})
	}
	return out
}

// builtinTemplates is LibraryTemplates for the built-in library.
func builtinTemplates() *LibraryTemplates {
	return &LibraryTemplates{Library: starters.Name, Version: starters.Version, Commit: starters.Commit(),
		Versions: []string{starters.Version}, Templates: builtinInfo()}
}

// ExpandBound expands a bound pipeline that uses only the starters that ship
// with Launchway, and says where a runner gets them: for a run on one's own
// machine, with no server and no Git library.
func ExpandBound(p *pipeline.Pipeline) (*pipeline.Pipeline, []engine.LibrarySource, error) {
	for _, l := range p.Libraries {
		if !isBuiltin(l) {
			return nil, nil, fmt.Errorf("the pipeline uses the library %s, which comes from a server: run it there (launchway run reads only the starters that ship with Launchway)", l.Name)
		}
		l.Commit, l.RefKind = starters.Commit(), "builtin"
	}
	out, _, err := pipeline.ExpandWith(p, func(_ *pipeline.Library, name string) ([]byte, error) { return builtinTemplate(name) }, nil)
	if err != nil {
		return nil, nil, plainBuiltin(err)
	}
	var libs []engine.LibrarySource
	for _, l := range out.Libraries {
		libs = append(libs, engine.LibrarySource{Name: l.Name, URL: starters.URL, Commit: l.Commit})
	}
	return out, libs, nil
}

// plainBuiltin makes the problem of a starter that does not exist say which
// do: "the library launchway has no template go-servce: did you mean
// go-service?". Other problems pass through.
func plainBuiltin(err error) error {
	var ps pipeline.Problems
	if !errors.As(err, &ps) {
		return err
	}
	out := make(pipeline.Problems, len(ps))
	for i, p := range ps {
		out[i] = p
		const mark = "the library launchway has no template "
		if rest, ok := strings.CutPrefix(p.Message, mark); ok {
			name, _, _ := strings.Cut(rest, ":")
			msg := mark + name + ": the starters are " + strings.Join(starters.Templates(), ", ")
			if near := starters.Nearest(name); near != "" {
				msg = mark + name + ": did you mean " + near + "? (the starters are " + strings.Join(starters.Templates(), ", ") + ")"
			}
			out[i].Message = msg
		}
	}
	return out
}

// ExpandOffline is pipeline.ExpandOffline that also knows the library that
// ships with Launchway: a pipeline's use of launchway/<name> needs no
// --library for it (give one, as --library launchway=DIRECTORY, to expand a
// changed copy instead). The embedded files are written to a temporary
// directory for the expansion and removed after.
func ExpandOffline(data []byte, dirs map[string]string, o pipeline.BindOptions, vars pipeline.Variables) (*pipeline.Pipeline, error) {
	if _, ok := dirs[starters.Name]; !ok {
		if p, err := pipeline.Parse(data); err == nil && p.Library(starters.Name) != nil && isBuiltin(p.Library(starters.Name)) {
			dir, err := os.MkdirTemp("", "launchway-starters-")
			if err != nil {
				return nil, err
			}
			defer os.RemoveAll(dir)
			if err := writeStarters(dir); err != nil {
				return nil, err
			}
			copied := map[string]string{starters.Name: dir}
			for k, v := range dirs {
				copied[k] = v
			}
			dirs = copied
		}
	}
	return pipeline.ExpandOffline(data, dirs, o, vars)
}

func writeStarters(dir string) error {
	return fs.WalkDir(starters.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, p)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := starters.File(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}
