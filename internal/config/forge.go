package config

import (
	"fmt"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge/egress"
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
	// MirrorAllow lists the internal hosts and networks that mirrors and
	// imports may contact: a host name (git.corp.example, or *.corp.example),
	// an address or a network (10.20.0.0/16). Launchway refuses to contact an
	// internal address (loopback, private, link-local, cloud metadata) that
	// is not listed, so that nobody who can set up a mirror can use the
	// server to reach the network around it. Public addresses need no entry.
	MirrorAllow []string `yaml:"mirror_allow"`
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
	if _, err := egress.New(f.MirrorAllow); err != nil {
		return fmt.Errorf("forge.mirror_allow: %w", err)
	}
	return nil
}

// MaintainInterval is MaintainEvery as a duration (0: the default).
func (f ForgeConfig) MaintainInterval() time.Duration {
	d, _ := time.ParseDuration(f.MaintainEvery)
	return d
}
