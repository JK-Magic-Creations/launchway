package importer

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Reader for the stage-based pipeline file. Syntax from the format's keyword
// reference (testdata/sources.md). Includes that are files of the repository are
// merged; every other include is only reported. Nothing is fetched or evaluated.

func init() {
	register(&Format{
		ID: "stages-yaml", Title: "Stage-based pipeline file", Where: "a file named .*-ci.yml at the top of the repository",
		Detect: func(p string, head []byte) bool {
			if strings.Contains(p, "/") || !stagesName.MatchString(p) {
				return false
			}
			return true
		},
		Read: readStages, Table: stagesTable,
		Sections: []string{"Structure", "Jobs", "Conditions", "Caches and artifacts", "Variables", "Environments and other keywords"},
	})
}

var stagesName = regexp.MustCompile(`^\.[a-z0-9]+-ci\.ya?ml$`)

var stagesGlobal = map[string]bool{"stages": true, "variables": true, "default": true, "include": true, "workflow": true, "image": true, "services": true, "cache": true, "before_script": true, "after_script": true, "types": true, "spec": true}

type stagesReader struct {
	p      *Pipeline
	rp     reporter
	all    map[string]File
	lim    Limits
	root   *N
	defs   *N
	vars   map[string]string
	unset  map[string]bool
	stages []string
	files  int
}

func readStages(root File, others map[string]File, lim Limits) *Pipeline {
	p := &Pipeline{Format: "stages-yaml", File: root.Path, Name: "ci"}
	rp := reporter{table: stagesTable, file: root.Path, out: &p.Findings}
	s := &stagesReader{p: p, rp: rp, all: others, lim: lim, vars: map[string]string{}, unset: map[string]bool{}}
	tree, err := ParseTree(root.Content, lim)
	if err != nil {
		p.Findings = append(p.Findings, Finding{Code: "read.error", Class: Manual, Message: err.Error(), Next: "Fix the file, or import it by hand", File: root.Path})
		return p
	}
	if !tree.IsMap() {
		p.Findings = append(p.Findings, Finding{Code: "read.shape", Class: Manual, Message: "This file is not a mapping of jobs", Next: "Choose another file", File: root.Path, Line: 1})
		return p
	}
	s.root = s.withIncludes(tree, 0)
	s.run()
	sortFindings(p.Findings)
	return p
}

// withIncludes merges local includes below the file's own keys.
func (s *stagesReader) withIncludes(t *N, depth int) *N {
	inc := t.Get("include")
	if inc == nil {
		return t
	}
	items := inc.List()
	if inc.IsScalar() || inc.IsMap() {
		items = []*N{inc}
	}
	merged := &N{Kind: kMap, Line: t.Line}
	for _, it := range items {
		var loc string
		switch {
		case it.IsScalar():
			loc = it.Val
			if strings.Contains(loc, "://") {
				s.rp.note("stages.include.remote", it.Line, loc)
				continue
			}
		case it.IsMap() && it.Has("local"):
			loc = it.Get("local").Str()
		case it.IsMap():
			for _, k := range []string{"remote", "project", "template", "component"} {
				if it.Has(k) {
					s.rp.note("stages.include.remote", it.Line, k+" "+it.Get(k).Str())
				}
			}
			continue
		}
		loc = path.Clean(strings.TrimPrefix(loc, "/"))
		f, ok := s.all[loc]
		if !ok || depth >= 8 || s.files >= 40 || strings.ContainsAny(loc, "*?") {
			s.rp.note("stages.include.remote", it.Line, loc+" (not among the files given)")
			continue
		}
		s.files++
		sub, err := ParseTreeFile(loc, f.Content, s.lim)
		if err != nil || !sub.IsMap() {
			s.rp.note("stages.include.remote", it.Line, loc+" (not readable)")
			continue
		}
		sub = s.withIncludes(sub, depth+1)
		merged = mergeN(merged, sub)
		s.rp.note("stages.include.local", it.Line, loc)
	}
	own := &N{Kind: kMap, Line: t.Line}
	own.Pairs(func(string, *N, int) {})
	for i, k := range t.Keys {
		if k != "include" {
			own.Keys, own.KeyLine, own.Vals = append(own.Keys, k), append(own.KeyLine, t.KeyLine[i]), append(own.Vals, t.Vals[i])
		}
	}
	return mergeN(merged, own)
}

// mergeN merges mappings deeply, over wins; anything else is replaced.
func mergeN(base, over *N) *N {
	if base == nil {
		return over
	}
	if over == nil {
		return base
	}
	if base.Kind != kMap || over.Kind != kMap {
		return over
	}
	out := &N{Kind: kMap, Line: over.Line, Src: over.Src}
	out.Keys = append(out.Keys, base.Keys...)
	out.KeyLine = append(out.KeyLine, base.KeyLine...)
	out.Vals = append(out.Vals, base.Vals...)
	for i, k := range over.Keys {
		found := false
		for j, bk := range out.Keys {
			if bk == k {
				out.Vals[j] = mergeN(out.Vals[j], over.Vals[i])
				out.KeyLine[j] = over.KeyLine[i]
				found = true
				break
			}
		}
		if !found {
			out.Keys, out.KeyLine, out.Vals = append(out.Keys, k), append(out.KeyLine, over.KeyLine[i]), append(out.Vals, over.Vals[i])
		}
	}
	return out
}

// resolve applies extends (reverse deep merge) to a job.
func (s *stagesReader) resolve(name string, n *N, depth int) *N {
	if depth > 11 {
		return n
	}
	ext := n.Get("extends")
	if ext == nil {
		return n
	}
	base := &N{Kind: kMap}
	for _, parent := range ext.Strings() {
		pn := s.root.Get(parent)
		if pn == nil || !pn.IsMap() {
			continue
		}
		base = mergeN(base, s.resolve(parent, pn, depth+1))
	}
	s.rp.note("stages.extends", n.LineOf("extends"))
	out := mergeN(base, n)
	return out
}

// lines flattens a script value (string, list, nested !reference) into lines.
func (s *stagesReader) lines(n *N, depth int) []string {
	if n == nil || depth > 6 {
		return nil
	}
	switch {
	case n.IsScalar():
		return []string{n.Val}
	case n.IsSeq() && n.Ref:
		return s.lines(s.refPath(n), depth+1)
	case n.IsSeq():
		var out []string
		for _, it := range n.Items {
			out = append(out, s.lines(it, depth+1)...)
		}
		return out
	}
	return nil
}

func (s *stagesReader) refPath(n *N) *N {
	if len(n.Items) == 0 {
		return nil
	}
	job := s.root.Get(n.Items[0].Val)
	if job == nil {
		return nil
	}
	cur := s.resolve(n.Items[0].Val, job, 0)
	for _, it := range n.Items[1:] {
		cur = cur.Get(it.Val)
	}
	return cur
}

var predef = map[string]string{
	"CI_COMMIT_SHA": "LAUNCHWAY_COMMIT", "CI_COMMIT_BRANCH": "LAUNCHWAY_BRANCH", "CI_COMMIT_REF_NAME": "LAUNCHWAY_BRANCH",
	"CI_COMMIT_TAG": "LAUNCHWAY_TAG", "CI_PIPELINE_ID": "LAUNCHWAY_RUN_ID", "CI_PIPELINE_IID": "LAUNCHWAY_RUN_NUMBER",
	"CI_JOB_NAME": "LAUNCHWAY_STEP", "CI_PIPELINE_SOURCE": "LAUNCHWAY_EVENT", "CI_PROJECT_NAME": "LAUNCHWAY_PROJECT",
}

var (
	ciVarRe   = regexp.MustCompile(`\$(?:\{([A-Z][A-Z0-9_]*)\}|([A-Z][A-Z0-9_]*))`)
	srcEvents = map[string]string{"push": "push", "merge_request_event": "pull_request", "schedule": "schedule", "web": "manual", "api": "webhook", "trigger": "webhook"}
)

func (s *stagesReader) xlate(j *Job, text string, line int, own map[string]string) string {
	return ciVarRe.ReplaceAllStringFunc(text, func(m string) string {
		sm := ciVarRe.FindStringSubmatch(m)
		name := sm[1] + sm[2]
		braced := sm[1] != ""
		put := func(v string) string {
			if braced {
				return "${" + v + "}"
			}
			return "$" + v
		}
		if r, ok := predef[name]; ok {
			s.rp.note("stages.predefined", line, name, r)
			if name == "CI_PIPELINE_SOURCE" {
				return put(r)
			}
			return put(r)
		}
		if name == "CI_PROJECT_DIR" {
			s.rp.note("stages.predefined", line, name, "the step's working directory")
			return "$PWD"
		}
		if strings.HasPrefix(name, "CI_NODE_") {
			return m
		}
		if strings.HasPrefix(name, "CI_") || strings.HasPrefix(name, "GITLAB_") {
			s.rp.note("stages.predefined.other", line, name)
			return m
		}
		if _, ok := own[name]; ok {
			return m
		}
		if _, ok := s.vars[name]; ok {
			return m
		}
		if isShellVar(name) {
			return m
		}
		// Not defined in the file: assume the old system's settings hold it.
		if !s.unset[name+"|"+j.ID] {
			s.unset[name+"|"+j.ID] = true
			j.SecretEnv = appendOnceKV(j.SecretEnv, KV{name, name})
			if j.SecretLine == nil {
				j.SecretLine = map[string]int{}
			}
			j.SecretLine[name] = line
			s.rp.note("stages.variables.unset", line, name)
		}
		return m
	})
}

var shellVars = map[string]bool{"HOME": true, "PATH": true, "PWD": true, "USER": true, "SHELL": true, "TERM": true, "HOSTNAME": true, "TMPDIR": true, "LANG": true, "IFS": true, "OLDPWD": true, "UID": true, "RANDOM": true, "CI": true, "DOCKER_HOST": true, "DOCKER_TLS_CERTDIR": true, "TZ": true, "GOPATH": true, "GOFLAGS": true}

func isShellVar(n string) bool {
	return shellVars[n] || strings.HasPrefix(n, "LAUNCHWAY_") || len(n) < 3 || (!strings.ContainsAny(n, "_") && len(n) <= 6)
}

func appendOnceKV(l []KV, kv KV) []KV {
	for _, x := range l {
		if x.K == kv.K {
			return l
		}
	}
	return append(l, kv)
}

func (s *stagesReader) run() {
	t := s.root
	s.defs = t.Get("default")
	// global variables
	t.Get("variables").Pairs(func(k string, v *N, line int) {
		val := v.Str()
		if v.IsMap() {
			val = v.Get("value").Str()
		}
		s.vars[k] = val
		s.p.Env = append(s.p.Env, KV{k, val})
	})
	if len(s.p.Env) > 0 {
		s.rp.note("stages.variables", t.LineOf("variables"))
	}
	if t.Has("workflow") {
		s.rp.note("stages.workflow", t.LineOf("workflow"))
	}
	if t.Has("default") {
		s.rp.note("stages.default", t.LineOf("default"))
	}
	s.stages = t.Get("stages").Strings()
	if len(s.stages) == 0 {
		s.stages = []string{".pre", "build", "test", "deploy", ".post"}
	} else {
		s.rp.note("stages.stages", t.LineOf("stages"))
	}
	type jb struct {
		id    string
		n     *N
		stage string
	}
	var jobs []jb
	t.Pairs(func(k string, v *N, line int) {
		if stagesGlobal[k] || strings.HasPrefix(k, ".") || !v.IsMap() {
			return
		}
		rn := s.resolve(k, v, 0)
		st := rn.Get("stage").Str()
		if st == "" {
			st = "test"
		}
		jobs = append(jobs, jb{k, rn, st})
	})
	byStage := map[string][]string{}
	order := func(st string) int {
		for i, x := range s.stages {
			if x == st {
				return i
			}
		}
		return len(s.stages)
	}
	sort.SliceStable(jobs, func(i, j int) bool { return order(jobs[i].stage) < order(jobs[j].stage) })
	for _, j := range jobs {
		byStage[j.stage] = append(byStage[j.stage], j.id)
	}
	anyMR, anySched := false, false
	jmap := map[string]*Job{}
	for _, jb := range jobs {
		j := s.job(jb.id, jb.n, byStage, &anyMR, &anySched)
		jmap[jb.id] = j
		s.p.Jobs = append(s.p.Jobs, j)
	}
	// Stage order: a job without needs waits for the previous stages' jobs.
	for _, jb := range jobs {
		j := jmap[jb.id]
		if jb.n.Has("needs") {
			continue
		}
		for i := order(jb.stage) - 1; i >= 0; i-- {
			if prev := byStage[s.stages[i]]; len(prev) > 0 {
				j.Needs = append([]string{}, prev...)
				break
			}
		}
	}
	// Artifacts of earlier jobs are fetched by the jobs that need them.
	for _, jb := range jobs {
		j := jmap[jb.id]
		deps := jb.n.Get("dependencies")
		for _, nd := range j.Needs {
			if deps != nil && !contains(deps.Strings(), nd) {
				continue
			}
			if nj := jmap[nd]; nj != nil {
				for _, a := range nj.Artifacts {
					j.Fetch = append(j.Fetch, Fetch{Name: a.Name})
				}
			}
		}
	}
	// Triggers
	s.p.On.Present = true
	s.p.On.Push = &Push{Branches: []string{"*"}, Tags: []string{"*"}}
	if anyMR {
		s.p.On.PR = &PullRequest{}
	}
	if anySched {
		s.rp.note("stages.rules.complex", 1, "a rule for scheduled pipelines: the schedules are set in the old system's settings, not in the file")
	}
	s.p.On.Manual = true
}

func (s *stagesReader) job(id string, n *N, byStage map[string][]string, anyMR, anySched *bool) *Job {
	j := &Job{ID: id, Line: n.Line}
	if n.Src != "" {
		saved := s.rp.file
		s.rp.file = n.Src
		defer func() { s.rp.file = saved }()
	}
	def := func(k string) *N {
		if v := n.Get(k); v != nil {
			return v
		}
		return s.defs.Get(k)
	}
	own := map[string]string{}
	n.Get("variables").Pairs(func(k string, v *N, line int) {
		val := v.Str()
		if v.IsMap() {
			val = v.Get("value").Str()
		}
		own[k] = val
	})
	s.jobNeeds(j, n)
	s.jobImage(j, n, def)
	n.Get("variables").Pairs(func(k string, v *N, line int) {
		val := v.Str()
		if v.IsMap() {
			val = v.Get("value").Str()
		}
		j.Env = append(j.Env, KV{k, s.xlate(j, val, line, own)})
	})
	if len(j.Env) > 0 {
		s.rp.note("stages.variables", n.LineOf("variables"))
	}
	// script
	var all []string
	for _, key := range []string{"before_script", "script", "after_script"} {
		ls := s.lines(def(key), 0)
		if key == "after_script" && len(ls) > 0 {
			s.rp.note("stages.after_script", n.LineOf(key))
		}
		all = append(all, ls...)
	}
	run := s.xlate(j, strings.Join(all, "\n"), n.LineOf("script"), own)
	if len(all) == 0 && !n.Has("trigger") {
		run = "true"
	}
	if run != "" {
		j.Steps = []Step{{Run: run, Line: n.LineOf("script")}}
		s.rp.note("stages.job", n.Line).Step = id
	}
	if n.Has("trigger") {
		s.rp.note("stages.trigger", n.LineOf("trigger")).Step = id
		j.Steps = []Step{{Run: "echo 'NEEDS A PERSON: this job started another pipeline'; exit 1"}}
	}
	// conditions
	s.conditions(j, n, anyMR, anySched)
	s.jobLimits(j, id, n, def)
	if p := n.Get("parallel"); p != nil {
		s.parallel(j, p)
	}
	// cache and artifacts
	s.cache(j, def("cache"))
	s.artifacts(j, n)
	if e := n.Get("environment"); e != nil {
		name := e.Str()
		if e.IsMap() {
			name = e.Get("name").Str()
			if e.Get("action").Str() == "stop" {
				s.rp.note("stages.environment.stop", e.Line).Step = id
			}
		}
		j.Delivers, j.DeliverWhy = true, "it targets the environment "+name
		s.rp.note("stages.environment", e.Line, name).Fix = "approval"
	}
	if !j.Delivers {
		j.Delivers, j.DeliverWhy = delivers(id, run, false)
	}
	if v := n.Get("tags"); v != nil {
		s.rp.note("stages.tags", v.Line, strings.Join(v.Strings(), ", "))
	}
	if v := n.Get("coverage"); v != nil {
		s.rp.note("stages.coverage", v.Line)
	}
	if v := n.Get("secrets"); v.IsMap() {
		s.rp.note("stages.secrets", v.Line, strings.Join(v.Keys, ", "))
	}
	if v := n.Get("resource_group"); v != nil {
		s.rp.note("stages.resource_group", v.Line, v.Str())
	}
	for i, k := range n.Keys {
		switch k {
		case "stage", "script", "before_script", "after_script", "image", "services", "variables", "rules", "only", "except", "when", "needs", "dependencies", "allow_failure", "retry", "timeout", "parallel", "cache", "artifacts", "environment", "tags", "coverage", "secrets", "resource_group", "trigger", "extends", "interruptible":
		default:
			s.rp.note("stages.other", n.KeyLine[i], k)
		}
	}
	j.Image = betterJavaImage(&Job{Image: j.Image, Steps: j.Steps})
	if len(j.Services) == 0 && len(j.Env) == 0 {
		detectTemplate(j)
	}
	return j
}

func (s *stagesReader) jobNeeds(j *Job, n *N) {
	if nd := n.Get("needs"); nd != nil {
		for _, it := range nd.List() {
			name := it.Str()
			if it.IsMap() {
				name = it.Get("job").Str()
				if it.Has("project") || it.Has("pipeline") {
					s.rp.note("stages.needs.other", it.Line, name+" of another project")
					continue
				}
				if b, _ := it.Get("optional").Bool(); b {
					s.rp.note("stages.needs.other", it.Line, name+" (optional)")
				}
			}
			if name != "" {
				j.Needs = append(j.Needs, name)
			}
		}
		if nd.IsScalar() {
			j.Needs = append(j.Needs, nd.Val)
		}
		s.rp.note("stages.needs", n.LineOf("needs"))
	}
}

// jobImage reads the job's image and service containers, falling back to the defaults.
func (s *stagesReader) jobImage(j *Job, n *N, def func(string) *N) {
	if im := def("image"); im != nil {
		if im.IsMap() {
			j.Image = im.Get("name").Str()
			if im.Has("entrypoint") {
				s.rp.note("stages.image.entrypoint", im.LineOf("entrypoint"), strings.Join(im.Get("entrypoint").Strings(), " "))
			}
		} else {
			j.Image = im.Str()
		}
		if n.Has("image") {
			s.rp.note("stages.image", im.Line)
		}
	}
	for _, sv := range def("services").List() {
		svc := Service{}
		if sv.IsMap() {
			svc.Image = sv.Get("name").Str()
			svc.Name = sv.Get("alias").Str()
			svc.Command = sv.Get("command").Strings()
			sv.Get("variables").Pairs(func(k string, v *N, _ int) { svc.Env = append(svc.Env, KV{k, v.Str()}) })
		} else {
			svc.Image = sv.Str()
		}
		if svc.Name == "" {
			svc.Name = lastSegment(svc.Image)
		}
		j.Services = append(j.Services, svc)
		s.rp.note("stages.services", sv.Line, svc.Name)
	}
}

// jobLimits reads when, allow_failure, retry and timeout.
func (s *stagesReader) jobLimits(j *Job, id string, n *N, def func(string) *N) {
	switch w := n.Get("when").Str(); w {
	case "always":
		j.When.Upstream = "always"
		s.rp.note("stages.when", n.LineOf("when"))
	case "on_failure":
		j.When.Upstream = "failed"
		s.rp.note("stages.when", n.LineOf("when"))
	case "manual":
		j.Approval = &Approval{Message: "Run " + id + "?"}
		s.rp.note("stages.rules.manual", n.LineOf("when"))
	case "delayed":
		s.rp.note("stages.when.delayed", n.LineOf("when"))
	}
	if v := n.Get("allow_failure"); v != nil {
		if v.IsMap() {
			s.rp.note("stages.allow_failure.codes", n.LineOf("allow_failure"))
		} else if b, _ := v.Bool(); b {
			j.AllowFail = true
			s.rp.note("stages.allow_failure", n.LineOf("allow_failure"))
		}
	}
	if r := def("retry"); r != nil {
		maxRetry := r.Str()
		if r.IsMap() {
			maxRetry = r.Get("max").Str()
			s.rp.note("stages.retry.when", r.Line)
		}
		j.Retry, _ = strconv.Atoi(maxRetry)
		if n.Has("retry") {
			s.rp.note("stages.retry", r.Line)
		}
	}
	if t := def("timeout"); t != nil {
		if d, ok := naturalDuration(t.Str()); ok {
			j.Timeout = d
			if n.Has("timeout") {
				s.rp.note("stages.timeout", t.Line)
			}
		} else {
			s.rp.note("stages.timeout.text", t.Line, t.Str())
		}
	}
}

func lastSegment(img string) string {
	img = strings.Split(img, ":")[0]
	if i := strings.LastIndex(img, "/"); i >= 0 {
		img = img[i+1:]
	}
	return img
}

var durRe = regexp.MustCompile(`(?i)(\d+)\s*(hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b`)

// naturalDuration reads "1h 30m", "90 minutes", "3 hours".
func naturalDuration(s string) (string, bool) {
	ms := durRe.FindAllStringSubmatch(s, -1)
	if len(ms) == 0 {
		return "", false
	}
	var b strings.Builder
	rest := durRe.ReplaceAllString(s, "")
	if strings.TrimSpace(strings.ReplaceAll(strings.ToLower(rest), "and", "")) != "" && strings.Trim(strings.TrimSpace(rest), ", ") != "" {
		return "", false
	}
	for _, m := range ms {
		n, _ := strconv.Atoi(m[1])
		switch strings.ToLower(m[2])[0] {
		case 'h':
			fmt.Fprintf(&b, "%dh", n)
		case 'm':
			fmt.Fprintf(&b, "%dm", n)
		default:
			fmt.Fprintf(&b, "%ds", n)
		}
	}
	return b.String(), true
}

func (s *stagesReader) parallel(j *Job, p *N) {
	if p.IsScalar() {
		n, _ := strconv.Atoi(p.Val)
		if n < 2 || n > 64 {
			return
		}
		mx := &Matrix{Keys: []string{"node-index"}, Values: map[string][]string{}}
		for i := 1; i <= n; i++ {
			mx.Values["node-index"] = append(mx.Values["node-index"], strconv.Itoa(i))
		}
		j.Matrix = mx
		j.Env = append(j.Env, KV{"CI_NODE_INDEX", "${LAUNCHWAY_EACH_NODE_INDEX}"}, KV{"CI_NODE_TOTAL", strconv.Itoa(n)})
		s.rp.note("stages.parallel", p.Line, strconv.Itoa(n))
		return
	}
	m := p.Get("matrix")
	if m == nil {
		return
	}
	if len(m.List()) != 1 {
		s.rp.note("stages.parallel.multi", p.Line)
		return
	}
	mx := &Matrix{Values: map[string][]string{}}
	m.Items[0].Pairs(func(k string, v *N, line int) {
		key := loopKey(k)
		mx.Keys = append(mx.Keys, key)
		mx.Values[key] = v.Strings()
		j.Env = append(j.Env, KV{k, "${LAUNCHWAY_EACH_" + eachName(k) + "}"})
	})
	j.Matrix = mx
	s.rp.note("stages.parallel.matrix", p.Line)
}

func (s *stagesReader) cache(j *Job, c *N) {
	if c == nil {
		return
	}
	list := c.List()
	if c.IsMap() {
		list = []*N{c}
	}
	for _, cn := range list {
		ca := Cache{}
		k := cn.Get("key")
		switch {
		case k.IsScalar():
			ca.KeyText = ciVarRe.ReplaceAllString(k.Val, "")
			ca.KeyText = strings.Trim(ca.KeyText, "-_ ")
		case k.IsMap():
			ca.KeyFiles = k.Get("files").Strings()
			ca.KeyText = strings.Trim(ciVarRe.ReplaceAllString(k.Get("prefix").Str(), ""), "-_ ")
		}
		var shown []string
		for _, p := range cn.Get("paths").Strings() {
			if np, ok := HomeCachePath(p); ok {
				k, v := splitExport(homeCacheExports[p])
				j.Env = appendOnce(j.Env, KV{k, v})
				p = np
			}
			if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "..") || strings.HasPrefix(p, "~") {
				s.rp.note("stages.cache.other", cn.Line, "the path "+p)
				continue
			}
			ca.Paths = append(ca.Paths, strings.TrimPrefix(p, "./"))
			shown = append(shown, p)
		}
		if len(ca.Paths) == 0 {
			if b, _ := cn.Get("untracked").Bool(); b {
				s.rp.note("stages.cache.other", cn.Line, "untracked")
			}
			continue
		}
		ca.Name = nameFromPath(ca.Paths[0])
		if ca.KeyText == "" && len(ca.KeyFiles) == 0 {
			ca.KeyText = ca.Name
		}
		for _, o := range []string{"policy", "fallback_keys", "untracked"} {
			if cn.Has(o) {
				s.rp.note("stages.cache.other", cn.LineOf(o), o)
			}
		}
		j.Caches = append(j.Caches, ca)
		s.rp.note("stages.cache", cn.Line, strings.Join(shown, ", "))
	}
}

func (s *stagesReader) artifacts(j *Job, n *N) {
	a := n.Get("artifacts")
	if a == nil {
		return
	}
	if paths := a.Get("paths").Strings(); len(paths) > 0 {
		art := Artifact{Name: sanitizeArtifact(firstNonEmpty(a.Get("name").Str(), j.ID)), Paths: paths, When: "success"}
		switch a.Get("when").Str() {
		case "on_failure":
			art.When = "failure"
		case "always":
			art.When = "always"
		}
		if e := a.Get("expire_in").Str(); e != "" {
			if r, ok := expireIn(e); ok {
				art.Retain = r
			}
		}
		j.Artifacts = append(j.Artifacts, art)
		s.rp.note("stages.artifacts", a.Line)
	}
	if r := a.Get("reports"); r.IsMap() {
		for i, k := range r.Keys {
			switch k {
			case "junit":
				j.Tests = append(j.Tests, r.Vals[i].Strings()...)
				s.rp.note("stages.artifacts.reports", r.KeyLine[i])
			case "coverage_report":
				if p := r.Vals[i].Get("path").Str(); p != "" {
					j.Coverage = append(j.Coverage, p)
					s.rp.note("stages.artifacts.reports", r.KeyLine[i])
				}
			default:
				s.rp.note("stages.artifacts.other", r.KeyLine[i], "reports: "+k)
			}
		}
	}
	for _, k := range []string{"exclude", "expose_as", "untracked", "access"} {
		if a.Has(k) {
			s.rp.note("stages.artifacts.other", a.LineOf(k), k)
		}
	}
}

func firstNonEmpty(l ...string) string {
	for _, s := range l {
		if s != "" {
			return s
		}
	}
	return ""
}

var expRe = regexp.MustCompile(`(?i)^(\d+)\s*(day|days|week|weeks|hour|hours|hrs?|h|d)$`)

func expireIn(s string) (string, bool) {
	m := expRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", false
	}
	n, _ := strconv.Atoi(m[1])
	switch strings.ToLower(m[2])[0] {
	case 'w':
		return fmt.Sprintf("%dw", n), true
	case 'd':
		return fmt.Sprintf("%dd", n), true
	}
	return fmt.Sprintf("%dh", n), true
}

var (
	ifEq  = regexp.MustCompile(`^\$([A-Z_]+)\s*(==|!=)\s*["']([^"']*)["']$`)
	ifTag = regexp.MustCompile(`^\$CI_COMMIT_TAG$`)
	ifPre = regexp.MustCompile(`^\$([A-Z_]+)\s*=~\s*/\^([A-Za-z0-9._/-]+)/$`)
)

// conditions turns rules (or only) into a when: filter when they are simple.
func (s *stagesReader) conditions(j *Job, n *N, anyMR, anySched *bool) {
	var c Cond
	ok := true
	var text string
	var manual bool
	if r := n.Get("rules"); r != nil {
		for i, rule := range r.List() {
			when := rule.Get("when").Str()
			text = oneLine(rule.Get("if").Str(), 120)
			if when == "never" {
				if i == len(r.Items)-1 && !rule.Has("if") && !rule.Has("changes") {
					continue
				}
				ok = false
				continue
			}
			if when == "manual" {
				manual = true
			}
			if when == "always" && i == 0 {
				c.Upstream = "always"
			}
			if cond := rule.Get("if"); cond != nil {
				if !s.ifAtom(&c, cond.Str(), anyMR, anySched) {
					ok = false
				}
			}
			if ch := rule.Get("changes"); ch != nil {
				c.Changed = append(c.Changed, ch.Get("paths").Strings()...)
				c.Changed = append(c.Changed, ch.Strings()...)
			}
		}
		if len(r.List()) == 0 {
			return
		}
	} else if o := n.Get("only"); o != nil {
		for _, v := range o.Get("refs").Strings() {
			s.refKeyword(&c, v, &ok, anyMR, anySched)
		}
		if !o.IsMap() {
			for _, v := range o.Strings() {
				s.refKeyword(&c, v, &ok, anyMR, anySched)
			}
		}
		if o.IsMap() && (o.Has("variables") || o.Has("kubernetes")) {
			ok = false
		}
		text = "only: " + strings.Join(o.Strings(), ", ")
		if ch := o.Get("changes"); ch != nil {
			c.Changed = append(c.Changed, ch.Strings()...)
		}
	} else if x := n.Get("except"); x != nil {
		ok = false
		text = "except: " + strings.Join(x.Strings(), ", ")
	} else {
		return
	}
	if !ok {
		s.rp.note("stages.rules.complex", n.Line, text).Step = j.ID
		return
	}
	if manual {
		j.Approval = &Approval{Message: "Run " + j.ID + "?"}
		s.rp.note("stages.rules.manual", n.Line)
	}
	if !c.Empty() {
		j.When = mergeCond(j.When, c)
		s.rp.note("stages.rules.simple", n.Line)
	}
}

func mergeCond(a, b Cond) Cond {
	if len(b.Branch) > 0 {
		a.Branch = b.Branch
	}
	if len(b.Tag) > 0 {
		a.Tag = b.Tag
	}
	if len(b.Event) > 0 {
		a.Event = b.Event
	}
	if len(b.Changed) > 0 {
		a.Changed = b.Changed
	}
	if b.Upstream != "" {
		a.Upstream = b.Upstream
	}
	return a
}

func (s *stagesReader) refKeyword(c *Cond, v string, ok *bool, anyMR, anySched *bool) {
	switch v {
	case "tags":
		c.Tag = append(c.Tag, "*")
	case "merge_requests":
		c.Event = append(c.Event, "pull_request")
		*anyMR = true
	case "schedules":
		c.Event = append(c.Event, "schedule")
		*anySched = true
	case "pushes":
		c.Event = append(c.Event, "push")
	case "web":
		c.Event = append(c.Event, "manual")
	case "api", "triggers":
		c.Event = append(c.Event, "webhook")
	case "branches":
	default:
		if strings.HasPrefix(v, "/") {
			*ok = false
			return
		}
		c.Branch = append(c.Branch, v)
	}
}

// ifAtom reads one if: expression of simple tests joined by && or ||.
func (s *stagesReader) ifAtom(c *Cond, e string, anyMR, anySched *bool) bool {
	e = strings.TrimSpace(e)
	good := true
	var br, tg, ev []string
	parts := strings.Split(e, "&&")
	if len(parts) == 1 {
		parts = strings.Split(e, "||")
	}
	for _, a := range parts {
		a = strings.TrimSpace(a)
		switch {
		case ifTag.MatchString(a):
			tg = append(tg, "*")
		case ifPre.MatchString(a):
			m := ifPre.FindStringSubmatch(a)
			switch m[1] {
			case "CI_COMMIT_TAG":
				tg = append(tg, m[2]+"*")
			case "CI_COMMIT_BRANCH", "CI_COMMIT_REF_NAME":
				br = append(br, m[2]+"*")
			default:
				good = false
			}
		case ifEq.MatchString(a):
			m := ifEq.FindStringSubmatch(a)
			switch {
			case m[2] != "==":
				good = false
			case m[1] == "CI_COMMIT_BRANCH" || m[1] == "CI_COMMIT_REF_NAME":
				br = append(br, m[3])
			case m[1] == "CI_COMMIT_TAG":
				tg = append(tg, m[3])
			case m[1] == "CI_PIPELINE_SOURCE" && srcEvents[m[3]] != "":
				ev = append(ev, srcEvents[m[3]])
				if m[3] == "merge_request_event" {
					*anyMR = true
				}
				if m[3] == "schedule" {
					*anySched = true
				}
			default:
				good = false
			}
		default:
			good = false
		}
	}
	if !good {
		return false
	}
	c.Branch = append(c.Branch, br...)
	c.Tag = append(c.Tag, tg...)
	c.Event = append(c.Event, ev...)
	return true
}
