package pipeline

import (
	"fmt"
	"strings"
)

// ParamTypes are the types a template's parameter may have. The last two are
// strings that mean something: the name of a delivery environment, and of an
// Argo CD application. A use that passes anything else is refused where it is
// written, not when the deploy runs.
var ParamTypes = []string{paramTypeString, paramTypeNumber, paramTypeBoolean, paramTypeList, paramTypeEnvironment, paramTypeApplication, paramTypeOutput}

// checkName returns why v is not an environment's or an application's name,
// or "".
func (p *Param) checkName(v string) string {
	switch {
	case p.Type == paramTypeOutput:
		if _, _, ok := OutputParam(v); !ok {
			return fmt.Sprintf("%s is an output of an earlier step: write { output: step.key } under with", p.Name)
		}
	case p.Type == paramTypeEnvironment && !nameRe.MatchString(v):
		return fmt.Sprintf("%s is the name of an environment: lower-case letters, digits and hyphens; %q is not one", p.Name, clipText(v, 60))
	case p.Type == paramTypeApplication && !k8sNameRe.MatchString(v):
		return fmt.Sprintf("%s is the name of an Argo CD application; %q is not a valid name", p.Name, clipText(v, 60))
	}
	return ""
}

// Delivery in templates: the checks and the renaming that deploy, rollback,
// rollout, verify and approval steps need when a template is expanded.

// deployOf gives a deploy, rollback, rollout, verify or approval step of a
// template the names it has in the run: the deploy step it names and the
// steps whose outputs its values take get the template's prefix. Values are
// copied, so the template read for this use is not changed.
func (x *expander) deployOf(sc *scope, c *Step) {
	if sc.prefix == "" {
		return
	}
	if c.Deployment != "" && sc.byID[c.Deployment] != nil {
		c.Deployment = sc.prefix + c.Deployment
	}
	local := func(v *Value) *Value {
		if v == nil || v.Output == nil || sc.byID[v.Output.Step] == nil {
			return v
		}
		cv := *v
		cv.Output = &OutputRef{Step: sc.prefix + v.Output.Step, Key: v.Output.Key}
		return &cv
	}
	c.Version = local(c.Version)
	if c.ArgoCD != nil && c.ArgoCD.WriteBack != nil {
		a := *c.ArgoCD
		w := *a.WriteBack
		w.Changes = make([]Change, len(a.WriteBack.Changes))
		for i, ch := range a.WriteBack.Changes {
			ch.Tag, ch.Digest, ch.Value = local(ch.Tag), local(ch.Digest), local(ch.Value)
			w.Changes[i] = ch
		}
		a.WriteBack = &w
		c.ArgoCD = &a
	}
	if c.Verify != nil && len(c.Verify.Params) > 0 {
		v := *c.Verify
		v.Params = make(map[string]*Value, len(c.Verify.Params))
		for k, pv := range c.Verify.Params {
			v.Params[k] = local(pv)
		}
		c.Verify = &v
	}
}

// checkDeployments checks again, on the whole expanded pipeline, what deploy,
// rollback, rollout, verify and approval steps say about each other: two
// templates may deploy the same service to the same environment, and a
// pipeline's step may need a template's step by its expanded name. The
// problems have no line, since the steps may be in a library's file.
func (x *expander) checkDeployments(out *Pipeline) {
	if len(x.problems) > 0 {
		return
	}
	ps := &parser{}
	ps.checkDeploys(out)
	ps.checkVerify(out)
	for _, pr := range ps.problems {
		if strings.Contains(pr.Message, " refers to ") {
			// An output of a group (a uses step) is mapped to a step when the
			// run starts; the pipeline file's own check covered it.
			continue
		}
		pr.Line = 0
		x.problems = append(x.problems, pr)
	}
}

// requireDeploy raises the schema the expanded pipeline needs to the one its
// template-made steps need: the pipeline file did not show them.
func (p *Pipeline) requireDeploy() {
	if p.UsesSchemaControl() {
		p.Requires = max(p.Requires, SchemaControl)
	}
	if p.UsesSchema15() {
		p.Requires = max(p.Requires, SchemaImages)
	}
	if p.UsesSchemaVerify() {
		p.Requires = max(p.Requires, SchemaVerify)
	}
}

// OutputParam reads the value of a parameter of type output, written
// output(step.key): the step and the key.
func OutputParam(v string) (step, key string, ok bool) {
	rest, found := strings.CutPrefix(v, "output(")
	rest, found2 := strings.CutSuffix(rest, ")")
	if !found || !found2 {
		return "", "", false
	}
	i := strings.LastIndex(rest, ".")
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}
