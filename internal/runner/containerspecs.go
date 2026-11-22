package runner

import (
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
)

// containerSpecs turns the containers a job names into what the executor makes pods from.
func containerSpecs(job *engine.Job) []executor.ContainerSpec {
	out := make([]executor.ContainerSpec, len(job.Containers))
	for i, c := range job.Containers {
		out[i] = executor.ContainerSpec{Name: c.Name, Image: c.Image, Env: c.Env, CPU: c.CPU, Memory: c.Memory,
			Service: c.Service, Command: c.Command,
			CPULimit: c.CPULimit, MemoryLimit: c.MemoryLimit, Threads: c.Threads,
			ExplicitCPU: c.ExplicitCPU, ExplicitMemory: c.ExplicitMemory, Ready: readySpec(c.Ready)}
	}
	return out
}
