package runner

import (
	"slices"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// containerSpecs is the job's containers as this Station's executor makes
// them. An executor that makes a pod for each step gets the step's own
// container as the server sized it for the step alone; every other executor,
// and a job from a server that does not say, keeps the run's container,
// which is sized for the largest step that shares its image.
func (r *Runner) containerSpecs(job *engine.Job) []executor.ContainerSpec {
	specs := r.scopeServices(job, containerSpecs(job))
	if ps, ok := r.Exec.(executor.PerStepSizer); !ok || !ps.PerStep() || job.StepContainer == nil {
		return specs
	}
	own := containerSpecs(&engine.Job{Containers: []pipeline.Container{*job.StepContainer}})[0]
	for i := range specs {
		if specs[i].Name == own.Name && !specs[i].Service {
			specs[i] = own
		}
	}
	return specs
}

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

// scopeServices leaves out the services a step did not list under services, where the step has its own
// place to run: a pod for each step, a container beside the step, or the host. A Kubernetes pod for the whole
// run is made once, from the first step's containers, so it keeps the services every step of the run needs together.
func (r *Runner) scopeServices(job *engine.Job, specs []executor.ContainerSpec) []executor.ContainerSpec {
	keep := job.Services
	if k, ok := r.Exec.(*executor.Kubernetes); ok && !k.PerStep() {
		// One pod for the whole run, made from the first step's containers: it keeps the services every step
		// of the run needs together (the server worked that out), not only the first step's.
		if !job.RunScopedServices {
			return specs
		}
		keep = job.RunServices
	} else if !job.ScopedServices {
		return specs
	}
	out := specs[:0:0]
	for _, c := range specs {
		if c.Service && !slices.Contains(keep, c.Name) {
			continue
		}
		out = append(out, c)
	}
	return out
}
