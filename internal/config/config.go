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

	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// Config is the whole server configuration. One file describes an
// installation completely.
type Config struct {
	Listen string `yaml:"listen"`
	Data   string `yaml:"data"`
	// Database is a SQLite file, or a postgres:// URL that several servers
	// share. The environment variable LAUNCHWAY_DATABASE overrides it; the
	// default is launchway.db in the data directory.
	Database string `yaml:"database"`
	// DatabaseWait is how long a starting server keeps trying a database
	// that does not answer before it gives up; default 60s.
	DatabaseWait string `yaml:"database_wait"`
	// User is who every action is recorded as when sign-in is off
	// (auth.mode: none).
	User      string          `yaml:"user"`
	Server    ServerConfig    `yaml:"server"`
	Auth      AuthConfig      `yaml:"auth"`
	Projects  []ProjectConfig `yaml:"projects"`
	Runner    RunnerConfig    `yaml:"runner"`
	Approvals ApprovalsConfig `yaml:"approvals"`
}

// ServerConfig holds settings about how the server is reached.
type ServerConfig struct {
	// TrustProxy says that a reverse proxy in front of the server sets
	// X-Forwarded-Proto and X-Forwarded-For, and that clients cannot reach
	// the server except through it.
	TrustProxy bool `yaml:"trust_proxy"`
	// DrainDelay is how long a stopping server keeps serving after /readyz
	// starts to report it is not ready, so that load balancers that check
	// /readyz stop sending it requests first; default 0s.
	DrainDelay string `yaml:"drain_delay"`
}

// AuthConfig controls sign-in and who may do what.
type AuthConfig struct {
	// Mode is none (the default: no sign-in, everyone acts as User with
	// full rights) or oidc.
	Mode string `yaml:"mode"`
	// AllowAnonymous lets a server with mode none listen on an address
	// other than loopback. Without it, such a server refuses to start.
	AllowAnonymous bool       `yaml:"allow_anonymous"`
	OIDC           OIDCConfig `yaml:"oidc"`
	// IdleTimeout ends a session nobody has used for this long; default 8h.
	IdleTimeout string `yaml:"idle_timeout"`
	// SessionLifetime ends every session this long after sign-in; default 24h.
	SessionLifetime string          `yaml:"session_lifetime"`
	Access          []AccessBinding `yaml:"access"`
}

// OIDCConfig is the OpenID Connect provider to sign in with.
type OIDCConfig struct {
	Issuer      string `yaml:"issuer"`
	ClientID    string `yaml:"client_id"`
	RedirectURL string `yaml:"redirect_url"` // https://launchway.example.com/auth/callback
	// The client secret is read from an environment variable or a file,
	// never from this file.
	ClientSecretEnv  string   `yaml:"client_secret_env"`
	ClientSecretFile string   `yaml:"client_secret_file"`
	Scopes           []string `yaml:"scopes"`         // default openid profile email groups
	UsernameClaim    string   `yaml:"username_claim"` // default preferred_username, then email, then sub
	GroupsClaim      string   `yaml:"groups_claim"`   // default groups
}

// AccessBinding grants a role to a group or a user, on the projects that
// match one of Projects, or on every project if there are none.
type AccessBinding struct {
	Group    string   `yaml:"group"`
	User     string   `yaml:"user"`
	Role     string   `yaml:"role"`
	Projects []string `yaml:"projects"`
}

// ApprovalsConfig controls approval steps.
type ApprovalsConfig struct {
	// AllowSelf lets the person who started a run approve it. The default
	// is yes when sign-in is off, where everyone is the same user, and no
	// when it is on.
	AllowSelf *bool `yaml:"allow_self"`
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
	Enabled        *bool `yaml:"enabled"`
	RunnerSettings `yaml:",inline"`
}

// RunnerSettings are what every runner is configured with: the one built
// into the server, and `launchway runner` (see RunnerFile).
type RunnerSettings struct {
	Concurrency int                        `yaml:"concurrency"`
	Executor    string                     `yaml:"executor"` // auto, container, podman, docker, host or kubernetes
	Kubernetes  executor.KubernetesOptions `yaml:"kubernetes"`
	Secrets     secrets.Config             `yaml:"secrets"` // secret stores, by name
}

// RunnerFile is the file given to `launchway runner --config`: the same
// settings as the server's runner section, and where to find the server.
// The token is not in it; it comes from --token or LAUNCHWAY_RUNNER_TOKEN.
type RunnerFile struct {
	Server         string `yaml:"server"` // the server's URL
	Name           string `yaml:"name"`   // default: the host name
	Data           string `yaml:"data"`   // directory for workspaces
	RunnerSettings `yaml:",inline"`
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
	return c, nil
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
			if c.Database != "" && !isURL(c.Database) && !filepath.IsAbs(c.Database) {
				c.Database = filepath.Join(base, c.Database)
			}
			for i, p := range c.Projects {
				if isLocalPath(p.Repo) && !filepath.IsAbs(p.Repo) {
					c.Projects[i].Repo = filepath.Join(base, p.Repo)
				}
			}
			c.Runner.Secrets.RelativeTo(base)
			if f := c.Auth.OIDC.ClientSecretFile; f != "" && !filepath.IsAbs(f) {
				c.Auth.OIDC.ClientSecretFile = filepath.Join(base, f)
			}
		}
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.Data == "" {
		c.Data = "launchway-data"
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
	if c.Auth.Mode == "" {
		c.Auth.Mode = AuthNone
	}
	if err := c.Auth.check(); err != nil {
		where := "auth"
		if path != "" {
			where = path + ": auth"
		}
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	return c, nil
}

func isURL(s string) bool { return strings.Contains(s, "://") }

func isLocalPath(repo string) bool {
	return !strings.Contains(repo, "://") && !strings.Contains(repo, "@")
}

// DatabaseAddr returns the database to use: LAUNCHWAY_DATABASE if set, else
// the database setting, else launchway.db in the data directory.
func (c *Config) DatabaseAddr() string {
	if v := strings.TrimSpace(os.Getenv("LAUNCHWAY_DATABASE")); v != "" {
		return v
	}
	if c.Database != "" {
		return c.Database
	}
	return filepath.Join(c.Data, "launchway.db")
}

// Timings returns the database wait and the drain delay.
func (c *Config) Timings() (databaseWait, drainDelay time.Duration, err error) {
	databaseWait, drainDelay = time.Minute, 0
	for _, v := range []struct {
		key, s string
		d      *time.Duration
	}{{"database_wait", c.DatabaseWait, &databaseWait}, {"server.drain_delay", c.Server.DrainDelay, &drainDelay}} {
		if v.s == "" {
			continue
		}
		d, err := time.ParseDuration(v.s)
		if err != nil || d < 0 {
			return 0, 0, fmt.Errorf("%s: %q is not a duration such as 30s", v.key, v.s)
		}
		*v.d = d
	}
	return databaseWait, drainDelay, nil
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

// TokenFile is where the runner token is written in the data directory.
func TokenFile(dataDir string) string { return filepath.Join(dataDir, "runner-token") }

// SharedStore keeps values that every server sharing a database agrees on.
// *engine.Engine is one.
type SharedStore interface {
	// SharedValue returns the value stored under key, storing proposed
	// first if there is none.
	SharedValue(key, proposed string) (string, error)
}

// RunnerToken returns the token runners use. LAUNCHWAY_RUNNER_TOKEN wins if
// it is set. Otherwise the token is kept in the database, so that every
// server sharing it accepts the same runners; the first server to start
// creates it, offering the token already in its data directory if there is
// one, so that existing runners keep working. The token in effect is also
// written to the data directory, for convenience.
func RunnerToken(dataDir string, store SharedStore) (string, error) {
	if tok := strings.TrimSpace(os.Getenv("LAUNCHWAY_RUNNER_TOKEN")); tok != "" {
		return tok, nil
	}
	path := TokenFile(dataDir)
	local := ""
	if b, err := os.ReadFile(path); err == nil {
		local = string(bytes.TrimSpace(b))
	}
	proposed := local
	if proposed == "" {
		buf := make([]byte, 24)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		proposed = hex.EncodeToString(buf)
	}
	tok, err := store.SharedValue("runner_token", proposed)
	if err != nil {
		return "", fmt.Errorf("reading the runner token from the database: %w", err)
	}
	if tok != local {
		if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
			return "", err
		}
	}
	return tok, nil
}
