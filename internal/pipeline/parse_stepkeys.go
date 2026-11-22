package pipeline

import (
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// stepScan holds the nodes of one step that are checked after all its keys are read.
type stepScan struct {
	secretsNode, kindNode, timeoutNode *yaml.Node
	timeout                            time.Duration
}

// parseSpecialStep parses the kinds of step that have their own parser.
func (ps *parser) parseSpecialStep(k, v *yaml.Node, at string) (*Step, bool) {
	switch mappingValue(v, "kind") {
	case KindDeploy, KindRollback, KindRolloutStep:
		return ps.parseDeployStep(k, v, at, mappingValue(v, "kind")), true
	case KindImage:
		return ps.parseImageStep(k, v, at), true
	case KindVerify:
		return ps.parseVerifyStep(k, v, at), true
	case KindAI:
		return ps.parseAIStep(k, v, at), true
	}
	return nil, false
}

// parseStepKey reads one key of a step.
func (ps *parser) parseStepKey(s *Step, sc *stepScan, k2, v2 *yaml.Node, where string) {
	switch k2.Value {
	case "for_each":
		s.ForEach = ps.parseForEach(v2, where)
	case "kind":
		sc.kindNode = v2
		s.Kind = ps.str(v2, where)
	case "container":
		s.Container = ps.str(v2, where)
	case "image":
		ps.curField = "image"
		s.Image = ps.scalar(v2, where)
		ps.curField = ""
	case "run":
		s.Run = ps.str(v2, where)
	case "needs":
		s.Needs = ps.strList(v2, where)
		s.NeedsLine = v2.Line
	case "when":
		s.When = ps.parseWhen(v2, where)
	case "env":
		s.Env = ps.parseEnv(v2, where)
	case "secrets":
		sc.secretsNode = v2
		s.Secrets = ps.parseSecrets(v2, where)
	case "retry":
		ps.parseRetry(s, v2, where)
	case "timeout":
		d, err := time.ParseDuration(ps.scalar(v2, where))
		if err != nil {
			ps.add(v2, where, "write a duration such as 30s, 10m or 1h")
		} else {
			sc.timeoutNode, sc.timeout = v2, d
			s.timeoutSet = true
		}
	case "on_failure", "runner":
		ps.parseRunStepFlow(s, k2.Value, v2, where)
	case "reports":
		s.Reports = ps.parseReports(v2, where)
	case "resources":
		s.Resources = ps.parseResources(v2, where)
		s.resourcesLine = v2.Line
	case "artifacts", "cache":
		ps.parseKept(s, k2.Value, v2, where) // files for the run's later steps, or caches for later runs
	case "fetch":
		s.Fetch = ps.parseFetch(v2, where)
	case "uses":
		s.Uses = ps.str(v2, where)
		s.usesLine = v2.Line
	case "with":
		s.With, s.withLines = ps.parseWith(v2, where)
	case "skip", "extend", "replace", "stages":
		ps.parseAdapt(s, k2.Value, v2, where)
	case "if":
		s.ifTest = ps.parseIf(v2, where)
	case "message":
		s.Message = strings.TrimSpace(ps.scalar(v2, where))
	case "deployment":
		s.Deployment = ps.str(v2, where)
		s.deploymentLine = v2.Line
	case "description":
		s.Description = strings.TrimSpace(ps.str(v2, where))
		switch {
		case strings.ContainsAny(s.Description, "\r\n"):
			ps.add(v2, where, "a description is one line; put longer notes in a comment")
		case utf8.RuneCountInString(s.Description) > MaxDescriptionLen:
			ps.add(v2, where, "the description is longer than %d characters", MaxDescriptionLen)
		}
	}
}
