package config

// PullRequestsConfig says whether the steps of pull-request runs may read
// secrets.
type PullRequestsConfig struct {
	// Secrets lets steps of pull-request runs read secrets; default false.
	// In a project's pull_requests, unset means the server's setting.
	Secrets *bool `yaml:"secrets"`
	// Untrusted says what happens to a pull request from an automation
	// account (see automation.accounts): restrict (the default) runs it
	// without secrets, deploys, caches or registry pushes, with the pipeline
	// of the default branch, until a maintainer approves it; allow treats it
	// like any other pull request. In a project, unset means the server's.
	Untrusted string `yaml:"untrusted"`
}

// untrustedAllowed says whether a project lets pull requests from automation
// accounts run unrestricted: pull_requests.untrusted: allow.
func (c *Config) untrustedAllowed(p ProjectConfig) bool {
	if p.PullRequests != nil && p.PullRequests.Untrusted != "" {
		return p.PullRequests.Untrusted == "allow"
	}
	return c.PullRequests.Untrusted == "allow"
}

// pullRequestSecrets says whether the steps of a project's pull-request runs
// may read secrets: the project's setting, else the server's, else no.
func (c *Config) pullRequestSecrets(p ProjectConfig) bool {
	if p.PullRequests != nil && p.PullRequests.Secrets != nil {
		return *p.PullRequests.Secrets
	}
	return c.PullRequests.Secrets != nil && *c.PullRequests.Secrets
}
