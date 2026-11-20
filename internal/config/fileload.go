package config

import (
	"fmt"
	"os/user"
	"path/filepath"
)

// relativeToFile makes the paths a file names, which are relative to the
// file, absolute from its directory.
func (c *Config) relativeToFile(path string) error {
	base := filepath.Dir(path)
	rel := func(p *string) {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
	rel(&c.Data)
	if c.Database != "" && !isURL(c.Database) {
		rel(&c.Database)
	}
	for i, p := range c.Projects {
		if pp := p.Pipeline; pp != nil && pp.OnAsTrue != nil {
			if pp.On.Push != nil || pp.On.PullRequest.Kind != 0 {
				return fmt.Errorf("%s: project %s: pipeline has both `on` and `true` (an unquoted on); keep one", path, p.Name)
			}
			pp.On, pp.OnAsTrue = *pp.OnAsTrue, nil
		}
		if isLocalPath(p.Repo) {
			rel(&c.Projects[i].Repo)
		}
	}
	for i, l := range c.Libraries {
		if isLocalPath(l.Repo) {
			rel(&c.Libraries[i].Repo)
		}
	}
	c.Runner.Secrets.RelativeTo(base)
	c.AI.Secrets.RelativeTo(base)
	c.Runner.Images.RelativeTo(base)
	c.relativeTriggerFiles(base)
	rel(&c.Auth.OIDC.ClientSecretFile)
	for i := range c.RunnerPools {
		rel(&c.RunnerPools[i].TokenFile)
	}
	rel(&c.Artifacts.Dir)
	return nil
}

// applyDefaults fills in what the file leaves out, and makes local paths
// absolute: runners and container runtimes may work from another directory.
func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.Data == "" {
		c.Data = "launchway-data"
	}
	if abs, err := filepath.Abs(c.Data); err == nil {
		c.Data = abs
	}
	for i, p := range c.Projects {
		if isLocalPath(p.Repo) {
			if abs, err := filepath.Abs(p.Repo); err == nil {
				c.Projects[i].Repo = abs
			}
		}
	}
	for i, l := range c.Libraries {
		if isLocalPath(l.Repo) {
			if abs, err := filepath.Abs(l.Repo); err == nil {
				c.Libraries[i].Repo = abs
			}
		}
	}
	if c.User == "" {
		if u, err := user.Current(); err == nil {
			c.User = u.Username
		} else {
			c.User = "local"
		}
	}
	if c.Runner.Concurrency <= 0 {
		c.Runner.Concurrency = 2
	}
	if c.Auth.Mode == "" {
		c.Auth.Mode = AuthNone
	}
}

// EgressConfig is what the server may reach when a person gives it an
// address: a repository to read, an identity provider, a model.
type EgressConfig struct {
	// AllowLocal lets repository addresses be paths on this server or file:
	// addresses. Off by default: through the API such an address reads any
	// repository the server's account can read. Local development
	// (make run) turns it on. Bootstrap file only.
	AllowLocal bool `yaml:"allow_local"`
	// Allow lists internal hosts (names, *.suffix), addresses and ranges
	// that may be reached although the server refuses internal addresses by
	// default: loopback, link-local and cloud metadata, private and shared
	// ranges. A self-hosted Git server on the private network goes here.
	// Kept in the database, set by an administrator.
	Allow []string `yaml:"allow"`
}

// HistoryConfig is the retention of the configuration's change history.
type HistoryConfig struct {
	// Retain is how long changes are kept (90d, 52w; at least 7d); empty
	// keeps all of them. The newest change of every object is always kept.
	// Revisions older than what is kept can no longer be put back.
	Retain string `yaml:"retain"`
}
