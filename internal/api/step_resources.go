package api

// Step resources (schema 22): the installation's default CPU and memory for
// each kind of step, and the most any step may ask for. They are the
// server's configuration, kept in the database; anyone signed in may read
// them (a pipeline's author needs to know what a step gets), and only an
// administrator changes them. Every change is audited with the values
// before and after.

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/resources"
)

// getStepResources answers the whole configuration: every kind's default
// and the limits.
func (s *Server) getStepResources(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Engine.StepResources()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// getStepResourceLimits answers the installation's limits.
func (s *Server) getStepResourceLimits(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Engine.StepResources()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg.Limits)
}

// getStepResourceDefault answers one kind's default.
func (s *Server) getStepResourceDefault(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	cfg, err := s.Engine.StepResources()
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, d := range cfg.Defaults {
		if d.Kind == kind {
			writeJSON(w, http.StatusOK, d)
			return
		}
	}
	if !slices.Contains(cfg.Kinds, kind) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("%w: %q has no default resources; the kinds that have are %v", engine.ErrNotFound, kind, cfg.Kinds).Error())
		return
	}
	writeJSON(w, http.StatusOK, engine.ResourceDefault{Kind: kind})
}

// setStepResourceLimits replaces the limits: {"max_cpu", "max_memory",
// "memory_per_thread"}; a value left out is not set.
func (s *Server) setStepResourceLimits(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MaxCPU          string `json:"max_cpu"`
		MaxMemory       string `json:"max_memory"`
		MemoryPerThread string `json:"memory_per_thread"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, "limits", "")
	before, after, err := s.Engine.SetStepResourceLimits(engine.ResourceLimits{MaxCPU: in.MaxCPU, MaxMemory: in.MaxMemory, MemoryPerThread: in.MemoryPerThread},
		identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, "limits", clip(limitsText(before)+" -> "+limitsText(after), 600))
	writeJSON(w, http.StatusOK, after)
}

// setStepResourceDefault replaces the default of one step kind:
// {"cpu", "cpu_limit", "memory", "memory_limit"}; a value left out is not
// set.
func (s *Server) setStepResourceDefault(w http.ResponseWriter, r *http.Request) {
	var in resources.Request
	if !decode(w, r, &in) {
		return
	}
	kind := r.PathValue("kind")
	setAudit(r, "default "+kind, "")
	before, after, err := s.Engine.SetStepResourceDefault(kind, in, identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, "default "+kind, clip(requestText(before.Request)+" -> "+requestText(after.Request), 600))
	writeJSON(w, http.StatusOK, after)
}

func limitsText(l engine.ResourceLimits) string {
	return fmt.Sprintf("max_cpu=%q max_memory=%q memory_per_thread=%q", l.MaxCPU, l.MaxMemory, l.MemoryPerThread)
}

func requestText(q resources.Request) string {
	return fmt.Sprintf("cpu=%q cpu_limit=%q memory=%q memory_limit=%q", q.CPU, q.CPULimit, q.Memory, q.MemoryLimit)
}
