package executor

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/resources"
)

// Step resources: what each executor does with the CPU and memory a
// container was given (see ContainerSpec), and how many parallel jobs the
// tools in it are told to start.
//
// The server decides the numbers (internal/engine/resources.go); an executor
// only applies them: Kubernetes as the pod's requests and limits, Podman and
// Docker as flags of each step's container, and the host executor, which can
// limit nothing, by refusing a step that asks.

// stepContainer is the container the step runs in.
func (s Spec) stepContainer() (ContainerSpec, bool) {
	for _, c := range s.Containers {
		if c.Name == s.Container {
			return c, true
		}
	}
	return ContainerSpec{}, false
}

// withParallelism returns the spec with the variables that make common tools
// start no more parallel jobs than its container's limits feed, for those the
// step does not set itself. The spec's own environment is not changed.
func (s Spec) withParallelism() Spec {
	if c, ok := s.stepContainer(); ok && !c.Service {
		s.Env = resources.WithDefaults(s.Env, c.Threads)
	}
	return s
}

// amount is what the container may use, in numbers; the zero Amount if the
// values do not read (the server checked them).
func (c ContainerSpec) amount() resources.Amount {
	a, _ := resources.Request{CPU: c.CPU, CPULimit: c.CPULimit, Memory: c.Memory, MemoryLimit: c.MemoryLimit}.Parse()
	if a.Memory > 0 && a.MemoryLimit == 0 {
		a.MemoryLimit = a.Memory // asked for memory without a limit: it is also the cap
	}
	return a
}

// describeResources says in a log line what a container was given.
func (c ContainerSpec) describeResources() string {
	text := c.amount().Describe()
	if c.Threads > 0 {
		text += fmt.Sprintf("; tools are told to run %d parallel jobs", c.Threads)
	}
	return text
}

// errHostCannotLimit is why the host executor refuses a step that asks for
// resources: it starts the script as a process of the runner's own user, with
// nothing to limit its CPU or memory.
func errHostCannotLimit(c ContainerSpec) error {
	return fmt.Errorf("the host executor cannot limit CPU or memory, so it cannot run a step that asks for resources (%s). "+
		"Run this on a runner with the podman, docker or kubernetes executor, or take resources out of the step",
		c.amount().Describe())
}

// containerFlags are the flags that give a Podman or Docker container its
// limits. The memory limit is also the swap limit, so that the container
// cannot go past it into swap, as a Kubernetes pod cannot.
func containerFlags(c ContainerSpec) []string {
	a := c.amount()
	var out []string
	if a.MemoryLimit > 0 {
		b := strconv.FormatInt(a.MemoryLimit, 10)
		out = append(out, "--memory", b, "--memory-swap", b)
		if a.Memory > 0 && a.Memory < a.MemoryLimit {
			out = append(out, "--memory-reservation", strconv.FormatInt(a.Memory, 10))
		}
	}
	if a.CPULimit > 0 {
		out = append(out, "--cpus", cpusFlag(a.CPULimit))
	}
	return out
}

// cpusFlag writes millicores as a decimal number of CPUs: 4, 0.5, 2.25.
func cpusFlag(milli int64) string {
	s := strconv.FormatFloat(float64(milli)/1000, 'f', 3, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
