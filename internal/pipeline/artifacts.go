package pipeline

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Artifacts are files a step keeps once it has run: people download them
// from the run, and later steps of the run fetch them into their workspace
// (fetch:), on whichever runner they run.
//
//	steps:
//	  build:
//	    run: make dist
//	    artifacts:
//	      - name: binary
//	        paths: [bin/app, "dist/*.tar.gz"]
//	        retain: 30d          # optional; the server has a default and a maximum
//	        when: success        # success (the default), failure or always
//	  deploy:
//	    needs: [build]
//	    fetch:
//	      - from: build
//	        name: binary
//	        into: out            # optional; default: where the files were
//	    run: ./out/bin/app --version

// Artifact is a named set of files a step keeps.
type Artifact struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"` // globs relative to the workspace, as for reports
	// Retain is how long the files are kept; zero means the server's
	// default. The server never keeps them longer than its maximum.
	Retain Duration `json:"retain,omitempty"`
	// When says after which outcomes the files are kept: ArtifactOnSuccess
	// (the default, written as empty), ArtifactOnFailure or ArtifactAlways.
	When string `json:"when,omitempty"`
	Line int    `json:"-"`
}

// When an artifact is kept.
const (
	ArtifactOnSuccess = "success"
	ArtifactOnFailure = "failure" // a failed attempt, including a timeout: for screenshots, logs and core files
	ArtifactAlways    = "always"  // succeeded, failed or canceled by someone
)

// KeepAfter reports whether the artifact is kept after an attempt that
// ended with outcome: succeeded, failed or canceled.
func (a *Artifact) KeepAfter(outcome string) bool {
	switch a.When {
	case ArtifactAlways:
		return outcome == "succeeded" || outcome == "failed" || outcome == "canceled"
	case ArtifactOnFailure:
		return outcome == "failed"
	default:
		return outcome == "succeeded"
	}
}

// Fetch puts an artifact of an earlier step into the workspace before a
// step runs.
type Fetch struct {
	// From is the step that keeps the artifact. It must be one the fetching
	// step needs, directly or through other steps. In a template it is a
	// step of the same template.
	From string `json:"from"`
	Name string `json:"name"`
	// Into is a directory relative to the workspace that the files are put
	// in, keeping their paths below it; empty means the workspace itself,
	// so the files are where the earlier step had them.
	Into string `json:"into,omitempty"`
	// Member, when From is a loop, names one member by every key of the loop
	// (schema 27); without it every member's files are fetched, each in a
	// directory of its own below Into.
	Member map[string]string `json:"member,omitempty"`
	Line   int               `json:"-"`
}

// SBOMArtifact is the name of the artifact that keeps a step's SBOM files
// (reports.sbom).
const SBOMArtifact = "sbom"

// Limits on artifacts.
const (
	MaxArtifactsPerStep = 10
	MaxArtifactGlobs    = 20
	MaxFetchesPerStep   = 20
	MinArtifactRetain   = time.Hour
	MaxArtifactRetain   = 3650 * 24 * time.Hour
)

var artifactNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidArtifactName reports whether name can name an artifact: lower-case
// letters, digits, '.', '-' and '_', starting with a letter or digit, at
// most 64 characters.
func ValidArtifactName(name string) bool { return artifactNameRe.MatchString(name) }

// UsesSchema12 reports whether the pipeline uses keys that servers older
// than schema 12 do not know: artifacts, fetch, and reports.findings and
// reports.sbom.
func (p *Pipeline) UsesSchema12() bool {
	for _, s := range p.Steps {
		if len(s.Artifacts) > 0 || len(s.Fetch) > 0 || (s.Reports != nil && (len(s.Reports.Findings) > 0 || len(s.Reports.SBOM) > 0)) {
			return true
		}
	}
	return false
}

// ParseRetain reads how long to keep an artifact: a whole number of days
// (30d) or weeks (2w), or a duration such as 12h or 90m.
func ParseRetain(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if n, ok := strings.CutSuffix(s, suffix); ok {
			v, err := strconv.Atoi(n)
			if err != nil || v < 0 || v > 100000 {
				return 0, false
			}
			return time.Duration(v) * unit, true
		}
	}
	d, err := time.ParseDuration(s)
	return d, err == nil
}

// artifactText reads an artifact's name, a path or a fetch's into: text,
// or in a template a { param: … }. Run inputs and loop values are not
// allowed: an artifact's name and paths are fixed when the pipeline is
// written, and each member of a loop keeps its own artifacts anyway (they
// belong to its step). A script sees those values as LAUNCHWAY_INPUT_<NAME>
// and LAUNCHWAY_EACH_<KEY>, and can write its files to fixed paths.
func (ps *parser) artifactText(n *yaml.Node, at string) string {
	if n.Kind == yaml.MappingNode && (hasKey(n, "input") || hasKey(n, "each")) {
		ps.add(n, at, "artifact names and paths are written as text; { input: … } and { each: … } are not allowed here "+
			"(the step's script sees them as LAUNCHWAY_INPUT_<NAME> and LAUNCHWAY_EACH_<KEY>, and each member of a loop keeps its own artifacts)")
		return ""
	}
	return ps.scalar(n, at)
}

// parseArtifacts reads a step's artifacts: a list of { name, paths, retain,
// when }.
func (ps *parser) parseArtifacts(n *yaml.Node, at string) []*Artifact {
	if n.Kind != yaml.SequenceNode {
		ps.add(n, at, "artifacts is a list, for example: artifacts: [{ name: binary, paths: [bin/app] }]")
		return nil
	}
	if len(n.Content) == 0 {
		ps.add(n, at, "list at least one artifact, or leave artifacts out")
		return nil
	}
	if len(n.Content) > MaxArtifactsPerStep {
		ps.add(n, at, "at most %d artifacts per step", MaxArtifactsPerStep)
	}
	var out []*Artifact
	for i, item := range n.Content {
		if i >= MaxArtifactsPerStep {
			break
		}
		where := at + "[" + strconv.Itoa(i) + "]"
		if item.Kind != yaml.MappingNode {
			ps.add(item, where, "an artifact is a mapping with a name and paths, for example: { name: binary, paths: [bin/app] }")
			continue
		}
		a := &Artifact{Line: item.Line}
		var pathsNode *yaml.Node
		ps.eachKey(item, where, []string{"name", "paths", "retain", "when"}, func(k, v *yaml.Node) {
			w := where + "." + k.Value
			switch k.Value {
			case "name":
				a.Name = ps.artifactText(v, w)
				if a.Name != "" && !ValidArtifactName(a.Name) {
					ps.add(v, w, "%q is not a valid artifact name: use lower-case letters, digits, '.', '-' and '_', at most 64", clipText(a.Name, 60))
				}
			case "paths":
				pathsNode = v
				a.Paths = ps.artifactGlobs(v, w)
			case "retain":
				d, ok := ParseRetain(ps.scalar(v, w))
				switch {
				case !ok:
					ps.add(v, w, "write how long to keep the files, such as 30d, 2w or 12h")
				case d < MinArtifactRetain || d > MaxArtifactRetain:
					ps.add(v, w, "retain must be between 1h and 3650d")
				default:
					a.Retain = Duration(d)
				}
			case "when":
				switch w2 := ps.scalar(v, w); w2 {
				case ArtifactOnSuccess:
				case ArtifactOnFailure, ArtifactAlways:
					a.When = w2
				default:
					ps.add(v, w, "when is success, failure or always")
				}
			}
		})
		if a.Name == "" {
			ps.add(item, where+".name", "an artifact needs a name, such as name: binary")
		}
		if pathsNode == nil {
			ps.add(item, where+".paths", "say which files to keep, such as paths: [bin/app, \"dist/*.tar.gz\"]")
		}
		out = append(out, a)
	}
	return out
}

// artifactGlobs reads the globs of an artifact's paths.
func (ps *parser) artifactGlobs(v *yaml.Node, where string) []string {
	items := []*yaml.Node{v}
	if v.Kind == yaml.SequenceNode {
		items = v.Content
	}
	before := len(ps.problems)
	switch {
	case len(items) == 0:
		ps.add(v, where, "list at least one file or glob, such as paths: [bin/app]")
	case len(items) > MaxArtifactGlobs:
		ps.add(v, where, "at most %d globs", MaxArtifactGlobs)
		items = items[:MaxArtifactGlobs]
	}
	var out []string
	seen := map[string]bool{}
	for _, item := range items {
		if item.Kind == yaml.SequenceNode {
			ps.add(item, where, "expected a glob or a list of globs")
			continue
		}
		var list []string
		if item.Kind == yaml.MappingNode {
			list = []string{ps.artifactText(item, where)}
		} else {
			list = ps.valueList(item, where)
		}
		if len(list) == 0 && item.Kind == yaml.ScalarNode {
			list = []string{""}
		}
		for _, g := range list {
			if g == "" && item.Kind == yaml.MappingNode {
				continue // said why above
			}
			clean, msg := checkGlob(g, "artifact files")
			if msg != "" {
				ps.add(item, where, "%s", msg)
				continue
			}
			if !seen[clean] {
				seen[clean] = true
				out = append(out, clean)
			}
		}
	}
	if len(out) == 0 && len(ps.problems) == before {
		ps.add(v, where, "no glob is left: list at least one file or glob")
	}
	return out
}

// parseFetch reads what a step fetches: a list of { from, name, into }.
func (ps *parser) parseFetch(n *yaml.Node, at string) []*Fetch {
	if n.Kind != yaml.SequenceNode {
		ps.add(n, at, "fetch is a list, for example: fetch: [{ from: build, name: binary }]")
		return nil
	}
	if len(n.Content) == 0 {
		ps.add(n, at, "list at least one artifact to fetch, or leave fetch out")
		return nil
	}
	if len(n.Content) > MaxFetchesPerStep {
		ps.add(n, at, "at most %d fetches per step", MaxFetchesPerStep)
	}
	var out []*Fetch
	for i, item := range n.Content {
		if i >= MaxFetchesPerStep {
			break
		}
		where := at + "[" + strconv.Itoa(i) + "]"
		if item.Kind != yaml.MappingNode {
			ps.add(item, where, "a fetch is a mapping, for example: { from: build, name: binary }")
			continue
		}
		f := &Fetch{Line: item.Line}
		ps.eachKey(item, where, []string{"from", "name", "into", "member"}, func(k, v *yaml.Node) {
			w := where + "." + k.Value
			switch k.Value {
			case "from":
				f.From = ps.str(v, w)
			case "name":
				f.Name = ps.artifactText(v, w)
			case "member":
				f.Member = ps.parseFetchMember(v, w)
			case "into":
				into, msg := CheckInto(ps.artifactText(v, w))
				if msg != "" {
					ps.add(v, w, "%s", msg)
				}
				f.Into = into
			}
		})
		if f.From == "" {
			ps.add(item, where+".from", "say which step keeps the artifact, such as from: build")
		}
		if f.Name == "" {
			ps.add(item, where+".name", "say which artifact, such as name: binary")
		}
		out = append(out, f)
	}
	return out
}

// CheckInto checks the directory a fetch puts files in, relative to the
// workspace, and returns it tidied ("" for the workspace itself), or why it
// cannot be used.
func CheckInto(dir string) (string, string) {
	d := strings.TrimSpace(dir)
	for strings.HasPrefix(d, "./") {
		d = strings.TrimLeft(d[2:], "/")
	}
	d = strings.TrimRight(d, "/")
	if d == "" || d == "." {
		return "", ""
	}
	quoted := "\"" + clipText(dir, 60) + "\""
	switch {
	case len(d) > MaxReportGlobLen:
		return "", quoted + " is too long"
	case strings.HasPrefix(d, "/") || strings.HasPrefix(d, "~"):
		return "", quoted + " is not relative to the workspace: write it without a leading / or ~"
	case strings.ContainsAny(d, "\\*?[") || strings.ContainsFunc(d, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return "", quoted + " is not a plain directory name"
	}
	for _, part := range strings.Split(d, "/") {
		switch part {
		case "..":
			return "", quoted + " leaves the workspace (..)"
		case "", ".":
			return "", quoted + " has an empty path part (// or /./)"
		case ".git":
			return "", quoted + " is inside .git"
		}
	}
	return d, ""
}

// checkArtifacts checks a file's artifacts and fetches against its steps:
// names are unique per step, and each fetch names a step of the same file
// that the fetching step needs, directly or not, and the artifact it keeps.
func (ps *parser) checkArtifacts(p *Pipeline) {
	ids := map[string]*Step{}
	for _, s := range p.Steps {
		ids[s.ID] = s
	}
	for _, s := range p.Steps {
		names := map[string]bool{}
		if s.Reports != nil && len(s.Reports.SBOM) > 0 {
			names[SBOMArtifact] = true
		}
		for _, a := range s.Artifacts {
			if a.Name == "" {
				continue
			}
			at := "steps." + s.ID + ".artifacts"
			switch {
			case a.Name == SBOMArtifact && s.Reports != nil && len(s.Reports.SBOM) > 0:
				ps.problems = append(ps.problems, Problem{Line: a.Line, Path: at,
					Message: "the artifact name sbom is taken by reports.sbom in this step; choose another"})
			case reservedArtifact(s, a.Name) != "":
				ps.problems = append(ps.problems, Problem{Line: a.Line, Path: at, Message: reservedArtifact(s, a.Name)})
			case names[a.Name]:
				ps.problems = append(ps.problems, Problem{Line: a.Line, Path: at, Message: "artifact " + a.Name + " is listed twice"})
			}
			names[a.Name] = true
		}
		upstream := upstreamOf(s, ids)
		fetched := map[string]bool{}
		for _, f := range s.Fetch {
			if f.From == "" || f.Name == "" {
				continue
			}
			at := "steps." + s.ID + ".fetch"
			from := ids[f.From]
			problem := ""
			switch {
			case f.From == s.ID:
				problem = "a step cannot fetch its own artifacts; they are kept after it runs"
			case from == nil && fromTemplateLoop(f.From, ids) != nil:
				// A loop inside a template, such as ci/build: checked once the template is read.
				if !upstream[fromTemplateLoop(f.From, ids).ID] {
					problem = "fetches from " + f.From + ", which this step does not need; add " + fromTemplateLoop(f.From, ids).ID + " to needs so it has run first"
				}
			case from == nil:
				problem = "fetches from " + quoteText(f.From) + ", but there is no step with that name"
			case f.Member != nil && from.ForEach == nil:
				problem = "member goes with a loop, and " + f.From + " is not repeated by for_each"
			case from.IsUse():
				problem = f.From + " uses a template; fetching an artifact a template's step keeps is not supported yet"
			case from.ForEach != nil && loopFetchProblem(f, from) != "":
				problem = loopFetchProblem(f, from)
			case !upstream[f.From]:
				problem = "fetches from " + f.From + ", which this step does not need; add it to needs so it has run first"
			case !keeps(from, f.Name):
				problem = f.From + " keeps no artifact called " + quoteText(f.Name)
			case s.When.UpstreamRule() == UpstreamFailed && keptOnlyOnSuccess(from, f.Name):
				problem = "this step runs only when a step it needs failed, but " + f.From + " keeps " + f.Name +
					" only when it succeeds; keep it with when: failure or when: always"
			case fetched[f.From+"\x00"+f.Name+"\x00"+fmt.Sprint(f.Member)]:
				problem = f.From + "'s " + f.Name + " is fetched twice"
			}
			if problem != "" {
				ps.problems = append(ps.problems, Problem{Line: f.Line, Path: at, Message: problem})
			}
			fetched[f.From+"\x00"+f.Name+"\x00"+fmt.Sprint(f.Member)] = true
		}
	}
}

// keptOnlyOnSuccess reports whether a step keeps the artifact only after
// it succeeds.
func keptOnlyOnSuccess(s *Step, name string) bool {
	for _, a := range s.Artifacts {
		if a.Name == name {
			return a.When == "" || a.When == ArtifactOnSuccess
		}
	}
	return false // the SBOM is kept whatever happened
}

// keeps reports whether a step keeps an artifact of that name.
func keeps(s *Step, name string) bool {
	if name == SBOMArtifact && s.Reports != nil && len(s.Reports.SBOM) > 0 {
		return true
	}
	for _, a := range s.Artifacts {
		if a.Name == name {
			return true
		}
	}
	return false
}

// upstreamOf returns the steps s needs, directly or through others.
func upstreamOf(s *Step, ids map[string]*Step) map[string]bool {
	seen := map[string]bool{}
	var walk func(*Step)
	walk = func(t *Step) {
		for _, n := range t.Needs {
			if seen[n] {
				continue
			}
			seen[n] = true
			if dep := ids[n]; dep != nil {
				walk(dep)
			}
		}
	}
	walk(s)
	return seen
}

func quoteText(s string) string { return "\"" + clipText(s, 60) + "\"" }

// fetchesOf gives a template's step's fetches the run's step IDs.
func (x *expander) fetchesOf(sc *scope, s *Step) []*Fetch {
	if len(s.Fetch) == 0 {
		return nil
	}
	out := make([]*Fetch, 0, len(s.Fetch))
	for _, f := range s.Fetch {
		c := *f
		c.From = sc.prefix + f.From
		if t := sc.byID[f.From]; t != nil && dropped(t) {
			x.problem(sc, f.Line, 0, "steps."+s.ID+".fetch", "fetches from %s, which the template leaves out (its if is false)", f.From)
		}
		out = append(out, &c)
	}
	return out
}
