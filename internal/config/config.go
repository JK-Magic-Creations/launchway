// Package config reads the server configuration file.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"slipway/internal/executor"
	"slipway/internal/source"
)

// Config is the whole server configuration. One file describes an
// installation completely.
type Config struct {
	Listen   string          `yaml:"listen"`
	Data     string          `yaml:"data"`
	User     string          `yaml:"user"` // recorded as the actor for UI actions, until sign-in exists
	Projects []ProjectConfig `yaml:"projects"`
	Runner   RunnerConfig    `yaml:"runner"`
}

// ProjectConfig is one repository to watch.
type ProjectConfig struct {
	Name   string `yaml:"name"`
	Repo   string `yaml:"repo"`
	Branch string `yaml:"branch"` // default branch for manual runs
	Poll   string `yaml:"poll"`   // how often to look for pushes, e.g. 30s
}

// RunnerConfig controls the runner built into the server.
type RunnerConfig struct {
	Enabled     *bool                      `yaml:"enabled"`
	Concurrency int                        `yaml:"concurrency"`
	Executor    string                     `yaml:"executor"` // auto, container, podman, docker, host or kubernetes
	Kubernetes  executor.KubernetesOptions `yaml:"kubernetes"`
}

// Load reads path. A missing file gives the defaults.
func Load(path string) (*Config, error) {
	c := &Config{}
	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return nil, err
		default:
			dec := yaml.NewDecoder(bytes.NewReader(data))
			dec.KnownFields(true)
			if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			// Relative paths are relative to the config file.
			base := filepath.Dir(path)
			if c.Data != "" && !filepath.IsAbs(c.Data) {
				c.Data = filepath.Join(base, c.Data)
			}
			for i, p := range c.Projects {
				if isLocalPath(p.Repo) && !filepath.IsAbs(p.Repo) {
					c.Projects[i].Repo = filepath.Join(base, p.Repo)
				}
			}
		}
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.Data == "" {
		c.Data = "slipway-data"
	}
	// Runners and container runtimes may work from another directory, so
	// every local path is made absolute.
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
	return c, nil
}

func isLocalPath(repo string) bool {
	return !strings.Contains(repo, "://") && !strings.Contains(repo, "@")
}

// RunnerEnabled reports whether the built-in runner should start.
func (c *Config) RunnerEnabled() bool { return c.Runner.Enabled == nil || *c.Runner.Enabled }

// SourceProjects converts the project list for the source package.
func (c *Config) SourceProjects() ([]source.Project, error) {
	var out []source.Project
	for _, p := range c.Projects {
		poll := 30 * time.Second
		if p.Poll != "" {
			d, err := time.ParseDuration(p.Poll)
			if err != nil {
				return nil, fmt.Errorf("project %s: poll %q is not a duration", p.Name, p.Poll)
			}
			poll = d
		}
		out = append(out, source.Project{Name: p.Name, URL: p.Repo, DefaultBranch: p.Branch, Poll: poll})
	}
	return out, nil
}

// RunnerToken returns the token runners use, creating and saving one in the
// data directory the first time.
func RunnerToken(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "runner-token")
	if b, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return string(bytes.TrimSpace(b)), nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}
