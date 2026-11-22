package importer

import (
	"fmt"
	"regexp"
	"strings"
)

func init() {
	register(&Format{
		ID: "jobs-config", Title: "Jobs-and-workflows config file", Where: "a config.yml in a dot-directory at the top of the repository",
		Detect: func(p string, head []byte) bool {
			if !jobsPath.MatchString(p) || strings.HasPrefix(p, ".github/") || strings.HasPrefix(p, ".vscode/") {
				return false
			}
			return jobsHead.Match(head)
		},
		Read: readJobs, Table: jobsTable,
		Sections: []string{"Structure", "Executors and steps", "Caches and files", "Workflows"},
	})
}

var (
	jobsPath = regexp.MustCompile(`^\.[a-z0-9_-]+/config\.ya?ml$`)
	jobsHead = regexp.MustCompile(`(?m)^version:\s*[0-9.]+\s*$`)
	paramRe  = regexp.MustCompile(`<<\s*([^>]*?)\s*>>`)
	checksum = regexp.MustCompile(`\{\{\s*checksum\s+"([^"]+)"\s*\}\}`)
)

type jobsReader struct {
	p    *Pipeline
	rp   reporter
	root *N
	exec *N
	cmds *N
	arts map[string]string
}

func readJobs(root File, _ map[string]File, lim Limits) *Pipeline {
	p := &Pipeline{Format: "jobs-config", File: root.Path, Name: "ci"}
	rp := reporter{table: jobsTable, file: root.Path, out: &p.Findings}
	t, err := ParseTree(root.Content, lim)
	if err != nil || !t.IsMap() {
		msg := "This file is not a mapping"
		if err != nil {
			msg = err.Error()
		}
		p.Findings = append(p.Findings, Finding{Code: "read.error", Class: Manual, Message: msg, Next: "Fix the file, or import it by hand", File: root.Path, Line: 1})
		return p
	}
	r := &jobsReader{p: p, rp: rp, root: t, exec: t.Get("executors"), cmds: t.Get("commands"), arts: map[string]string{}}
	if t.Has("parameters") || t.Get("setup").Str() == "true" {
		rp.note("jobs.pipeline_params", t.LineOf("parameters"), "pipeline parameters or setup")
	}
	wfs := t.Get("workflows")
	jobs := t.Get("jobs")
	if wfs == nil || jobs == nil {
		p.Findings = append(p.Findings, Finding{Code: "read.shape", Class: Manual, Message: "The file has no workflows and jobs sections", Next: "Choose another file", File: root.Path, Line: 1})
		return p
	}
	multi := 0
	wfs.Pairs(func(k string, v *N, _ int) {
		if v.IsMap() {
			multi++
		}
	})
	p.On.Present = true
	p.On.Push = &Push{Branches: []string{"*"}}
	p.On.Manual = true
	wfs.Pairs(func(wname string, w *N, line int) {
		if !w.IsMap() {
			return
		}
		r.workflow(wname, w, jobs, multi > 1)
	})
	sortFindings(p.Findings)
	return p
}

func (r *jobsReader) workflow(wname string, w, jobs *N, prefix bool) {
	r.rp.note("jobs.workflow", w.Line)
	idOf := func(name string) string {
		if prefix {
			return wname + "-" + name
		}
		return name
	}
	for _, tr := range w.Get("triggers").List() {
		if sc := tr.Get("schedule"); sc != nil {
			if c := sc.Get("cron").Str(); c != "" {
				r.p.On.Schedules = append(r.p.On.Schedules, Schedule{Cron: c})
				r.rp.note("jobs.schedule", sc.Line)
			}
		}
	}
	for _, it := range w.Get("jobs").List() {
		var name string
		var cfg *N
		if it.IsScalar() {
			name = it.Val
		} else if it.IsMap() && len(it.Keys) > 0 {
			name, cfg = it.Keys[0], it.Vals[0]
		}
		if name == "" {
			continue
		}
		src := name
		if cfg != nil && cfg.Get("name").Str() != "" {
			name = cfg.Get("name").Str()
		}
		jn := jobs.Get(src)
		if jn == nil && cfg.Get("type").Str() == "approval" {
			aj := &Job{ID: idOf(name), Line: it.Line, ApprovalOnly: true, Approval: &Approval{Message: "Run the jobs after " + name + "?"}}
			for _, rq := range cfg.Get("requires").List() {
				aj.Needs = append(aj.Needs, idOf(rq.Val))
			}
			r.rp.note("jobs.approval", cfg.LineOf("type"))
			r.filters(aj, cfg.Get("filters"))
			r.p.Jobs = append(r.p.Jobs, aj)
			continue
		}
		if jn == nil {
			if strings.Contains(src, "/") {
				r.rp.note("jobs.step.orb", it.Line, src)
			}
			continue
		}
		j := r.job(idOf(name), jn, cfg)
		for _, rq := range cfg.Get("requires").List() {
			j.Needs = append(j.Needs, idOf(rq.Val))
		}
		if cfg.Get("requires").IsMap() {
			cfg.Get("requires").Pairs(func(k string, _ *N, _ int) { j.Needs = append(j.Needs, idOf(k)) })
		}
		if cfg.Get("type").Str() == "approval" {
			j.Approval = &Approval{Message: "Run " + name + "?"}
			r.rp.note("jobs.approval", cfg.LineOf("type"))
		}
		if ctx := cfg.Get("context"); ctx != nil {
			r.rp.note("jobs.context", cfg.LineOf("context"), strings.Join(ctx.Strings(), ", "))
		}
		if cfg.Has("matrix") {
			r.rp.note("jobs.matrix", cfg.LineOf("matrix"), name)
		}
		r.filters(j, cfg.Get("filters"))
		r.p.Jobs = append(r.p.Jobs, j)
	}
}

func (r *jobsReader) filters(j *Job, f *N) {
	if f == nil {
		return
	}
	for _, kind := range []string{"branches", "tags"} {
		fk := f.Get(kind)
		if fk == nil {
			continue
		}
		only := fk.Get("only").Strings()
		if fk.Has("ignore") || anyRegex(only) {
			r.rp.note("jobs.filters.regex", f.LineOf(kind), kind)
			continue
		}
		if len(only) == 0 {
			continue
		}
		if kind == "branches" {
			j.When.Branch = only
			r.rp.note("jobs.filters.branch", f.LineOf(kind))
		} else {
			j.When.Tag = only
			r.p.On.Push.Tags = appendUnique(r.p.On.Push.Tags, only...)
			r.rp.note("jobs.filters.tags", f.LineOf(kind))
		}
	}
}

func anyRegex(l []string) bool {
	for _, s := range l {
		if strings.HasPrefix(s, "/") || strings.ContainsAny(s, "^$()|+\\") {
			return true
		}
	}
	return false
}

func appendUnique(l []string, add ...string) []string {
	for _, a := range add {
		if !contains(l, a) {
			l = append(l, a)
		}
	}
	return l
}

func (r *jobsReader) job(id string, n, cfg *N) *Job {
	j := &Job{ID: id, Line: n.Line}
	ex := n
	if e := n.Get("executor"); e != nil {
		name := e.Str()
		if e.IsMap() {
			name = e.Get("name").Str()
		}
		if x := r.exec.Get(name); x != nil {
			ex = x
			r.rp.note("jobs.executor.named", n.LineOf("executor"))
		}
	}
	switch {
	case ex.Has("docker"):
		for i, d := range ex.Get("docker").List() {
			img := d.Get("image").Str()
			if i == 0 {
				j.Image = img
				r.rp.note("jobs.executor.image", d.Line)
				d.Get("environment").Pairs(func(k string, v *N, _ int) { j.Env = append(j.Env, KV{k, v.Str()}) })
				continue
			}
			svc := Service{Name: firstNonEmpty(d.Get("name").Str(), lastSegment(img)), Image: img, Command: d.Get("command").Strings()}
			d.Get("environment").Pairs(func(k string, v *N, _ int) { svc.Env = append(svc.Env, KV{k, v.Str()}) })
			j.Services = append(j.Services, svc)
			r.rp.note("jobs.executor.services", d.Line, svc.Name)
		}
	case ex.Has("machine") || ex.Has("macos") || ex.Has("windows"):
		for _, k := range []string{"machine", "macos", "windows"} {
			if ex.Has(k) {
				r.rp.note("jobs.executor.machine", ex.LineOf(k), k+" machines")
			}
		}
	}
	if rc := ex.Get("resource_class").Str(); rc != "" {
		r.rp.note("jobs.executor.resource", ex.LineOf("resource_class"), rc)
	}
	n.Get("environment").Pairs(func(k string, v *N, _ int) { j.Env = append(j.Env, KV{k, v.Str()}) })
	if n.Has("environment") {
		r.rp.note("jobs.environment", n.LineOf("environment"))
	}
	dir := ""
	if wd := firstNonEmpty(n.Get("working_directory").Str()); wd != "" {
		dir = strings.TrimPrefix(strings.TrimPrefix(wd, "~/"), "project/")
		if dir == "project" || dir == "~/project" || dir == "." {
			dir = ""
		}
		if dir != "" {
			r.rp.note("jobs.workdir", n.LineOf("working_directory"), wd)
		}
	}
	if par := n.Get("parallelism").Str(); par != "" && atoi(par) > 1 && atoi(par) <= 64 {
		cnt := atoi(par)
		mx := &Matrix{Keys: []string{"node-index"}, Values: map[string][]string{}}
		for i := 0; i < cnt; i++ {
			mx.Values["node-index"] = append(mx.Values["node-index"], fmt.Sprint(i))
		}
		j.Matrix = mx
		j.Env = append(j.Env, KV{"CIRCLE_NODE_INDEX", "${LAUNCHWAY_EACH_NODE_INDEX}"}, KV{"CIRCLE_NODE_TOTAL", par})
		r.rp.note("jobs.parallelism", n.LineOf("parallelism"), par)
	}
	if n.Has("parameters") && cfg != nil {
		n.Get("parameters").Pairs(func(k string, v *N, line int) {
			r.rp.note("jobs.step.command_params", line, k)
		})
	}
	r.steps(j, n.Get("steps"), dir, 0)
	r.rp.note("jobs.job", n.Line).Step = id
	var all strings.Builder
	for _, s := range j.Steps {
		all.WriteString(s.Run + "\n")
	}
	j.Delivers, j.DeliverWhy = delivers(id, all.String(), false)
	j.Image = betterJavaImage(j)
	if len(j.Steps) == 0 {
		j.Steps = []Step{{Run: "true"}}
	}
	detectTemplate(j)
	return j
}

func (r *jobsReader) steps(j *Job, steps *N, dir string, depth int) {
	if depth > 5 {
		return
	}
	for _, s := range steps.List() {
		var kind string
		var cfg *N
		switch {
		case s.IsScalar():
			kind = s.Val
		case s.IsMap() && len(s.Keys) > 0:
			kind, cfg = s.Keys[0], s.Vals[0]
		}
		line := s.Line
		switch kind {
		case "checkout":
			r.rp.note("jobs.step.checkout", line)
		case "run":
			r.runStep(j, cfg, dir, line)
		case "restore_cache", "save_cache":
			r.cache(j, kind, cfg, line)
		case "store_artifacts":
			p := cfg.Get("path").Str()
			if p != "" {
				j.Artifacts = append(j.Artifacts, Artifact{Name: sanitizeArtifact(strings.ReplaceAll(strings.Trim(p, "/."), "/", "-")), Paths: []string{p}, When: "always"})
				r.rp.note("jobs.step.artifacts", line)
			}
		case "store_test_results":
			if p := cfg.Get("path").Str(); p != "" {
				j.Tests = append(j.Tests, strings.TrimSuffix(p, "/")+"/**/*.xml")
				r.rp.note("jobs.step.artifacts", line)
			}
		case "persist_to_workspace":
			r.persistStep(j, cfg, line)
		case "attach_workspace":
			j.Fetch = append(j.Fetch, Fetch{Name: "*", Into: strings.TrimPrefix(cfg.Get("at").Str(), "./")})
			r.rp.note("jobs.step.workspace", line, "workspace-<job>")
		case "setup_remote_docker":
			r.rp.note("jobs.step.remote_docker", line)
		case "when", "unless":
			r.rp.note("jobs.step.when", line)
			j.Steps = append(j.Steps, Step{Run: "echo 'NEEDS A PERSON: a conditional block was here'; exit 1", Line: line})
		case "add_ssh_keys", "deploy":
			if kind == "deploy" {
				r.steps(j, &N{Kind: kSeq, Items: []*N{{Kind: kMap, Keys: []string{"run"}, KeyLine: []int{line}, Vals: []*N{cfg}, Line: line}}}, dir, depth+1)
			} else {
				r.rp.note("jobs.step.orb", line, kind)
			}
		default:
			if cmd := r.cmds.Get(kind); cmd != nil {
				if cmd.Has("parameters") {
					r.rp.note("jobs.step.command_params", line, kind)
				}
				r.rp.note("jobs.step.command", line)
				r.steps(j, cmd.Get("steps"), dir, depth+1)
			} else if kind != "" {
				r.rp.note("jobs.step.orb", line, kind).Step = j.ID
				j.Steps = append(j.Steps, Step{Run: "echo 'NEEDS A PERSON: the step " + strings.ReplaceAll(kind, "'", "") + " ran here'; exit 1", Line: line})
			}
		}
	}
}

func (r *jobsReader) params(s string, cfg *N, line int) string {
	return paramRe.ReplaceAllStringFunc(s, func(m string) string {
		e := paramRe.FindStringSubmatch(m)[1]
		r.rp.note("jobs.step.command_params", line, e)
		return m
	})
}

func (r *jobsReader) cache(j *Job, kind string, cfg *N, line int) {
	if kind == "restore_cache" {
		return // the save step carries the cache; restore alone adds nothing
	}
	key := cfg.Get("key").Str()
	c := Cache{}
	for _, m := range checksum.FindAllStringSubmatch(key, -1) {
		c.KeyFiles = append(c.KeyFiles, "**/"+strings.TrimPrefix(m[1], "./"))
	}
	var shown []string
	for _, p := range cfg.Get("paths").Strings() {
		if np, ok := HomeCachePath(p); ok {
			k, v := splitExport(homeCacheExports[p])
			j.Env = appendOnce(j.Env, KV{k, v})
			c.Paths = append(c.Paths, np)
			shown = append(shown, np)
			continue
		}
		if strings.HasPrefix(p, "~") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "..") {
			r.rp.note("jobs.step.cache_path", line, p)
			continue
		}
		c.Paths = append(c.Paths, strings.TrimPrefix(p, "./"))
		shown = append(shown, p)
	}
	if len(c.Paths) == 0 {
		return
	}
	c.Name = nameFromPath(c.Paths[0])
	if len(c.KeyFiles) == 0 {
		c.KeyText = c.Name
	}
	j.Caches = append(j.Caches, c)
	r.rp.note("jobs.step.cache", line, strings.Join(shown, ", "))
}

func (r *jobsReader) runStep(j *Job, cfg *N, dir string, line int) {
	cmd, name := cfg.Str(), ""
	var env []KV
	d := dir
	if cfg.IsMap() {
		cmd, name = cfg.Get("command").Str(), cfg.Get("name").Str()
		cfg.Get("environment").Pairs(func(k string, v *N, _ int) { env = append(env, KV{k, v.Str()}) })
		if wd := cfg.Get("working_directory").Str(); wd != "" {
			d = wd
		}
		for _, k := range []string{"when", "background", "no_output_timeout"} {
			if cfg.Has(k) && (k != "when" || cfg.Get(k).Str() != "always") {
				r.rp.note("jobs.step.run_when", cfg.LineOf(k), k)
			}
		}
	}
	cmd = r.params(cmd, cfg, line)
	j.Steps = append(j.Steps, Step{Name: name, Run: cmd, Dir: d, Env: env, Line: line})
	r.rp.note("jobs.step.run", line)
}

func (r *jobsReader) persistStep(j *Job, cfg *N, line int) {
	name := "workspace-" + j.ID
	root := cfg.Get("root").Str()
	var paths []string
	for _, p := range cfg.Get("paths").Strings() {
		if root != "" && root != "." {
			p = strings.TrimSuffix(root, "/") + "/" + p
		}
		paths = append(paths, p)
	}
	j.Artifacts = append(j.Artifacts, Artifact{Name: sanitizeArtifact(name), Paths: paths, When: "success"})
	r.arts[j.ID] = sanitizeArtifact(name)
	r.rp.note("jobs.step.workspace", line, sanitizeArtifact(name))
}
