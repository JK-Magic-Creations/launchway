package pipeline

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/resources"
)

// SchemaResources is the server schema that brought step resources: a
// step's resources key, and the installation's defaults and limits for them
// (see internal/engine/resources.go).
const SchemaResources = 22

// Resources is what a step asks for: CPU and memory, promised (cpu, memory)
// and capped (cpu_limit, memory_limit). A limit left out is the same as the
// amount promised, so "memory: 6Gi" means a step that is given 6 GiB and
// may use no more. What a step leaves out comes from the installation's
// default for its kind.
//
//	resources: { cpu: 2, memory: 6Gi }
//	resources: { cpu: 1, cpu_limit: 8, memory: 2Gi, memory_limit: 6Gi }
//
// The quantities are written as the containers key's cpu and memory are:
// 500m or 2 for CPU, 512Mi or 6Gi for memory.
type Resources = resources.Request

var resourcesKeys = []string{"cpu", "cpu_limit", "memory", "memory_limit"}

// ResourceKinds are the step kinds that run in a container Launchway sizes,
// and so the kinds that have default resources. Image steps are built by a
// builder whose size is set where the builder runs; deploy, rollout,
// rollback, verify and ai steps run in the runner itself, and an approval
// runs nothing.
var ResourceKinds = []string{KindStep}

// SeedStepResources are the default resources a new installation starts
// with, by step kind. They are only a seed: the installation keeps its
// defaults in the database, where an administrator changes them, and these
// are used once, to fill the empty table (see internal/engine/resources.go).
//
// A script step is sized for a build: it is promised one CPU and 2 GiB, and
// may use four CPUs and 6 GiB, which is room for a Go or C++ build of a large
// program, below the 8 GiB a namespace commonly allows per container. The
// promise is low so that many steps fit on a cluster; a step that needs a
// guarantee asks for it.
var SeedStepResources = map[string]Resources{
	KindStep: {CPU: "1", CPULimit: "4", Memory: "2Gi", MemoryLimit: "6Gi"},
}

// ResourcesLine is the line the step's resources were written on, or 0.
func (s *Step) ResourcesLine() int { return s.resourcesLine }

// ResourceKind is the kind a step's defaults are kept under, or "" if the
// step runs in no container of the run's.
func (s *Step) ResourceKind() string {
	if s.IsApproval() || s.IsGate() || s.IsDeployKind() || s.IsUse() || s.IsImage() || s.IsVerify() || s.IsAI() {
		return ""
	}
	return KindStep
}

// UsesSchemaResources reports whether a step asks for resources, which
// servers older than schema 22 would not know and so refuse.
func (p *Pipeline) UsesSchemaResources() bool {
	for _, s := range p.Steps {
		if s.Resources != nil {
			return true
		}
	}
	return false
}

// notHere are keys that are known but that a mapping does not take, with
// why. They are reported instead of the list of keys expected.
var notHere = map[string]string{
	"on_failure": "only steps that run a script and image steps can tolerate a failure; deploy, rollout, rollback, verify, ai and approval " +
		"steps have their own failure handling, and a step that uses a template takes it inside the template",
	"cache": "cache keeps a directory between runs for steps that run a script. An image step has a cache key of its own, a different shape, " +
		"for the registry that keeps its build layers; deploy, rollout, rollback, verify and ai steps run in the runner and an approval runs nothing",
	"resources": "resources are for steps that run a script. An image step is built by the builder, whose memory is set where the builder is " +
		"deployed; deploy, rollout, rollback, verify and ai steps run in the runner and an approval runs nothing. A container under containers " +
		"takes cpu and memory",
}

// parseResources reads a step's resources.
func (ps *parser) parseResources(n *yaml.Node, at string) *Resources {
	r := &Resources{}
	ps.eachKey(n, at, resourcesKeys, func(k, v *yaml.Node) {
		where := at + "." + k.Value
		q := ps.str(v, where)
		var err error
		switch k.Value {
		case "cpu":
			r.CPU = q
			_, err = resources.ParseCPU(q)
		case "cpu_limit":
			r.CPULimit = q
			_, err = resources.ParseCPU(q)
		case "memory":
			r.Memory = q
			_, err = resources.ParseMemory(q)
		case "memory_limit":
			r.MemoryLimit = q
			_, err = resources.ParseMemory(q)
		}
		if err != nil {
			ps.add(v, where, "%v", err)
		}
	})
	if n.Kind != yaml.MappingNode {
		return nil
	}
	if r.IsZero() {
		ps.add(n, at, "say what the step needs, for example: resources: { cpu: 2, memory: 6Gi }")
		return nil
	}
	if len(ps.problems) == 0 || !ps.hasProblemUnder(at) {
		if _, err := r.Parse(); err != nil {
			ps.add(n, at, "%v", err)
		}
	}
	return r
}

// hasProblemUnder reports whether a problem has already been found at or
// under a path, so that a check that needs valid values is not made on
// invalid ones.
func (ps *parser) hasProblemUnder(at string) bool {
	for _, p := range ps.problems {
		if p.Path == at || len(p.Path) > len(at) && p.Path[:len(at)+1] == at+"." {
			return true
		}
	}
	return false
}

// ResourceAmount is what a step asks for, in numbers. It is the zero Amount
// for a step that asks for nothing. The values were checked when the
// pipeline was parsed.
func (s *Step) ResourceAmount() (resources.Amount, error) {
	if s.Resources == nil {
		return resources.Amount{}, nil
	}
	a, err := s.Resources.Parse()
	if err != nil {
		return a, fmt.Errorf("step %s: resources: %w", s.ID, err)
	}
	return a, nil
}

// parseServices reads a step's services: a list of the names of the service
// containers it needs ([] for none), or the word all, which is what a step
// that says nothing gets. It returns the names and whether the step listed
// them.
func (ps *parser) parseServices(n *yaml.Node, at string) ([]string, bool) {
	if n.Kind == yaml.ScalarNode && n.Value == "all" {
		return nil, false
	}
	if n.Kind != yaml.SequenceNode {
		ps.add(n, at, "list the services the step needs, such as services: [db] (services: [] for none, services: all for every service of the pipeline)")
		return nil, false
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range n.Content {
		if c.Kind != yaml.ScalarNode || !containerRe.MatchString(c.Value) {
			ps.add(c, at, "%q is not the name of a container: list the names of the service containers the step needs, such as services: [db]", clipText(c.Value, 40))
			continue
		}
		if seen[c.Value] {
			ps.add(c, at, "the service %q is listed twice", c.Value)
			continue
		}
		seen[c.Value] = true
		out = append(out, c.Value)
	}
	return out, true
}

// checkServices checks that a step's services are service containers of the
// pipeline.
func (ps *parser) checkServices(n *yaml.Node, at string, s *Step, p *Pipeline) {
	for _, name := range s.Services {
		if msg := serviceProblem(p, name); msg != "" {
			ps.add(n, at, "%s", msg)
		}
	}
}

// serviceProblem says what is wrong with naming a container as a service of a step; "" if nothing.
func serviceProblem(p *Pipeline, name string) string {
	var services []string
	for _, c := range p.Containers {
		if c.Service {
			services = append(services, c.Name)
		}
	}
	switch c := p.Container(name); {
	case c == nil && len(services) == 0:
		return fmt.Sprintf("there is no container called %q under containers, and the pipeline has no service at all: declare it as containers: { %s: { image: ..., service: true } }", name, name)
	case c == nil:
		return fmt.Sprintf("there is no container called %q under containers; the services are %s", name, strings.Join(services, ", "))
	case !c.Service && len(services) == 0:
		return fmt.Sprintf("%q is not a service (service: true): steps run in it, it does not run beside them; add service: true to it if it should", name)
	case !c.Service:
		return fmt.Sprintf("%q is not a service (service: true): steps run in it, it does not run beside them; the services are %s", name, strings.Join(services, ", "))
	}
	return ""
}
