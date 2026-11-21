package engine

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/policy"
)

// LiveOptions is the part of the engine's options an administrator manages:
// environments, runner pools, policies, health sources, models, approvals
// and limits. Since schema 23 they are kept in the database (see
// internal/settings), and Reconfigure replaces them while the server runs,
// so a change made through any server takes effect on every server within
// seconds and needs no restart.
type LiveOptions struct {
	AllowSelfApproval *bool
	// EmergencyRollback is the installation's setting for an emergency
	// rollback of a protected environment: allow (the default) or
	// require_approval.
	EmergencyRollback string
	Tests             TestLimits
	Findings          FindingLimits
	Reports           ReportOptions
	// Artifacts gives only the limits and retention (MaxFileBytes,
	// MaxRunBytes, MaxProjectBytes, DefaultRetain, MaxRetain); where
	// artifacts are kept is fixed when the engine opens.
	Artifacts     ArtifactOptions
	Environments  []Environment
	Policy        PolicyOptions
	HealthSources []HealthSource
	Models        *ai.Registry
	RunnerPools   []RunnerPool
}

// Live returns the options' live part.
func (o Options) Live() LiveOptions {
	return LiveOptions{AllowSelfApproval: o.AllowSelfApproval, EmergencyRollback: o.EmergencyRollback, Tests: o.Tests, Findings: o.Findings, Reports: o.Reports,
		Artifacts: o.Artifacts, Environments: o.Environments, Policy: o.Policy, HealthSources: o.HealthSources,
		Models: o.Models, RunnerPools: o.RunnerPools}
}

// liveConfig is LiveOptions checked and with its defaults: what the engine
// reads. It is never changed once stored; Reconfigure stores a new one.
type liveConfig struct {
	selfOK    bool
	emergency string
	tests     TestLimits
	finds     FindingLimits
	reps      ReportOptions
	arts      ArtifactOptions // limits and retention only
	envs      map[string]Environment
	pol       PolicyOptions
	polHash   string // names the configuration's own policies
	sources   map[string]HealthSource
	models    *ai.Registry
	pools     map[string]RunnerPool
}

// live returns the configuration in force.
func (e *Engine) live() *liveConfig { return e.cur.Load() }

// buildLive checks the live options for a build that knows schema known.
func buildLive(o LiveOptions, known int) (*liveConfig, error) {
	tests, err := o.Tests.withDefaults()
	if err != nil {
		return nil, err
	}
	arts, err := ArtifactOptions{MaxFileBytes: o.Artifacts.MaxFileBytes, MaxRunBytes: o.Artifacts.MaxRunBytes,
		MaxProjectBytes: o.Artifacts.MaxProjectBytes, DefaultRetain: o.Artifacts.DefaultRetain, MaxRetain: o.Artifacts.MaxRetain}.withDefaults()
	if err != nil {
		return nil, err
	}
	finds, err := o.Findings.withDefaults()
	if err != nil {
		return nil, err
	}
	reps, err := o.Reports.withDefaults()
	if err != nil {
		return nil, err
	}
	envs, err := checkEnvironments(o.Environments)
	if err != nil {
		return nil, err
	}
	sources, err := checkHealthSources(o.HealthSources)
	if err != nil {
		return nil, err
	}
	if (len(sources) > 0 || (o.Models != nil && len(o.Models.List()) > 0)) && known < schemaVerify {
		return nil, fmt.Errorf("health sources and AI models need schema %d; this build knows %d", schemaVerify, known)
	}
	if len(envs) > 0 && known < schemaDeploy {
		return nil, fmt.Errorf("environments need schema %d; this build knows %d", schemaDeploy, known)
	}
	pol, err := checkPolicyOptions(o.Policy)
	if err != nil {
		return nil, err
	}
	if (len(pol.Inline) > 0 || len(pol.Sources) > 0) && known < schemaPolicy {
		return nil, fmt.Errorf("policies need schema %d; this build knows %d", schemaPolicy, known)
	}
	pools, err := checkPools(o.RunnerPools)
	if err != nil {
		return nil, err
	}
	for _, p := range o.RunnerPools {
		if p.Kind == PoolBuild && known < schemaPools {
			return nil, fmt.Errorf("build runner pools need schema %d; this build knows %d", schemaPools, known)
		}
	}
	if err := checkEmergency(o.EmergencyRollback, "approvals.emergency_rollback"); err != nil {
		return nil, err
	}
	return &liveConfig{
		selfOK: o.AllowSelfApproval == nil || *o.AllowSelfApproval, emergency: o.EmergencyRollback,
		tests: tests, finds: finds, reps: reps, arts: arts,
		envs: envs, pol: pol, polHash: policy.Hash(pol.Inline), sources: sources, models: o.Models, pools: pools,
	}, nil
}

// CheckLive checks live options as Reconfigure would, without applying
// them: the engine's half of validating a change to the configuration.
func (e *Engine) CheckLive(o LiveOptions) error {
	_, err := buildLive(o, e.schema)
	return err
}

// Reconfigure replaces the live configuration. Work in progress carries on;
// what is decided from now on (leases, gates, limits) uses the new values.
// Policies written in the configuration are recorded again when they
// changed, as at start.
func (e *Engine) Reconfigure(o LiveOptions) error {
	l, err := buildLive(o, e.schema)
	if err != nil {
		return err
	}
	old := e.cur.Swap(l)
	if old == nil || old.polHash != l.polHash {
		if err := e.recordConfigPolicies(); err != nil {
			return fmt.Errorf("recording the configuration's policies: %w", err)
		}
	}
	e.notify()
	return nil
}

// selfApprovalNow is the setting to record in a run being created.
func (e *Engine) selfApprovalNow() *bool {
	v := e.live().selfOK
	return &v
}

// SelfApprovalAllowed says whether the person who started a run may approve
// it on this installation now: the setting approvals.allow_self, or its
// default. It is what a decision card tells a person who is refused.
func (e *Engine) SelfApprovalAllowed() bool { return e.live().selfOK }
