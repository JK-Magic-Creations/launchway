package config

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/jk-magic-creations/launchway/internal/accept"
	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	hsource "github.com/jk-magic-creations/launchway/internal/verify/source"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"path/filepath"
)

// RunnerFile is the file given to `launchway runner --config`: the same
// settings as the server's runner section, and where to find the server.
// The token is not in it; it comes from --token or LAUNCHWAY_RUNNER_TOKEN.
type RunnerFile struct {
	Server         string `yaml:"server"` // the server's URL
	Name           string `yaml:"name"`   // default: the host name
	Data           string `yaml:"data"`   // directory for workspaces
	RunnerSettings `yaml:",inline"`
	// Deploy makes this a deploy runner: it does the work of deploy and
	// rollback steps for the targets listed, and runs no other steps. Its
	// token must be a runner pool's.
	Deploy *argocd.Config `yaml:"deploy"`
	// HealthSources, on a deploy runner, are the health sources it reads
	// for verify steps of its targets, by the names the servers'
	// verification.sources give them (schema 19).
	HealthSources map[string]hsource.Config `yaml:"health_sources"`
	// Labels are what the runner says it has, such as arm64 or gpu, for
	// pipelines that ask for runners with labels (runner: { labels }).
	Labels []string `yaml:"labels"`
	// Accept is the runner's own limits on the work it takes: checked on
	// the runner before anything runs, whatever the server says.
	Accept *accept.Config `yaml:"accept"`
	// Proxy and NoProxy override HTTPS_PROXY, HTTP_PROXY and NO_PROXY for
	// the runner's requests to the server; CAFile adds certificates (PEM)
	// to the system's, for a server or proxy a private authority signed.
	Proxy   string `yaml:"proxy"`
	NoProxy string `yaml:"no_proxy"`
	CAFile  string `yaml:"ca_file"`
}

// LoadRunner reads a runner configuration file. Unknown settings are
// refused, and relative paths in it are relative to the file. Defaults are
// not filled in: the command line may still override any setting.
func LoadRunner(path string) (*RunnerFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &RunnerFile{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	base := filepath.Dir(path)
	if c.Data != "" && !filepath.IsAbs(c.Data) {
		c.Data = filepath.Join(base, c.Data)
	}
	if k := c.Kubernetes.Kubeconfig; k != "" && !filepath.IsAbs(k) {
		c.Kubernetes.Kubeconfig = filepath.Join(base, k)
	}
	c.Secrets.RelativeTo(base)
	c.Images.RelativeTo(base)
	if err := c.Images.Check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.CAFile != "" && !filepath.IsAbs(c.CAFile) {
		c.CAFile = filepath.Join(base, c.CAFile)
	}
	if err := c.Accept.Check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Deploy != nil {
		for name, t := range c.Deploy.Targets {
			if k := t.Kubernetes.Kubeconfig; k != "" && !filepath.IsAbs(k) {
				t.Kubernetes.Kubeconfig = filepath.Join(base, k)
				c.Deploy.Targets[name] = t
			}
		}
		for name, r := range c.Deploy.Repos {
			if isLocalPath(r.URL) && !filepath.IsAbs(r.URL) {
				r.URL = filepath.Join(base, r.URL)
				c.Deploy.Repos[name] = r
			}
		}
		if err := c.Deploy.Check(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if len(c.HealthSources) > 0 && c.Deploy == nil {
		return nil, fmt.Errorf("%s: health_sources are read by deploy runners for verify steps; add a deploy: section, or take them out", path)
	}
	for name, h := range c.HealthSources {
		if h.CAFile != "" && !filepath.IsAbs(h.CAFile) {
			h.CAFile = filepath.Join(base, h.CAFile)
			c.HealthSources[name] = h
		}
	}
	return c, nil
}
