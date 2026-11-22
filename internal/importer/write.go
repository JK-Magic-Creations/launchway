package importer

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Options steer the writer.
type Options struct {
	// Name is the pipeline's name; empty takes the source's name.
	Name string
	// Dry marks every step that delivers (pushes an image, releases, deploys)
	// as not yet enabled: it is skipped until the pipeline's input
	// enable_delivery is true. One switch turns them all on.
	Dry bool
	// SecretStore is the Station secret store the written secret references name.
	SecretStore string
	// DefaultImage runs jobs that name no image.
	DefaultImage string
	// NoTemplates writes explicit steps even where a template fits.
	NoTemplates bool
	// Templates overrides a job's template (a fix-up): job id to template name,
	// or "none" for explicit steps.
	Templates map[string]string
	// SecretKeys renames the secret a source secret is read from (a fix-up):
	// source name to the key in the secret store. Names only, never values.
	SecretKeys map[string]string
}

// StepRef says where a job went in the written pipeline.
type StepRef struct {
	Job      string `json:"job"`
	Step     string `json:"step"`
	Line     int    `json:"line"`
	Template string `json:"template,omitempty"`
}

// Plan is the proposed Launchway pipeline with everything that was found.
type Plan struct {
	Format   string    `json:"format"`
	Files    []string  `json:"files"`
	Name     string    `json:"name"`
	Pipeline string    `json:"pipeline"` // the text of .launchway/pipeline.yaml
	Findings []Finding `json:"findings"`
	Steps    []StepRef `json:"steps"`
	Secrets  []string  `json:"secrets"` // names to create, never values
	Dry      bool      `json:"dry"`
}

const (
	switchName = "enable_delivery"
	pipeDefImg = "docker.io/library/ubuntu:24.04"
)

var (
	badStepChars = regexp.MustCompile(`[^a-z0-9_-]+`)
	eachRef      = regexp.MustCompile(`\$\{LAUNCHWAY_EACH_([A-Z0-9_]+)\}`)
	loopValueRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,39}$`)
	loopKeyRe    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
)

func stepName(s string) string {
	s = badStepChars.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-_")
	if s == "" {
		s = "job"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

func pipeName(s string) string {
	s = strings.Trim(badStepChars.ReplaceAllString(strings.ToLower(s), "-"), "-_")
	s = strings.ReplaceAll(s, "_", "-")
	if s == "" {
		return "imported"
	}
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

type writer struct {
	p           *Pipeline
	o           Options
	table       Table
	out         []Finding
	names       map[string]string // job id -> step name (the last step of the job)
	jobs        map[string]*Job
	steps       []StepRef
	secs        map[string]bool
	cont        *yaml.Node // containers
	needsSwitch bool
	noImage     []string
	noImageLine int
}

// Write turns the model into a Launchway pipeline file.
func Write(p *Pipeline, o Options) (*Plan, error) {
	if o.SecretStore == "" {
		o.SecretStore = "vault"
	}
	if o.DefaultImage == "" {
		o.DefaultImage = pipeDefImg
	}
	w := &writer{p: p, o: o, table: writerTable, names: map[string]string{}, jobs: map[string]*Job{}, secs: map[string]bool{}, cont: ymap()}
	root := ymap()
	name := o.Name
	if name == "" {
		name = p.Name
	}
	name = pipeName(name)
	yadd(root, "pipeline", ystr(name))

	// Names first, so needs can point at any job.
	used := map[string]bool{}
	for _, j := range p.Jobs {
		n := stepName(j.ID)
		for base, i := n, 2; used[n]; i++ {
			n = fmt.Sprintf("%s-%d", base, i)
		}
		used[n] = true
		w.names[j.ID] = n
		w.jobs[j.ID] = j
	}

	steps := ymap()
	for _, j := range p.Jobs {
		w.job(steps, j)
	}
	if len(w.noImage) > 0 {
		w.note("write.image.default", w.noImageLine, strings.Join(w.noImage, ", "), w.o.DefaultImage)
	}
	w.on(root)
	if p.Supersede {
		yadd(root, "supersede", ybool(true))
	}
	if w.needsSwitch || o.Dry {
		in := ymap()
		sw := yadd(in, switchName, ymap())
		yadd(sw, "type", ystr("boolean"))
		yadd(sw, "default", ybool(false))
		yadd(sw, "description", ystr("Turn on the steps that deliver (push an image, release, deploy). Off while you compare this pipeline with the old one."))
		w.addInputs(root, in)
	} else {
		w.addInputs(root, nil)
	}
	if len(p.Env) > 0 {
		d := ymap()
		yadd(d, "env", ykv(p.Env))
		yadd(root, "defaults", d)
	}
	if len(w.cont.Content) > 0 {
		yadd(root, "containers", w.cont)
	}
	if len(steps.Content) == 0 {
		return nil, fmt.Errorf("nothing was found to run: the files hold no jobs the importer could read")
	}
	yadd(root, "steps", steps)

	text, err := yencode(root)
	if err != nil {
		return nil, err
	}
	head := fmt.Sprintf("# Imported from %s by launchway import pipeline. The old files are untouched.\n# Read the findings it printed: lines marked NEEDS A PERSON below are not converted.\n", p.File)
	if o.Dry {
		head += "# Dry import: steps that deliver are off. Set the input " + switchName + " default to true to turn them all on.\n"
	}
	text = head + text
	plan := &Plan{Format: p.Format, Files: []string{p.File}, Name: name, Pipeline: text, Dry: o.Dry}
	plan.Findings = append(append([]Finding{}, p.Findings...), w.out...)
	sortFindings(plan.Findings)
	for n := range w.secs {
		plan.Secrets = append(plan.Secrets, n)
	}
	sort.Strings(plan.Secrets)
	plan.Steps = w.locate(text)
	for i := range plan.Findings {
		f := &plan.Findings[i]
		if f.Step == "" && f.Fix == "" {
			continue
		}
	}
	return plan, nil
}

// locate finds the line of each step in the text.
func (w *writer) locate(text string) []StepRef {
	lines := strings.Split(text, "\n")
	in := false
	pos := map[string]int{}
	for i, l := range lines {
		if l == "steps:" {
			in = true
			continue
		}
		if in && len(l) > 2 && l[0] == ' ' && l[1] == ' ' && l[2] != ' ' && l[2] != '#' && strings.HasSuffix(l, ":") {
			pos[strings.TrimSuffix(strings.TrimSpace(l), ":")] = i + 1
		}
	}
	out := w.steps
	for i := range out {
		out[i].Line = pos[out[i].Step]
	}
	return out
}

func (w *writer) addInputs(root *yaml.Node, extra *yaml.Node) {
	if len(w.p.Inputs) == 0 && extra == nil {
		return
	}
	in := ymap()
	for _, i := range w.p.Inputs {
		m := yadd(in, i.Name, ymap())
		m.Style = yaml.FlowStyle
		if i.Type != "" && i.Type != "string" {
			yadd(m, "type", ystr(i.Type))
		}
		if i.Description != "" {
			yadd(m, "description", ystr(oneLine(i.Description, 200)))
		}
		if len(i.Options) > 0 {
			yadd(m, "options", ystrs(i.Options))
		}
		if i.Default != "" {
			if i.Type == "boolean" {
				yadd(m, "default", ybool(i.Default == "true"))
			} else {
				yadd(m, "default", ystr(i.Default))
			}
		}
		if i.Required {
			yadd(m, "required", ybool(true))
		}
	}
	if extra != nil {
		for k := 0; k+1 < len(extra.Content); k += 2 {
			in.Content = append(in.Content, extra.Content[k], extra.Content[k+1])
		}
	}
	yadd(root, "inputs", in)
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		s = s[:limit-1] + "…"
	}
	return s
}

func (w *writer) on(root *yaml.Node) {
	t := w.p.On
	if !t.Present {
		return
	}
	on := ymap()
	if t.Push != nil {
		m := yadd(on, "push", ymap())
		m.Style = yaml.FlowStyle
		if len(t.Push.Branches) > 0 {
			yadd(m, "branches", ystrs(t.Push.Branches))
		}
		if len(t.Push.Tags) > 0 {
			yadd(m, "tags", ystrs(t.Push.Tags))
		}
		if len(t.Push.Paths) > 0 {
			yadd(m, "paths", ystrs(t.Push.Paths))
		} else if len(t.Push.PathsIgnore) > 0 {
			yadd(m, "paths_ignore", ystrs(t.Push.PathsIgnore))
		}
		if len(t.Push.Branches)+len(t.Push.Tags) == 0 {
			// Launchway needs a branch or a tag: a push trigger with neither is every push to the default.
			m.Content = nil
			yadd(m, "branches", ystrs([]string{"*"}))
		}
	}
	if t.PR != nil {
		m := yadd(on, "pull_request", ymap())
		m.Style = yaml.FlowStyle
		if len(t.PR.Into) > 0 {
			yadd(m, "into", ystrs(t.PR.Into))
		}
		if len(t.PR.Paths) > 0 {
			yadd(m, "paths", ystrs(t.PR.Paths))
		} else if len(t.PR.PathsIgnore) > 0 {
			yadd(m, "paths_ignore", ystrs(t.PR.PathsIgnore))
		}
	}
	if len(t.Schedules) > 0 {
		s := yadd(on, "schedule", yseq(false))
		for _, sc := range t.Schedules {
			m := ymap()
			m.Style = yaml.FlowStyle
			yadd(m, "cron", ystr(sc.Cron))
			if sc.Zone != "" {
				yadd(m, "timezone", ystr(sc.Zone))
			}
			s.Content = append(s.Content, m)
		}
	}
	if len(on.Content) > 0 {
		yadd(root, "on", on)
	}
}

func (w *writer) note(code string, line int, args ...any) *Finding {
	c := w.table.find(code)
	if c == nil {
		panic("importer: writer construct not in the table: " + code)
	}
	w.out = append(w.out, Finding{Code: code, Class: c.Class, Message: fmt.Sprintf(c.Note, args...), Next: c.Next, File: w.p.File, Line: line})
	return &w.out[len(w.out)-1]
}

func (w *writer) needs(j *Job, extra string) *yaml.Node {
	var list []string
	if extra != "" {
		list = append(list, extra)
	}
	for _, n := range j.Needs {
		if s, ok := w.names[n]; ok {
			list = append(list, s)
		} else {
			w.note("write.needs.unknown", j.Line, n, j.ID)
		}
	}
	if len(list) == 0 {
		return nil
	}
	if len(list) == 1 {
		return ystr(list[0])
	}
	return ystrs(list)
}

func (w *writer) whenNode(j *Job, gate bool) *yaml.Node {
	c := j.When
	if c.Empty() && !gate {
		return nil
	}
	m := ymap()
	m.Style = yaml.FlowStyle
	if len(c.Branch) > 0 {
		yadd(m, "branch", oneOrList(c.Branch))
	}
	if len(c.Tag) > 0 {
		yadd(m, "tag", oneOrList(c.Tag))
	}
	if len(c.Event) > 0 {
		yadd(m, "event", oneOrList(c.Event))
	}
	if len(c.Changed) > 0 {
		yadd(m, "changed", ystrs(c.Changed))
	}
	if gate {
		yadd(m, "input", ystr(switchName))
	}
	if c.Upstream != "" {
		yadd(m, "upstream", ystr(c.Upstream))
	}
	return m
}

func oneOrList(l []string) *yaml.Node {
	if len(l) == 1 {
		return ystr(l[0])
	}
	return ystrs(l)
}

func (w *writer) job(steps *yaml.Node, j *Job) {
	name := w.names[j.ID]
	gate := w.o.Dry && j.Delivers
	if j.Delivers && !w.o.Dry {
		w.needsSwitch = false
	}
	if gate {
		w.note("write.dry.gated", j.Line, name, j.DeliverWhy)
	}
	needsPrev := ""
	if j.Approval != nil {
		an := name + "-approval"
		m := ymap()
		yadd(m, "kind", ystr("approval"))
		if n := w.needs(j, ""); n != nil {
			yadd(m, "needs", n)
		}
		if gate {
			yadd(m, "when", w.whenNode(&Job{}, true))
		}
		msg := j.Approval.Message
		if msg == "" {
			msg = "Run " + name + "?"
		}
		yadd(m, "message", ystr(oneLine(msg, 500)))
		if len(j.Approval.Users) > 0 {
			a := ymap()
			a.Style = yaml.FlowStyle
			yadd(a, "users", ystrs(j.Approval.Users))
			yadd(m, "approvers", a)
			w.note("write.approvers.users", j.Line, an)
		}
		w.addStep(steps, an, m, nil)
		needsPrev = an
		if j.ApprovalOnly {
			w.names[j.ID] = an
			w.steps = append(w.steps, StepRef{Job: j.ID, Step: an})
			return
		}
	}

	m := ymap()
	ref := StepRef{Job: j.ID, Step: name}
	switch {
	case j.Image2 != nil:
		w.imageStep(steps, j, name, needsPrev, gate)
		return
	case j.Template != nil && !w.o.NoTemplates && w.o.Templates[j.ID] != "none":
		t := j.Template
		tn := t.Name
		if o := w.o.Templates[j.ID]; o != "" {
			tn = o
		}
		yadd(m, "uses", ystr("launchway/"+tn))
		if len(t.With) > 0 {
			wm := ymap()
			wm.Style = yaml.FlowStyle
			for _, kv := range t.With {
				yadd(wm, kv.K, ystr(kv.V))
			}
			yadd(m, "with", wm)
		}
		if n := w.needs(j, needsPrev); n != nil {
			yadd(m, "needs", n)
		}
		if wh := w.whenNode(j, gate); wh != nil {
			yadd(m, "when", wh)
		}
		ref.Template = tn
		w.note("write.template", j.Line, name, tn)
		w.addStep(steps, name, m, j.NoteLines)
		w.steps = append(w.steps, ref)
		return
	}

	w.explicit(m, steps, j, name, needsPrev, gate)
	w.steps = append(w.steps, ref)
}

func (w *writer) addStep(steps *yaml.Node, name string, m *yaml.Node, comments []string) {
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
	if len(comments) > 0 {
		key.HeadComment = strings.Join(comments, "\n")
	}
	steps.Content = append(steps.Content, key, m)
}

func (w *writer) explicit(m, steps *yaml.Node, j *Job, name, needsPrev string, gate bool) {
	img := j.Image
	if img == "" {
		img = w.o.DefaultImage
		w.noImage = append(w.noImage, name)
		if w.noImageLine == 0 {
			w.noImageLine = j.Line
		}
	}
	var mx *Matrix
	if j.Matrix != nil {
		mx = j.Matrix
	}
	imgNode := ystr(img)
	if mx != nil {
		if f, ok := w.eachValue(j, img, "image"); ok {
			imgNode = f
		}
	}
	w.containerNode(m, j, name, imgNode)
	if n := w.needs(j, needsPrev); n != nil {
		yadd(m, "needs", n)
	}
	if wh := w.whenNode(j, gate); wh != nil {
		yadd(m, "when", wh)
	}
	if j.Timeout != "" {
		yadd(m, "timeout", ystr(j.Timeout))
	}
	if j.Retry > 0 {
		yadd(m, "retry", yint(min(j.Retry, 10)))
	}
	if j.AllowFail {
		yadd(m, "on_failure", ystr("continue"))
	}
	if mx != nil {
		w.matrix(m, j, mx)
	}
	if len(j.Env) > 0 {
		env := ymap()
		for _, kv := range j.Env {
			if f, ok := w.eachValue(j, kv.V, "env"); ok && mx != nil {
				yadd(env, kv.K, f)
			} else {
				yadd(env, kv.K, ystr(kv.V))
			}
		}
		yadd(m, "env", env)
	}
	w.secretsNode(m, j)
	script, _ := w.script(j)
	yadd(m, "run", ystr(script))
	w.cachesNode(m, j)
	if len(j.Tests)+len(j.Coverage)+len(j.Findings) > 0 {
		r := ymap()
		if len(j.Tests) > 0 {
			yadd(r, "tests", ystrs(j.Tests))
		}
		if len(j.Coverage) > 0 {
			yadd(r, "coverage", ystrs(j.Coverage))
		}
		if len(j.Findings) > 0 {
			yadd(r, "findings", ystrs(j.Findings))
		}
		yadd(m, "reports", r)
	}
	w.artifactsNode(m, j)
	w.fetchNode(m, j)
	w.addStep(steps, name, m, j.NoteLines)
}

// containerNode writes the job's image, or a container group when it has services.
func (w *writer) containerNode(m *yaml.Node, j *Job, name string, imgNode *yaml.Node) {
	if len(j.Services) > 0 {
		c := ymap()
		c.Style = yaml.FlowStyle
		yadd(c, "image", imgNode)
		cn := name
		yadd(w.cont, cn, c)
		for _, s := range j.Services {
			sc := ymap()
			yadd(sc, "image", ystr(s.Image))
			yadd(sc, "service", ybool(true))
			if len(s.Env) > 0 {
				yadd(sc, "env", ykv(s.Env))
			}
			if len(s.Command) > 0 {
				yadd(sc, "command", ystrs(s.Command))
			}
			if s.Port != "" {
				r := ymap()
				r.Style = yaml.FlowStyle
				yadd(r, "tcp", yint(atoi(s.Port)))
				yadd(sc, "ready", r)
			}
			yadd(w.cont, name+"-"+stepName(s.Name), sc)
		}
		yadd(m, "container", ystr(name))
	} else {
		yadd(m, "image", imgNode)
	}
}

func (w *writer) secretsNode(m *yaml.Node, j *Job) {
	if len(j.SecretEnv) == 0 {
		return
	}
	sm := ymap()
	for _, kv := range j.SecretEnv {
		key := kv.V
		if k := w.o.SecretKeys[kv.V]; k != "" {
			key = k
		}
		r := ymap()
		r.Style = yaml.FlowStyle
		yadd(r, "store", ystr(w.o.SecretStore))
		yadd(r, "key", ystr(key))
		yadd(r, "field", ystr("value"))
		yadd(sm, kv.K, r)
		if !w.secs[kv.V] {
			w.secs[kv.V] = true
			ln := j.SecretLine[kv.V]
			if ln == 0 {
				ln = j.Line
			}
			f := w.note("write.secret", ln, kv.V, w.o.SecretStore)
			f.Fix, f.Name = "secret", kv.V
		}
	}
	yadd(m, "secrets", sm)
}

func (w *writer) cachesNode(m *yaml.Node, j *Job) {
	if len(j.Caches) == 0 {
		return
	}
	cl := yseq(false)
	for _, c := range j.Caches {
		cm := ymap()
		cm.Style = yaml.FlowStyle
		yadd(cm, "name", ystr(stepName(c.Name)))
		yadd(cm, "paths", ystrs(trimSlashes(c.Paths)))
		k := ymap()
		k.Style = yaml.FlowStyle
		if len(c.KeyFiles) > 0 {
			yadd(k, "files", ystrs(c.KeyFiles))
		}
		if c.KeyText != "" || len(c.KeyFiles) == 0 {
			t := c.KeyText
			if t == "" {
				t = stepName(c.Name)
			}
			yadd(k, "text", ystr(t))
		}
		yadd(cm, "key", k)
		cl.Content = append(cl.Content, cm)
	}
	yadd(m, "cache", cl)
}

func (w *writer) artifactsNode(m *yaml.Node, j *Job) {
	if len(j.Artifacts) == 0 {
		return
	}
	al := yseq(false)
	for _, a := range j.Artifacts {
		am := ymap()
		am.Style = yaml.FlowStyle
		yadd(am, "name", ystr(a.Name))
		yadd(am, "paths", ystrs(trimSlashes(a.Paths)))
		if a.Retain != "" {
			yadd(am, "retain", ystr(a.Retain))
		}
		if a.When != "" && a.When != "success" {
			yadd(am, "when", ystr(a.When))
		}
		al.Content = append(al.Content, am)
	}
	yadd(m, "artifacts", al)
}

func (w *writer) fetchNode(m *yaml.Node, j *Job) {
	if len(j.Fetch) == 0 {
		return
	}
	fl := yseq(false)
	for _, f := range w.expandFetch(j) {
		from := w.producer(j, f.Name)
		if from == "" {
			w.note("write.fetch.unknown", j.Line, f.Name)
			continue
		}
		fm := ymap()
		fm.Style = yaml.FlowStyle
		yadd(fm, "from", ystr(from))
		yadd(fm, "name", ystr(f.Name))
		if f.Into != "" {
			yadd(fm, "into", ystr(f.Into))
		}
		fl.Content = append(fl.Content, fm)
	}
	if len(fl.Content) > 0 {
		yadd(m, "fetch", fl)
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// producer finds the job upstream of j that keeps an artifact of the name.
func (w *writer) producer(j *Job, art string) string {
	seen := map[string]bool{}
	var walk func(ids []string) string
	walk = func(ids []string) string {
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			u := w.jobs[id]
			if u == nil {
				continue
			}
			for _, a := range u.Artifacts {
				if a.Name == art {
					return w.names[id]
				}
			}
			if s := walk(u.Needs); s != "" {
				return s
			}
		}
		return ""
	}
	return walk(j.Needs)
}

// eachValue turns a value with one matrix reference into the loop form.
func (w *writer) eachValue(j *Job, s, where string) (*yaml.Node, bool) {
	ms := eachRef.FindAllStringSubmatch(s, -1)
	if len(ms) != 1 || j.Matrix == nil {
		return nil, false
	}
	key := strings.ToLower(strings.ReplaceAll(ms[0][1], "_", "-"))
	if !contains(j.Matrix.Keys, key) {
		key = strings.ToLower(ms[0][1])
	}
	m := ymap()
	m.Style = yaml.FlowStyle
	yadd(m, "each", ystr(key))
	format := eachRef.ReplaceAllString(strings.ReplaceAll(s, "%", "%%"), "%s")
	if format != "%s" {
		yadd(m, "format", ystr(format))
	}
	return m, true
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (w *writer) matrix(m *yaml.Node, j *Job, mx *Matrix) {
	fe := ymap()
	for _, k := range mx.Keys {
		vals := mx.Values[k]
		ok := loopKeyRe.MatchString(k) && len(vals) > 0
		for _, v := range vals {
			ok = ok && loopValueRe.MatchString(v)
		}
		if !ok {
			w.note("write.matrix.values", j.Line, k)
			continue
		}
		yadd(fe, k, ystrs(vals))
	}
	if len(fe.Content) == 0 {
		return
	}
	yadd(m, "for_each", fe)
	if len(mx.Exclude) > 0 {
		yadd(m, "exclude", mapList(mx.Exclude))
	}
	if len(mx.Add) > 0 {
		yadd(m, "add", mapList(mx.Add))
	}
	if mx.MaxParallel > 0 {
		yadd(m, "max_parallel", yint(mx.MaxParallel))
	}
	if mx.FailFast != nil && !*mx.FailFast {
		yadd(m, "fail_fast", ybool(false))
	}
}

func mapList(l []map[string]string) *yaml.Node {
	s := yseq(false)
	for _, e := range l {
		m := ymap()
		m.Style = yaml.FlowStyle
		keys := make([]string, 0, len(e))
		for k := range e {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			yadd(m, k, ystr(e[k]))
		}
		s.Content = append(s.Content, m)
	}
	return s
}

var bashisms = regexp.MustCompile(`\[\[|<\(|pipefail|\$\{[a-zA-Z_]+[,^]{1,2}\}|\bsource\b|<<<|\barray\b|declare -`)

// script joins a job's steps into one script.
func (w *writer) script(j *Job) (string, []string) {
	var pre []string
	var b strings.Builder
	for _, c := range j.Caches {
		_ = c
	}
	for _, e := range cacheExports(j) {
		b.WriteString(e + "\n")
	}
	bash := false
	for i, s := range j.Steps {
		if bashisms.MatchString(s.Run) {
			bash = true
		}
		body := tidyScript(s.Run)
		wrap := s.Dir != "" || len(s.Env) > 0
		if s.Name != "" && (len(j.Steps) > 1) {
			b.WriteString("# " + oneLine(s.Name, 120) + "\n")
		}
		if s.Cond != "" {
			b.WriteString("# NEEDS A PERSON: this step had the condition `" + oneLine(s.Cond, 160) + "` and now always runs\n")
		}
		if wrap {
			b.WriteString("(\n")
			if s.Dir != "" {
				b.WriteString("  cd " + shq(s.Dir) + "\n")
			}
			for _, e := range s.Env {
				b.WriteString("  export " + e.K + "=" + shq(e.V) + "\n")
			}
			for _, l := range strings.Split(body, "\n") {
				b.WriteString("  " + l + "\n")
			}
			b.WriteString(")\n")
		} else {
			b.WriteString(body + "\n")
		}
		_ = i
	}
	if bash {
		w.note("write.shell.bash", j.Line, w.names[j.ID])
	}
	return b.String(), pre
}

func shq(s string) string {
	if strings.Contains(s, "$") && !strings.ContainsAny(s, "\"`\\\n") {
		return `"` + s + `"` // variable references stay live
	}
	if s != "" && !strings.ContainsAny(s, " \t\n'\"$`\\;&|<>()*?[]{}!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cacheExports points toolchains whose caches live in the home directory at a
// directory below the workspace, which is what a Launchway cache can keep.
func cacheExports(j *Job) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range j.Caches {
		for _, p := range c.Paths {
			if e := homeCacheExports[p]; e != "" && !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	return out
}

func (w *writer) imageStep(steps *yaml.Node, j *Job, name, needsPrev string, gate bool) {
	ib := j.Image2
	mk := func(push bool) *yaml.Node {
		m := ymap()
		yadd(m, "kind", ystr("image"))
		if ib.Context != "" && ib.Context != "." {
			yadd(m, "context", ystr(ib.Context))
		}
		if ib.File != "" && ib.File != "Dockerfile" {
			yadd(m, "file", ystr(ib.File))
		}
		if ib.Target != "" {
			yadd(m, "target", ystr(ib.Target))
		}
		if len(ib.Platforms) > 0 {
			yadd(m, "platforms", ystrs(ib.Platforms))
		}
		if len(ib.BuildArgs) > 0 {
			ba := ymap()
			for _, kv := range ib.BuildArgs {
				if kv.V == "{sha}" {
					v := ymap()
					v.Style = yaml.FlowStyle
					yadd(v, "run", ystr("sha"))
					yadd(ba, kv.K, v)
				} else {
					yadd(ba, kv.K, ystr(kv.V))
				}
			}
			yadd(m, "build_args", ba)
		}
		yadd(m, "push", ybool(push))
		if push {
			tl := yseq(false)
			tags := ib.Tags
			if len(tags) == 0 {
				tags = []string{"{sha}"}
			}
			for _, t := range tags {
				tm := ymap()
				tm.Style = yaml.FlowStyle
				yadd(tm, "repository", ystr(ib.Repo))
				if t == "{sha}" {
					v := ymap()
					v.Style = yaml.FlowStyle
					yadd(v, "run", ystr("sha"))
					yadd(tm, "tag", v)
				} else {
					yadd(tm, "tag", ystr(t))
				}
				tl.Content = append(tl.Content, tm)
			}
			yadd(m, "tags", tl)
		}
		return m
	}
	if ib.Repo == "" {
		ib.Repo = "app"
	}
	if !ib.Push {
		m := mk(false)
		w.finishImage(m, j, needsPrev, false)
		w.addStep(steps, name, m, j.NoteLines)
		w.steps = append(w.steps, StepRef{Job: j.ID, Step: name})
		return
	}
	if !gate {
		m := mk(true)
		w.finishImage(m, j, needsPrev, false)
		w.addStep(steps, name, m, j.NoteLines)
		w.steps = append(w.steps, StepRef{Job: j.ID, Step: name})
		w.note("write.image.registry", j.Line, name, ib.Repo)
		return
	}
	// Dry: build with nothing pushed, then a second step that pushes once delivery is on.
	m := mk(false)
	w.finishImage(m, j, needsPrev, false)
	w.addStep(steps, name, m, j.NoteLines)
	w.steps = append(w.steps, StepRef{Job: j.ID, Step: name})
	pn := name + "-push"
	pm := mk(true)
	yadd(pm, "needs", ystr(name))
	if wh := w.whenNode(j, true); wh != nil {
		yadd(pm, "when", wh)
	}
	w.addStep(steps, pn, pm, nil)
	w.steps = append(w.steps, StepRef{Job: j.ID, Step: pn})
	w.names[j.ID] = pn
	w.note("write.image.registry", j.Line, pn, ib.Repo)
}

func (w *writer) finishImage(m *yaml.Node, j *Job, needsPrev string, gate bool) {
	if n := w.needs(j, needsPrev); n != nil {
		yadd(m, "needs", n)
	}
	if wh := w.whenNode(j, gate); wh != nil {
		yadd(m, "when", wh)
	}
	if j.Timeout != "" {
		yadd(m, "timeout", ystr(j.Timeout))
	}
}
