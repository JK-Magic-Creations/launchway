package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// RegistryConfig is the built-in OCI registry (container images, Helm charts
// and other OCI artifacts under /v2/). It is on by default wherever the
// server has a blob store and sign-in; every setting here is optional.
type RegistryConfig struct {
	// Enabled turns the registry on or off (default on). It needs sign-in:
	// there is no anonymous registry.
	Enabled *bool `yaml:"enabled"`
	// PublicURL is the address clients push and pull at, and the address
	// pipelines' image steps use when they name no registry (default
	// forge.public_url, then the address a request came in on).
	PublicURL string `yaml:"public_url"`
	// AllowInsecureHTTP lets a password or token travel over plain HTTP from
	// anywhere; otherwise only a loopback listener accepts them.
	AllowInsecureHTTP bool `yaml:"allow_insecure_http"`
	// MaxBlobMiB is the largest layer or blob accepted (default 10240).
	MaxBlobMiB int `yaml:"max_blob_mib"`
	// MaxManifestKiB is the largest manifest accepted (default 4096).
	MaxManifestKiB int `yaml:"max_manifest_kib"`
	// DefaultQuotaGiB is what a project may store when no policy says
	// otherwise (default 20; -1: no quota).
	DefaultQuotaGiB int `yaml:"default_quota_gib"`
	// RequestsPerMinute caps the requests one caller makes (default 3000;
	// -1: no cap).
	RequestsPerMinute int `yaml:"requests_per_minute"`
	// MaxRepositoriesPerProject and MaxTagsPerRepository keep one project
	// from filling the lists (defaults 500 and 5000).
	MaxRepositoriesPerProject int `yaml:"max_repositories_per_project"`
	MaxTagsPerRepository      int `yaml:"max_tags_per_repository"`
	// GCGrace is how old an unreferenced blob must be before garbage
	// collection removes it (default 1h; never less than 1m).
	GCGrace string `yaml:"gc_grace"`
	// MaintainEvery is how often retention rules are applied and garbage is
	// collected (default 6h; "off" turns scheduled maintenance off).
	MaintainEvery string `yaml:"maintain_every"`
	// UploadTTL is how long an unfinished upload may sit idle (default 24h).
	UploadTTL string `yaml:"upload_ttl"`
	// Fallback is the registry (a name the Stations' configuration lists) the starter Flight plans push to when
	// this server's own registry cannot serve, which is when sign-in is off. Without it, they do not push by
	// default in that case, and say so.
	Fallback string `yaml:"fallback"`
}

var registryNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// On reports whether the registry is switched on.
func (c RegistryConfig) On() bool { return c.Enabled == nil || *c.Enabled }

// Maintain returns how often maintenance runs; 0 is never.
func (c RegistryConfig) Maintain() time.Duration {
	if c.MaintainEvery == "off" {
		return 0
	}
	if d, err := time.ParseDuration(c.MaintainEvery); err == nil && d > 0 {
		return d
	}
	return 6 * time.Hour
}

// Check validates the settings.
func (c RegistryConfig) Check() error {
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.Trim(u.Path, "/") != "" {
			return fmt.Errorf("registry.public_url must be an address such as https://registry.example.com, without a path")
		}
	}
	if c.Fallback != "" && (!registryNameRe.MatchString(c.Fallback) || c.Fallback == "launchway") {
		return fmt.Errorf("registry.fallback names a registry in the Stations' configuration (lower-case letters, digits, dots, dashes), not %q", c.Fallback)
	}
	for name, v := range map[string]int{"max_blob_mib": c.MaxBlobMiB, "max_manifest_kib": c.MaxManifestKiB,
		"max_repositories_per_project": c.MaxRepositoriesPerProject, "max_tags_per_repository": c.MaxTagsPerRepository} {
		if v < 0 {
			return fmt.Errorf("registry.%s cannot be negative", name)
		}
	}
	for name, v := range map[string]string{"gc_grace": c.GCGrace, "upload_ttl": c.UploadTTL, "maintain_every": c.MaintainEvery} {
		if v == "" || (name == "maintain_every" && v == "off") {
			continue
		}
		if d, err := time.ParseDuration(v); err != nil || d < time.Minute {
			return fmt.Errorf("registry.%s must be a duration of at least 1m, such as 6h", name)
		}
	}
	return nil
}
