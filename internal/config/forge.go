package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ForgeConfig is the server's Git hosting. Hosting is off by default; turning
// it off stops the Git endpoints and keeps the data.
//
// It is part of the installation's settings (kind installation, key forge),
// kept in the database like the others. Servers read it when they start, so
// a change takes effect when each server next restarts.
type ForgeConfig struct {
	// Hosting turns on Launchway's own Git hosting.
	Hosting bool `yaml:"hosting"`
	// PublicURL is the address clients clone from, used in clone addresses
	// (default: the address the request came in on).
	PublicURL string `yaml:"public_url"`
	// DefaultOwner is the owner of a repository made without one (default launchway).
	DefaultOwner string `yaml:"default_owner"`
	// AllowInsecureHTTP lets Basic credentials travel over plain HTTP from
	// anywhere; otherwise only a loopback listener accepts them.
	AllowInsecureHTTP bool `yaml:"allow_insecure_http"`
	// MaxRepoMiB caps one repository's packs; MaxPushMiB caps one push's body
	// and pack (default 2048, the database blob store's largest object).
	MaxRepoMiB int `yaml:"max_repo_mib"`
	MaxPushMiB int `yaml:"max_push_mib"`
	// MaxPerOwner caps repositories per owner; CreatesPerMinute caps how fast
	// they are made (default 60).
	MaxPerOwner      int `yaml:"max_per_owner"`
	CreatesPerMinute int `yaml:"creates_per_minute"`
	// MaxConcurrent caps git processes serving requests at once (default 32).
	MaxConcurrent int `yaml:"max_concurrent"`
	// CacheGiB caps the repository caches on this server (0: no cap).
	CacheGiB int `yaml:"cache_gib"`
	// MaintainEvery is how often packs are consolidated and checked (default 15m).
	MaintainEvery string `yaml:"maintain_every"`
	// SSH is Git over SSH (off until Listen is set).
	SSH ForgeSSHConfig `yaml:"ssh"`
}

// ForgeSSHConfig is the built-in SSH server for Git. It is off by default; the
// listen address turns it on.
type ForgeSSHConfig struct {
	// Listen is the address the server listens on, such as ":2222" (empty: off).
	Listen string `yaml:"listen"`
	// PublicHost and PublicPort are what clone addresses say (default: the host
	// of forge.public_url, and the port of Listen).
	PublicHost string `yaml:"public_host"`
	PublicPort int    `yaml:"public_port"`
	// UserCA is the public keys (authorized_keys format, one per line) of the
	// certificate authorities whose user certificates are accepted. The first
	// principal of a certificate is the person's name. Empty: certificates are
	// not accepted.
	UserCA string `yaml:"user_ca"`
	// MaxConnections and MaxPerAddress cap connections (256 and 20);
	// IdleTimeout ends a quiet connection (10m) and MaxDuration any (6h).
	MaxConnections int    `yaml:"max_connections"`
	MaxPerAddress  int    `yaml:"max_per_address"`
	IdleTimeout    string `yaml:"idle_timeout"`
	MaxDuration    string `yaml:"max_duration"`
}

// On reports whether SSH is turned on.
func (c ForgeSSHConfig) On() bool { return strings.TrimSpace(c.Listen) != "" }

// Port is the port clone addresses name.
func (c ForgeSSHConfig) Port() int {
	if c.PublicPort > 0 {
		return c.PublicPort
	}
	_, p, err := net.SplitHostPort(c.Listen)
	if n, e := strconv.Atoi(p); err == nil && e == nil {
		return n
	}
	return 22
}

// CAs parses UserCA.
func (c ForgeSSHConfig) CAs() ([]ssh.PublicKey, error) {
	var out []ssh.PublicKey
	for _, line := range strings.Split(c.UserCA, "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("forge.ssh.user_ca: %q is not a public key in the OpenSSH format", clipKey(line))
		}
		out = append(out, k)
	}
	return out, nil
}

func clipKey(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// Check validates the SSH settings.
func (c ForgeSSHConfig) Check() error {
	if c.MaxConnections < 0 || c.MaxPerAddress < 0 || c.PublicPort < 0 || c.PublicPort > 65535 {
		return fmt.Errorf("forge.ssh: limits and ports cannot be negative or above 65535")
	}
	if c.On() {
		if _, _, err := net.SplitHostPort(c.Listen); err != nil {
			return fmt.Errorf("forge.ssh.listen must be an address with a port, such as :2222 or 0.0.0.0:2222")
		}
	}
	for _, d := range []string{c.IdleTimeout, c.MaxDuration} {
		if d != "" {
			if v, err := time.ParseDuration(d); err != nil || v < time.Minute {
				return fmt.Errorf("forge.ssh: idle_timeout and max_duration must be durations of at least 1m, such as 10m")
			}
		}
	}
	_, err := c.CAs()
	return err
}

// Check validates the settings.
func (f ForgeConfig) Check() error {
	if f.MaxRepoMiB < 0 || f.MaxPushMiB < 0 || f.MaxPerOwner < 0 || f.CreatesPerMinute < 0 || f.MaxConcurrent < 0 || f.CacheGiB < 0 {
		return fmt.Errorf("forge: limits cannot be negative")
	}
	if f.MaintainEvery != "" {
		if d, err := time.ParseDuration(f.MaintainEvery); err != nil || d < time.Minute {
			return fmt.Errorf("forge: maintain_every must be a duration of at least 1m, such as 15m")
		}
	}
	return f.SSH.Check()
}

// MaintainInterval is MaintainEvery as a duration (0: the default).
func (f ForgeConfig) MaintainInterval() time.Duration {
	d, _ := time.ParseDuration(f.MaintainEvery)
	return d
}
