package importer

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func init() {
	register(&Format{
		ID: "groovy-declarative", Title: "Declarative Groovy pipeline file", Where: "a file with a pipeline { ... } block at the top (usually named Jenkinsfile; any file name works with --from)",
		Detect: func(p string, head []byte) bool {
			base := p
			if i := strings.LastIndex(p, "/"); i >= 0 {
				base = p[i+1:]
			}
			if !strings.HasSuffix(strings.ToLower(base), "file") && !strings.HasSuffix(base, ".groovy") {
				return false
			}
			return groovyHead.Match(head)
		},
		Read: readGroovy, Table: groovyTable,
		Sections: []string{"Structure", "Agents and environment", "Conditions and parameters", "Steps", "Post and other"},
	})
}

var groovyHead = regexp.MustCompile(`(?m)^\s*(pipeline\s*\{|node\b|stage\s*\(|@Library)`)

type greader struct {
	p       *Pipeline
	rp      reporter
	env     []KV
	secEnv  []KV
	image   string
	params  map[string]bool
	timeout string
	retry   int
	prev    []string // job ids the next stage waits for
	used    map[string]bool
}

func readGroovy(root File, _ map[string]File, lim Limits) *Pipeline {
	p := &Pipeline{Format: "groovy-declarative", File: root.Path, Name: "pipeline"}
	rp := reporter{table: groovyTable, file: root.Path, out: &p.Findings}
	if len(root.Content) > lim.MaxBytes {
		p.Findings = append(p.Findings, Finding{Code: "read.error", Class: Manual, Message: fmt.Sprintf("%v (%d bytes; at most %d)", ErrTooLarge, len(root.Content), lim.MaxBytes), Next: "Split the file or import it by hand", File: root.Path})
		return p
	}
	stmts, err := parseGroovy(string(root.Content))
	if err != nil {
		line := 0
		if ge, ok := err.(*gerr); ok {
			line = ge.line
		}
		p.Findings = append(p.Findings, Finding{Code: "read.error", Class: Manual, Message: "The file could not be read as a pipeline: " + err.Error(), Next: "Fix the syntax or import it by hand", File: root.Path, Line: line})
		return p
	}
	r := &greader{p: p, rp: rp, params: map[string]bool{}, used: map[string]bool{}}
	var pl *gs
	var libs []string
	for _, s := range stmts {
		switch {
		case s.name == "pipeline" && s.hasBlock:
			pl = s
		case s.name == "(raw)" && strings.HasPrefix(s.raw, "@Library"):
			libs = append(libs, s.raw)
		}
	}
	if len(libs) > 0 {
		rp.note("groovy.library", 1, strings.Join(libs, "; "))
	}
	if pl == nil {
		var names []string
		var walk func([]*gs, int)
		walk = func(l []*gs, d int) {
			for _, s := range l {
				if s.name == "stage" {
					if f := s.first(); f != nil {
						names = append(names, f.str())
					}
				}
				if d < 6 {
					walk(s.block, d+1)
				}
			}
		}
		walk(stmts, 0)
		list := "none"
		if len(names) > 0 {
			list = strings.Join(names, ", ")
		}
		rp.note("groovy.scripted", 1, list)
		return p
	}
	r.pipeline(pl)
	sortFindings(p.Findings)
	return p
}

func (r *greader) pipeline(pl *gs) {
	r.rp.note("groovy.pipeline", pl.line)
	var stages, post *gs
	for _, d := range pl.block {
		switch d.name {
		case "agent":
			r.agent(d, &r.image)
		case "environment":
			r.environment(d, &r.env, &r.secEnv)
		case "options":
			r.options(d)
		case "parameters":
			r.parameters(d)
		case "triggers":
			r.triggers(d)
		case "tools":
			var tl []string
			for _, t := range d.block {
				tl = append(tl, t.name+" "+t.first().str())
			}
			r.rp.note("groovy.tools", d.line, strings.Join(tl, ", "))
		case "stages":
			stages = d
		case "post":
			post = d
		}
	}
	if stages == nil {
		return
	}
	last := r.stageList(stages.block, nil, r.prev)
	if post != nil {
		r.post(post, "", last, nil)
	}
}

// stageList turns stages in sequence; each waits for the ids in prev, and the returned ids are what the next waits for.
func (r *greader) stageList(list []*gs, parent *stageCtx, prev []string) []string {
	cur := prev
	for _, s := range list {
		if s.name != "stage" {
			continue
		}
		cur = r.stage(s, parent, cur)
	}
	return cur
}

type stageCtx struct {
	image    string
	env, sec []KV
	prefix   string
}

func (r *greader) stage(s *gs, parent *stageCtx, prev []string) []string {
	name := s.first().str()
	if name == "" {
		name = fmt.Sprintf("stage-%d", s.line)
	}
	ctx := &stageCtx{image: r.image, env: r.env, sec: r.secEnv}
	if parent != nil {
		ctx = &stageCtx{image: parent.image, env: parent.env, sec: parent.sec}
	}
	id := r.unique(stepName(name))
	var steps, post, whenB, inputB, par, seq, matrix *gs
	for _, d := range s.block {
		switch d.name {
		case "agent":
			r.agent(d, &ctx.image)
		case "environment":
			r.environment(d, &ctx.env, &ctx.sec)
		case "steps":
			steps = d
		case "post":
			post = d
		case "when":
			whenB = d
		case "input":
			inputB = d
		case "parallel":
			par = d
		case "stages":
			seq = d
		case "matrix":
			matrix = d
		case "options":
			r.options(d)
		case "tools":
			r.rp.note("groovy.tools", d.line, "named in this stage")
		}
	}
	// nested structures
	if par != nil {
		var ids []string
		for _, ps := range par.block {
			if ps.name == "stage" {
				ids = append(ids, r.stage(ps, ctx, prev)...)
			}
		}
		r.rp.note("groovy.parallel", par.line)
		return ids
	}
	if seq != nil {
		r.rp.note("groovy.parallel", seq.line)
		return r.stageList(seq.block, ctx, prev)
	}
	j := &Job{ID: id, Name: name, Line: s.line, Needs: append([]string{}, prev...), Image: ctx.image, Env: append([]KV{}, ctx.env...), SecretEnv: append([]KV{}, ctx.sec...)}
	if len(ctx.sec) > 0 {
		j.SecretLine = map[string]int{}
		for _, kv := range ctx.sec {
			j.SecretLine[kv.V] = s.line
		}
	}
	if matrix != nil {
		var inner *gs
		for _, d := range matrix.block {
			if d.name == "stages" {
				inner = d
			}
		}
		r.matrix(j, matrix)
		if inner != nil {
			for _, is := range inner.block {
				if is.name == "stage" {
					r.stageSteps(j, is)
				}
			}
		}
	}
	if steps != nil {
		r.stageSteps(j, s)
	}
	if inputB != nil {
		j.Approval = &Approval{Message: firstNonEmpty(inputB.named("message").str(), r.argOf(inputB, "message"), "Continue with "+name+"?")}
		if sub := inputB.named("submitter").str(); sub != "" {
			j.Approval.Users = strings.Split(sub, ",")
		}
		r.rp.note("groovy.input", inputB.line)
	}
	if whenB != nil {
		r.when(j, whenB)
	}
	r.timeoutRetry(j)
	var all strings.Builder
	for _, st := range j.Steps {
		all.WriteString(st.Run + "\n")
	}
	if j.Approval != nil {
		j.Delivers, j.DeliverWhy = delivers(name, all.String(), false)
	} else {
		j.Delivers, j.DeliverWhy = delivers(name, all.String(), false)
	}
	j.Image = betterJavaImage(j)
	if len(j.Steps) == 0 && j.Matrix == nil {
		j.Steps = []Step{{Run: "true"}}
	}
	if len(j.Steps) == 0 {
		j.Steps = []Step{{Run: "true"}}
	}
	r.rp.note("groovy.stage", s.line).Step = id
	r.p.Jobs = append(r.p.Jobs, j)
	detectTemplate(j)
	ids := []string{id}
	if post != nil {
		ids = r.post(post, id, ids, j)
	}
	return ids
}

func (r *greader) argOf(s *gs, key string) string {
	for _, d := range s.block {
		if d.name == key && d.first() != nil {
			return d.first().str()
		}
	}
	return ""
}

func (r *greader) unique(id string) string {
	n := id
	for i := 2; r.used[n]; i++ {
		n = fmt.Sprintf("%s-%d", id, i)
	}
	r.used[n] = true
	return n
}

func (r *greader) timeoutRetry(j *Job) {
	if r.timeout != "" && j.Timeout == "" {
		j.Timeout = r.timeout
	}
	if r.retry > 0 && j.Retry == 0 {
		j.Retry = r.retry
	}
}

func (r *greader) agent(d *gs, image *string) {
	if f := d.first(); f != nil && d.block == nil {
		switch f.str() {
		case "any", "none":
			r.rp.note("groovy.agent.any", d.line)
		}
		return
	}
	for _, b := range d.block {
		switch b.name {
		case "label":
			r.rp.note("groovy.agent.label", b.line, b.first().str())
		case "docker":
			img := b.first().str()
			for _, in := range b.block {
				if in.name == "image" {
					img = in.first().str()
				}
			}
			if img == "" {
				img = b.named("image").str()
			}
			*image = img
			r.rp.note("groovy.agent.docker", b.line)
		case "dockerfile", "kubernetes":
			r.rp.note("groovy.agent.other", b.line, b.name)
		case "node":
			for _, in := range b.block {
				if in.name == "label" {
					r.rp.note("groovy.agent.label", in.line, in.first().str())
				}
			}
		}
	}
	if d.name == "agent" && len(d.block) == 0 && d.first() == nil {
		r.rp.note("groovy.agent.any", d.line)
	}
	if f := d.first(); f != nil && f.kind == 'm' {
		_ = f
	}
}

var (
	envAssign = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$`)
	credCall  = regexp.MustCompile(`^credentials\(\s*['"]([^'"]+)['"]\s*\)$`)
)

func (r *greader) environment(d *gs, env, sec *[]KV) {
	cp := append([]KV{}, *env...)
	*env = cp
	scp := append([]KV{}, *sec...)
	*sec = scp
	for _, e := range d.block {
		if e.name != "(raw)" {
			continue
		}
		m := envAssign.FindStringSubmatch(e.raw)
		if m == nil {
			continue
		}
		val := strings.TrimSpace(m[2])
		if c := credCall.FindStringSubmatch(val); c != nil {
			*sec = append(*sec, KV{m[1], c[1]})
			r.rp.note("groovy.credentials", e.line, c[1], m[1])
			continue
		}
		if len(val) >= 2 && (val[0] == '\'' || val[0] == '"') && val[len(val)-1] == val[0] {
			v := val[1 : len(val)-1]
			if val[0] == '"' {
				v = r.interp(v, e.line)
			}
			*env = append(*env, KV{m[1], v})
		} else {
			*env = append(*env, KV{m[1], val})
			r.rp.note("groovy.interp.unknown", e.line, val)
		}
	}
	r.rp.note("groovy.environment", d.line)
}

func (r *greader) options(d *gs) {
	for _, o := range d.block {
		switch o.name {
		case "timeout":
			t := o.named("time").str()
			if t == "" {
				t = o.named("time").str()
			}
			n, _ := strconv.Atoi(t)
			if n > 0 {
				unit := strings.ToUpper(o.named("unit").str())
				switch unit {
				case "HOURS":
					r.timeout = fmt.Sprintf("%dh", n)
				case "SECONDS":
					r.timeout = fmt.Sprintf("%ds", n)
				default:
					r.timeout = fmt.Sprintf("%dm", n)
				}
			}
			r.rp.note("groovy.options.timeout", o.line)
		case "retry":
			n, _ := strconv.Atoi(o.first().str())
			r.retry = n
			r.rp.note("groovy.options.retry", o.line)
		case "buildDiscarder", "timestamps", "ansiColor", "preserveStashes", "quietPeriod", "skipStagesAfterUnstable":
			r.rp.note("groovy.options.ignored", o.line, o.name)
		case "disableConcurrentBuilds":
			r.rp.note("groovy.options.concurrent", o.line)
		default:
			r.rp.note("groovy.options.other", o.line, o.name)
		}
	}
}

func (r *greader) parameters(d *gs) {
	for _, pr := range d.block {
		name := pr.named("name").str()
		if name == "" {
			continue
		}
		in := Input{Name: inputName(name), Description: pr.named("description").str(), Default: pr.named("defaultValue").str(), Type: "string"}
		switch pr.name {
		case "booleanParam":
			in.Type = "boolean"
			in.Default = strings.ToLower(firstNonEmpty(pr.named("defaultValue").str(), "false"))
		case "choice":
			in.Type = "choice"
			in.Options = pr.named("choices").strs()
			if len(in.Options) == 0 {
				in.Type = "string"
			}
		case "password", "file":
			r.rp.note("groovy.parameters.other", pr.line, name)
			continue
		}
		r.p.Inputs = append(r.p.Inputs, in)
		r.params[name] = true
		r.rp.note("groovy.parameters", pr.line)
	}
}

func (r *greader) triggers(d *gs) {
	r.p.On.Present = true
	r.p.On.Push = &Push{Branches: []string{"*"}, Tags: []string{"*"}}
	r.p.On.Manual = true
	for _, t := range d.block {
		switch t.name {
		case "cron":
			spec := t.first().str()
			fixed := hashRe.ReplaceAllString(spec, "0")
			r.p.On.Schedules = append(r.p.On.Schedules, Schedule{Cron: fixed})
			r.rp.note("groovy.triggers.cron", t.line, spec)
		default:
			r.rp.note("groovy.triggers.other", t.line, t.name)
		}
	}
}

var hashRe = regexp.MustCompile(`\bH\b`)

func (r *greader) matrix(j *Job, m *gs) {
	mx := &Matrix{Values: map[string][]string{}}
	for _, d := range m.block {
		switch d.name {
		case "axes":
			for _, ax := range d.block {
				if ax.name != "axis" {
					continue
				}
				var name string
				var vals []string
				for _, a := range ax.block {
					switch a.name {
					case "name":
						name = a.first().str()
					case "values":
						for _, v := range a.args {
							vals = append(vals, v.str())
						}
					}
				}
				if name != "" {
					k := loopKey(name)
					mx.Keys = append(mx.Keys, k)
					mx.Values[k] = vals
					j.Env = append(j.Env, KV{name, "${LAUNCHWAY_EACH_" + eachName(name) + "}"})
				}
			}
		case "excludes":
			for _, ex := range d.block {
				e := map[string]string{}
				for _, ax := range ex.block {
					var name string
					var vals []string
					for _, a := range ax.block {
						switch a.name {
						case "name":
							name = a.first().str()
						case "values":
							for _, v := range a.args {
								vals = append(vals, v.str())
							}
						}
					}
					if name != "" && len(vals) == 1 {
						e[loopKey(name)] = vals[0]
					}
				}
				if len(e) > 0 {
					mx.Exclude = append(mx.Exclude, e)
				}
			}
		}
	}
	if len(mx.Keys) > 0 {
		j.Matrix = mx
		r.rp.note("groovy.matrix", m.line)
	}
}

func (r *greader) when(j *Job, w *gs) {
	var c Cond
	ok := true
	var visit func(list []*gs, any bool)
	visit = func(list []*gs, any bool) {
		for _, x := range list {
			switch x.name {
			case "branch":
				v := x.first().str()
				if p := x.named("pattern").str(); p != "" {
					v = p
				}
				if x.named("comparator") != nil && strings.ToUpper(x.named("comparator").str()) == "REGEXP" {
					ok = false
					continue
				}
				c.Branch = append(c.Branch, v)
			case "tag":
				c.Tag = append(c.Tag, firstNonEmpty(x.named("pattern").str(), x.first().str()))
			case "buildingTag":
				c.Tag = append(c.Tag, "*")
			case "changeRequest":
				c.Event = append(c.Event, "pull_request")
			case "changeset":
				c.Changed = append(c.Changed, firstNonEmpty(x.named("pattern").str(), x.first().str()))
			case "triggeredBy":
				if strings.Contains(x.first().str(), "Timer") {
					c.Event = append(c.Event, "schedule")
				} else {
					ok = false
				}
			case "anyOf":
				visit(x.block, true)
			case "allOf":
				visit(x.block, false)
			case "beforeAgent", "beforeInput", "beforeOptions":
			default:
				ok = false
			}
		}
	}
	visit(w.block, false)
	if !ok {
		raw := ""
		for _, x := range w.block {
			raw += x.name + " "
		}
		r.rp.note("groovy.when.expr", w.line, oneLine(strings.TrimSpace(raw), 100)).Step = j.ID
		return
	}
	j.When = c
	r.rp.note("groovy.when", w.line)
}

// post turns post conditions into steps after the stage. For stage == "" it is the pipeline's own.
func (r *greader) post(d *gs, stage string, after []string, sj *Job) []string {
	r.rp.note("groovy.post", d.line)
	ids := after
	for _, c := range d.block {
		var upstream string
		switch c.name {
		case "always", "cleanup":
			upstream = "always"
		case "failure":
			upstream = "failed"
		case "success":
			upstream = ""
		default:
			r.rp.note("groovy.post.other", c.line, c.name)
			continue
		}
		j := &Job{ID: r.unique(firstNonEmpty(stage, "pipeline") + "-post-" + c.name), Line: c.line, Needs: append([]string{}, after...), Image: r.image, Env: r.env, SecretEnv: r.secEnv}
		j.When.Upstream = upstream
		j.AllowFail = true
		var steps []*gs = c.block
		var own []*gs
		for _, st := range steps {
			switch st.name {
			case "junit", "archiveArtifacts":
				if sj != nil {
					r.step(sj, st, "", nil)
					continue
				}
			}
			own = append(own, st)
		}
		for _, st := range own {
			r.step(j, st, "", nil)
		}
		if len(j.Steps) == 0 && len(j.Artifacts)+len(j.Tests) == 0 {
			continue
		}
		if len(j.Steps) == 0 {
			j.Steps = []Step{{Run: "true"}}
		}
		if sj != nil {
			j.Image = sj.Image
		}
		r.p.Jobs = append(r.p.Jobs, j)
	}
	return ids
}

func (r *greader) stageSteps(j *Job, s *gs) {
	var steps *gs
	for _, d := range s.block {
		if d.name == "steps" {
			steps = d
		}
	}
	if steps == nil {
		return
	}
	for _, st := range steps.block {
		r.step(j, st, "", nil)
	}
}

var (
	interpBrace = regexp.MustCompile(`\$\{([^}]*)\}`)
	interpBare  = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
	envMap      = map[string]string{"BUILD_NUMBER": "LAUNCHWAY_RUN_NUMBER", "BRANCH_NAME": "LAUNCHWAY_BRANCH", "GIT_COMMIT": "LAUNCHWAY_COMMIT", "TAG_NAME": "LAUNCHWAY_TAG", "WORKSPACE": "PWD", "JOB_NAME": "LAUNCHWAY_PIPELINE", "BUILD_ID": "LAUNCHWAY_RUN_ID"}
)

// interp rewrites Groovy interpolation in a double-quoted string into shell variables.
func (r *greader) interp(s string, line int) string {
	out := interpBrace.ReplaceAllStringFunc(s, func(m string) string {
		e := strings.TrimSpace(interpBrace.FindStringSubmatch(m)[1])
		switch {
		case strings.HasPrefix(e, "params."):
			n := strings.TrimPrefix(e, "params.")
			if identRe.MatchString(n) {
				v := "${LAUNCHWAY_INPUT_" + strings.ToUpper(strings.ReplaceAll(inputName(n), "-", "_")) + "}"
				r.rp.note("groovy.interp", line, m, v)
				return v
			}
		case strings.HasPrefix(e, "env."):
			n := strings.TrimPrefix(e, "env.")
			if identRe.MatchString(n) {
				v := "${" + firstNonEmpty(envMap[n], n) + "}"
				r.rp.note("groovy.interp", line, m, v)
				return v
			}
		case identRe.MatchString(e):
			return "${" + firstNonEmpty(envMap[e], e) + "}"
		}
		r.rp.note("groovy.interp.unknown", line, e)
		return m
	})
	return out
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// shellText turns a sh argument into the command text.
func (r *greader) shellText(a *gx, line int) string {
	if a == nil {
		return ""
	}
	if a.kind != 's' {
		if a.kind == 'r' || a.kind == 'i' || a.kind == 'c' {
			r.rp.note("groovy.interp.unknown", line, oneLine(a.text, 80))
			return "echo 'NEEDS A PERSON: the command was an expression: " + strings.ReplaceAll(oneLine(a.text, 80), "'", "") + "'; exit 1"
		}
		return ""
	}
	t := a.text
	if a.interp {
		t = r.interpScript(t, line)
	}
	return t
}

// interpScript handles $name in a double-quoted script: Groovy would interpolate; the shell does the same for environment variables.
func (r *greader) interpScript(s string, line int) string {
	s = r.interp(s, line)
	return interpBare.ReplaceAllStringFunc(s, func(m string) string {
		n := m[1:]
		if v, ok := envMap[n]; ok {
			return "$" + v
		}
		return m
	})
}

func (r *greader) step(j *Job, st *gs, dir string, env []KV) {
	add := func(run string, line int) {
		if strings.TrimSpace(run) == "" {
			return
		}
		j.Steps = append(j.Steps, Step{Run: run, Line: line, Dir: dir, Env: env})
	}
	switch st.name {
	case "sh":
		a := st.first()
		if a == nil || a.kind == 'k' {
			a = st.named("script")
		}
		add(r.shellText(a, st.line), st.line)
		r.rp.note("groovy.step.sh", st.line)
	case "echo":
		add("echo "+shq(r.shellText(st.first(), st.line)), st.line)
		r.rp.note("groovy.step.sh", st.line)
	case "error":
		add("echo "+shq(r.shellText(st.first(), st.line))+"; exit 1", st.line)
		r.rp.note("groovy.step.sh", st.line)
	case "sleep":
		t := st.named("time").str()
		if t == "" {
			t = st.first().str()
		}
		add("sleep "+t, st.line)
		r.rp.note("groovy.step.sh", st.line)
	case "bat", "powershell", "pwsh":
		add("echo 'NEEDS A PERSON: a Windows command ran here'; exit 1", st.line)
		r.rp.note("groovy.step.bat", st.line).Step = j.ID
	case "dir":
		d := st.first().str()
		for _, b := range st.block {
			r.step(j, b, d, env)
		}
		r.rp.note("groovy.step.dir", st.line)
	case "withEnv":
		var ne []KV
		ne = append(ne, env...)
		if l := st.first(); l != nil {
			for _, e := range l.strs() {
				if k, v, ok := strings.Cut(e, "="); ok {
					ne = append(ne, KV{k, v})
				}
			}
		}
		for _, b := range st.block {
			r.step(j, b, dir, ne)
		}
		r.rp.note("groovy.step.dir", st.line)
	case "timeout":
		n, _ := strconv.Atoi(firstNonEmpty(st.named("time").str(), st.first().str()))
		if n > 0 {
			unit := strings.ToUpper(st.named("unit").str())
			if unit == "HOURS" {
				j.Timeout = fmt.Sprintf("%dh", n)
			} else {
				j.Timeout = fmt.Sprintf("%dm", n)
			}
		}
		r.rp.note("groovy.options.timeout", st.line)
		for _, b := range st.block {
			r.step(j, b, dir, env)
		}
	case "retry":
		n, _ := strconv.Atoi(st.first().str())
		j.Retry = n
		r.rp.note("groovy.options.retry", st.line)
		for _, b := range st.block {
			r.step(j, b, dir, env)
		}
	case "withCredentials":
		for _, c := range st.first().itemsOrSelf() {
			id := c.namedItem("credentialsId")
			v := c.namedItem("variable")
			if id != "" && v != "" {
				j.SecretEnv = appendOnceKV(j.SecretEnv, KV{v, id})
				if j.SecretLine == nil {
					j.SecretLine = map[string]int{}
				}
				j.SecretLine[id] = st.line
				r.rp.note("groovy.credentials", st.line, id, v)
			}
		}
		for _, b := range st.block {
			r.step(j, b, dir, env)
		}
	case "script":
		add("# NEEDS A PERSON: Groovy code was here:\n"+commentOut(st.raw)+"echo 'NEEDS A PERSON: a script block ran here'; exit 1", st.line)
		r.rp.note("groovy.step.script", st.line).Step = j.ID
	case "archiveArtifacts":
		p := firstNonEmpty(st.named("artifacts").str(), st.first().str())
		if p != "" {
			var paths []string
			for _, x := range strings.Split(p, ",") {
				paths = append(paths, strings.TrimSpace(x))
			}
			j.Artifacts = append(j.Artifacts, Artifact{Name: sanitizeArtifact(j.ID), Paths: paths, When: "always"})
			r.rp.note("groovy.step.archive", st.line)
		}
	case "junit":
		p := firstNonEmpty(st.named("testResults").str(), st.first().str())
		if p != "" {
			j.Tests = append(j.Tests, p)
			r.rp.note("groovy.step.junit", st.line)
		}
	case "checkout":
		r.rp.note("groovy.step.checkout", st.line)
	case "stash", "unstash":
		r.rp.note("groovy.step.stash", st.line).Step = j.ID
		add("echo 'NEEDS A PERSON: files were passed between stages here'; exit 1", st.line)
	case "input":
		j.Approval = &Approval{Message: firstNonEmpty(st.named("message").str(), st.first().str(), "Continue?")}
		r.rp.note("groovy.input", st.line)
	case "deleteDir", "cleanWs":
		// nothing to clean: every step starts from a fresh checkout
	case "(raw)":
		r.rp.note("groovy.step.other", st.line, oneLine(st.raw, 60)).Step = j.ID
		add("echo 'NEEDS A PERSON: this line was not understood: "+strings.ReplaceAll(oneLine(st.raw, 80), "'", "")+"'; exit 1", st.line)
	default:
		r.rp.note("groovy.step.other", st.line, st.name).Step = j.ID
		add("echo 'NEEDS A PERSON: the plug-in step "+strings.ReplaceAll(st.name, "'", "")+" ran here'; exit 1", st.line)
	}
}

func (g *gx) itemsOrSelf() []*gx {
	if g == nil {
		return nil
	}
	if g.kind == 'l' {
		return g.items
	}
	return []*gx{g}
}

// namedItem reads key: 'value' out of a call such as string(credentialsId: 'x', variable: 'Y').
func (g *gx) namedItem(key string) string {
	if g == nil {
		return ""
	}
	for _, it := range g.items {
		if it.kind == 'k' && it.text == key && len(it.items) > 0 {
			return it.items[0].str()
		}
	}
	return ""
}
