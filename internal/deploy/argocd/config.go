// Package argocd is the deploy runner's side of deploy and rollback steps
// with Argo CD: what it may touch (Config), how it reaches a cluster through
// the Kubernetes API (Cluster), the actions it does (commit and sync,
// revert and sync), and what it observes.
//
// Everything goes through the Kubernetes API with the runner's own
// identity: no Argo CD token, no step container, no kubectl. The operations
// are a closed set: read Applications, Rollouts and AnalysisRuns; set an
// Application's operation (to sync) and its refresh annotation.
package argocd

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Config is the deploy section of a deploy runner's configuration.
type Config struct {
	// Targets this runner serves, by name: the names environments give in
	// the server's configuration.
	Targets map[string]Target `yaml:"targets"`
	// Repos are the GitOps repositories deploy steps may commit to, by the
	// name pipelines use (write_back.repo).
	Repos map[string]Repo `yaml:"repos"`
}

// Target is one cluster and what in it deploy steps may touch.
type Target struct {
	Kubernetes Kubernetes `yaml:"kubernetes"`
	// ArgoCDNamespace is where the Applications are; default argocd.
	ArgoCDNamespace string `yaml:"argocd_namespace"`
	// Applications lists which projects may deploy which applications.
	Applications []AppRule `yaml:"applications"`
	// RolloutNamespaces are the namespaces whose Rollouts deploy steps may
	// watch.
	RolloutNamespaces []string `yaml:"rollout_namespaces"`
	// RefreshAnnotation, true unless set to false, asks Argo CD to look at
	// the repository at once after a commit (the refresh annotation), so
	// that it notices the commit without waiting for its next poll. It needs
	// patch on the Application; without that right it is skipped, with a
	// note in the step's log.
	RefreshAnnotation *bool `yaml:"refresh"`
}

// Kubernetes says how to reach a target's cluster. It must say so
// explicitly: a deploy runner never uses whatever context happens to be
// current.
type Kubernetes struct {
	InCluster  bool   `yaml:"in_cluster"`
	Kubeconfig string `yaml:"kubeconfig"` // default: the usual kubeconfig files
	Context    string `yaml:"context"`
}

// AppRule lets projects deploy applications.
type AppRule struct {
	Projects []string `yaml:"projects"` // names or patterns
	Names    []string `yaml:"names"`    // application names or patterns
}

// Repo is a GitOps repository.
type Repo struct {
	URL string `yaml:"url"` // what the runner clones and pushes to
	// ArgoCDURL is the repository's address as Argo CD's Applications write
	// it (spec.source.repoURL), when it differs from URL; default URL.
	ArgoCDURL string `yaml:"argocd_url"`
	// Credentials name a secret in one of the runner's secret stores: the
	// token or password for HTTP basic authentication. Never in this file.
	Credentials *pipeline.SecretRef `yaml:"credentials"`
	Username    string              `yaml:"username"` // default launchway
	// Hosted says Launchway itself hosts the repository, and url is its
	// address. Deploy steps then write to it with the step's own credential
	// (the environment's write_back) and never with a stored one; credentials
	// here, if any, only read (observing what Argo CD has synced).
	Hosted bool `yaml:"hosted"`
	// Paths lists which projects may change which files (patterns, where
	// ** matches any number of directories).
	Paths  []PathRule `yaml:"paths"`
	Author Author     `yaml:"author"`
}

// PathRule lets projects change files.
type PathRule struct {
	Projects []string `yaml:"projects"`
	Files    []string `yaml:"files"`
}

// Author is who the runner's commits are by.
type Author struct {
	Name  string `yaml:"name"`  // default Launchway
	Email string `yaml:"email"` // default launchway@localhost
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Check checks the configuration and fills in defaults.
func (c *Config) Check() error {
	if len(c.Targets) == 0 {
		return errors.New("deploy: list at least one target under targets")
	}
	for name, t := range c.Targets {
		where := "deploy.targets." + name
		if !nameRe.MatchString(name) {
			return fmt.Errorf("%s: use lower-case letters, digits and hyphens", where)
		}
		k := t.Kubernetes
		switch {
		case k.InCluster && (k.Kubeconfig != "" || k.Context != ""):
			return fmt.Errorf("%s.kubernetes: give in_cluster: true, or a context (and optionally a kubeconfig), not both", where)
		case !k.InCluster && k.Context == "":
			return fmt.Errorf("%s.kubernetes: say which cluster: in_cluster: true, or context: <a kubeconfig context>; the current context is never used", where)
		}
		if t.ArgoCDNamespace == "" {
			t.ArgoCDNamespace = "argocd"
		}
		if len(t.Applications) == 0 {
			return fmt.Errorf("%s: list which projects may deploy which applications under applications", where)
		}
		for _, r := range t.Applications {
			if len(r.Projects) == 0 || len(r.Names) == 0 {
				return fmt.Errorf("%s.applications: every rule needs projects and names", where)
			}
			if err := checkPatterns(append(slices.Clone(r.Projects), r.Names...)); err != nil {
				return fmt.Errorf("%s.applications: %w", where, err)
			}
		}
		c.Targets[name] = t
	}
	for name, r := range c.Repos {
		where := "deploy.repos." + name
		if !nameRe.MatchString(name) {
			return fmt.Errorf("%s: use lower-case letters, digits, hyphens and underscores", where)
		}
		if r.URL == "" {
			return fmt.Errorf("%s: url is required", where)
		}
		if err := r.checkAddress(where); err != nil {
			return err
		}
		if r.ArgoCDURL == "" {
			r.ArgoCDURL = r.URL
		}
		if r.Username == "" {
			r.Username = "launchway"
		}
		if r.Author.Name == "" {
			r.Author.Name = "Launchway"
		}
		if r.Author.Email == "" {
			r.Author.Email = "launchway@localhost"
		}
		if r.Credentials != nil && (!pipeline.ValidStoreName(r.Credentials.Store) || r.Credentials.Key == "") {
			return fmt.Errorf("%s.credentials: name a store and a key, as in { store: vault, key: ci/gitops, field: token }", where)
		}
		if len(r.Paths) == 0 {
			return fmt.Errorf("%s: list which projects may change which files under paths", where)
		}
		for _, p := range r.Paths {
			if len(p.Projects) == 0 || len(p.Files) == 0 {
				return fmt.Errorf("%s.paths: every rule needs projects and files", where)
			}
			if err := checkPatterns(p.Projects); err != nil {
				return fmt.Errorf("%s.paths: %w", where, err)
			}
		}
		c.Repos[name] = r
	}
	return nil
}

// checkAddress checks a repository's address: no credentials in it, and a
// hosted repository's is Launchway's, over http or https.
func (r Repo) checkAddress(where string) error {
	if strings.Contains(r.URL, "@") && strings.Contains(r.URL, "://") {
		return fmt.Errorf("%s: the url must not hold credentials; name them under credentials", where)
	}
	if r.Hosted && !strings.HasPrefix(r.URL, "https://") && !strings.HasPrefix(r.URL, "http://") {
		return fmt.Errorf("%s: a hosted repository's url is Launchway's address over http or https", where)
	}
	return nil
}

func checkPatterns(ps []string) error {
	for _, p := range ps {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("%q is not a valid pattern", p)
		}
	}
	return nil
}

func matchAny(patterns []string, v string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, v); ok {
			return true
		}
	}
	return false
}

// AppAllowed reports whether a project may deploy an application on the
// target.
func (t Target) AppAllowed(project, app string) bool {
	for _, r := range t.Applications {
		if matchAny(r.Projects, project) && matchAny(r.Names, app) {
			return true
		}
	}
	return false
}

// RolloutAllowed reports whether rollouts of a namespace may be watched.
func (t Target) RolloutAllowed(namespace string) bool {
	return slices.Contains(t.RolloutNamespaces, namespace)
}

// Refresh reports whether to set the refresh annotation after a commit.
func (t Target) Refresh() bool { return t.RefreshAnnotation == nil || *t.RefreshAnnotation }

// FileAllowed reports whether a project may change a file of the
// repository.
func (r Repo) FileAllowed(project, file string) bool {
	for _, rule := range r.Paths {
		if !matchAny(rule.Projects, project) {
			continue
		}
		for _, f := range rule.Files {
			if globMatch(f, file) {
				return true
			}
		}
	}
	return false
}

// AllowsProject reports whether any path rule of the repository lets the
// project change files in it.
func (r Repo) AllowsProject(project string) bool {
	for _, rule := range r.Paths {
		if matchAny(rule.Projects, project) && len(rule.Files) > 0 {
			return true
		}
	}
	return false
}

// globMatch matches a path against a pattern where a part ** matches any
// number of directories (none included).
func globMatch(pattern, name string) bool {
	return matchParts(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchParts(ps, ns []string) bool {
	for len(ps) > 0 {
		if ps[0] == "**" {
			for i := 0; i <= len(ns); i++ {
				if matchParts(ps[1:], ns[i:]) {
					return true
				}
			}
			return false
		}
		if len(ns) == 0 {
			return false
		}
		if ok, _ := path.Match(ps[0], ns[0]); !ok {
			return false
		}
		ps, ns = ps[1:], ns[1:]
	}
	return len(ns) == 0
}
