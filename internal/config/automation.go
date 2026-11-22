package config

import (
	"fmt"
	"path"
	"strings"
)

// AutomationConfig names the accounts that are automation (dependency bots,
// release tools): runs they start are counted apart in the queue (see
// queues.automation_runs) and pull requests from them run in restricted mode
// (see pull_requests.untrusted). A forge hosted by Launchway also marks its
// bot accounts, which count too.
type AutomationConfig struct {
	// Accounts are account names or patterns (`*` and `?`, any case), matched
	// against the author and the person who started the run. Left out, the
	// default list below applies; set to an empty list to name none.
	Accounts []string `yaml:"accounts"`
}

// DefaultAutomationAccounts are the names common dependency and release bots
// use: a name ending in [bot], and the usual tools.
var DefaultAutomationAccounts = []string{"*[bot]", "dependabot*", "renovate*", "github-actions*", "release-please*", "launchway-updater*"}

// Validate checks the patterns.
func (a AutomationConfig) Validate() error {
	for _, p := range a.Accounts {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("automation.accounts: a pattern is empty")
		}
		if _, err := path.Match(strings.ToLower(p), ""); err != nil {
			return fmt.Errorf("automation.accounts: %q is not a pattern: %v", p, err)
		}
	}
	return nil
}

// AutomationAccounts is the list in force: the configured one, or the default.
func (c *Config) AutomationAccounts() []string {
	if c.Automation.Accounts != nil {
		return c.Automation.Accounts
	}
	return DefaultAutomationAccounts
}

// checkUntrusted checks pull_requests.untrusted, in the server's settings and
// each project's.
func (c *Config) checkUntrusted() error {
	ok := func(v string) bool { return v == "" || v == "restrict" || v == "allow" }
	if !ok(c.PullRequests.Untrusted) {
		return fmt.Errorf("pull_requests.untrusted is restrict or allow, not %q", c.PullRequests.Untrusted)
	}
	for _, p := range c.Projects {
		if p.PullRequests != nil && !ok(p.PullRequests.Untrusted) {
			return fmt.Errorf("Flight %s: pull_requests.untrusted is restrict or allow, not %q", p.Name, p.PullRequests.Untrusted)
		}
	}
	return nil
}

// checkQueuesAndTrust checks the queues, automation and pull_requests.untrusted settings.
func (c *Config) checkQueuesAndTrust() error {
	if _, err := c.EngineQueues(); err != nil {
		return err
	}
	if err := c.checkUntrusted(); err != nil {
		return err
	}
	return c.Automation.Validate()
}
