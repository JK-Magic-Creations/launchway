package settings

import (
	"encoding/json"
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/resources"
)

// StepResourcesName is the name of the one step-resources object.
const StepResourcesName = "step-resources"

// StepResources is the installation's CPU and memory for steps (kind
// step-resources): what a step gets when it writes no resources, by step
// kind, and the most a step may ask for. The engine reads the tables of
// schema 22; this object is where they are changed (with history and
// versions), and every server writes what it says into the tables when it
// sees it change, so the tables are its projection.
type StepResources struct {
	Limits   StepResourceLimits         `yaml:"limits"`
	Defaults map[string]StepResourceSet `yaml:"defaults"`
}

// StepResourceLimits bound what steps may ask for.
type StepResourceLimits struct {
	MaxCPU          string `yaml:"max_cpu"`
	MaxMemory       string `yaml:"max_memory"`
	MemoryPerThread string `yaml:"memory_per_thread"`
}

// StepResourceSet is the default of one kind of step.
type StepResourceSet struct {
	CPU         string `yaml:"cpu"`
	CPULimit    string `yaml:"cpu_limit"`
	Memory      string `yaml:"memory"`
	MemoryLimit string `yaml:"memory_limit"`
}

func (s *StepResources) parts() (engine.ResourceLimits, map[string]resources.Request) {
	l := engine.ResourceLimits{MaxCPU: s.Limits.MaxCPU, MaxMemory: s.Limits.MaxMemory, MemoryPerThread: s.Limits.MemoryPerThread}
	d := map[string]resources.Request{}
	for k, v := range s.Defaults {
		d[k] = resources.Request{CPU: v.CPU, CPULimit: v.CPULimit, Memory: v.Memory, MemoryLimit: v.MemoryLimit}
	}
	return l, d
}

func stepResourcesFrom(l engine.ResourceLimits, d map[string]resources.Request) *StepResources {
	s := &StepResources{Limits: StepResourceLimits{l.MaxCPU, l.MaxMemory, l.MemoryPerThread}, Defaults: map[string]StepResourceSet{}}
	for k, v := range d {
		s.Defaults[k] = StepResourceSet{v.CPU, v.CPULimit, v.Memory, v.MemoryLimit}
	}
	return s
}

// checkStepResources is the kind's own check: the engine's.
func checkStepResources(s *StepResources) error {
	_, _, err := engine.ValidateStepResources(s.parts())
	if err != nil {
		return invalid(err)
	}
	return nil
}

// stepResourcesFromTables is the configuration as the tables hold it, as a
// spec: what the object is until somebody changes it through the object.
func (s *Service) stepResourcesFromTables() (*StepResources, error) {
	cfg, err := s.eng.StepResources()
	if err != nil {
		return nil, err
	}
	d := map[string]resources.Request{}
	for _, x := range cfg.Defaults {
		if !x.IsZero() {
			d[x.Kind] = x.Request
		}
	}
	return stepResourcesFrom(cfg.Limits, d), nil
}

// stepResourcesObject is the object as it reads before it has ever been
// changed: the tables' content, version 0.
func (s *Service) stepResourcesObject() (*engine.ConfigObject, error) {
	spec, err := s.stepResourcesFromTables()
	if err != nil {
		return nil, err
	}
	raw, err := encodeSpec(kindByName("step-resources"), spec)
	if err != nil {
		return nil, err
	}
	return &engine.ConfigObject{Kind: "step-resources", Name: StepResourcesName, Spec: raw, ManagedBy: "default"}, nil
}

// projectStepResources writes the step-resources object, if there is one,
// into the tables the engine reads. Every server does it as it sees the
// configuration change; it does nothing when the tables already agree.
func (s *Service) projectStepResources(objs []engine.ConfigObject) error {
	for _, o := range objs {
		if o.Kind != "step-resources" || o.Deleted() {
			continue
		}
		v, _, err := decodeSpec(kindByName("step-resources"), o.Name, o.Spec)
		if err != nil {
			return err
		}
		l, d := v.(*StepResources).parts()
		changed, err := s.eng.ReplaceStepResources(l, d, o.UpdatedBy)
		if err != nil {
			return fmt.Errorf("step resources: %w", err)
		}
		if changed {
			s.log.Info("applied a change to the step resources", "version", o.Version)
		}
	}
	return nil
}

// SetStepResourceLimits changes the limits through the step-resources
// object (so the change has a version and a history), keeping the defaults
// as they are. It returns the limits before and after.
func (s *Service) SetStepResourceLimits(actor string, l engine.ResourceLimits) (before, after engine.ResourceLimits, err error) {
	cur, err := s.stepResourcesFromTables()
	if err != nil {
		return before, after, err
	}
	before, _ = cur.parts()
	cur.Limits = StepResourceLimits{l.MaxCPU, l.MaxMemory, l.MemoryPerThread}
	err = s.putStepResources(actor, cur, "limits changed")
	if err != nil {
		return before, after, err
	}
	after, _ = cur.parts()
	return before, after, nil
}

// SetStepResourceDefault changes one kind's default through the object.
func (s *Service) SetStepResourceDefault(actor, kind string, req resources.Request) (before, after resources.Request, err error) {
	cur, err := s.stepResourcesFromTables()
	if err != nil {
		return before, after, err
	}
	_, d := cur.parts()
	before = d[kind]
	if req.IsZero() {
		delete(cur.Defaults, kind)
	} else {
		cur.Defaults[kind] = StepResourceSet{req.CPU, req.CPULimit, req.Memory, req.MemoryLimit}
	}
	if err := s.putStepResources(actor, cur, "default of "+kind+" changed"); err != nil {
		return before, after, err
	}
	_, d = cur.parts()
	return before, d[kind], nil
}

func (s *Service) putStepResources(actor string, spec *StepResources, comment string) error {
	raw, err := encodeSpec(kindByName("step-resources"), spec)
	if err != nil {
		return err
	}
	_, err = s.Apply(actor, []Change{{Kind: kindByName("step-resources"), Name: StepResourcesName, Action: engine.ConfigUpdate,
		Spec: json.RawMessage(raw), IfVersion: -1, ManagedBy: engine.ManagedByAPI, Comment: comment}})
	return err
}

// ArtifactsFromInstallation returns the configuration with its artifact
// settings replaced by those of the installation object's spec: where
// artifacts are kept and their limits. The engine uses it as it opens, when
// the database can be read but the rest of the configuration has not been
// composed yet. A spec that cannot be read leaves the configuration alone
// and says why.
func ArtifactsFromInstallation(boot *config.Config, spec json.RawMessage) (*config.Config, error) {
	v, _, err := decodeSpec(kindByName("installation"), "installation", spec)
	if err != nil {
		return boot, err
	}
	c := *boot
	a := v.(*Installation).Artifacts
	c.Artifacts = config.ArtifactsConfig{Store: a.Store, Database: a.Database, Dir: a.Dir, S3: a.S3,
		MaxFileMiB: a.MaxFileMiB, MaxRunMiB: a.MaxRunMiB, MaxProjectGiB: a.MaxProjectGiB, Retain: a.Retain, MaxRetain: a.MaxRetain}
	return &c, nil
}
