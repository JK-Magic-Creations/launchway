// Package accept is a runner's own limits on the work it takes: the accept:
// section of its configuration. The server sends a runner work; a runner
// with an accept: section checks each job against it before anything runs
// (no clone, no secret looked up, no container started) and refuses what it
// does not allow, saying why. It is the customer's last line of defence
// against a control plane that has been taken over: the server cannot
// change it, because it lives only in the runner's file.
//
// It limits what work reaches the runner's network and secrets. It cannot
// make the work that it does accept safe: a compromised server can still
// send any script for an accepted project, repository and image. See the
// docs/guide/runner-identity.md, "What a runner trusts the server with".
package accept

import (
	"fmt"
	"strings"
	"time"
)

// Config is the accept: section. Every list that is empty allows anything;
// a list that is set allows only what matches one of its patterns.
// Patterns are globs in which * matches any run of characters, slashes
// included, and ? matches one character.
type Config struct {
	// Projects the runner takes work for, by name.
	Projects []string `yaml:"projects" json:"projects,omitempty"`
	// Repositories it clones: the run's repository and every library's,
	// as URLs without credentials, such as https://git.example.com/shop/*.
	Repositories []string `yaml:"repositories" json:"repositories,omitempty"`
	// Images steps and their containers may use, as written and in full
	// (docker.io/library/golang:1.22 for golang:1.22), such as
	// registry.example.com/* or docker.io/library/golang@sha256:*.
	Images []string `yaml:"images" json:"images,omitempty"`
	// ImagesByDigest refuses images not pinned by digest (name@sha256:...).
	ImagesByDigest bool `yaml:"images_by_digest" json:"images_by_digest,omitempty"`
	// Executors the runner may run work with: its own executor's name
	// (podman, docker, host, kubernetes) for ordinary steps, buildkit for
	// image steps, deploy for deploy steps.
	Executors []string `yaml:"executors" json:"executors,omitempty"`
	// Environments a deploy runner deploys to.
	Environments []string `yaml:"environments" json:"environments,omitempty"`
	// PullRequests and Drafts: false refuses runs of pull requests (code
	// anyone who can open one wrote) and draft runs (pipeline text that is
	// not in Git). Unset allows them.
	PullRequests *bool `yaml:"pull_requests" json:"pull_requests,omitempty"`
	Drafts       *bool `yaml:"drafts" json:"drafts,omitempty"`
	// MaxTimeout refuses steps that may run longer, such as 2h.
	MaxTimeout string `yaml:"max_timeout" json:"max_timeout,omitempty"`
	// EnvDeny refuses steps that set environment variables (or secrets) of
	// these names, such as LD_PRELOAD or GIT_*.
	EnvDeny []string `yaml:"env_deny" json:"env_deny,omitempty"`
}

// Empty reports whether the section limits nothing.
func (c *Config) Empty() bool {
	return c == nil || (len(c.Projects) == 0 && len(c.Repositories) == 0 && len(c.Images) == 0 && !c.ImagesByDigest &&
		len(c.Executors) == 0 && len(c.Environments) == 0 && c.PullRequests == nil && c.Drafts == nil && c.MaxTimeout == "" &&
		len(c.EnvDeny) == 0)
}

// Check reports a mistake in the section.
func (c *Config) Check() error {
	if c == nil {
		return nil
	}
	for key, list := range map[string][]string{"projects": c.Projects, "repositories": c.Repositories, "images": c.Images,
		"executors": c.Executors, "environments": c.Environments, "env_deny": c.EnvDeny} {
		for _, p := range list {
			if strings.TrimSpace(p) == "" {
				return fmt.Errorf("accept.%s: an empty pattern", key)
			}
		}
	}
	if c.MaxTimeout != "" {
		if d, err := time.ParseDuration(c.MaxTimeout); err != nil || d <= 0 {
			return fmt.Errorf("accept.max_timeout: %q is not a duration such as 2h", c.MaxTimeout)
		}
	}
	return nil
}

// Timeout is MaxTimeout read; 0 if unset.
func (c *Config) Timeout() time.Duration {
	if c == nil || c.MaxTimeout == "" {
		return 0
	}
	d, _ := time.ParseDuration(c.MaxTimeout)
	return d
}

// Match reports whether s matches the glob pattern: * matches any run of
// characters, slashes included, and ? exactly one.
func Match(pattern, s string) bool {
	// The classic two-pointer wildcard match, on runes.
	p, t := []rune(pattern), []rune(s)
	pi, ti, star, mark := 0, 0, -1, 0
	for ti < len(t) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == t[ti]):
			pi++
			ti++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ti
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			ti = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// MatchAny reports whether s matches one of patterns; an empty list matches
// anything.
func MatchAny(patterns []string, s string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if Match(p, s) {
			return true
		}
	}
	return false
}

// Project says why the runner refuses a project's work, or "".
func (c *Config) Project(project string) string {
	if c != nil && !MatchAny(c.Projects, project) {
		return fmt.Sprintf("project %s is not in accept.projects", project)
	}
	return ""
}

// Event says why the runner refuses work of a run started by event, or "".
// An event the server did not say (an older server) is refused when pull
// requests or drafts are refused, since it cannot be told apart.
func (c *Config) Event(event string) string {
	if c == nil {
		return ""
	}
	off := func(b *bool) bool { return b != nil && !*b }
	switch {
	case event == "" && (off(c.PullRequests) || off(c.Drafts)):
		return "the server did not say what started the run, and accept refuses pull requests or drafts"
	case event == "pull_request" && off(c.PullRequests):
		return "the run is of a pull request, and accept.pull_requests is false"
	case event == "draft" && off(c.Drafts):
		return "the run is a draft (pipeline text not in Git), and accept.drafts is false"
	}
	return ""
}

// Repository says why the runner refuses to clone url, or "".
func (c *Config) Repository(url string) string {
	if c != nil && !MatchAny(c.Repositories, url) {
		return fmt.Sprintf("repository %s is not in accept.repositories", url)
	}
	return ""
}

// Image says why the runner refuses an image, or "".
func (c *Config) Image(image string) string {
	if c == nil || image == "" {
		return ""
	}
	full := FullImage(image)
	if c.ImagesByDigest && !strings.Contains(image, "@sha256:") {
		return fmt.Sprintf("image %s is not pinned by digest, and accept.images_by_digest is true", image)
	}
	if len(c.Images) > 0 && !MatchAny(c.Images, image) && !MatchAny(c.Images, full) {
		return fmt.Sprintf("image %s is not in accept.images", image)
	}
	return ""
}

// Executor says why the runner refuses to run work with an executor, or "".
func (c *Config) Executor(name string) string {
	if c != nil && !MatchAny(c.Executors, name) {
		return fmt.Sprintf("the work needs executor %s, which is not in accept.executors", name)
	}
	return ""
}

// Environment says why a deploy runner refuses to deploy to env, or "".
func (c *Config) Environment(env string) string {
	if c != nil && !MatchAny(c.Environments, env) {
		return fmt.Sprintf("environment %s is not in accept.environments", env)
	}
	return ""
}

// StepTimeout says why the runner refuses a step that may run for d, or "".
func (c *Config) StepTimeout(d time.Duration) string {
	if max := c.Timeout(); max > 0 && d > max {
		return fmt.Sprintf("the step may run for %s, longer than accept.max_timeout %s", d, max)
	}
	return ""
}

// EnvName says why the runner refuses a step that sets variable name, or "".
func (c *Config) EnvName(name string) string {
	if c == nil {
		return ""
	}
	for _, p := range c.EnvDeny {
		if Match(p, name) {
			return fmt.Sprintf("the step sets %s, which accept.env_deny refuses", name)
		}
	}
	return ""
}

// FullImage writes an image reference in full: with its registry, and with
// library/ for an official Docker Hub image. golang:1.22 becomes
// docker.io/library/golang:1.22.
func FullImage(image string) string {
	first, rest, ok := strings.Cut(image, "/")
	if !ok {
		return "docker.io/library/" + image
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return image
	}
	return "docker.io/" + first + "/" + rest
}
