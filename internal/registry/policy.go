package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
)

// Levels a policy may be set at, widest first. A nearer level overrides what
// a wider one says, except immutable tags, which accumulate: a release tag a
// wider level protects cannot be unprotected nearer.
const (
	ScopeInstallation = "installation"
	ScopeOrganization = "organization"
	ScopeProject      = "project"
	ScopeRepository   = "repository"
)

// Retention says which images a collection may delete. Nothing is deleted
// unless a rule says so; an image a rule keeps is kept whatever the others say.
type Retention struct {
	// KeepLast keeps the N most recently tagged images (0: no such rule, and
	// older tagged images are not deleted by age).
	KeepLast int `json:"keep_last,omitempty"`
	// KeepTags keeps every image with a tag matching one of the patterns
	// (v*, release-*).
	KeepTags []string `json:"keep_tags,omitempty"`
	// UntaggedDays deletes images that have no tag, are not part of an image
	// index that stays and carry no attached signature or SBOM of an image
	// that stays, once they are this many days old (0: never).
	UntaggedDays int `json:"untagged_days,omitempty"`
}

// Policy is what an installation, organization, project or repository sets.
// A nil field says nothing, so the wider level's value stands.
type Policy struct {
	// QuotaBytes caps what a project (or an organization, at its level)
	// stores, counting each distinct blob once. 0 is no quota.
	QuotaBytes *int64 `json:"quota_bytes,omitempty"`
	// Retention is the rule collections apply (see Retention).
	Retention *Retention `json:"retention,omitempty"`
	// ImmutableTags are tag patterns that, once pushed, cannot be pointed at
	// another image or deleted.
	ImmutableTags []string `json:"immutable_tags,omitempty"`
	// PullFrom, at a project, lists projects (patterns) whose runs may pull
	// this project's repositories with a run credential. A run may always
	// pull its own project's.
	PullFrom []string `json:"pull_from,omitempty"`
}

// Effective is the policy that applies to a repository after the levels are
// combined, and which level each part came from.
type Effective struct {
	QuotaBytes     int64     `json:"quota_bytes"` // -1: none
	QuotaFrom      string    `json:"quota_from"`
	Retention      Retention `json:"retention"`
	RetentionFrom  string    `json:"retention_from,omitempty"`
	ImmutableTags  []string  `json:"immutable_tags,omitempty"`
	PullFrom       []string  `json:"pull_from,omitempty"`
	OrgQuotaBytes  int64     `json:"org_quota_bytes"`
	HasRetention   bool      `json:"has_retention"`
	RetentionLevel string    `json:"-"`
}

// Validate refuses a policy with a nonsense value, in words.
func (p Policy) Validate() error {
	if p.QuotaBytes != nil && *p.QuotaBytes < 0 {
		return fmt.Errorf("quota_bytes cannot be negative; use 0 for no quota")
	}
	if r := p.Retention; r != nil {
		if r.KeepLast < 0 || r.UntaggedDays < 0 {
			return fmt.Errorf("retention numbers cannot be negative")
		}
	}
	for _, list := range [][]string{p.ImmutableTags, p.PullFrom, retentionTags(p.Retention)} {
		for _, pat := range list {
			if pat == "" {
				return fmt.Errorf("a pattern is empty")
			}
			if _, err := path.Match(pat, ""); err != nil {
				return fmt.Errorf("pattern %q is not valid: %v", pat, err)
			}
		}
	}
	return nil
}

func retentionTags(r *Retention) []string {
	if r == nil {
		return nil
	}
	return r.KeepTags
}

// SetPolicy stores the policy of one level; an empty policy removes it.
func (s *Service) SetPolicy(ctx context.Context, scope, name string, p Policy, by string) error {
	if err := checkScope(scope, name); err != nil {
		return newErr(http.StatusBadRequest, CodeNameInvalid, err.Error(), "")
	}
	if err := p.Validate(); err != nil {
		return newErr(http.StatusBadRequest, CodeDenied, err.Error(), "")
	}
	if p.QuotaBytes == nil && p.Retention == nil && len(p.ImmutableTags) == 0 && len(p.PullFrom) == 0 {
		_, err := s.db.Exec(ctx, `DELETE FROM registry_policies WHERE scope = ? AND name = ?`, scope, name)
		return err
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO registry_policies(scope, name, body, updated_at, updated_by) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT (scope, name) DO UPDATE SET body = excluded.body, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		scope, name, string(body), s.now(), by)
	return err
}

func checkScope(scope, name string) error {
	switch scope {
	case ScopeInstallation:
		if name != "" {
			return fmt.Errorf("the installation level has no name")
		}
	case ScopeOrganization, ScopeProject, ScopeRepository:
		if name == "" {
			return fmt.Errorf("name the %s", scope)
		}
	default:
		return fmt.Errorf("scope %q is not installation, organization, project or repository", scope)
	}
	return nil
}

// GetPolicy returns the policy set at exactly one level (the zero Policy when
// none is).
func (s *Service) GetPolicy(ctx context.Context, scope, name string) (Policy, bool, error) {
	var p Policy
	var body string
	err := s.db.QueryRow(ctx, `SELECT body FROM registry_policies WHERE scope = ? AND name = ?`, scope, name).Scan(&body)
	if isNoRows(err) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	return p, true, json.Unmarshal([]byte(body), &p)
}

// EffectivePolicy combines the levels for one repository.
func (s *Service) EffectivePolicy(ctx context.Context, repo string) (Effective, error) {
	project := ProjectOf(repo)
	org := s.org(project)
	e := Effective{QuotaBytes: s.limits.DefaultQuotaBytes, QuotaFrom: "default", OrgQuotaBytes: -1}
	if e.QuotaBytes < 0 {
		e.QuotaBytes = -1
	}
	levels := []struct{ scope, name string }{
		{ScopeInstallation, ""}, {ScopeOrganization, org}, {ScopeProject, project}, {ScopeRepository, repo},
	}
	imm := map[string]bool{}
	for _, l := range levels {
		p, ok, err := s.GetPolicy(ctx, l.scope, l.name)
		if err != nil {
			return e, err
		}
		if !ok {
			continue
		}
		if p.QuotaBytes != nil {
			if l.scope == ScopeOrganization {
				e.OrgQuotaBytes = noneIfZero(*p.QuotaBytes)
			} else {
				e.QuotaBytes, e.QuotaFrom = noneIfZero(*p.QuotaBytes), l.scope
			}
		}
		if p.Retention != nil {
			e.Retention, e.RetentionFrom, e.HasRetention = *p.Retention, l.scope, true
		}
		for _, t := range p.ImmutableTags {
			imm[t] = true
		}
		if l.scope == ScopeProject {
			e.PullFrom = append([]string(nil), p.PullFrom...)
		}
	}
	for t := range imm {
		e.ImmutableTags = append(e.ImmutableTags, t)
	}
	sort.Strings(e.ImmutableTags)
	return e, nil
}

func noneIfZero(v int64) int64 {
	if v == 0 {
		return -1
	}
	return v
}

// matchAny reports whether s matches one of the patterns.
func matchAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, s); ok {
			return true
		}
	}
	return false
}

// MayRunPull reports whether a run of project from may pull a repository of
// project to: its own project's always; another's when that project's policy
// lists it in pull_from.
func (s *Service) MayRunPull(ctx context.Context, from, to string) bool {
	if from == to {
		return true
	}
	p, ok, err := s.GetPolicy(ctx, ScopeProject, to)
	return err == nil && ok && matchAny(p.PullFrom, from)
}

// ---- usage and quota ----

// ProjectUsage is the bytes a project stores, each distinct blob counted
// once however many of its repositories use it.
func (s *Service) ProjectUsage(ctx context.Context, project string) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT COALESCE(SUM(b.size), 0) FROM registry_blobs b WHERE b.org = ? AND EXISTS (
		SELECT 1 FROM registry_repo_blobs l JOIN registry_repositories r ON r.name = l.repo WHERE l.digest = b.digest AND r.project = ?)`,
		s.org(project), project).Scan(&n)
	return n, err
}

// OrgUsage is the bytes an organization's blobs take.
func (s *Service) OrgUsage(ctx context.Context, org string) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT COALESCE(SUM(size), 0) FROM registry_blobs WHERE org = ?`, org).Scan(&n)
	return n, err
}

// refuseOverQuota refuses a push to a project that is already at or over its
// quota, before any bytes are taken.
func (s *Service) refuseOverQuota(ctx context.Context, project string, add int64) error {
	return s.checkQuotaFor(ctx, project, "", add)
}

// checkQuota refuses to add a blob when it would take the project (or the
// organization) over its quota. A blob the project already stores adds
// nothing.
func (s *Service) checkQuota(ctx context.Context, r Repository, digest string, size int64) error {
	var have int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM registry_repo_blobs l JOIN registry_repositories x ON x.name = l.repo WHERE l.digest = ? AND x.project = ?`,
		digest, r.Project).Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		size = 0
	}
	return s.checkQuotaFor(ctx, r.Project, digest, size)
}

func (s *Service) checkQuotaFor(ctx context.Context, project, _ string, add int64) error {
	eff, err := s.EffectivePolicy(ctx, project)
	if err != nil {
		return err
	}
	if eff.QuotaBytes >= 0 {
		used, err := s.ProjectUsage(ctx, project)
		if err != nil {
			return err
		}
		if used+add > eff.QuotaBytes || (add == 0 && used >= eff.QuotaBytes) {
			s.emit(ctx, Event{Type: "quota", Repo: project, Outcome: "denied", Detail: fmt.Sprintf("uses %d of %d", used, eff.QuotaBytes)})
			return newErr(http.StatusForbidden, CodeQuotaExceeded,
				fmt.Sprintf("project %s stores %s of its %s quota (%s of the quota set at %s level), so this push is refused", project, human(used), human(eff.QuotaBytes), human(add), eff.QuotaFrom),
				"Delete tags you no longer need and run garbage collection, or ask an administrator to raise the project's quota (launchway registry policy set).")
		}
	}
	if eff.OrgQuotaBytes >= 0 {
		org := s.org(project)
		used, err := s.OrgUsage(ctx, org)
		if err != nil {
			return err
		}
		if used+add > eff.OrgQuotaBytes {
			return newErr(http.StatusForbidden, CodeQuotaExceeded,
				fmt.Sprintf("the organization stores %s of its %s quota, so this push is refused", human(used), human(eff.OrgQuotaBytes)),
				"Delete tags that are no longer needed and run garbage collection, or ask an administrator to raise the organization's quota.")
		}
	}
	return nil
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
