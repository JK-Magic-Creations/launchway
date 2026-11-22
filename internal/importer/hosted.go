package importer

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Reader for the hosted-workflow format (.github/workflows/*.yml). Every
// construct is taken from the format's own reference; testdata/sources.md names
// the page of each. Nothing in the file is evaluated: ${{ }} expressions are
// matched against a closed list of simple forms and the rest are reported.

func init() {
	register(&Format{
		ID: "hosted-workflows", Title: "Hosted workflow files", Where: ".github/workflows/*.yml, and the same layout in a self-hosted forge's own dot-directory (.<forge>/workflows/*.yml)",
		Detect: func(p string, head []byte) bool {
			return workflowPath.MatchString(p) && bytes.Contains(head, []byte("jobs:")) || strings.HasPrefix(p, ".github/workflows/") && workflowPath.MatchString(p)
		},
		Read:     readHosted,
		Table:    hostedTable,
		Sections: []string{"Triggers", "Jobs", "Steps", "Expressions", "Actions"},
	})
}

var workflowPath = regexp.MustCompile(`^\.[a-z0-9_-]+/workflows/[^/]+\.ya?ml$`)

type hostedJob struct {
	j         *Job
	secrets   map[string]bool
	setupIm   string
	setups    int
	homePaths []string
}

func readHosted(root File, _ map[string]File, lim Limits) *Pipeline {
	p := &Pipeline{Format: "hosted-workflows", File: root.Path}
	rp := reporter{table: hostedTable, file: root.Path, out: &p.Findings}
	tree, err := ParseTree(root.Content, lim)
	if err != nil {
		p.Findings = append(p.Findings, Finding{Code: "read.error", Class: Manual, Message: err.Error(), Next: "Fix the file, or import it by hand", File: root.Path})
		return p
	}
	if !tree.IsMap() || !tree.Has("jobs") {
		p.Findings = append(p.Findings, Finding{Code: "read.shape", Class: Manual, Message: "This file has no jobs section, so it is not a workflow file", Next: "Choose another file", File: root.Path, Line: 1})
		return p
	}
	p.Name = tree.Get("name").Str()
	if p.Name == "" {
		p.Name = strings.TrimSuffix(path.Base(root.Path), path.Ext(root.Path))
	}
	h := &hostedReader{p: p, rp: rp, inputs: map[string]bool{}, vars: map[string]bool{}, seenRunsOn: map[string]bool{}}
	h.triggers(tree)
	h.workflowEnv(tree)
	if c := tree.Get("concurrency"); c != nil {
		if v, _ := c.Get("cancel-in-progress").Bool(); v {
			p.Supersede = true
			rp.note("hosted.concurrency", tree.LineOf("concurrency"))
		} else {
			rp.note("hosted.concurrency.queue", tree.LineOf("concurrency"))
		}
	}
	if tree.Has("permissions") {
		rp.note("hosted.permissions", tree.LineOf("permissions"))
	}
	if d := tree.Get("defaults").Get("run"); d != nil {
		h.defDir = d.Get("working-directory").Str()
		if d.Has("shell") {
			rp.note("hosted.defaults", d.LineOf("shell"))
		}
		if h.defDir != "" {
			rp.note("hosted.defaults", d.LineOf("working-directory"))
		}
	}
	jobs := tree.Get("jobs")
	for i, id := range jobs.Keys {
		h.job(id, jobs.Vals[i])
	}
	for v := range h.vars {
		_ = v
	}
	sortFindings(p.Findings)
	return p
}

type hostedReader struct {
	p          *Pipeline
	rp         reporter
	inputs     map[string]bool
	vars       map[string]bool
	defDir     string
	seenRunsOn map[string]bool
}

var branchPat = regexp.MustCompile(`^!`)

func (h *hostedReader) triggers(t *N) {
	on := t.Get("on")
	if on == nil {
		// "on" is a YAML 1.1 boolean in some parsers; the library keeps it as text, but a file may say true.
		on = t.Get("true")
	}
	if on == nil {
		return
	}
	tr := &h.p.On
	tr.Present = true
	line := t.LineOf("on")
	events := map[string]*N{}
	switch on.Kind {
	case kScalar:
		events[on.Val] = &N{Kind: kNull, Line: on.Line}
	case kSeq:
		for _, it := range on.Items {
			events[it.Val] = &N{Kind: kNull, Line: it.Line}
		}
	case kMap:
		for i, k := range on.Keys {
			events[k] = on.Vals[i]
		}
	}
	order := []string{}
	if on.Kind == kMap {
		order = on.Keys
	} else {
		for _, it := range on.Items {
			order = append(order, it.Val)
		}
		if on.Kind == kScalar {
			order = []string{on.Val}
		}
	}
	for _, ev := range order {
		cfg := events[ev]
		ln := line
		if on.Kind == kMap {
			ln = on.LineOf(ev)
		} else if cfg != nil {
			ln = cfg.Line
		}
		switch ev {
		case "push":
			pu := &Push{Branches: cfg.Get("branches").Strings(), Tags: cfg.Get("tags").Strings(), Paths: cfg.Get("paths").Strings(), PathsIgnore: cfg.Get("paths-ignore").Strings()}
			h.ignored(cfg, ln, "branches-ignore", "tags-ignore")
			pu.Branches = h.positive(pu.Branches, ln)
			pu.Tags = h.positive(pu.Tags, ln)
			pu.Paths = h.positive(pu.Paths, ln)
			if len(pu.Paths) > 0 && len(pu.PathsIgnore) > 0 {
				h.rp.note("hosted.on.ignore", ln, "paths-ignore (it cannot be combined with paths)")
				pu.PathsIgnore = nil
			}
			if len(pu.Branches)+len(pu.Tags) == 0 && !cfg.Has("branches-ignore") && !cfg.Has("tags-ignore") {
				pu.Branches = []string{"*"}
			}
			if len(pu.Branches)+len(pu.Tags) == 0 {
				pu.Branches = []string{"*"}
			}
			tr.Push = pu
			h.rp.note("hosted.on.push", ln)
		case "pull_request":
			pr := &PullRequest{Into: h.positive(cfg.Get("branches").Strings(), ln), Paths: h.positive(cfg.Get("paths").Strings(), ln), PathsIgnore: cfg.Get("paths-ignore").Strings()}
			h.ignored(cfg, ln, "branches-ignore")
			if len(pr.Paths) > 0 {
				pr.PathsIgnore = nil
			}
			tr.PR = pr
			h.rp.note("hosted.on.pr", ln)
		case "pull_request_target":
			h.rp.note("hosted.on.pr_target", ln)
		case "schedule":
			for _, s := range cfg.List() {
				if c := s.Get("cron").Str(); c != "" {
					tr.Schedules = append(tr.Schedules, Schedule{Cron: c, Zone: s.Get("timezone").Str()})
				}
			}
			h.rp.note("hosted.on.schedule", ln)
		case "workflow_dispatch":
			tr.Manual = true
			if in := cfg.Get("inputs"); in.IsMap() {
				for i, name := range in.Keys {
					h.input(name, in.Vals[i])
				}
			}
			h.rp.note("hosted.on.dispatch", ln)
		default:
			h.rp.note("hosted.on.other", ln, ev)
		}
	}
}

func (h *hostedReader) ignored(cfg *N, line int, keys ...string) {
	for _, k := range keys {
		if cfg.Has(k) {
			h.rp.note("hosted.on.ignore", cfg.LineOf(k), k)
		}
	}
}

// positive drops patterns that start with ! and reports them.
func (h *hostedReader) positive(l []string, line int) []string {
	var out []string
	for _, s := range l {
		if branchPat.MatchString(s) {
			h.rp.note("hosted.on.ignore", line, "the pattern "+s)
			continue
		}
		out = append(out, s)
	}
	return out
}

var inputNameRe = regexp.MustCompile(`[^a-z0-9_-]+`)

func inputName(s string) string {
	s = inputNameRe.ReplaceAllString(strings.ToLower(s), "_")
	s = strings.Trim(s, "_-")
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "input_" + s
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func (h *hostedReader) input(name string, n *N) {
	in := Input{Name: inputName(name), Description: n.Get("description").Str(), Default: n.Get("default").Str()}
	if r, _ := n.Get("required").Bool(); r && in.Default == "" {
		in.Required = true
	}
	switch t := n.Get("type").Str(); t {
	case "boolean":
		in.Type = "boolean"
		if in.Default == "" {
			in.Default = "false"
		}
	case "choice":
		in.Type = "choice"
		in.Options = n.Get("options").Strings()
		if len(in.Options) == 0 {
			in.Type = "string"
		}
	case "number":
		in.Type = "number"
	case "environment":
		in.Type = "string"
		h.rp.note("hosted.on.input_type", n.LineOf("type"), name)
	default:
		in.Type = "string"
	}
	if in.Required && h.p.On.Push != nil {
		in.Required = false // a push-started run has no value to give
	}
	h.p.Inputs = append(h.p.Inputs, in)
	h.inputs[name] = true
}

func (h *hostedReader) workflowEnv(t *N) {
	if e := t.Get("env"); e.IsMap() {
		for i, k := range e.Keys {
			h.p.Env = append(h.p.Env, KV{k, h.xlate(nil, e.Vals[i].Str(), e.Vals[i].Line)})
		}
		h.rp.note("hosted.env", t.LineOf("env"))
	}
}

var windowsMac = regexp.MustCompile(`(?i)^(windows|macos)`)

func (h *hostedReader) job(id string, n *N) {
	j := &Job{ID: id, Line: n.Line, Name: n.Get("name").Str()}
	h.p.Jobs = append(h.p.Jobs, j)
	hj := &hostedJob{j: j, secrets: map[string]bool{}}
	j.Needs = append(j.Needs, n.Get("needs").Strings()...)
	if len(j.Needs) > 0 {
		h.rp.note("hosted.job.needs", n.LineOf("needs"))
	}
	if u := n.Get("uses"); u != nil {
		h.rp.note("hosted.job.reusable", n.LineOf("uses"), u.Str()).Step = id
		j.Steps = []Step{{Run: "echo 'NEEDS A PERSON: this job called a reusable workflow: " + oneLine(u.Str(), 100) + "'; exit 1", Line: n.LineOf("uses")}}
		return
	}
	h.jobRunsOn(j, n)
	h.jobContainer(hj, n)
	h.jobServices(hj, n)
	if st := n.Get("strategy"); st != nil {
		h.matrix(hj, st)
	}
	if v := n.Get("timeout-minutes").Str(); v != "" {
		if m := atoi(v); m > 0 && !strings.Contains(v, "$") {
			j.Timeout = fmt.Sprintf("%dm", m)
			h.rp.note("hosted.job.timeout", n.LineOf("timeout-minutes"))
		}
	}
	if v, ok := n.Get("continue-on-error").Bool(); ok && v {
		j.AllowFail = true
		h.rp.note("hosted.job.continue", n.LineOf("continue-on-error"))
	}
	if o := n.Get("outputs"); o.IsMap() {
		h.rp.note("hosted.job.outputs", n.LineOf("outputs"), strings.Join(o.Keys, ", "))
	}
	if e := n.Get("env"); e.IsMap() {
		for i, k := range e.Keys {
			j.Env = append(j.Env, KV{k, h.xlate(hj, e.Vals[i].Str(), e.Vals[i].Line)})
		}
		h.rp.note("hosted.env", n.LineOf("env"))
	}
	if n.Has("permissions") {
		h.rp.note("hosted.permissions", n.LineOf("permissions"))
	}
	if n.Has("concurrency") {
		h.rp.note("hosted.concurrency.queue", n.LineOf("concurrency"))
	}
	env := ""
	if e := n.Get("environment"); e != nil {
		env = e.Str()
		if e.IsMap() {
			env = e.Get("name").Str()
		}
		h.rp.note("hosted.job.environment", n.LineOf("environment"), env).Fix = "approval"
	}
	if c := n.Get("if"); c != nil {
		h.condition(j, c.Str(), n.LineOf("if"))
	}
	h.jobSteps(hj, n, id, env)
}

func allEmpty(s []Step) bool {
	for _, x := range s {
		if strings.TrimSpace(x.Run) != "" {
			return false
		}
	}
	return true
}

func (h *hostedReader) matrix(hj *hostedJob, st *N) {
	j := hj.j
	if v := st.Get("fail-fast"); v != nil {
		if b, ok := v.Bool(); ok {
			j.Matrix = &Matrix{Values: map[string][]string{}}
			j.Matrix.FailFast = &b
			h.rp.note("hosted.job.fail_fast", st.LineOf("fail-fast"))
		}
	}
	m := st.Get("matrix")
	if m == nil {
		return
	}
	mx := j.Matrix
	if mx == nil {
		mx = &Matrix{Values: map[string][]string{}}
	}
	if m.IsScalar() {
		h.rp.note("hosted.job.matrix_expr", st.LineOf("matrix"))
		j.Matrix = nil
		return
	}
	for i, k := range m.Keys {
		v := m.Vals[i]
		switch k {
		case "include", "exclude":
			continue
		}
		if !v.IsSeq() {
			h.rp.note("hosted.job.matrix_expr", m.KeyLine[i])
			j.Matrix = nil
			return
		}
		key := loopKey(k)
		mx.Keys = append(mx.Keys, key)
		for _, it := range v.List() {
			mx.Values[key] = append(mx.Values[key], it.Val)
		}
	}
	for _, e := range m.Get("exclude").List() {
		em := map[string]string{}
		for i, k := range e.Keys {
			em[loopKey(k)] = e.Vals[i].Val
		}
		mx.Exclude = append(mx.Exclude, em)
	}
	for _, e := range m.Get("include").List() {
		em := map[string]string{}
		extra := []string{}
		for i, k := range e.Keys {
			lk := loopKey(k)
			em[lk] = e.Vals[i].Val
			if !contains(mx.Keys, lk) {
				extra = append(extra, k)
			}
		}
		if len(extra) > 0 {
			h.rp.note("hosted.job.matrix_extra", e.Line, strings.Join(extra, ", "))
			continue
		}
		if len(em) == len(mx.Keys) {
			mx.Add = append(mx.Add, em)
		} else {
			h.rp.note("hosted.job.matrix_extra", e.Line, "a partial combination")
		}
	}
	if v := st.Get("max-parallel").Str(); v != "" {
		mx.MaxParallel = atoi(v)
		h.rp.note("hosted.job.fail_fast", st.LineOf("max-parallel"))
	}
	if len(mx.Keys) == 0 {
		j.Matrix = nil
		return
	}
	// A runner-label axis cannot run on other operating systems.
	for _, k := range mx.Keys {
		var keep []string
		for _, v := range mx.Values[k] {
			if windowsMac.MatchString(v) {
				h.rp.note("hosted.job.runs_on_os", m.Line, v)
				continue
			}
			keep = append(keep, v)
		}
		mx.Values[k] = keep
	}
	j.Matrix = mx
	h.rp.note("hosted.job.matrix", st.LineOf("matrix"))
}

func loopKey(k string) string {
	k = strings.ToLower(strings.ReplaceAll(k, "_", "-"))
	return strings.ReplaceAll(k, ".", "-")
}

var (
	exprRe     = regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)
	matrixRe   = regexp.MustCompile(`^matrix\.([A-Za-z0-9_-]+)$`)
	inputRe    = regexp.MustCompile(`^(?:inputs|github\.event\.inputs)\.([A-Za-z0-9_-]+)$`)
	secretRe   = regexp.MustCompile(`^secrets\.([A-Za-z0-9_]+)$`)
	envRe      = regexp.MustCompile(`^(?:env|vars)\.([A-Za-z0-9_]+)$`)
	ghVarRe    = regexp.MustCompile(`\$\{?(GITHUB_[A-Z_]+)\}?`)
	ghReplace  = map[string]string{"GITHUB_SHA": "LAUNCHWAY_COMMIT", "GITHUB_REF_NAME": "LAUNCHWAY_BRANCH", "GITHUB_REF": "LAUNCHWAY_REF", "GITHUB_RUN_NUMBER": "LAUNCHWAY_RUN_NUMBER", "GITHUB_RUN_ID": "LAUNCHWAY_RUN_ID", "GITHUB_EVENT_NAME": "LAUNCHWAY_EVENT", "GITHUB_OUTPUT": "LAUNCHWAY_OUTPUTS"}
	ghContexts = map[string]string{"github.sha": "${LAUNCHWAY_COMMIT}", "github.ref": "${LAUNCHWAY_REF}", "github.ref_name": "${LAUNCHWAY_BRANCH}", "github.run_number": "${LAUNCHWAY_RUN_NUMBER}", "github.run_id": "${LAUNCHWAY_RUN_ID}", "github.event_name": "${LAUNCHWAY_EVENT}"}
)

func eachName(k string) string {
	return strings.ToUpper(strings.ReplaceAll(loopKey(k), "-", "_"))
}

// xlate replaces the simple ${{ }} forms and the old system's variables in a
// text. hj may be nil (workflow-level values).
func (h *hostedReader) xlate(hj *hostedJob, s string, line int) string {
	if !strings.Contains(s, "${{") && !strings.Contains(s, "GITHUB_") {
		return s
	}
	var done []string
	out := exprRe.ReplaceAllStringFunc(s, func(m string) string {
		e := exprRe.FindStringSubmatch(m)[1]
		switch {
		case matrixRe.MatchString(e):
			k := matrixRe.FindStringSubmatch(e)[1]
			done = append(done, "matrix."+k)
			return "${LAUNCHWAY_EACH_" + eachName(k) + "}"
		case inputRe.MatchString(e):
			k := inputRe.FindStringSubmatch(e)[1]
			done = append(done, "inputs."+k)
			return "${LAUNCHWAY_INPUT_" + strings.ToUpper(strings.ReplaceAll(inputName(k), "-", "_")) + "}"
		case secretRe.MatchString(e):
			k := secretRe.FindStringSubmatch(e)[1]
			if k == "GITHUB_TOKEN" {
				h.rp.note("hosted.secret.token", line)
				return m
			}
			if hj != nil {
				if !hj.secrets[k] {
					hj.secrets[k] = true
					hj.j.SecretEnv = append(hj.j.SecretEnv, KV{k, k})
					if hj.j.SecretLine == nil {
						hj.j.SecretLine = map[string]int{}
					}
					hj.j.SecretLine[k] = line
				}
			}
			return "${" + k + "}"
		case envRe.MatchString(e):
			k := envRe.FindStringSubmatch(e)[1]
			if strings.HasPrefix(e, "vars.") && !h.vars[k] {
				h.vars[k] = true
				h.rp.note("hosted.var", line, k)
			}
			return "${" + k + "}"
		}
		if v, ok := ghContexts[e]; ok {
			done = append(done, e)
			return v
		}
		h.rp.note("hosted.expr.unknown", line, e)
		return m
	})
	if len(done) > 0 {
		h.rp.note("hosted.expr", line, strings.Join(dedupe(done), ", "))
	}
	out = ghVarRe.ReplaceAllStringFunc(out, func(m string) string {
		name := ghVarRe.FindStringSubmatch(m)[1]
		if name == "GITHUB_ENV" || name == "GITHUB_PATH" || name == "GITHUB_STEP_SUMMARY" {
			return m // reported once, with the other workflow commands
		}
		if r, ok := ghReplace[name]; ok {
			if name == "GITHUB_OUTPUT" {
				h.rp.note("hosted.step.output_file", line)
			} else {
				h.rp.note("hosted.step.env_names", line, name)
			}
			if strings.Contains(m, "{") {
				return "${" + r + "}"
			}
			return "$" + r
		}
		h.rp.note("hosted.step.commands", line, "the variable "+name)
		return m
	})
	return out
}

func dedupe(l []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range l {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

var (
	cmdFileRe = regexp.MustCompile(`(GITHUB_ENV|GITHUB_PATH|GITHUB_STEP_SUMMARY|::set-output|::set-env|::add-path|::error|::warning|::notice|::group::)`)
	eqRe      = regexp.MustCompile(`^(github\.ref_name|github\.ref|github\.event_name|github\.base_ref)\s*(==|!=)\s*'([^']*)'$`)
	startsRe  = regexp.MustCompile(`^startsWith\(\s*github\.ref(?:_name)?\s*,\s*'([^']*)'\s*\)$`)
	eventMap  = map[string]string{"push": "push", "pull_request": "pull_request", "workflow_dispatch": "manual", "schedule": "schedule", "repository_dispatch": "webhook"}
)

// condition turns a simple if: into a when: filter, or reports the expression.
func (h *hostedReader) condition(j *Job, text string, line int) {
	e := strings.TrimSpace(text)
	if m := exprRe.FindStringSubmatch(e); m != nil && m[0] == e {
		e = m[1]
	}
	var c Cond
	ok := true
	for _, a := range strings.Split(e, "&&") {
		a = strings.TrimSpace(a)
		if strings.HasPrefix(a, "(") && strings.HasSuffix(a, ")") {
			a = strings.TrimSpace(a[1 : len(a)-1])
		}
		alts := strings.Split(a, "||")
		var br, tg, ev []string
		for _, alt := range alts {
			if !condAlt(strings.TrimSpace(alt), &c, &br, &tg, &ev) {
				ok = false
			}
		}
		if !mergeAtom(&c, br, tg, ev) {
			ok = false
		}
		if len(alts) > 1 && (len(br) > 0 && len(tg)+len(ev) > 0) {
			ok = false
		}
	}
	if !ok {
		h.rp.note("hosted.job.if_expr", line, oneLine(text, 160)).Step = j.ID
		return
	}
	j.When = c
	h.rp.note("hosted.job.if", line)
}

// mergeAtom adds one && term's branches, tags and events to c; it reports false when a
// field was already set by an earlier term.
func mergeAtom(c *Cond, br, tg, ev []string) bool {
	ok := true
	if len(br) > 0 && len(c.Branch) == 0 {
		c.Branch = br
	} else if len(br) > 0 {
		ok = false
	}
	if len(tg) > 0 && len(c.Tag) == 0 {
		c.Tag = tg
	} else if len(tg) > 0 {
		ok = false
	}
	if len(ev) > 0 && len(c.Event) == 0 {
		c.Event = ev
	} else if len(ev) > 0 {
		ok = false
	}
	return ok
}

// condAlt reads one || alternative; it reports false when the expression is not understood.
func condAlt(alt string, c *Cond, br, tg, ev *[]string) bool {
	switch {
	case alt == "always()":
		c.Upstream = "always"
	case alt == "failure()":
		c.Upstream = "failed"
	case alt == "success()":
	case startsRe.MatchString(alt):
		pre := startsRe.FindStringSubmatch(alt)[1]
		if strings.HasPrefix(pre, "refs/tags/") {
			*tg = append(*tg, strings.TrimPrefix(pre, "refs/tags/")+"*")
		} else if strings.HasPrefix(pre, "refs/heads/") {
			*br = append(*br, strings.TrimPrefix(pre, "refs/heads/")+"*")
		} else {
			return false
		}
	case eqRe.MatchString(alt):
		return condEq(eqRe.FindStringSubmatch(alt), br, tg, ev)
	default:
		return false
	}
	return true
}

func condEq(m []string, br, tg, ev *[]string) bool {
	switch {
	case m[1] == "github.ref" && m[2] == "==" && strings.HasPrefix(m[3], "refs/heads/"):
		*br = append(*br, strings.TrimPrefix(m[3], "refs/heads/"))
	case m[1] == "github.ref" && m[2] == "==" && strings.HasPrefix(m[3], "refs/tags/"):
		*tg = append(*tg, strings.TrimPrefix(m[3], "refs/tags/"))
	case m[1] == "github.ref_name" && m[2] == "==":
		*br = append(*br, m[3])
	case m[1] == "github.event_name" && m[2] == "==" && eventMap[m[3]] != "":
		*ev = append(*ev, eventMap[m[3]])
	case m[1] == "github.event_name" && m[2] == "!=" && m[3] == "pull_request":
		*ev = append(*ev, "push", "manual", "schedule", "webhook")
	default:
		return false
	}
	return true
}

func (h *hostedReader) step(hj *hostedJob, s *N, defDir string) {
	j := hj.j
	line := s.Line
	name := s.Get("name").Str()
	cond := s.Get("if").Str()
	if cond != "" {
		h.rp.note("hosted.step.if", s.LineOf("if"), oneLine(cond, 160))
	}
	if v, ok := s.Get("continue-on-error").Bool(); ok && v {
		h.rp.note("hosted.step.continue", s.LineOf("continue-on-error"))
	}
	if s.Has("timeout-minutes") {
		h.rp.note("hosted.step.timeout", s.LineOf("timeout-minutes"))
	}
	if u := s.Get("uses"); u != nil {
		h.uses(hj, s, u.Str(), cond)
		return
	}
	run := s.Get("run")
	if run == nil {
		return
	}
	text := h.xlate(hj, run.Str(), run.Line)
	if cmdFileRe.MatchString(run.Str()) {
		h.rp.note("hosted.step.commands", run.Line, cmdFileRe.FindString(run.Str()))
	}
	if sh := s.Get("shell").Str(); sh != "" {
		switch {
		case sh == "bash" || strings.HasPrefix(sh, "bash "):
			h.rp.note("hosted.step.shell", s.LineOf("shell"), "bash")
		case sh == "sh":
		default:
			h.rp.note("hosted.step.shell_other", s.LineOf("shell"), sh)
			text = "echo 'NEEDS A PERSON: this step used the shell " + strings.ReplaceAll(sh, "'", "") + "'; exit 1\n# original:\n" + commentOut(text)
		}
	}
	st := Step{Name: name, Run: text, Line: run.Line, Cond: cond}
	st.Dir = h.xlate(hj, s.Get("working-directory").Str(), line)
	if st.Dir == "" || st.Dir == "." {
		st.Dir = ""
		if defDir != "." {
			st.Dir = defDir
		}
	} else {
		h.rp.note("hosted.step.workdir", s.LineOf("working-directory"))
	}
	if e := s.Get("env"); e.IsMap() {
		for i, k := range e.Keys {
			st.Env = append(st.Env, KV{k, h.xlate(hj, e.Vals[i].Str(), e.Vals[i].Line)})
		}
		h.rp.note("hosted.env", s.LineOf("env"))
	}
	h.rp.note("hosted.step.run", run.Line)
	j.Steps = append(j.Steps, st)
}

func commentOut(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("# " + l + "\n")
	}
	return b.String()
}
