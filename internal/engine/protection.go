package engine

// protected says whether a deployment's environment is protected: as it
// was when the deployment opened, or, for one that predates the record, as
// it is now. An environment that is not configured at all counts as
// protected: a missing setting never unprotects anything.
func (c *cmd) protected(d *DeployState) bool {
	if d != nil && d.Protected != nil {
		return *d.Protected
	}
	if d == nil {
		return true
	}
	env, ok := c.e.live().envs[d.Environment]
	return env.Protected || !ok
}

// verifyProtected is protected for a verification: that of the deployment it
// watches if it has one, else of its environment now (a missing environment
// counts as protected).
func (c *cmd) verifyProtected(v *VerifyState) bool {
	if v.Deployment != "" {
		if _, ds := c.st.deploymentStep(v.Deployment); ds != nil && ds.Dep != nil && ds.Dep.Protected != nil {
			return *ds.Dep.Protected
		}
	}
	env, ok := c.e.live().envs[v.Environment]
	return env.Protected || !ok
}
