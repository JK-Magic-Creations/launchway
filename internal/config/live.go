package config

import (
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// EngineLive converts what the engine is given that an administrator
// manages: environments, pools, policies, health sources, models, approvals
// and limits. The server gives it to engine.Open at start and to
// Engine.Reconfigure on every change; the configuration must have passed
// Validate.
func (c *Config) EngineLive() (engine.LiveOptions, error) {
	var o engine.LiveOptions
	var err error
	allowSelf := c.AllowSelfApproval()
	o.AllowSelfApproval = &allowSelf
	o.EmergencyRollback = c.Approvals.EmergencyRollback
	if o.Tests, err = c.TestLimits(); err != nil {
		return o, err
	}
	if o.Artifacts, err = c.ArtifactLimits(); err != nil {
		return o, err
	}
	if o.Findings, err = c.FindingLimits(); err != nil {
		return o, err
	}
	if o.Reports, err = c.ReportOptions(); err != nil {
		return o, err
	}
	if o.Policy, err = c.PolicyOptions(); err != nil {
		return o, err
	}
	if o.Models, err = c.Models(); err != nil {
		return o, err
	}
	pools, err := c.Pools("")
	if err != nil {
		return o, err
	}
	o.Environments = c.EngineEnvironments()
	o.HealthSources = c.EngineHealthSources()
	o.RunnerPools = EnginePools(pools)
	return o, nil
}
