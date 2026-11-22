package importer

import (
	"fmt"
	"regexp"
	"strings"
)

func init() {
	register(&Format{
		ID: "tasks-pipelines", Title: "Tasks-and-stages pipeline file", Where: "a file named *-pipelines.yml at the top of the repository",
		Detect: func(p string, head []byte) bool {
			return !strings.Contains(p, "/") && tasksName.MatchString(p)
		},
		Read: readTasks, Table: tasksTable,
		Sections: []string{"Triggers", "Structure", "Steps and tasks", "Variables"},
	})
}

var (
	tasksName = regexp.MustCompile(`^[a-z0-9]+-pipelines\.ya?ml$`)
	macroRe   = regexp.MustCompile(`\$\(([A-Za-z][A-Za-z0-9_.]*)\)`)
	tplExpr   = regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)
	macros    = map[string]string{"Build.SourceVersion": "LAUNCHWAY_COMMIT", "Build.BuildId": "LAUNCHWAY_RUN_ID", "Build.BuildNumber": "LAUNCHWAY_RUN_NUMBER", "Build.SourceBranchName": "LAUNCHWAY_BRANCH", "System.DefaultWorkingDirectory": "PWD", "Build.SourcesDirectory": "PWD"}
)

type tasksReader struct {
	p         *Pipeline
	rp        reporter
	vars      map[string]string
	resC      *N
	used      map[string]bool
	secrets   map[string]bool
	rootPool  *N
	poolNoted bool
}

func readTasks(root File, _ map[string]File, lim Limits) *Pipeline {
	p := &Pipeline{Format: "tasks-pipelines", File: root.Path, Name: "ci"}
	rp := reporter{table: tasksTable, file: root.Path, out: &p.Findings}
	t, err := ParseTree(root.Content, lim)
	if err != nil || !t.IsMap() {
		msg := "This file is not a mapping"
		if err != nil {
			msg = err.Error()
		}
		p.Findings = append(p.Findings, Finding{Code: "read.error", Class: Manual, Message: msg, Next: "Fix the file, or import it by hand", File: root.Path, Line: 1})
		return p
	}
	r := &tasksReader{p: p, rp: rp, vars: map[string]string{}, used: map[string]bool{}, secrets: map[string]bool{}}
	r.rootPool = t.Get("pool")
	r.triggers(t)
	r.variables(t)
	if res := t.Get("resources"); res != nil {
		r.resC = res.Get("containers")
		var names []string
		for _, k := range []string{"repositories", "pipelines", "builds", "packages", "webhooks"} {
			if res.Has(k) {
				names = append(names, k)
			}
		}
		if len(names) > 0 {
			rp.note("tasks.resources", t.LineOf("resources"), strings.Join(names, ", "))
		}
	}
	top := &stageInfo{}
	switch {
	case t.Has("stages"):
		r.stages(t.Get("stages"), t)
	case t.Has("jobs"):
		for _, jn := range t.Get("jobs").List() {
			r.job(jn, "", t, top)
		}
	case t.Has("steps"):
		j := &Job{ID: "build", Line: t.Line}
		r.applyHeader(j, t, t)
		r.steps(j, t.Get("steps"))
		r.finishJob(j)
		r.p.Jobs = append(r.p.Jobs, j)
	default:
		p.Findings = append(p.Findings, Finding{Code: "read.shape", Class: Manual, Message: "The file has no stages, jobs or steps", Next: "Choose another file", File: root.Path, Line: 1})
	}
	sortFindings(p.Findings)
	return p
}

type stageInfo struct{ ids []string }

func (r *tasksReader) triggers(t *N) {
	p := r.p
	p.On.Present = true
	p.On.Manual = true
	tr := t.Get("trigger")
	switch {
	case tr == nil:
		p.On.Push = &Push{Branches: []string{"*"}}
		r.rp.note("tasks.trigger.implied", 1)
	case tr.IsScalar() && tr.Val == "none":
	case tr.IsSeq():
		p.On.Push = r.filterPush(&N{Kind: kMap, Keys: []string{"branches"}, KeyLine: []int{tr.Line}, Vals: []*N{{Kind: kMap, Keys: []string{"include"}, KeyLine: []int{tr.Line}, Vals: []*N{tr}, Line: tr.Line}}, Line: tr.Line})
		r.rp.note("tasks.trigger", tr.Line)
	case tr.IsMap():
		p.On.Push = r.filterPush(tr)
		r.rp.note("tasks.trigger", tr.Line)
		if tr.Has("batch") {
			r.rp.note("tasks.trigger.exclude", tr.LineOf("batch"), "batch")
		}
	}
	if pr := t.Get("pr"); pr != nil && !(pr.IsScalar() && pr.Val == "none") {
		prr := &PullRequest{}
		branches := pr
		if pr.IsMap() {
			branches = pr.Get("branches")
			if branches.IsMap() {
				prr.Into = branches.Get("include").Strings()
				if branches.Has("exclude") {
					r.rp.note("tasks.trigger.exclude", branches.LineOf("exclude"), "the branch exclude list")
				}
			} else {
				prr.Into = branches.Strings()
			}
			if pa := pr.Get("paths"); pa != nil {
				prr.Paths = pa.Get("include").Strings()
			}
		} else {
			prr.Into = branches.Strings()
		}
		p.On.PR = prr
		r.rp.note("tasks.pr", pr.Line)
	} else if pr != nil {
		r.rp.note("tasks.pr", pr.Line)
	}
	for _, s := range t.Get("schedules").List() {
		if c := s.Get("cron").Str(); c != "" {
			p.On.Schedules = append(p.On.Schedules, Schedule{Cron: c})
			r.rp.note("tasks.schedules", s.Line)
		}
	}
}

func (r *tasksReader) filterPush(tr *N) *Push {
	pu := &Push{}
	if b := tr.Get("branches"); b != nil {
		pu.Branches = b.Get("include").Strings()
		if b.Has("exclude") {
			r.rp.note("tasks.trigger.exclude", b.LineOf("exclude"), "the branch exclude list")
		}
	}
	if tg := tr.Get("tags"); tg != nil {
		pu.Tags = tg.Get("include").Strings()
		if tg.Has("exclude") {
			r.rp.note("tasks.trigger.exclude", tg.LineOf("exclude"), "the tag exclude list")
		}
	}
	if pa := tr.Get("paths"); pa != nil {
		pu.Paths = pa.Get("include").Strings()
		if len(pu.Paths) == 0 {
			pu.PathsIgnore = pa.Get("exclude").Strings()
		}
	}
	var tags []string
	var br []string
	for _, b := range pu.Branches {
		if strings.HasPrefix(b, "refs/tags/") {
			tags = append(tags, strings.TrimPrefix(b, "refs/tags/"))
		} else {
			br = append(br, strings.TrimPrefix(b, "refs/heads/"))
		}
	}
	pu.Branches = br
	pu.Tags = append(pu.Tags, tags...)
	if len(pu.Branches)+len(pu.Tags) == 0 {
		pu.Branches = []string{"*"}
	}
	return pu
}

func (r *tasksReader) variables(t *N) {
	v := t.Get("variables")
	add := func(name, val string, line int) {
		r.vars[name] = val
		r.p.Env = append(r.p.Env, KV{name, val})
	}
	if v.IsMap() {
		v.Pairs(func(k string, x *N, line int) { add(k, x.Str(), line) })
	}
	for _, it := range v.List() {
		switch {
		case it.Has("group"):
			r.rp.note("tasks.variables.group", it.Line, "group "+it.Get("group").Str())
		case it.Has("template"):
			r.rp.note("tasks.variables.group", it.Line, "template "+it.Get("template").Str())
		case it.Has("name"):
			add(it.Get("name").Str(), it.Get("value").Str(), it.Line)
		}
	}
	if len(r.p.Env) > 0 {
		r.rp.note("tasks.variables", t.LineOf("variables"))
	}
	for _, pa := range t.Get("parameters").List() {
		n := pa.Get("name").Str()
		if n == "" {
			continue
		}
		in := Input{Name: inputName(n), Default: pa.Get("default").Str(), Type: "string"}
		switch pa.Get("type").Str() {
		case "boolean":
			in.Type = "boolean"
			if in.Default == "" {
				in.Default = "false"
			}
		case "number":
			in.Type = "number"
		}
		if vals := pa.Get("values").Strings(); len(vals) > 0 {
			in.Type, in.Options = "choice", vals
		}
		r.p.Inputs = append(r.p.Inputs, in)
		r.rp.note("tasks.variables", pa.Line)
	}
}

func (r *tasksReader) stages(stages *N, t *N) {
	r.rp.note("tasks.stages", stages.Line)
	byStage := map[string][]string{}
	var order []string
	for _, st := range stages.List() {
		name := st.Get("stage").Str()
		if name == "" {
			continue
		}
		order = append(order, name)
		info := &stageInfo{}
		before := len(r.p.Jobs)
		for _, jn := range st.Get("jobs").List() {
			r.job(jn, name, st, info)
		}
		byStage[name] = info.ids
		deps := st.Get("dependsOn")
		var depStages []string
		if deps != nil {
			depStages = deps.Strings()
		} else if i := len(order) - 2; i >= 0 {
			depStages = []string{order[i]}
		}
		var needIDs []string
		for _, ds := range depStages {
			needIDs = append(needIDs, byStage[ds]...)
		}
		for _, j := range r.p.Jobs[before:] {
			if c := st.Get("condition"); c != nil {
				r.condition(j, c.Str(), c.Line)
			}
			if len(j.Needs) == 0 {
				j.Needs = append([]string{}, needIDs...)
			}
		}
	}
}

func (r *tasksReader) job(jn *N, stage string, parent *N, info *stageInfo) {
	name := jn.Get("job").Str()
	dep := false
	if name == "" {
		name = jn.Get("deployment").Str()
		dep = name != ""
	}
	if name == "" {
		if jn.Has("template") {
			r.rp.note("tasks.step.template", jn.LineOf("template"), jn.Get("template").Str())
		}
		return
	}
	id := name
	if stage != "" {
		id = stage + "-" + name
	}
	id = stepName(id)
	for base, i := id, 2; r.used[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	r.used[id] = true
	j := &Job{ID: id, Line: jn.Line}
	r.applyHeader(j, jn, parent)
	for _, d := range jn.Get("dependsOn").Strings() {
		j.Needs = append(j.Needs, d2id(stage, d))
	}
	if jn.Has("dependsOn") {
		r.rp.note("tasks.job.depends", jn.LineOf("dependsOn"))
	}
	if c := jn.Get("condition"); c != nil {
		r.condition(j, c.Str(), c.Line)
	}
	if jn.Has("strategy") && jn.Get("strategy").Has("matrix") || jn.Has("strategy") && jn.Get("strategy").Has("parallel") {
		r.rp.note("tasks.strategy.matrix", jn.LineOf("strategy"))
	}
	var steps *N
	if dep {
		env := jn.Get("environment")
		en := env.Str()
		if env.IsMap() {
			en = env.Get("name").Str()
		}
		j.Delivers, j.DeliverWhy = true, "it deploys to the environment "+en
		r.rp.note("tasks.deployment", jn.Line, en).Fix = "approval"
		strat := jn.Get("strategy")
		for _, k := range []string{"canary", "rolling"} {
			if strat.Has(k) {
				r.rp.note("tasks.deployment.hooks", strat.LineOf(k), k)
			}
		}
		ro := strat.Get("runOnce")
		for _, k := range []string{"preDeploy", "routeTraffic", "postRouteTraffic", "on"} {
			if ro.Has(k) {
				r.rp.note("tasks.deployment.hooks", ro.LineOf(k), k)
			}
		}
		steps = ro.Get("deploy").Get("steps")
	} else {
		steps = jn.Get("steps")
	}
	r.steps(j, steps)
	r.finishJob(j)
	r.rp.note("tasks.job", jn.Line).Step = id
	r.p.Jobs = append(r.p.Jobs, j)
	if info != nil {
		info.ids = append(info.ids, id)
	}
}

func d2id(stage, d string) string {
	if stage != "" {
		return stepName(stage + "-" + d)
	}
	return stepName(d)
}

func (r *tasksReader) applyHeader(j *Job, n, parent *N) {
	if pool := firstN(n.Get("pool"), parent.Get("pool"), r.rootPool); pool != nil && (n.Has("pool") || !r.poolNoted) {
		r.poolNoted = true
		desc := pool.Get("vmImage").Str()
		if desc == "" {
			desc = pool.Get("name").Str()
		}
		if desc == "" {
			desc = pool.Str()
		}
		r.rp.note("tasks.pool", pool.Line, firstNonEmpty(desc, "the agent pool"))
	}
	if c := n.Get("container"); c != nil {
		img := c.Str()
		if c.IsMap() {
			img = c.Get("image").Str()
		}
		for _, rc := range r.resC.List() {
			if rc.Get("container").Str() == img {
				img = rc.Get("image").Str()
			}
		}
		j.Image = img
		r.rp.note("tasks.container", c.Line, img)
	}
	if sv := n.Get("services"); sv.IsMap() {
		sv.Pairs(func(k string, v *N, line int) {
			img := v.Str()
			for _, rc := range r.resC.List() {
				if rc.Get("container").Str() == img {
					img = rc.Get("image").Str()
					svc := Service{Name: k, Image: img}
					rc.Get("env").Pairs(func(ek string, ev *N, _ int) { svc.Env = append(svc.Env, KV{ek, ev.Str()}) })
					j.Services = append(j.Services, svc)
				}
			}
			r.rp.note("tasks.container", line, k)
		})
	}
	if t := n.Get("timeoutInMinutes").Str(); t != "" && atoi(t) > 0 {
		j.Timeout = fmt.Sprintf("%dm", atoi(t))
		r.rp.note("tasks.step.continue", n.LineOf("timeoutInMinutes"))
	}
	if v, ok := n.Get("continueOnError").Bool(); ok && v {
		j.AllowFail = true
		r.rp.note("tasks.step.continue", n.LineOf("continueOnError"))
	}
	v := n.Get("variables")
	if v.IsMap() {
		v.Pairs(func(k string, x *N, _ int) { j.Env = append(j.Env, KV{k, x.Str()}) })
	}
	for _, it := range v.List() {
		if it.Has("name") {
			j.Env = append(j.Env, KV{it.Get("name").Str(), it.Get("value").Str()})
		}
	}
}

func firstN(l ...*N) *N {
	for _, n := range l {
		if n != nil {
			return n
		}
	}
	return nil
}

func (r *tasksReader) finishJob(j *Job) {
	var all strings.Builder
	for _, s := range j.Steps {
		all.WriteString(s.Run + "\n")
	}
	if !j.Delivers {
		j.Delivers, j.DeliverWhy = delivers(j.ID, all.String(), false)
	}
	j.Image = betterJavaImage(j)
	if len(j.Steps) == 0 && j.Image2 == nil {
		j.Steps = []Step{{Run: "true"}}
	}
	if len(j.Services) == 0 {
		detectTemplate(j)
	}
}

func (r *tasksReader) condition(j *Job, c string, line int) {
	switch strings.ReplaceAll(strings.TrimSpace(c), " ", "") {
	case "always()":
		j.When.Upstream = "always"
		r.rp.note("tasks.job.condition_simple", line)
	case "failed()":
		j.When.Upstream = "failed"
		r.rp.note("tasks.job.condition_simple", line)
	case "succeeded()":
		r.rp.note("tasks.job.condition_simple", line)
	default:
		if m := regexp.MustCompile(`^eq\(variables\['Build\.SourceBranch'\],'refs/heads/([^']+)'\)$`).FindStringSubmatch(strings.ReplaceAll(c, " ", "")); m != nil {
			j.When.Branch = []string{m[1]}
			r.rp.note("tasks.job.condition_simple", line)
			return
		}
		r.rp.note("tasks.job.condition", line, oneLine(c, 140)).Step = j.ID
	}
}

func (r *tasksReader) steps(j *Job, steps *N) {
	for _, s := range steps.List() {
		line := s.Line
		name := s.Get("displayName").Str()
		if c := s.Get("condition"); c != nil {
			r.rp.note("tasks.job.condition", c.Line, oneLine(c.Str(), 140))
		}
		switch {
		case s.Has("script") || s.Has("bash"):
			key := "script"
			if s.Has("bash") {
				key = "bash"
			}
			text := r.macro(j, s.Get(key).Str(), line)
			st := Step{Name: name, Run: text, Dir: s.Get("workingDirectory").Str(), Line: line}
			s.Get("env").Pairs(func(k string, v *N, _ int) { st.Env = append(st.Env, KV{k, r.macro(j, v.Str(), line)}) })
			j.Steps = append(j.Steps, st)
			r.rp.note("tasks.step.script", line)
		case s.Has("pwsh") || s.Has("powershell"):
			r.rp.note("tasks.step.pwsh", line).Step = j.ID
			j.Steps = append(j.Steps, Step{Run: "echo 'NEEDS A PERSON: a PowerShell step ran here'; exit 1", Line: line})
		case s.Has("checkout"):
			r.rp.note("tasks.step.checkout", line)
		case s.Has("publish"):
			p := s.Get("publish").Str()
			an := firstNonEmpty(s.Get("artifact").Str(), "drop")
			j.Artifacts = append(j.Artifacts, Artifact{Name: sanitizeArtifact(an), Paths: []string{p}, When: "success"})
			r.rp.note("tasks.step.publish", line)
		case s.Has("download"):
			if an := s.Get("artifact").Str(); an != "" {
				j.Fetch = append(j.Fetch, Fetch{Name: sanitizeArtifact(an)})
				r.rp.note("tasks.step.publish", line)
			}
		case s.Has("template"):
			r.rp.note("tasks.step.template", line, s.Get("template").Str())
			j.Steps = append(j.Steps, Step{Run: "echo 'NEEDS A PERSON: a template of steps was included here'; exit 1", Line: line})
		case s.Has("task"):
			r.task(j, s, line)
		}
	}
}

func (r *tasksReader) macro(j *Job, s string, line int) string {
	s = tplExpr.ReplaceAllStringFunc(s, func(m string) string {
		e := tplExpr.FindStringSubmatch(m)[1]
		if strings.HasPrefix(e, "parameters.") {
			n := strings.TrimPrefix(e, "parameters.")
			if identRe.MatchString(n) {
				return "${LAUNCHWAY_INPUT_" + strings.ToUpper(inputName(n)) + "}"
			}
		}
		r.rp.note("tasks.expr", line, e)
		return m
	})
	return macroRe.ReplaceAllStringFunc(s, func(m string) string {
		n := macroRe.FindStringSubmatch(m)[1]
		if v, ok := macros[n]; ok {
			r.rp.note("tasks.macro", line, "$("+n+")", "$"+v)
			return "$" + v
		}
		if _, ok := r.vars[n]; ok {
			return "${" + n + "}"
		}
		for _, kv := range j.Env {
			if kv.K == n {
				return "${" + n + "}"
			}
		}
		if strings.Contains(n, ".") {
			r.rp.note("tasks.macro", line, "$("+n+")", "nothing (it is set only by the old system)")
			return m
		}
		j.SecretEnv = appendOnceKV(j.SecretEnv, KV{n, n})
		if j.SecretLine == nil {
			j.SecretLine = map[string]int{}
		}
		j.SecretLine[n] = line
		r.rp.note("tasks.macro.unset", line, n)
		return "${" + n + "}"
	})
}

func (r *tasksReader) task(j *Job, s *N, line int) {
	task := s.Get("task").Str()
	base := task
	if i := strings.IndexByte(base, '@'); i >= 0 {
		base = base[:i]
	}
	in := s.Get("inputs")
	switch base {
	case "NodeTool", "UsePythonVersion", "GoTool", "JavaToolInstaller", "UseNode", "UseDotNet":
		ver := firstNonEmpty(in.Get("versionSpec").Str(), in.Get("version").Str(), in.Get("versionSpec").Str())
		ver = strings.TrimSuffix(strings.Trim(ver, "^~>= "), ".x")
		fmtImg := map[string]string{"NodeTool": "docker.io/library/node:%s", "UseNode": "docker.io/library/node:%s", "UsePythonVersion": "docker.io/library/python:%s", "GoTool": "docker.io/library/golang:%s", "JavaToolInstaller": "docker.io/library/eclipse-temurin:%s-jdk", "UseDotNet": "mcr.microsoft.com/dotnet/sdk:%s"}[base]
		if ver != "" && j.Image == "" {
			j.Image = strings.Replace(fmtImg, "%s", ver, 1)
			r.rp.note("tasks.step.tool", line, j.Image)
			return
		}
		r.rp.note("tasks.step.task", line, task).Step = j.ID
	case "Cache":
		c := Cache{}
		for _, part := range strings.Split(in.Get("key").Str(), "|") {
			part = strings.TrimSpace(part)
			if part != "" && part != "$(Agent.OS)" && !strings.HasPrefix(part, "$(") && (strings.Contains(part, "/") || strings.Contains(part, ".")) {
				c.KeyFiles = append(c.KeyFiles, part)
			}
		}
		var shown []string
		p := in.Get("path").Str()
		if p != "" {
			if np, ok := HomeCachePath(p); ok {
				k, v := splitExport(homeCacheExports[p])
				j.Env = appendOnce(j.Env, KV{k, v})
				p = np
			}
			if strings.HasPrefix(p, "~") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "..") || strings.HasPrefix(p, "$(") {
				r.rp.note("tasks.step.cache_path", line, p)
				return
			}
			c.Paths, shown = []string{p}, []string{p}
		}
		if len(c.Paths) == 0 {
			return
		}
		c.Name = nameFromPath(p)
		if len(c.KeyFiles) == 0 {
			c.KeyText = c.Name
		}
		j.Caches = append(j.Caches, c)
		r.rp.note("tasks.step.cache", line, strings.Join(shown, ", "))
	case "PublishBuildArtifacts", "PublishPipelineArtifact":
		p := firstNonEmpty(in.Get("PathtoPublish").Str(), in.Get("targetPath").Str(), in.Get("path").Str())
		p = strings.TrimPrefix(p, "$(Build.ArtifactStagingDirectory)")
		p = strings.TrimPrefix(p, "/")
		if p == "" {
			p = "."
		}
		an := firstNonEmpty(in.Get("ArtifactName").Str(), in.Get("artifact").Str(), "drop")
		j.Artifacts = append(j.Artifacts, Artifact{Name: sanitizeArtifact(an), Paths: []string{p}, When: "success"})
		r.rp.note("tasks.step.publish", line)
	case "DownloadBuildArtifacts", "DownloadPipelineArtifact":
		an := firstNonEmpty(in.Get("artifactName").Str(), in.Get("artifact").Str(), "drop")
		j.Fetch = append(j.Fetch, Fetch{Name: sanitizeArtifact(an)})
		r.rp.note("tasks.step.publish", line)
	case "PublishTestResults":
		for _, f := range strings.Split(firstNonEmpty(in.Get("testResultsFiles").Str(), "**/TEST-*.xml"), "\n") {
			if f = strings.TrimSpace(f); f != "" {
				j.Tests = append(j.Tests, f)
			}
		}
		r.rp.note("tasks.step.tests", line)
	case "PublishCodeCoverageResults":
		if f := firstNonEmpty(in.Get("summaryFileLocation").Str(), in.Get("summaryFile").Str()); f != "" {
			j.Coverage = append(j.Coverage, strings.TrimPrefix(f, "$(System.DefaultWorkingDirectory)/"))
			r.rp.note("tasks.step.tests", line)
		}
	case "Docker":
		cmd := firstNonEmpty(in.Get("command").Str(), "buildAndPush")
		if cmd == "login" || cmd == "logout" {
			r.rp.note("tasks.step.task", line, task+" "+cmd)
			return
		}
		ib := &ImageBuild{Context: firstNonEmpty(in.Get("buildContext").Str(), "."), File: in.Get("Dockerfile").Str(), Repo: in.Get("repository").Str(), Push: cmd == "push" || cmd == "buildAndPush"}
		ib.Context = strings.TrimPrefix(strings.ReplaceAll(ib.Context, "$(Build.SourcesDirectory)", "."), "./")
		if ib.Context == "" {
			ib.Context = "."
		}
		ib.File = strings.TrimPrefix(strings.ReplaceAll(ib.File, "$(Build.SourcesDirectory)/", ""), "./")
		for _, t := range strings.Fields(strings.ReplaceAll(in.Get("tags").Str(), "\n", " ")) {
			if t == "$(Build.SourceVersion)" {
				t = "{sha}"
			}
			if strings.Contains(t, "$(") {
				continue
			}
			ib.Tags = append(ib.Tags, t)
		}
		if ib.Repo == "" || strings.Contains(ib.Repo, "$(") {
			ib.Repo = "app"
		}
		if ib.Push && len(ib.Tags) == 0 {
			ib.Tags = []string{"latest"}
		}
		j.Image2 = ib
		if ib.Push {
			j.Delivers, j.DeliverWhy = true, "it pushes an image"
		}
		r.rp.note("tasks.step.docker", line)
	default:
		r.rp.note("tasks.step.task", line, task).Step = j.ID
		j.Steps = append(j.Steps, Step{Run: "echo 'NEEDS A PERSON: the task " + strings.ReplaceAll(task, "'", "") + " ran here'; exit 1", Line: line})
	}
}
