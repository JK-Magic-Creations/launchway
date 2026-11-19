package imagebuild

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Config is what a runner's operator sets up for image steps (images: in
// the runner's configuration). Pipelines name registries and builders by
// what this configuration calls them; credentials, addresses and who may
// push where are only ever here.
type Config struct {
	// Builders are the BuildKit daemons the runner sends builds to. An
	// image step uses the first one that serves its project and every
	// platform it asks for; when no single one does, one per platform (see
	// README: multi-platform images).
	Builders []BuilderConfig `yaml:"builders"`
	// Registries are the registries image steps push to and pull from, by
	// the name pipelines use (tags: [{ registry: harbor, ... }]).
	Registries map[string]RegistryConfig `yaml:"registries"`
	// Scanner is the image that makes SBOMs and finds vulnerabilities.
	Scanner ScannerConfig `yaml:"scanner"`
	// Signing is the runner's own signing key, used by steps that say
	// sign: true. Steps never see it: it is a file the runner reads, never
	// mounted where steps run (keep it out of the host executor's reach).
	Signing *SigningConfig `yaml:"signing"`
	// MaxContextMiB is the largest build context a step may send, after
	// .dockerignore; default 2048.
	MaxContextMiB int64 `yaml:"max_context_mib"`
	// MaxImageMiB is the largest image (all platforms, compressed) the
	// runner accepts back from a builder; default 20480.
	MaxImageMiB int64 `yaml:"max_image_mib"`
}

// BuilderConfig is one BuildKit daemon.
type BuilderConfig struct {
	Name string `yaml:"name"`
	// Address is tcp://host:port (with TLS, see below), or unix:///path
	// for a daemon on the runner's own machine.
	Address string `yaml:"address"`
	// TLS for tcp:// addresses: the CA that signed the daemon's
	// certificate, and the runner's client certificate and key. A tcp://
	// address without them is refused unless Insecure is set, which is for
	// tests on one machine only.
	CAFile     string `yaml:"ca_file"`
	CertFile   string `yaml:"cert_file"`
	KeyFile    string `yaml:"key_file"`
	ServerName string `yaml:"server_name"` // default: the address's host
	Insecure   bool   `yaml:"insecure"`
	// Platforms, if set, are the only platforms built here, such as
	// [linux/arm64] for a native arm64 builder. Default: every platform the
	// daemon says it can build, which includes emulated ones when the
	// daemon's machine has binfmt handlers installed.
	Platforms []string `yaml:"platforms"`
	// Projects may build here: names or patterns such as web-*. Empty
	// allows every project.
	Projects []string `yaml:"projects"`
}

// RegistryConfig is one registry.
type RegistryConfig struct {
	// Host is the registry as written in image references: host or
	// host:port, such as harbor.example.com.
	Host string `yaml:"host"`
	// Credentials push (and pull). They are never read for a pull-request
	// or draft run, which never pushes.
	Credentials *Credentials `yaml:"credentials"`
	// PullCredentials, if set, are read for every run, pull requests
	// included, to pull base images and read the build cache. Give a
	// read-only account here.
	PullCredentials *Credentials `yaml:"pull_credentials"`
	// CAFile is a CA the runner trusts for this registry, besides the
	// system's. The builder has its own (see deploy/buildkit/README.md).
	CAFile string `yaml:"ca_file"`
	// Insecure talks plain HTTP. Only for a registry on the runner's own
	// machine or in tests.
	Insecure bool `yaml:"insecure"`
	// Push says which projects may push to which repositories. Nothing is
	// pushed to a registry without a rule that allows it.
	Push []PushRule `yaml:"push"`
}

// PushRule lets projects push to repositories.
type PushRule struct {
	// Projects are names or patterns such as web-*.
	Projects []string `yaml:"projects"`
	// Repositories are repository paths or patterns, without the host:
	// team/shop, team/* (one level) or team/** (any depth).
	Repositories []string `yaml:"repositories"`
}

// Credentials are a user name and a password or token. The password is
// always a secret reference; the user name may be written in place (robot
// account names are not secret) or be a reference too.
type Credentials struct {
	Username     string              `yaml:"username"`
	UsernameFrom *pipeline.SecretRef `yaml:"username_from"`
	Password     pipeline.SecretRef  `yaml:"password"`
}

// SigningConfig is the runner's cosign key.
type SigningConfig struct {
	KeyFile      string `yaml:"key_file"`      // a cosign private key (cosign generate-key-pair), or PKCS#8 PEM
	PasswordFile string `yaml:"password_file"` // its password, if it is encrypted
}

// ScannerConfig is the image that writes SBOMs and finds vulnerabilities,
// run inside the builder.
type ScannerConfig struct {
	// Image, pinned by digest. Default DefaultScannerImage.
	Image string `yaml:"image"`
	// DBRepository and JavaDBRepository are where the scanner downloads its
	// vulnerability databases, for installations that mirror them.
	DBRepository     string `yaml:"db_repository"`
	JavaDBRepository string `yaml:"java_db_repository"`
}

// DefaultScannerImage is the scanner used unless the configuration names
// another: Trivy, pinned by the digest of its multi-platform index.
const DefaultScannerImage = "docker.io/aquasec/trivy:0.76.2@sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb"

var (
	builderNameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	registryNameRe = builderNameRe
	hostRe         = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*(?::[0-9]{1,5})?$`)
	platformRe     = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9_]+(/[a-z0-9]+)?$`)
	pinnedImageRe  = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
)

// Check reports what is wrong with the configuration, if anything.
func (c *Config) Check() error {
	if c == nil {
		return nil
	}
	seen := map[string]bool{}
	for i, b := range c.Builders {
		at := fmt.Sprintf("images.builders[%d]", i)
		switch {
		case !builderNameRe.MatchString(b.Name):
			return fmt.Errorf("%s: name %q: use lower-case letters, digits, - and _", at, b.Name)
		case seen[b.Name]:
			return fmt.Errorf("%s: builder %q is listed twice", at, b.Name)
		}
		seen[b.Name] = true
		scheme, rest, _ := strings.Cut(b.Address, "://")
		switch scheme {
		case "tcp":
			if rest == "" {
				return fmt.Errorf("%s (%s): address %q has no host", at, b.Name, b.Address)
			}
			if !b.Insecure && (b.CAFile == "" || b.CertFile == "" || b.KeyFile == "") {
				return fmt.Errorf("%s (%s): a tcp:// builder needs ca_file, cert_file and key_file: the runner and the builder "+
					"must know each other (mutual TLS); insecure: true is only for tests on one machine", at, b.Name)
			}
		case "unix":
			if rest == "" {
				return fmt.Errorf("%s (%s): address %q has no path", at, b.Name, b.Address)
			}
		default:
			return fmt.Errorf("%s (%s): address %q: write tcp://host:port or unix:///path", at, b.Name, b.Address)
		}
		if (b.CertFile == "") != (b.KeyFile == "") {
			return fmt.Errorf("%s (%s): give both cert_file and key_file", at, b.Name)
		}
		for _, p := range b.Platforms {
			if !platformRe.MatchString(p) {
				return fmt.Errorf("%s (%s): platform %q: write it as os/arch, such as linux/arm64", at, b.Name, p)
			}
		}
	}
	for name, r := range c.Registries {
		at := "images.registries." + name
		switch {
		case !registryNameRe.MatchString(name):
			return fmt.Errorf("%s: use lower-case letters, digits, - and _ in registry names", at)
		case !hostRe.MatchString(r.Host):
			return fmt.Errorf("%s: host %q: write the registry as in image references, such as harbor.example.com or localhost:5000", at, r.Host)
		}
		for _, cr := range []*Credentials{r.Credentials, r.PullCredentials} {
			if cr == nil {
				continue
			}
			if cr.Password.Store == "" || cr.Password.Key == "" {
				return fmt.Errorf("%s: credentials need a password: { store: ..., key: ..., field: ... }", at)
			}
			if cr.Username == "" && cr.UsernameFrom == nil {
				return fmt.Errorf("%s: credentials need a username, written in place or as username_from", at)
			}
		}
		for j, rule := range r.Push {
			if len(rule.Projects) == 0 || len(rule.Repositories) == 0 {
				return fmt.Errorf("%s.push[%d]: a rule names projects and repositories", at, j)
			}
			for _, p := range rule.Repositories {
				if _, err := path.Match(strings.ReplaceAll(p, "**", "*"), ""); err != nil || strings.HasPrefix(p, "/") {
					return fmt.Errorf("%s.push[%d]: repository pattern %q is not valid", at, j, p)
				}
			}
		}
	}
	if img := c.Scanner.Image; img != "" && !pinnedImageRe.MatchString(img) {
		return fmt.Errorf("images.scanner.image %q: pin the scanner by digest (…@sha256:…), so every build is scanned by the same thing", img)
	}
	if c.Signing != nil && c.Signing.KeyFile == "" {
		return fmt.Errorf("images.signing: key_file is required")
	}
	if c.MaxContextMiB < 0 || c.MaxImageMiB < 0 {
		return fmt.Errorf("images: limits cannot be negative")
	}
	return nil
}

// RelativeTo makes the relative file paths in c relative to dir.
func (c *Config) RelativeTo(dir string) {
	if c == nil {
		return
	}
	abs := func(p *string) {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(dir, *p)
		}
	}
	for i := range c.Builders {
		b := &c.Builders[i]
		abs(&b.CAFile)
		abs(&b.CertFile)
		abs(&b.KeyFile)
		if rest, ok := strings.CutPrefix(b.Address, "unix://"); ok && !filepath.IsAbs(rest) {
			b.Address = "unix://" + filepath.Join(dir, rest)
		}
	}
	for name, r := range c.Registries {
		abs(&r.CAFile)
		c.Registries[name] = r
	}
	if c.Signing != nil {
		abs(&c.Signing.KeyFile)
		abs(&c.Signing.PasswordFile)
	}
}

// MaxContextBytes is the largest build context the runner sends.
func (c *Config) MaxContextBytes() int64 { return c.maxContextBytes() }

func (c *Config) maxContextBytes() int64 {
	if c.MaxContextMiB > 0 {
		return c.MaxContextMiB << 20
	}
	return 2048 << 20
}

func (c *Config) maxImageBytes() int64 {
	if c.MaxImageMiB > 0 {
		return c.MaxImageMiB << 20
	}
	return 20480 << 20
}

func (c *Config) scannerImage() string {
	if c.Scanner.Image != "" {
		return c.Scanner.Image
	}
	return DefaultScannerImage
}

// MayPush reports whether a project may push to a repository of a
// registry.
func (r RegistryConfig) MayPush(project, repository string) bool {
	for _, rule := range r.Push {
		if matchAny(rule.Projects, project) && matchRepo(rule.Repositories, repository) {
			return true
		}
	}
	return false
}

// builderFor reports whether a builder serves a project.
func (b BuilderConfig) serves(project string) bool {
	return len(b.Projects) == 0 || matchAny(b.Projects, project)
}

// matchAny reports whether value matches one of the patterns, where * does
// not cross a /.
func matchAny(patterns []string, value string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, value); ok {
			return true
		}
	}
	return false
}

// matchRepo matches repository paths: * within one level, and a final /**
// for any depth below.
func matchRepo(patterns []string, repo string) bool {
	for _, p := range patterns {
		if prefix, ok := strings.CutSuffix(p, "/**"); ok {
			if ok, _ := path.Match(prefix, repo); ok {
				return true
			}
			parts := strings.Split(repo, "/")
			pp := strings.Count(prefix, "/") + 1
			if len(parts) > pp {
				if ok, _ := path.Match(prefix, strings.Join(parts[:pp], "/")); ok {
					return true
				}
			}
			continue
		}
		if ok, _ := path.Match(p, repo); ok {
			return true
		}
	}
	return false
}

// Registry returns the registry a pipeline names, or an error that says
// which ones the runner has.
func (c *Config) Registry(name string) (RegistryConfig, error) {
	if c != nil {
		if r, ok := c.Registries[name]; ok {
			return r, nil
		}
	}
	var names []string
	if c != nil {
		for n := range c.Registries {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return RegistryConfig{}, fmt.Errorf("registry %q: this runner has no registries configured (images.registries)", name)
	}
	return RegistryConfig{}, fmt.Errorf("registry %q is not configured on this runner; it has %s", name, strings.Join(names, ", "))
}
