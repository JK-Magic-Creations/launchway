package pipeline

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Verify steps (kind: verify) watch a deployment, or an environment, for a
// time window and decide whether it is healthy: passed, failed or
// inconclusive. They run no script. A deploy runner serving the
// environment's target reads the metrics and logs the step names from
// health sources its operator configured, one interval at a time, and the
// server judges what it read with the statistics of package verify. With
// on_fail a failing verification also aborts the deployment's rollout, or
// leaves it to the deployment's rollback step; a passing one lets the steps
// that need it (a rollout step that promotes, say) go on.
//
// Why a kind of its own: the queries need the network and the credentials
// of the deploy runner, which sits next to the monitoring systems and
// never runs anyone's script; the build runners that run scripts must not
// hold them.
const KindVerify = "verify"

// SchemaVerify is the server schema that brought verify and ai steps. A
// pipeline that uses them carries it as Requires.
const SchemaVerify = 19

// Defaults and limits of verify steps.
const (
	DefaultVerifyDuration = 10 * time.Minute
	DefaultVerifyInterval = time.Minute
	MinVerifyInterval     = 10 * time.Second
	MaxVerifyDuration     = 6 * time.Hour
	MaxVerifyDelay        = time.Hour
	MaxVerifyIntervals    = 120
	MaxVerifyMetrics      = 20
	MaxVerifyLogs         = 10
	MaxVerifyParams       = 30
	MaxVerifyQueryBytes   = 8 << 10
	DefaultVerifyBaseline = 3
	MaxVerifyBaseline     = 10
)

// Verify is what a verify step watches and how it decides.
type Verify struct {
	Delay       Duration `json:"delay,omitempty"` // before the first interval: let the new version warm up
	Duration    Duration `json:"duration"`
	Interval    Duration `json:"interval"`
	Sensitivity string   `json:"sensitivity"` // low, medium or high
	// Baseline is how many earlier passed verifications of the same
	// environment and service the step compares with; 0 compares with none.
	Baseline int `json:"baseline"`
	// OnFail is fail (the step fails; a rollback step that needs it then
	// runs), abort (also abort the deployment's rollout at once) or
	// rollback (abort the rollout if there is one, and leave the revert to
	// the deployment's rollback step, which must need this step).
	OnFail string `json:"on_fail"`
	// OnInconclusive is fail, pass or pause (wait for a person to mark it
	// passed or failed, like an approval).
	OnInconclusive string            `json:"on_inconclusive"`
	Params         map[string]*Value `json:"params,omitempty"`
	Metrics        []*VerifyMetric   `json:"metrics,omitempty"`
	Logs           []*VerifyLog      `json:"logs,omitempty"`
	Model          string            `json:"model,omitempty"`  // a model of the server's ai.models, to explain the verdict
	Decide         string            `json:"decide,omitempty"` // statistics (the default) or model-assisted
	lines          map[string]int    // where each key was written
	paramLines     map[string]int    // where each parameter was written
}

// VerifyMetric is one metric a verify step reads and how it is judged.
type VerifyMetric struct {
	Name   string `json:"name"`
	Source string `json:"source"` // a health source of the server's verification.sources
	// Query is written in the source's language (PromQL, LogQL, or an
	// Elasticsearch query as JSON); Statement names a query the source's
	// operator wrote (SQL and HTTP sources). Exactly one is set.
	Query       string   `json:"query,omitempty"`
	StableQuery string   `json:"stable_query,omitempty"` // the stable version's, to compare in the same interval
	Statement   string   `json:"statement,omitempty"`
	Index       string   `json:"index,omitempty"`     // Elasticsearch and OpenSearch
	Direction   string   `json:"direction,omitempty"` // higher-is-worse (default), lower-is-worse or either
	Max         *float64 `json:"max,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	FailFastMax *float64 `json:"fail_fast_max,omitempty"`
	FailFastMin *float64 `json:"fail_fast_min,omitempty"`
	// Failures is how many failing intervals in a row fail the metric.
	Failures     int      `json:"failures,omitempty"`
	MinIntervals int      `json:"min_intervals,omitempty"`
	MinPoints    int      `json:"min_points,omitempty"`
	Step         Duration `json:"step,omitempty"` // resolution of the readings; default 30s
	// NoBaseline turns the comparison with earlier verifications off for
	// this metric.
	NoBaseline bool `json:"no_baseline,omitempty"`
	Line       int  `json:"-"`
}

// VerifyLog is one log query a verify step reads, clusters and compares.
type VerifyLog struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Query       string `json:"query,omitempty"`
	StableQuery string `json:"stable_query,omitempty"`
	Statement   string `json:"statement,omitempty"`
	Index       string `json:"index,omitempty"`
	// Errors is auto (clusters with an error word count as errors) or all
	// (the query selects only errors).
	Errors     string `json:"errors,omitempty"`
	MaxLines   int    `json:"max_lines,omitempty"` // per interval; default 5000
	NoBaseline bool   `json:"no_baseline,omitempty"`
	Line       int    `json:"-"`
}

// Values of verify steps.
var (
	Sensitivities      = []string{"low", "medium", "high"}
	VerifyDirections   = []string{"higher-is-worse", "lower-is-worse", "either"}
	VerifyOnFail       = []string{"fail", "abort", "rollback"}
	VerifyInconclusive = []string{"fail", "pass", "pause"}
	VerifyDecide       = []string{"statistics", "model-assisted"}
	// DeploymentFacts are what { deployment: fact } may name in a verify
	// step's params: facts about the deployment it watches, as the deploy
	// runner last observed them.
	DeploymentFacts = []string{"canary_hash", "stable_hash", "namespace", "rollout", "application", "version", "previous_version", "environment", "service"}
	// VerifyOutputs are the outputs a verify step sets.
	VerifyOutputs = []string{"verdict", "summary"}
	// ReservedParams are filled in by the deploy runner: the interval's
	// start and end.
	ReservedParams = []string{"start", "end"}
)

var (
	verifyKeys       = []string{"kind", "description", "needs", "when", "timeout", "deployment", "environment", "service", "delay", "duration", "interval", "sensitivity", "baseline", "on_fail", "on_inconclusive", "params", "metrics", "logs", "model", "decide"}
	verifyMetricKeys = []string{"name", "source", "query", "stable_query", "statement", "index", "direction", "max", "min", "fail_fast", "failures", "min_intervals", "min_points", "step", "baseline"}
	verifyLogKeys    = []string{"name", "source", "query", "stable_query", "statement", "index", "errors", "max_lines", "baseline"}
	verifyNameRe     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	verifyParamRe    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	// placeholderRe finds {{name}} in a query. Other {{...}} text, such as
	// LogQL's {{.label}}, is not a placeholder and is left as written.
	placeholderRe = regexp.MustCompile(`\{\{([a-z][a-z0-9_]{0,39})\}\}`)
)

// IsVerify reports whether the step is a verification.
func (s *Step) IsVerify() bool { return s.Kind == KindVerify }

// Placeholders returns the parameter names a query uses, in order of first
// use.
func Placeholders(query string) []string {
	var out []string
	for _, m := range placeholderRe.FindAllStringSubmatch(query, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// UsesSchemaVerify reports whether the pipeline uses what schema 19
// brought: verify and ai steps.
func (p *Pipeline) UsesSchemaVerify() bool {
	for _, s := range p.Steps {
		if s.IsVerify() || s.IsAI() {
			return true
		}
	}
	return false
}

// VerifyEnvironment is the environment a verify step watches: its own, or
// its deployment's.
func (p *Pipeline) VerifyEnvironment(s *Step) (env, service string) {
	if s.Deployment != "" {
		if d := p.Step(s.Deployment); d != nil {
			return d.Environment, d.Service
		}
	}
	return s.Environment, s.Service
}

// verifyValues lists every value of a verify step's params, by name order.
func verifyValues(s *Step) []*Value {
	if s.Verify == nil {
		return nil
	}
	var out []*Value
	for _, k := range slices.Sorted(maps.Keys(s.Verify.Params)) {
		out = append(out, s.Verify.Params[k])
	}
	return out
}

func (ps *parser) parseVerifyStep(k, v *yaml.Node, at string) *Step {
	s := &Step{ID: k.Value, Line: k.Line, Kind: KindVerify}
	vf := &Verify{Duration: Duration(DefaultVerifyDuration), Interval: Duration(DefaultVerifyInterval), Sensitivity: "medium",
		Baseline: DefaultVerifyBaseline, OnFail: "fail", OnInconclusive: "fail", lines: map[string]int{}, paramLines: map[string]int{}}
	s.Verify = vf
	if ps.tmpl != nil {
		ps.add(k, at+".kind", "a verify step cannot be in a template yet; write it in the pipeline that uses the template")
		return s
	}
	if v.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(v.Content); i += 2 {
			key := v.Content[i]
			switch {
			case contains(verifyKeys, key.Value):
			case contains(loopKeys, key.Value):
				ps.add(key, at+"."+key.Value, "a verify step cannot be repeated by for_each; write one verify step for each environment")
			case contains(runOnly, key.Value) || key.Value == "uses" || key.Value == "with":
				ps.add(key, at+"."+key.Value, "a verify step runs no script: a deploy runner reads the metrics and logs it names, so it takes no %s", key.Value)
			default:
				ps.add(key, at+"."+key.Value, "%q is not a key of verify steps; expected one of: %s", key.Value, strings.Join(verifyKeys, ", "))
			}
		}
	}
	var timeoutNode *yaml.Node
	var timeout time.Duration
	dur := func(n *yaml.Node, where string) (time.Duration, bool) {
		d, err := time.ParseDuration(ps.scalar(n, where))
		if err != nil {
			ps.add(n, where, "write a duration such as 30s, 10m or 1h")
			return 0, false
		}
		return d, true
	}
	ps.eachKey(v, at, append(append([]string{}, verifyKeys...), loopKeys...), func(key, n *yaml.Node) {
		where := at + "." + key.Value
		vf.lines[key.Value] = n.Line
		switch key.Value {
		case "kind":
		case "description":
			s.Description = strings.TrimSpace(ps.str(n, where))
			switch {
			case strings.ContainsAny(s.Description, "\r\n"):
				ps.add(n, where, "a description is one line; put longer notes in a comment")
			case utf8.RuneCountInString(s.Description) > MaxDescriptionLen:
				ps.add(n, where, "the description is longer than %d characters", MaxDescriptionLen)
			}
		case "needs":
			s.Needs = ps.strList(n, where)
			s.NeedsLine = n.Line
		case "when":
			s.When = ps.parseWhen(n, where)
		case "timeout":
			if d, ok := dur(n, where); ok {
				timeoutNode, timeout = n, d
				s.timeoutSet = true
			}
		case "deployment":
			s.Deployment = ps.str(n, where)
			s.deploymentLine = n.Line
		case "environment":
			s.Environment = ps.str(n, where)
			s.environmentLine = n.Line
			if !nameRe.MatchString(s.Environment) {
				ps.add(n, where, "%q is not a valid environment name: use lower-case letters, digits and hyphens", s.Environment)
			}
		case "service":
			s.Service = ps.str(n, where)
			if !nameRe.MatchString(s.Service) {
				ps.add(n, where, "%q is not a valid service name: use lower-case letters, digits and hyphens", s.Service)
			}
		case "delay":
			if d, ok := dur(n, where); ok {
				if d < 0 || d > MaxVerifyDelay {
					ps.add(n, where, "delay must be between 0s and %s", Duration(MaxVerifyDelay).Short())
				}
				vf.Delay = Duration(d)
			}
		case "duration":
			if d, ok := dur(n, where); ok {
				vf.Duration = Duration(d)
			}
		case "interval":
			if d, ok := dur(n, where); ok {
				vf.Interval = Duration(d)
			}
		case "sensitivity":
			vf.Sensitivity = ps.str(n, where)
			if !slices.Contains(Sensitivities, vf.Sensitivity) {
				ps.add(n, where, "write low, medium or high: how large a deviation must be to fail (high fails on smaller ones)")
			}
		case "baseline":
			switch n.Value {
			case "false", "no", "off":
				vf.Baseline = 0
			default:
				vf.Baseline = ps.int(n, where)
				if vf.Baseline < 0 || vf.Baseline > MaxVerifyBaseline {
					ps.add(n, where, "baseline is how many earlier passed verifications to compare with: 0 to %d, or false", MaxVerifyBaseline)
				}
			}
		case "on_fail":
			vf.OnFail = ps.str(n, where)
			if !slices.Contains(VerifyOnFail, vf.OnFail) {
				ps.add(n, where, "write fail, abort (also abort the deployment's rollout) or rollback (abort it, and let the deployment's rollback step revert it)")
			}
		case "on_inconclusive":
			vf.OnInconclusive = ps.str(n, where)
			if !slices.Contains(VerifyInconclusive, vf.OnInconclusive) {
				ps.add(n, where, "write fail, pass or pause (wait for a person to mark it passed or failed)")
			}
		case "params":
			ps.parseVerifyParams(n, where, vf)
		case "metrics":
			if n.Kind != yaml.SequenceNode {
				ps.add(n, where, "metrics is a list, for example: - { name: error-rate, source: prometheus, query: '...', max: 0.01 }")
				return
			}
			if len(n.Content) > MaxVerifyMetrics {
				ps.add(n, where, "at most %d metrics", MaxVerifyMetrics)
			}
			for i, m := range n.Content {
				vf.Metrics = append(vf.Metrics, ps.parseVerifyMetric(m, fmt.Sprintf("%s[%d]", where, i)))
			}
		case "logs":
			if n.Kind != yaml.SequenceNode {
				ps.add(n, where, "logs is a list, for example: - { name: app, source: loki, query: '{namespace=\"shop\"}' }")
				return
			}
			if len(n.Content) > MaxVerifyLogs {
				ps.add(n, where, "at most %d log queries", MaxVerifyLogs)
			}
			for i, m := range n.Content {
				vf.Logs = append(vf.Logs, ps.parseVerifyLog(m, fmt.Sprintf("%s[%d]", where, i)))
			}
		case "model":
			vf.Model = ps.str(n, where)
			if !modelNameRe.MatchString(vf.Model) {
				ps.add(n, where, "%q is not a model name; name one of the server's ai.models", vf.Model)
			}
		case "decide":
			vf.Decide = ps.str(n, where)
			if !slices.Contains(VerifyDecide, vf.Decide) {
				ps.add(n, where, "write statistics (the default: the statistics decide) or model-assisted (the model may turn a pass into inconclusive or a fail, never a fail into a pass)")
			}
		}
	})
	if v.Kind != yaml.MappingNode {
		return s
	}
	iv, du := vf.Interval.D(), vf.Duration.D()
	switch {
	case iv < MinVerifyInterval:
		ps.add(k, at+".interval", "interval must be at least %s", Duration(MinVerifyInterval).Short())
	case du < iv:
		ps.add(k, at+".duration", "duration must be at least one interval (%s)", vf.Interval.Short())
	case du > MaxVerifyDuration:
		ps.add(k, at+".duration", "duration must be at most %s", Duration(MaxVerifyDuration).Short())
	case du%iv != 0:
		ps.add(k, at+".duration", "duration must be a whole number of intervals (%s is not a multiple of %s)", vf.Duration.Short(), vf.Interval.Short())
	case int(du/iv) > MaxVerifyIntervals:
		ps.add(k, at+".interval", "at most %d intervals; make the interval longer", MaxVerifyIntervals)
	}
	if len(vf.Metrics) == 0 && len(vf.Logs) == 0 {
		ps.add(k, at, "say what to watch: metrics, logs or both")
	}
	if vf.Decide == "model-assisted" && vf.Model == "" {
		ps.add(k, at+".decide", "decide: model-assisted needs a model to ask: model: <one of the server's ai.models>")
	}
	if vf.Decide == "" {
		vf.Decide = "statistics"
	}
	seen := map[string]bool{}
	for _, m := range vf.Metrics {
		if seen[m.Name] && m.Name != "" {
			ps.problems = append(ps.problems, Problem{Line: m.Line, Path: at + ".metrics", Message: fmt.Sprintf("%s: %s is named twice among metrics and logs", s.ID, m.Name)})
		}
		seen[m.Name] = true
	}
	for _, l := range vf.Logs {
		if seen[l.Name] && l.Name != "" {
			ps.problems = append(ps.problems, Problem{Line: l.Line, Path: at + ".logs", Message: fmt.Sprintf("%s: %s is named twice among metrics and logs", s.ID, l.Name)})
		}
		seen[l.Name] = true
	}
	// Every placeholder a query uses must be a parameter.
	queries := func(q, stable string, line int, what string) {
		for _, text := range []string{q, stable} {
			for _, name := range Placeholders(text) {
				if vf.Params[name] == nil && !slices.Contains(ReservedParams, name) {
					ps.problems = append(ps.problems, Problem{Line: line, Path: at + "." + what, Message: fmt.Sprintf(
						"%s: the query uses {{%s}}, which is not under params; add it, for example params: { %s: { deployment: namespace } }", s.ID, name, name)})
				}
			}
		}
	}
	for _, m := range vf.Metrics {
		queries(m.Query, m.StableQuery, m.Line, "metrics")
	}
	for _, l := range vf.Logs {
		queries(l.Query, l.StableQuery, l.Line, "logs")
	}
	// The whole step: the window, plus time to be taken and, with pause, to
	// be decided by a person.
	s.Timeout = Duration(vf.Delay.D() + du + time.Hour)
	if timeoutNode != nil {
		switch {
		case timeout < vf.Delay.D()+du+time.Minute:
			ps.add(timeoutNode, at+".timeout", "timeout must leave room for the delay and the whole window (%s), and a minute more", Duration(vf.Delay.D()+du).Short())
		case timeout > MaxTimeout:
			ps.add(timeoutNode, at+".timeout", "timeout must be at most %s", Duration(MaxTimeout).Short())
		default:
			s.Timeout = Duration(timeout)
		}
	}
	return s
}

func (ps *parser) parseVerifyParams(n *yaml.Node, at string, vf *Verify) {
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "params is a mapping of names to values, for example: { ns: { deployment: namespace }, sha: { run: sha } }")
		return
	}
	if len(n.Content)/2 > MaxVerifyParams {
		ps.add(n, at, "at most %d params", MaxVerifyParams)
	}
	vf.Params = map[string]*Value{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		where := at + "." + k.Value
		switch {
		case vf.Params[k.Value] != nil:
			ps.add(k, where, "%q is written twice", k.Value)
			continue
		case !verifyParamRe.MatchString(k.Value):
			ps.add(k, where, "%q is not a parameter name: start with a lower-case letter; use letters, digits and _", k.Value)
		case slices.Contains(ReservedParams, k.Value):
			ps.add(k, where, "%q is filled in by the deploy runner (the interval's start and end); choose another name", k.Value)
		}
		vf.paramLines[k.Value] = v.Line
		if v.Kind == yaml.MappingNode && hasKey(v, "deployment") {
			val := &Value{}
			ps.eachKey(v, where, []string{"deployment"}, func(_, x *yaml.Node) {
				val.Fact = ps.str(x, where+".deployment")
				if !slices.Contains(DeploymentFacts, val.Fact) {
					ps.add(x, where+".deployment", "%q is not a fact about the deployment; use %s", val.Fact, strings.Join(DeploymentFacts, ", "))
				}
			})
			vf.Params[k.Value] = val
			continue
		}
		val := ps.value(v, where)
		if strings.ContainsAny(val.Literal, "\r\n") {
			ps.add(v, where, "a parameter is one line")
		}
		vf.Params[k.Value] = val
	}
}

func (ps *parser) verifyNumber(n *yaml.Node, at string) *float64 {
	var f float64
	if n.Kind != yaml.ScalarNode || n.Decode(&f) != nil {
		ps.add(n, at, "write a number, such as 0.01 or 250")
		return nil
	}
	return &f
}

// verifyQuery reads a query: text, or, for Elasticsearch and OpenSearch, a
// mapping written as YAML, kept as JSON.
func (ps *parser) verifyQuery(n *yaml.Node, at string) string {
	var q string
	if n.Kind == yaml.MappingNode {
		var m any
		if err := n.Decode(&m); err != nil {
			ps.add(n, at, "the query cannot be read: %v", err)
			return ""
		}
		b, err := json.Marshal(jsonable(m))
		if err != nil {
			ps.add(n, at, "the query cannot be written as JSON: %v", err)
			return ""
		}
		q = string(b)
	} else {
		q = strings.TrimSpace(ps.str(n, at))
	}
	switch {
	case q == "":
		ps.add(n, at, "a query is required")
	case len(q) > MaxVerifyQueryBytes:
		ps.add(n, at, "the query is longer than %d KiB", MaxVerifyQueryBytes>>10)
	}
	return q
}

// jsonable turns what YAML decoding gives (maps with any keys) into what
// encoding/json writes.
func jsonable(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = jsonable(e)
		}
		return out
	case map[any]any:
		out := map[string]any{}
		for k, e := range x {
			out[fmt.Sprint(k)] = jsonable(e)
		}
		return out
	case []any:
		for i := range x {
			x[i] = jsonable(x[i])
		}
	}
	return v
}

func (ps *parser) parseVerifyMetric(n *yaml.Node, at string) *VerifyMetric {
	m := &VerifyMetric{Line: n.Line}
	ps.eachKey(n, at, verifyMetricKeys, func(k, v *yaml.Node) {
		where := at + "." + k.Value
		switch k.Value {
		case "name":
			m.Name = ps.str(v, where)
			if !verifyNameRe.MatchString(m.Name) {
				ps.add(v, where, "%q is not a valid name: lower-case letters, digits, - and _, starting with a letter", m.Name)
			}
		case "source":
			m.Source = ps.str(v, where)
		case "query":
			m.Query = ps.verifyQuery(v, where)
		case "stable_query":
			m.StableQuery = ps.verifyQuery(v, where)
		case "statement":
			m.Statement = ps.str(v, where)
		case "index":
			m.Index = ps.str(v, where)
		case "direction":
			m.Direction = ps.str(v, where)
			if !slices.Contains(VerifyDirections, m.Direction) {
				ps.add(v, where, "write higher-is-worse, lower-is-worse or either")
			}
		case "max":
			m.Max = ps.verifyNumber(v, where)
		case "min":
			m.Min = ps.verifyNumber(v, where)
		case "fail_fast":
			ps.eachKey(v, where, []string{"max", "min"}, func(k2, v2 *yaml.Node) {
				if k2.Value == "max" {
					m.FailFastMax = ps.verifyNumber(v2, where+".max")
				} else {
					m.FailFastMin = ps.verifyNumber(v2, where+".min")
				}
			})
		case "failures":
			m.Failures = ps.int(v, where)
			if m.Failures < 1 || m.Failures > MaxVerifyIntervals {
				ps.add(v, where, "failures is how many failing intervals in a row fail the metric: 1 to %d", MaxVerifyIntervals)
			}
		case "min_intervals":
			m.MinIntervals = ps.int(v, where)
			if m.MinIntervals < 1 || m.MinIntervals > MaxVerifyIntervals {
				ps.add(v, where, "min_intervals is 1 to %d", MaxVerifyIntervals)
			}
		case "min_points":
			m.MinPoints = ps.int(v, where)
			if m.MinPoints < 1 || m.MinPoints > 500 {
				ps.add(v, where, "min_points is 1 to 500")
			}
		case "step":
			d, err := time.ParseDuration(ps.scalar(v, where))
			if err != nil || d < 5*time.Second || d > time.Hour {
				ps.add(v, where, "step is the resolution of the readings: 5s to 1h")
			} else {
				m.Step = Duration(d)
			}
		case "baseline":
			m.NoBaseline = !ps.bool(v, where)
		}
	})
	if n.Kind != yaml.MappingNode {
		return m
	}
	ps.checkVerifySource(n, at, m.Name, m.Source, m.Query, m.StableQuery, m.Statement)
	if m.Direction == "" {
		m.Direction = "higher-is-worse"
	}
	if m.Max != nil && m.Min != nil && *m.Min > *m.Max {
		ps.add(n, at, "min is above max")
	}
	return m
}

func (ps *parser) parseVerifyLog(n *yaml.Node, at string) *VerifyLog {
	l := &VerifyLog{Line: n.Line}
	ps.eachKey(n, at, verifyLogKeys, func(k, v *yaml.Node) {
		where := at + "." + k.Value
		switch k.Value {
		case "name":
			l.Name = ps.str(v, where)
			if !verifyNameRe.MatchString(l.Name) {
				ps.add(v, where, "%q is not a valid name: lower-case letters, digits, - and _, starting with a letter", l.Name)
			}
		case "source":
			l.Source = ps.str(v, where)
		case "query":
			l.Query = ps.verifyQuery(v, where)
		case "stable_query":
			l.StableQuery = ps.verifyQuery(v, where)
		case "statement":
			l.Statement = ps.str(v, where)
		case "index":
			l.Index = ps.str(v, where)
		case "errors":
			l.Errors = ps.str(v, where)
			if l.Errors != "auto" && l.Errors != "all" {
				ps.add(v, where, "write auto (lines with words such as error, exception or panic are errors) or all (the query selects only errors)")
			}
		case "max_lines":
			l.MaxLines = ps.int(v, where)
			if l.MaxLines < 1 || l.MaxLines > 50000 {
				ps.add(v, where, "max_lines is 1 to 50000 per interval")
			}
		case "baseline":
			l.NoBaseline = !ps.bool(v, where)
		}
	})
	if n.Kind != yaml.MappingNode {
		return l
	}
	ps.checkVerifySource(n, at, l.Name, l.Source, l.Query, l.StableQuery, l.Statement)
	if l.Errors == "" {
		l.Errors = "auto"
	}
	return l
}

func (ps *parser) checkVerifySource(n *yaml.Node, at, name, source, query, stable, statement string) {
	if name == "" {
		ps.add(n, at+".name", "a name is required, for example: name: error-rate")
	}
	switch {
	case source == "":
		ps.add(n, at+".source", "name a health source of the server's verification.sources, for example: source: prometheus")
	case !verifyNameRe.MatchString(source):
		ps.add(n, at+".source", "%q is not a health source name", source)
	}
	switch {
	case query == "" && statement == "":
		ps.add(n, at, "give a query (Prometheus, Loki, Elasticsearch) or a statement (a query the source's operator wrote, for SQL and HTTP sources)")
	case query != "" && statement != "":
		ps.add(n, at, "give a query or a statement, not both")
	case stable != "" && query == "":
		ps.add(n, at+".stable_query", "stable_query goes with query")
	case statement != "" && !verifyNameRe.MatchString(statement):
		ps.add(n, at+".statement", "%q is not a statement name", statement)
	}
}

// checkVerify validates what verify steps say about other steps, once every
// step is known.
func (ps *parser) checkVerify(p *Pipeline) {
	for _, s := range p.Steps {
		if !s.IsVerify() || s.Verify == nil {
			continue
		}
		vf := s.Verify
		at := "steps." + s.ID
		line := func(key string) int {
			if l := vf.lines[key]; l > 0 {
				return l
			}
			return s.Line
		}
		add := func(l int, path, format string, a ...any) {
			ps.problems = append(ps.problems, Problem{Line: l, Path: path, Message: s.ID + ": " + fmt.Sprintf(format, a...)})
		}
		var d *Step
		switch {
		case s.Deployment != "":
			d = p.Step(s.Deployment)
			switch {
			case d == nil:
				add(line("deployment"), at+".deployment", "there is no step %q", s.Deployment)
			case !d.IsDeploy():
				add(line("deployment"), at+".deployment", "deployment %q is not a deploy step", s.Deployment)
				d = nil
			case !p.Upstream(s.ID)[d.ID]:
				add(max(s.NeedsLine, line("deployment")), at+".needs", "must need %s, directly or through other steps", d.ID)
			}
			if s.Environment != "" || s.Service != "" {
				add(line("environment"), at+".environment", "a verify step that names a deployment watches its environment and service; take environment and service out")
			}
		case s.Environment == "":
			add(s.Line, at+".environment", "name the deployment to watch (deployment: deploy-prod) or the environment (environment: prod)")
		}
		for _, name := range slices.Sorted(maps.Keys(vf.Params)) {
			v := vf.Params[name]
			pl := vf.paramLines[name]
			switch {
			case v.Fact != "" && d == nil:
				add(pl, at+".params."+name, "{ deployment: %s } needs the step to name a deployment", v.Fact)
			case (v.Fact == "canary_hash" || v.Fact == "stable_hash") && d != nil && d.Rollout == nil:
				add(pl, at+".params."+name, "%s comes from the deployment's rollout, but %s names no rollout", v.Fact, d.ID)
			case v.Output != nil && p.Step(v.Output.Step) == nil:
				add(pl, at+".params."+name, "refers to %s.%s, but there is no step %s", v.Output.Step, v.Output.Key, v.Output.Step)
			case v.Output != nil && !p.Upstream(s.ID)[v.Output.Step]:
				add(pl, at+".params."+name, "refers to %s.%s, but %s is not in this step's needs", v.Output.Step, v.Output.Key, v.Output.Step)
			case v.Input != "":
				switch in := p.Input(v.Input); {
				case in == nil:
					add(pl, at+".params."+name, "there is no input called %q", clipText(v.Input, 60))
				case in.Type == InputList || in.Type == InputBoolean:
					add(pl, at+".params."+name, "%s is a %s; a parameter takes a string, choice or number input", in.Name, in.Type)
				}
			}
		}
		switch vf.OnFail {
		case "abort":
			switch {
			case d == nil:
				add(line("on_fail"), at+".on_fail", "on_fail: abort aborts the deployment's rollout; name the deployment (deployment: ...)")
			case d.Rollout == nil:
				add(line("on_fail"), at+".on_fail", "on_fail: abort aborts the rollout of %s, but %s names no rollout", d.ID, d.ID)
			}
		case "rollback":
			if d == nil {
				add(line("on_fail"), at+".on_fail", "on_fail: rollback rolls the deployment back; name the deployment (deployment: ...)")
				break
			}
			rb := p.RollbackOf(d.ID)
			if rb == nil || !p.Upstream(rb.ID)[s.ID] {
				add(line("on_fail"), at+".on_fail", "on_fail: rollback leaves the revert to the rollback step of %s, which must need %s; "+
					"add a step with kind: rollback, deployment: %s, needs: [%s]", d.ID, s.ID, d.ID, s.ID)
			}
		}
	}
	// Steps that read a verify step's outputs may read only the ones it sets.
	for _, s := range p.Steps {
		var refs []*Value
		refs = append(refs, deployValues(s)...)
		refs = append(refs, imageValues(s)...)
		refs = append(refs, verifyValues(s)...)
		for _, v := range refs {
			if v.Output == nil {
				continue
			}
			if src := p.Step(v.Output.Step); src != nil && src.IsVerify() && !contains(VerifyOutputs, v.Output.Key) {
				ps.problems = append(ps.problems, Problem{Line: s.Line, Path: "steps." + s.ID,
					Message: fmt.Sprintf("%s refers to %s.%s, but a verify step's outputs are %s", s.ID, v.Output.Step, v.Output.Key, strings.Join(VerifyOutputs, ", "))})
			}
		}
	}
}

// bindVerify puts a run's input values in a verify step's params. It
// returns a copy; s is not changed.
func bindVerify(s *Step, values map[string]string) *Step {
	need := false
	for _, v := range verifyValues(s) {
		need = need || v.Input != ""
	}
	if !need {
		return s
	}
	c := *s
	vf := *s.Verify
	vf.Params = map[string]*Value{}
	for k, v := range s.Verify.Params {
		if v.Input != "" {
			v = &Value{Literal: values[v.Input]}
		}
		vf.Params[k] = v
	}
	c.Verify = &vf
	return &c
}
