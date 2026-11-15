package config

// Settings for run inputs, loops, schedules, webhooks and API tokens. Each
// is optional, with its default built in.

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// LoopsConfig limits loops.
type LoopsConfig struct {
	// MaxMembers is how many combinations one loop may have; default 64,
	// at most 500.
	MaxMembers int `yaml:"max_members"`
}

// SchedulesConfig turns schedules on or off.
type SchedulesConfig struct {
	// Enabled: unset means on (or, for a project, the server's setting).
	Enabled *bool `yaml:"enabled"`
}

// TokensConfig limits API tokens.
type TokensConfig struct {
	// MaxDays is the longest a token may last; default 365, at most 3650.
	MaxDays int `yaml:"max_days"`
}

// GitWebhookConfig is how a project's Git host proves its webhook calls.
// The secret is read from an environment variable or a file, never from
// this file.
type GitWebhookConfig struct {
	Kind       string `yaml:"kind"` // github, gitea, forgejo or gitlab
	SecretEnv  string `yaml:"secret_env"`
	SecretFile string `yaml:"secret_file"`
}

// WebhookConfig is what callers of one webhook trigger must prove: a
// bearer token, or an HMAC-SHA256 signature of the body made with a
// secret. Exactly one of the two, from an environment variable or a file.
type WebhookConfig struct {
	TokenEnv       string `yaml:"token_env"`
	TokenFile      string `yaml:"token_file"`
	HMACSecretEnv  string `yaml:"hmac_secret_env"`
	HMACSecretFile string `yaml:"hmac_secret_file"`
}

// GitHookKinds are the Git hosts whose webhooks are understood.
var GitHookKinds = []string{"github", "gitea", "forgejo", "gitlab"}

// LoopLimit is the most combinations one loop may have; 0 is the default.
func (c *Config) LoopLimit() (int, error) {
	n := c.Loops.MaxMembers
	if n < 0 || n > pipeline.MaxLoopMembersLimit {
		return 0, fmt.Errorf("loops.max_members is between 1 and %d", pipeline.MaxLoopMembersLimit)
	}
	return n, nil
}

// TokenMaxDays is the longest an API token may last; 0 is the default.
func (c *Config) TokenMaxDays() (int, error) {
	n := c.Auth.Tokens.MaxDays
	if n < 0 || n > 3650 {
		return 0, errors.New("auth.tokens.max_days is between 1 and 3650")
	}
	return n, nil
}

// checkTriggers checks the settings of this file.
func (c *Config) checkTriggers() error {
	if _, err := c.LoopLimit(); err != nil {
		return err
	}
	if _, err := c.TokenMaxDays(); err != nil {
		return err
	}
	for _, p := range c.Projects {
		if h := p.GitWebhook; h != nil {
			if !slices.Contains(GitHookKinds, h.Kind) {
				return fmt.Errorf("project %s: git_webhook.kind is github, gitea, forgejo or gitlab", p.Name)
			}
			if (h.SecretEnv == "") == (h.SecretFile == "") {
				return fmt.Errorf("project %s: git_webhook needs exactly one of secret_env and secret_file", p.Name)
			}
		}
		for name, w := range p.Webhooks {
			if !pipeline.ValidWebhookName(name) {
				return fmt.Errorf("project %s: webhooks: %q is not a webhook name", p.Name, name)
			}
			n := 0
			for _, v := range []string{w.TokenEnv, w.TokenFile, w.HMACSecretEnv, w.HMACSecretFile} {
				if v != "" {
					n++
				}
			}
			if n != 1 {
				return fmt.Errorf("project %s: webhooks.%s needs exactly one of token_env, token_file, hmac_secret_env and hmac_secret_file", p.Name, name)
			}
		}
	}
	return nil
}

// applyTriggers adds a project's schedule and webhook settings to its
// source.Project.
func (c *Config) applyTriggers(p ProjectConfig, sp *source.Project) {
	on := c.Schedules.Enabled == nil || *c.Schedules.Enabled
	if p.Schedules != nil && p.Schedules.Enabled != nil {
		on = *p.Schedules.Enabled
	}
	sp.SchedulesOff = !on
	if h := p.GitWebhook; h != nil {
		sp.Hook = &source.GitHook{Kind: h.Kind, Secret: source.SecretSource{Env: h.SecretEnv, File: h.SecretFile}}
	}
	if len(p.Webhooks) > 0 {
		sp.Triggers = map[string]*source.TriggerAuth{}
		for name, w := range p.Webhooks {
			sp.Triggers[name] = &source.TriggerAuth{Token: source.SecretSource{Env: w.TokenEnv, File: w.TokenFile},
				HMAC: source.SecretSource{Env: w.HMACSecretEnv, File: w.HMACSecretFile}}
		}
	}
}

// relativeTriggerFiles makes the secret files of webhooks relative to the
// configuration file's directory.
func (c *Config) relativeTriggerFiles(base string) {
	rel := func(f *string) {
		if *f != "" && !filepath.IsAbs(*f) {
			*f = filepath.Join(base, *f)
		}
	}
	for i := range c.Projects {
		p := &c.Projects[i]
		if p.GitWebhook != nil {
			rel(&p.GitWebhook.SecretFile)
		}
		for name, w := range p.Webhooks {
			rel(&w.TokenFile)
			rel(&w.HMACSecretFile)
			p.Webhooks[name] = w
		}
	}
}
