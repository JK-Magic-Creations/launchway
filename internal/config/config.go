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
	"path"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/accept"
	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/imagebuild"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/policy"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/source"
	hsource "github.com/jk-magic-creations/launchway/internal/verify/source"
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
	User     string          `yaml:"user"`
	Server   ServerConfig    `yaml:"server"`
	Auth     AuthConfig      `yaml:"auth"`
	Projects []ProjectConfig `yaml:"projects"`
	// Libraries are the repositories of templates that pipelines may use,
	// by name. A pipeline can use no other: it names one of these, never an
	// address.
	Libraries []LibraryConfig `yaml:"libraries"`
	Runner    RunnerConfig    `yaml:"runner"`
	Approvals ApprovalsConfig `yaml:"approvals"`
	// Drafts controls draft runs, which run pipeline text from the editor
	// that is not in Git. Each project may override it.
	Drafts DraftsConfig `yaml:"drafts"`
	// PullRequests controls runs of pull requests, whose code may come from
	// anyone who can open one. Each project may override it.
	PullRequests PullRequestsConfig `yaml:"pull_requests"`
	// Tests limits what the server keeps of the test reports steps declare.
	Tests TestsConfig `yaml:"tests"`
	// Artifacts says where the files steps keep are stored, and limits
	// them; Findings limits what is kept of security findings and SBOMs.
	Artifacts ArtifactsConfig `yaml:"artifacts"`
	Findings  FindingsConfig  `yaml:"findings"`
	// Reports bounds test attachments, coverage and HTML reports, and says
	// where hosted HTML reports are served from.
	Reports ServerReportsConfig `yaml:"reports"`
	// Loops limits loops (for_each) in pipelines.
	Loops LoopsConfig `yaml:"loops"`
	// Schedules turns pipelines' schedules off for every project; a
	// project may say otherwise.
	Schedules SchedulesConfig `yaml:"schedules"`
	// Environments are where deploy steps may deploy. Every server sharing a
	// database must have the same ones.
	Environments []EnvironmentConfig `yaml:"environments"`
	// RunnerPools are deploy runner pools: each has a token of its own and
	// the targets its runners may serve. Only a pool's token can take deploy
	// work.
	RunnerPools []RunnerPoolConfig `yaml:"runner_pools"`
	// RunnerAdmin is the role needed on every project to manage runners:
	// make join tokens, revoke and rotate runners. admin (the default),
	// operator or none.
	RunnerAdmin string `yaml:"runner_admin"`
	// Policy holds delivery policies: rules pipelines cannot opt out of,
	// in this file or loaded from Git sources (schema 16). Every server
	// sharing a database must have the same ones. Unset: no policies.
	Policy *PolicyConfig `yaml:"policy"`

	// MCP controls the endpoint agents use, /mcp; see mcp.go.
	MCP MCPConfig `yaml:"mcp"`

	// Verification names the health sources verify steps may read (schema
	// 19). Where they are and their credentials are in the deploy runners'
	// configuration (health_sources); every server must list the same ones.
	// Unset: verify steps are refused.
	Verification VerificationConfig `yaml:"verification"`
	// AI lists the models steps may ask (schema 19). Unset: no model, and
	// nothing is ever sent to one; every other feature works.
	AI AIConfig `yaml:"ai"`

	// file is the path the configuration was read from, for messages.
	file string
}

// PolicyConfig is the policy section.
type PolicyConfig struct {
	// Waivers says who may waive a policy's rule, and for how long.
	Waivers PolicyWaiversConfig `yaml:"waivers"`
	// Sources are Git repositories (from libraries) of policy files.
	Sources []PolicySourceConfig `yaml:"sources"`
	// Policies are policies written here (see internal/policy); kept as a
	// node so mistakes are reported with this file's lines.
	Policies yaml.Node `yaml:"policies"`
}

// PolicyWaiversConfig: Allow is admin (the default), operator or none: the
// role needed on a project to waive one of its policies' rules (on the
// environment too, for a deploy rule). MaxDays is the longest a waiver may
// last; default 30, at most 365.
type PolicyWaiversConfig struct {
	Allow   string `yaml:"allow"`
	MaxDays int    `yaml:"max_days"`
}

// PolicySourceConfig is one Git source of policies.
type PolicySourceConfig struct {
	Name       string   `yaml:"name"`
	Library    string   `yaml:"library"`     // one of libraries:
	Version    string   `yaml:"version"`     // a tag or a full commit id
	Path       string   `yaml:"path"`        // default policies
	Projects   []string `yaml:"projects"`    // the projects it may govern; default all
	Refresh    string   `yaml:"refresh"`     // default 1m
	StaleAfter string   `yaml:"stale_after"` // default 1h
}

// PolicyOptions reads the policy section for the engine. Problems in the
// policies carry the file's lines.
func (c *Config) PolicyOptions() (engine.PolicyOptions, error) {
	var o engine.PolicyOptions
	pc := c.Policy
	if pc == nil {
		return o, nil
	}
	if pc.Policies.Kind != 0 {
		ps, err := policy.ParseList(&pc.Policies, c.file, "policy.policies", "config")
		if err != nil {
			return o, err
		}
		o.Inline = ps
	}
	libs := map[string]bool{}
	for _, l := range c.Libraries {
		libs[l.Name] = true
	}
	for _, s := range pc.Sources {
		where := "policy.sources: " + s.Name
		if !libs[s.Library] {
			return o, fmt.Errorf("%s: library %q is not one of libraries:", where, s.Library)
		}
		if !pipeline.ValidVersion(s.Version) {
			return o, fmt.Errorf("%s: version %q is not a tag or a full commit id", where, s.Version)
		}
		for _, p := range s.Projects {
			if _, err := path.Match(p, ""); err != nil || p == "" {
				return o, fmt.Errorf("%s: %q is not a valid project pattern", where, p)
			}
		}
		sc := engine.PolicySourceConfig{Name: s.Name, Library: s.Library, Version: s.Version, Path: s.Path, Projects: s.Projects}
		for _, d := range []struct {
			text string
			dst  *time.Duration
			key  string
		}{{s.Refresh, &sc.Refresh, "refresh"}, {s.StaleAfter, &sc.StaleAfter, "stale_after"}} {
			if d.text == "" {
				continue
			}
			v, err := time.ParseDuration(d.text)
			if err != nil || v < 10*time.Second || v > 7*24*time.Hour {
				return o, fmt.Errorf("%s: %s %q: write a duration between 10s and 168h", where, d.key, d.text)
			}
			*d.dst = v
		}
		if strings.Contains(s.Path, "..") {
			return o, fmt.Errorf("%s: path %q may not contain ..", where, s.Path)
		}
		o.Sources = append(o.Sources, sc)
	}
	o.MaxWaiverDays = pc.Waivers.MaxDays
	return o, nil
}

// WaiverRole is the role needed to waive a policy's rule: admin by default.
func (c *Config) WaiverRole() (auth.Role, error) {
	if c.Policy == nil || c.Policy.Waivers.Allow == "" {
		return auth.Admin, nil
	}
	r, err := auth.ParseDraftAllow(c.Policy.Waivers.Allow)
	if err != nil {
		return auth.NoRole, fmt.Errorf("policy.waivers.%w", err)
	}
	return r, nil
}

// VerificationConfig is the server's side of health sources.
type VerificationConfig struct {
	Sources []HealthSourceConfig `yaml:"sources"`
}

// HealthSourceConfig names one health source.
type HealthSourceConfig struct {
	Name     string   `yaml:"name"`
	Type     string   `yaml:"type"` // prometheus, loki, clickhouse, elasticsearch, opensearch, postgres or http
	Projects []string `yaml:"projects"`
}

// AIConfig is the model registry.
type AIConfig struct {
	Models []ai.Model `yaml:"models"`
	// Secrets are the server's own secret stores, for the keys of models it
	// calls itself (to explain a failed step); runners read keys from their
	// own. Unset: the server explains only with models that need no key.
	Secrets secrets.Config `yaml:"secrets"`
}

// EngineHealthSources converts the health sources for the engine.
func (c *Config) EngineHealthSources() []engine.HealthSource {
	out := make([]engine.HealthSource, 0, len(c.Verification.Sources))
	for _, h := range c.Verification.Sources {
		out = append(out, engine.HealthSource{Name: h.Name, Type: h.Type, Projects: h.Projects})
	}
	return out
}

// Models checks the model registry; nil when none is configured.
func (c *Config) Models() (*ai.Registry, error) {
	if len(c.AI.Models) == 0 {
		return nil, nil
	}
	r, err := ai.NewRegistry(c.AI.Models)
	if err != nil {
		return nil, fmt.Errorf("ai.models: %w", err)
	}
	return r, nil
}

// RunnerAdminRole reads runner_admin: the role needed on every project to
// manage runners.
func (c *Config) RunnerAdminRole() (auth.Role, error) {
	if c.RunnerAdmin == "" {
		return auth.Admin, nil
	}
	r, err := auth.ParseDraftAllow(c.RunnerAdmin)
	if err != nil {
		return auth.NoRole, fmt.Errorf("runner_admin: %w", err)
	}
	return r, nil
}

// EnvironmentConfig is one environment.
type EnvironmentConfig struct {
	Name string `yaml:"name"`
	// Target is what deploy runners serve to reach the environment: in
	// practice, its cluster.
	Target string `yaml:"target"`
	// Projects may deploy here: names or patterns such as mcp-*.
	Projects []string `yaml:"projects"`
	// Queue is newest (the default: a run waiting for the lock is skipped
	// when a newer run of the same service starts waiting) or all.
	Queue string `yaml:"queue"`
	// Protected: the person who started a run may not promote its
	// deployment here (unless approvals.allow_self lets them approve their
	// own runs); anyone with the operator role here may abort it. Off by
	// default.
	Protected bool `yaml:"protected"`
}

// RunnerPoolConfig is one runner pool. A deploy pool (the default kind)
// serves targets; its runners use the pool's token, read from an
// environment variable or a file, never from this file, or enrol with a
// join token for the pool. A build pool (schema 21) serves projects, and
// optionally only some events; its runners enrol with join tokens. The
// pool named default is the build pool of the shared runner token: list
// it to limit what it serves or to require identities.
type RunnerPoolConfig struct {
	Name      string   `yaml:"name"`
	Kind      string   `yaml:"kind"` // deploy (the default) or build
	TokenEnv  string   `yaml:"token_env"`
	TokenFile string   `yaml:"token_file"`
	Targets   []string `yaml:"targets"`
	// Projects and Events, for a build pool.
	Projects []string `yaml:"projects"`
	Events   []string `yaml:"events"`
	// RequireIdentity refuses shared tokens: the pool's own (a deploy
	// pool) or the shared runner token (the default pool). Only runners
	// enrolled with a join token for the pool serve it.
	RequireIdentity bool `yaml:"require_identity"`
}

// EngineEnvironments converts the environments for the engine.
func (c *Config) EngineEnvironments() []engine.Environment {
	out := make([]engine.Environment, 0, len(c.Environments))
	for _, e := range c.Environments {
		out = append(out, engine.Environment{Name: e.Name, Target: e.Target, Projects: e.Projects, Queue: e.Queue, Protected: e.Protected})
	}
	return out
}

// Pool is a runner pool with its token read.
type Pool struct {
	Name     string
	Kind     string // engine.PoolBuild or engine.PoolDeploy
	Token    string // a deploy pool's; "" if it requires identities
	Targets  []string
	Projects []string
	Events   []string
	// RequireIdentity: only enrolled runners serve the pool.
	RequireIdentity bool
}

// EnginePools converts pools for the engine.
func EnginePools(pools []Pool) []engine.RunnerPool {
	out := make([]engine.RunnerPool, 0, len(pools))
	for _, p := range pools {
		out = append(out, engine.RunnerPool{Name: p.Name, Kind: p.Kind, Projects: p.Projects, Events: p.Events,
			RequireIdentity: p.RequireIdentity, Targets: p.Targets})
	}
	return out
}

// Pools reads the runner pools' tokens and checks them: every pool has a
// name, a token of at least 32 characters that no other pool (nor the
// shared runner token) has, and targets that some environment uses.
func (c *Config) Pools(sharedToken string) ([]Pool, error) {
	targets := map[string]bool{}
	for _, e := range c.Environments {
		targets[e.Target] = true
	}
	var out []Pool
	seen := map[string]bool{}
	for _, p := range c.RunnerPools {
		where := "runner_pools: " + p.Name
		switch {
		case p.Name == "":
			return nil, errors.New("runner_pools: every pool needs a name")
		case seen[p.Name]:
			return nil, fmt.Errorf("runner_pools: %s is listed twice", p.Name)
		case p.Kind != "" && p.Kind != engine.PoolBuild && p.Kind != engine.PoolDeploy:
			return nil, fmt.Errorf("%s: kind is deploy (the default) or build", where)
		}
		if p.Kind == engine.PoolBuild {
			switch {
			case len(p.Targets) > 0 || p.TokenEnv != "" || p.TokenFile != "":
				return nil, fmt.Errorf("%s: a build pool has no targets and no token: its runners enrol with join tokens", where)
			case len(p.Projects) == 0 && p.Name != engine.DefaultPool:
				return nil, fmt.Errorf("%s: list the projects it serves (names or patterns such as web-*; [\"*\"] for all)", where)
			case p.Name == engine.DefaultPool && p.RequireIdentity && c.RunnerEnabled():
				return nil, fmt.Errorf("%s: require_identity refuses the shared runner token, which the built-in runner uses; turn the built-in runner off (runner.enabled: false)", where)
			}
			seen[p.Name] = true
			out = append(out, Pool{Name: p.Name, Kind: engine.PoolBuild, Projects: p.Projects, Events: p.Events, RequireIdentity: p.RequireIdentity})
			continue
		}
		switch {
		case len(p.Projects) > 0 || len(p.Events) > 0:
			return nil, fmt.Errorf("%s: projects and events are for build pools (kind: build); a deploy pool serves targets", where)
		case p.Name == engine.DefaultPool:
			return nil, fmt.Errorf("%s: the pool named default is the build pool of the shared runner token; give the deploy pool another name", where)
		case p.RequireIdentity && p.TokenEnv == "" && p.TokenFile == "" && len(p.Targets) > 0:
			seen[p.Name] = true
			for _, t := range p.Targets {
				if !targets[t] {
					return nil, fmt.Errorf("%s: no environment has target %q", where, t)
				}
			}
			out = append(out, Pool{Name: p.Name, Kind: engine.PoolDeploy, Targets: p.Targets, RequireIdentity: true})
			continue
		case p.RequireIdentity:
			return nil, fmt.Errorf("%s: require_identity refuses the pool's token, so give it none (no token_env or token_file)", where)
		case len(p.Targets) == 0:
			return nil, fmt.Errorf("%s: list the targets its runners may serve", where)
		case (p.TokenEnv == "") == (p.TokenFile == ""):
			return nil, fmt.Errorf("%s: give the token with exactly one of token_env or token_file", where)
		}
		seen[p.Name] = true
		for _, t := range p.Targets {
			if !targets[t] {
				return nil, fmt.Errorf("%s: no environment has target %q", where, t)
			}
		}
		tok := ""
		if p.TokenEnv != "" {
			tok = strings.TrimSpace(os.Getenv(p.TokenEnv))
			if tok == "" {
				return nil, fmt.Errorf("%s: the variable %s is not set", where, p.TokenEnv)
			}
		} else {
			b, err := os.ReadFile(p.TokenFile)
			if err != nil {
				return nil, fmt.Errorf("%s: token_file: %w", where, err)
			}
			tok = strings.TrimSpace(string(b))
		}
		if len(tok) < 32 {
			return nil, fmt.Errorf("%s: the token is shorter than 32 characters; make a long random one, for example with: openssl rand -hex 24", where)
		}
		if tok == sharedToken {
			return nil, fmt.Errorf("%s: the token is the shared runner token; a pool needs a token of its own", where)
		}
		for _, o := range out {
			if o.Token == tok {
				return nil, fmt.Errorf("%s: the token is also the token of pool %s", where, o.Name)
			}
		}
		out = append(out, Pool{Name: p.Name, Kind: engine.PoolDeploy, Token: tok, Targets: p.Targets})
	}
	return out, nil
}

// TestsConfig limits what the server keeps of test reports. Zero means the
// default.
type TestsConfig struct {
	// MaxUploadMiB is how many MiB of report files the server accepts for
	// one step attempt; runners are told and send no more. Default 24.
	MaxUploadMiB int `yaml:"max_upload_mib"`
	// MaxCasesPerRun is how many individual test results one run keeps;
	// beyond it, only the totals. Default 50000.
	MaxCasesPerRun int `yaml:"max_cases_per_run"`
	// MaxCasesPerProject is how many a project keeps over all its runs: the
	// oldest runs' are removed first, keeping their totals. Default 1000000.
	MaxCasesPerProject int `yaml:"max_cases_per_project"`
}

// TestLimits converts the tests settings for the engine.
func (c *Config) TestLimits() (engine.TestLimits, error) {
	t := c.Tests
	if t.MaxUploadMiB < 0 || t.MaxCasesPerRun < 0 || t.MaxCasesPerProject < 0 {
		return engine.TestLimits{}, errors.New("tests: limits cannot be negative")
	}
	if t.MaxUploadMiB > 1024 {
		return engine.TestLimits{}, errors.New("tests: max_upload_mib is at most 1024")
	}
	return engine.TestLimits{MaxUploadBytes: int64(t.MaxUploadMiB) << 20, MaxCasesPerRun: t.MaxCasesPerRun,
		MaxCasesPerProject: t.MaxCasesPerProject}, nil
}

// ReportsConfig limits the report files a runner collects after a step.
// Zero means the default.
type ReportsConfig struct {
	MaxFiles    int `yaml:"max_files"`     // files per step attempt; default 100
	MaxFileMiB  int `yaml:"max_file_mib"`  // MiB read of one file; a larger one is cut; default 10
	MaxTotalMiB int `yaml:"max_total_mib"` // MiB of all of them together; default 20
}

// Limits converts the reports settings for the runner.
func (r ReportsConfig) Limits() (executor.CollectLimits, error) {
	if r.MaxFiles < 0 || r.MaxFileMiB < 0 || r.MaxTotalMiB < 0 {
		return executor.CollectLimits{}, errors.New("reports: limits cannot be negative")
	}
	if r.MaxFileMiB > 1024 || r.MaxTotalMiB > 1024 {
		return executor.CollectLimits{}, errors.New("reports: max_file_mib and max_total_mib are at most 1024")
	}
	return executor.CollectLimits{MaxFiles: r.MaxFiles, MaxFileBytes: int64(r.MaxFileMiB) << 20,
		MaxBytes: int64(r.MaxTotalMiB) << 20}.WithDefaults(), nil
}

// PullRequestsConfig says whether the steps of pull-request runs may read
// secrets.
type PullRequestsConfig struct {
	// Secrets lets steps of pull-request runs read secrets; default false.
	// In a project's pull_requests, unset means the server's setting.
	Secrets *bool `yaml:"secrets"`
}

// DraftsConfig says who may start draft runs and whether their steps may
// read secrets.
type DraftsConfig struct {
	// Allow is admin, operator (the default) or none: the role needed on a
	// project to start a draft run there. In a project's drafts, empty means
	// the server's setting.
	Allow string `yaml:"allow"`
	// Secrets lets steps of draft runs read secrets; default false. In a
	// project's drafts, unset means the server's setting.
	Secrets *bool `yaml:"secrets"`
}

// PushConfig lets the pipeline editor save to a project's repository. It
// uses the credentials the server reads the repository with.
type PushConfig struct {
	// BranchPrefix starts every branch a save creates; default launchway/.
	BranchPrefix string `yaml:"branch_prefix"`
	// AllowDirect lists branch patterns that may be saved to directly,
	// without a new branch. Default: none.
	AllowDirect []string `yaml:"allow_direct"`
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
	// Admins are users and groups who are admins of every project whatever
	// the role bindings (in the database since schema 23) say: the first
	// administrator of a new installation, and the way back in if the
	// bindings lock everyone out. Bootstrap: it stays in this file.
	Admins AdminsConfig `yaml:"admins"`
	// Tokens limits API tokens.
	Tokens TokensConfig `yaml:"tokens"`

	// accessInDatabase: the role bindings are in the database, so an empty
	// access here is not a mistake (see LoadBootstrap).
	accessInDatabase bool
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
	// Environments, if set, limits what the binding grants on deployments
	// (promote, abort and the like, and approvals that name a deployment)
	// to these environments.
	Environments []string `yaml:"environments"`
}

// AdminsConfig names the bootstrap administrators.
type AdminsConfig struct {
	Users  []string `yaml:"users"`
	Groups []string `yaml:"groups"`
}

// Empty reports whether nobody is named.
func (a AdminsConfig) Empty() bool { return len(a.Users) == 0 && len(a.Groups) == 0 }

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
	// Drafts overrides the server's drafts setting for this project.
	Drafts *DraftsConfig `yaml:"drafts"`
	// PullRequests overrides the server's pull_requests setting for this
	// project.
	PullRequests *PullRequestsConfig `yaml:"pull_requests"`
	// Push lets the pipeline editor commit to this repository. Without it,
	// edited pipelines can be copied or downloaded, not saved.
	Push *PushConfig `yaml:"push"`
	// Pipeline is the project's pipeline at commits that have no
	// .launchway/pipeline.yaml: a template of a library. A file in the
	// repository takes its place.
	Pipeline *ProjectPipelineConfig `yaml:"pipeline"`
	// Schedules overrides the server's schedules setting for this project.
	Schedules *SchedulesConfig `yaml:"schedules"`
	// GitWebhook lets the project's Git host call a webhook that makes the
	// server poll the project at once.
	GitWebhook *GitWebhookConfig `yaml:"git_webhook"`
	// Webhooks says what callers of the pipeline's webhook triggers must
	// prove, by trigger name. A trigger not listed here cannot be called.
	Webhooks map[string]WebhookConfig `yaml:"webhooks"`
}

// LibraryConfig is a repository of templates and the scripts they use.
type LibraryConfig struct {
	Name string `yaml:"name"`
	Repo string `yaml:"repo"` // fetched with the same credentials as projects
}

// ProjectPipelineConfig runs a library's template as a project's pipeline.
type ProjectPipelineConfig struct {
	Library  string            `yaml:"library"`
	Version  string            `yaml:"version"` // a tag, branch or full commit id
	Template string            `yaml:"template"`
	With     map[string]string `yaml:"with"`
	On       ProjectPipelineOn `yaml:"on"`
	// Supersede: a new run of a push or pull request cancels the older runs
	// of the same ref that are still going (see the pipeline key).
	Supersede bool `yaml:"supersede"`
	// Tools that read YAML 1.1 (Helm values among them) turn a bare `on`
	// key into the boolean true and write it back as "true". Take that
	// spelling for what it was meant to be rather than refusing to start.
	OnAsTrue *ProjectPipelineOn `yaml:"true"`
}

// ProjectPipelineOn says which events start a configured pipeline.
type ProjectPipelineOn struct {
	Push *struct {
		Branches []string `yaml:"branches"`
		Tags     []string `yaml:"tags"`
	} `yaml:"push"`
	// PullRequest, written pull_request: {} (or with no value), starts runs
	// for pull requests. It takes no settings.
	PullRequest yaml.Node `yaml:"pull_request"`
}

// pullRequests reports whether on.pull_request is written, and checks that
// it has no settings.
func (o ProjectPipelineOn) pullRequests() (bool, error) {
	n := o.PullRequest
	switch {
	case n.Kind == 0:
		return false, nil
	case n.Kind == yaml.ScalarNode && n.Tag == "!!null":
		return true, nil
	case n.Kind == yaml.MappingNode && len(n.Content) == 0:
		return true, nil
	}
	return false, fmt.Errorf("pipeline.on.pull_request takes no settings (pull requests cannot be filtered by branch); write pull_request: {}")
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
	// Reports limits the report files collected after each step.
	Reports ReportsConfig `yaml:"reports"`
	// Images lets the runner take image steps: the builders it sends builds
	// to, and the registries it pushes to (see internal/imagebuild). Unset,
	// the runner takes no image steps.
	Images *imagebuild.Config `yaml:"images"`
	// AI lets the runner call models: for ai steps on a runner, and to
	// explain verdicts on a deploy runner (schema 19). The models are the
	// server's registry; their keys come from this runner's secret stores.
	// Unset or false: it calls none.
	AI *bool `yaml:"ai"`
}

// AIEnabled reports whether the runner calls models.
func (s RunnerSettings) AIEnabled() bool { return s.AI != nil && *s.AI }

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

// Load reads path. A missing file gives the defaults.
func Load(path string) (*Config, error) { return readConfig(path, false) }

// LoadBootstrap reads the file a server starts with. It is checked as Load
// checks it, except that it need not grant anyone a role: since schema 23
// the role bindings are in the database, which is checked with them when
// the server reads it.
func LoadBootstrap(path string) (*Config, error) { return readConfig(path, true) }

func readConfig(path string, bootstrap bool) (*Config, error) {
	c := &Config{}
	c.Auth.accessInDatabase = bootstrap
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
			c.file = path
			// Relative paths are relative to the config file.
			base := filepath.Dir(path)
			if c.Data != "" && !filepath.IsAbs(c.Data) {
				c.Data = filepath.Join(base, c.Data)
			}
			if c.Database != "" && !isURL(c.Database) && !filepath.IsAbs(c.Database) {
				c.Database = filepath.Join(base, c.Database)
			}
			for i, p := range c.Projects {
				if pp := p.Pipeline; pp != nil && pp.OnAsTrue != nil {
					if pp.On.Push != nil || pp.On.PullRequest.Kind != 0 {
						return nil, fmt.Errorf("%s: project %s: pipeline has both `on` and `true` (an unquoted on); keep one", path, p.Name)
					}
					pp.On, pp.OnAsTrue = *pp.OnAsTrue, nil
				}
				if isLocalPath(p.Repo) && !filepath.IsAbs(p.Repo) {
					c.Projects[i].Repo = filepath.Join(base, p.Repo)
				}
			}
			for i, l := range c.Libraries {
				if isLocalPath(l.Repo) && !filepath.IsAbs(l.Repo) {
					c.Libraries[i].Repo = filepath.Join(base, l.Repo)
				}
			}
			c.Runner.Secrets.RelativeTo(base)
			c.AI.Secrets.RelativeTo(base)
			c.Runner.Images.RelativeTo(base)
			c.relativeTriggerFiles(base)
			if f := c.Auth.OIDC.ClientSecretFile; f != "" && !filepath.IsAbs(f) {
				c.Auth.OIDC.ClientSecretFile = filepath.Join(base, f)
			}
			for i, p := range c.RunnerPools {
				if p.TokenFile != "" && !filepath.IsAbs(p.TokenFile) {
					c.RunnerPools[i].TokenFile = filepath.Join(base, p.TokenFile)
				}
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
	for i, l := range c.Libraries {
		if isLocalPath(l.Repo) {
			if abs, err := filepath.Abs(l.Repo); err == nil {
				c.Libraries[i].Repo = abs
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
	if c.Artifacts.Dir != "" && !filepath.IsAbs(c.Artifacts.Dir) && path != "" {
		c.Artifacts.Dir = filepath.Join(filepath.Dir(path), c.Artifacts.Dir)
	}
	if err := c.Validate(path); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks the whole configuration: the file's when the server
// starts, and the database's (with the file's bootstrap settings) before
// any change to it is stored, so both are held to exactly the same rules.
// path names where it came from in messages.
func (c *Config) Validate(path string) error {
	if err := c.Auth.check(); err != nil {
		where := "auth"
		if path != "" {
			where = path + ": auth"
		}
		return fmt.Errorf("%s: %w", where, err)
	}
	if _, err := c.TestLimits(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := c.Runner.Reports.Limits(); err != nil {
		return fmt.Errorf("%s: runner.%w", path, err)
	}
	if err := c.Runner.Images.Check(); err != nil {
		return fmt.Errorf("%s: runner.%w", path, err)
	}
	if _, err := c.ArtifactLimits(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := c.FindingLimits(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := c.ReportOptions(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := c.ReportsOrigin(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := c.checkTriggers(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := c.MCP.Check(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if c.MCP.OAuth != nil && c.Auth.Mode != AuthOIDC {
		return fmt.Errorf("%s: mcp.oauth needs sign-in with OpenID Connect (auth.mode: oidc)", path)
	}
	if _, err := c.Models(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := c.DraftRules(); err != nil {
		if path != "" {
			err = fmt.Errorf("%s: %w", path, err)
		}
		return err
	}
	for _, p := range c.Projects {
		if p.Pipeline == nil {
			continue
		}
		if _, err := p.Pipeline.On.pullRequests(); err != nil {
			if path != "" {
				return fmt.Errorf("%s: project %s: %w", path, p.Name, err)
			}
			return fmt.Errorf("project %s: %w", p.Name, err)
		}
	}
	// Policies are refused here, with their lines, so a mistake stops the
	// server before it starts.
	if _, err := c.PolicyOptions(); err != nil {
		var pp policy.Problems
		if !errors.As(err, &pp) && path != "" {
			err = fmt.Errorf("%s: %w", path, err)
		}
		return err
	}
	if _, err := c.WaiverRole(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := c.RunnerAdminRole(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// DraftRules works out who may start draft runs on each project.
func (c *Config) DraftRules() (*auth.DraftRules, error) {
	def := auth.DefaultDraftRule
	if c.Drafts.Allow != "" {
		role, err := auth.ParseDraftAllow(c.Drafts.Allow)
		if err != nil {
			return nil, fmt.Errorf("drafts: %w", err)
		}
		def.Allow = role
	}
	if c.Drafts.Secrets != nil {
		def.Secrets = *c.Drafts.Secrets
	}
	rules := &auth.DraftRules{Default: def, Projects: map[string]auth.DraftRule{}}
	for _, p := range c.Projects {
		if p.Drafts == nil {
			continue
		}
		r := def
		if p.Drafts.Allow != "" {
			role, err := auth.ParseDraftAllow(p.Drafts.Allow)
			if err != nil {
				return nil, fmt.Errorf("project %s: drafts: %w", p.Name, err)
			}
			r.Allow = role
		}
		if p.Drafts.Secrets != nil {
			r.Secrets = *p.Drafts.Secrets
		}
		rules.Projects[p.Name] = r
	}
	return rules, nil
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
		sp := source.Project{Name: p.Name, URL: p.Repo, DefaultBranch: p.Branch, Poll: poll, PullRequestSecrets: c.pullRequestSecrets(p)}
		if p.Push != nil {
			sp.Push = &source.PushSettings{BranchPrefix: p.Push.BranchPrefix, AllowDirect: p.Push.AllowDirect}
		}
		c.applyTriggers(p, &sp)
		if pp := p.Pipeline; pp != nil {
			sp.Pipeline = &source.ProjectPipeline{Library: pp.Library, Version: pp.Version, Template: pp.Template, With: pp.With,
				Supersede: pp.Supersede}
			if t := pp.On.Push; t != nil {
				sp.Pipeline.On.Push = &pipeline.PushTrigger{Branches: t.Branches, Tags: t.Tags}
			}
			prs, err := pp.On.pullRequests()
			if err != nil {
				return nil, fmt.Errorf("project %s: %w", p.Name, err)
			}
			if prs {
				sp.Pipeline.On.PullRequest = &pipeline.PullRequestTrigger{}
			}
		}
		out = append(out, sp)
	}
	return out, nil
}

// pullRequestSecrets says whether the steps of a project's pull-request runs
// may read secrets: the project's setting, else the server's, else no.
func (c *Config) pullRequestSecrets(p ProjectConfig) bool {
	if p.PullRequests != nil && p.PullRequests.Secrets != nil {
		return *p.PullRequests.Secrets
	}
	return c.PullRequests.Secrets != nil && *c.PullRequests.Secrets
}

// SourceLibraries converts the library list for the source package.
func (c *Config) SourceLibraries() []source.Library {
	out := make([]source.Library, 0, len(c.Libraries))
	for _, l := range c.Libraries {
		out = append(out, source.Library{Name: l.Name, URL: l.Repo})
	}
	return out
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
