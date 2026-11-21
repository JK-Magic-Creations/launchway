package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// ---- the policy ----

// CachePolicy is the configuration of caches: whether they are kept at all,
// and how much. It is held in the database, as the installation's default
// and as an override for a project, and an administrator changes it.
type CachePolicy struct {
	Enabled bool `json:"enabled"`
	// MaxProjectGiB is how much one project's entries may add up to, and
	// MaxEntryGiB the most one entry may hold. They are GiB, and may be
	// fractions (0.05 is about 51 MiB).
	MaxProjectGiB float64 `json:"max_project_gib"`
	MaxEntryGiB   float64 `json:"max_entry_gib"`
	// IdleDays is how long an entry nobody restored is kept.
	IdleDays int `json:"idle_days"`
}

// The policy as compiled in: what an installation has until an
// administrator sets one.
var defaultCachePolicy = CachePolicy{Enabled: true, MaxProjectGiB: 10, MaxEntryGiB: 2, IdleDays: 7}

// CachePolicyView is a policy with where it comes from.
type CachePolicyView struct {
	CachePolicy
	// Source is project (this project's own), installation (set for every
	// project) or default (nothing was ever set: the values compiled in).
	Source string `json:"source"`
	// Project is the project an override is for; empty for the installation's.
	Project   string `json:"project,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// cacheLimits is a policy in bytes.
type cacheLimits struct {
	enabled         bool
	maxProject      int64
	maxEntry        int64
	idle            time.Duration
	source, project string
	updatedAt       int64
	updatedBy       string
}

const gib = float64(1 << 30)

func (l cacheLimits) view() CachePolicyView {
	return CachePolicyView{CachePolicy: CachePolicy{Enabled: l.enabled, MaxProjectGiB: float64(l.maxProject) / gib,
		MaxEntryGiB: float64(l.maxEntry) / gib, IdleDays: int(l.idle / (24 * time.Hour))},
		Source: l.source, Project: l.project, UpdatedAt: l.updatedAt, UpdatedBy: l.updatedBy}
}

func limitsOf(p CachePolicy) cacheLimits {
	return cacheLimits{enabled: p.Enabled, maxProject: int64(math.Round(p.MaxProjectGiB * gib)),
		maxEntry: int64(math.Round(p.MaxEntryGiB * gib)), idle: time.Duration(p.IdleDays) * 24 * time.Hour}
}

// CheckCachePolicy returns why a policy cannot be used, or nil.
func CheckCachePolicy(p CachePolicy) error {
	switch {
	case math.IsNaN(p.MaxProjectGiB) || math.IsInf(p.MaxProjectGiB, 0) || p.MaxProjectGiB < 1.0/1024 || p.MaxProjectGiB > 1<<20:
		return fmt.Errorf("%w: max_project_gib is between 0.001 (about 1 MiB) and 1048576", ErrInvalid)
	case math.IsNaN(p.MaxEntryGiB) || math.IsInf(p.MaxEntryGiB, 0) || p.MaxEntryGiB < 1.0/1024 || p.MaxEntryGiB > 1<<10:
		return fmt.Errorf("%w: max_entry_gib is between 0.001 (about 1 MiB) and 1024", ErrInvalid)
	case p.MaxEntryGiB > p.MaxProjectGiB:
		return fmt.Errorf("%w: max_entry_gib (%g) is more than max_project_gib (%g): an entry could never be kept", ErrInvalid, p.MaxEntryGiB, p.MaxProjectGiB)
	case p.IdleDays < 1 || p.IdleDays > 3650:
		return fmt.Errorf("%w: idle_days is between 1 and 3650", ErrInvalid)
	}
	return nil
}

func (e *Engine) readCacheLimits(q querier, project string) (cacheLimits, error) {
	for _, row := range []struct{ project, source string }{{project, "project"}, {"", "installation"}} {
		if row.project == "" && row.source == "project" {
			continue
		}
		var l cacheLimits
		var enabled int
		var idleDays int
		err := q.QueryRow(`SELECT enabled, max_project_bytes, max_entry_bytes, idle_days, updated_at, updated_by FROM cache_policy WHERE project = ?`,
			row.project).Scan(&enabled, &l.maxProject, &l.maxEntry, &idleDays, &l.updatedAt, &l.updatedBy)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return l, err
		}
		l.enabled, l.idle, l.source = enabled != 0, time.Duration(idleDays)*24*time.Hour, row.source
		if row.source == "project" {
			l.project = project
		}
		return l, nil
	}
	l := limitsOf(defaultCachePolicy)
	l.source = "default"
	return l, nil
}

// CachePolicyFor returns the policy that applies to a project: its own, or
// the installation's.
func (e *Engine) CachePolicyFor(project string) (CachePolicyView, error) {
	if !e.knows(schemaCache) {
		return CachePolicyView{}, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	l, err := e.readCacheLimits(e.db, project)
	return l.view(), err
}

// CachePolicyInstallation returns the installation's policy.
func (e *Engine) CachePolicyInstallation() (CachePolicyView, error) {
	if !e.knows(schemaCache) {
		return CachePolicyView{}, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	l, err := e.readCacheLimits(e.db, "")
	return l.view(), err
}

// SetCachePolicy replaces the installation's policy (project empty) or a
// project's override. It returns the policy that applied before and after.
func (e *Engine) SetCachePolicy(project string, p CachePolicy, actor string) (before, after CachePolicyView, err error) {
	if !e.knows(schemaCache) {
		return before, after, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	if err := CheckCachePolicy(p); err != nil {
		return before, after, err
	}
	l := limitsOf(p)
	err = e.runWrite(func(tx *txx) error {
		b, err := e.readCacheLimits(tx, project)
		if err != nil {
			return err
		}
		before = b.view()
		now := e.now().UnixMilli()
		if _, err := tx.Exec(`INSERT INTO cache_policy(project, enabled, max_project_bytes, max_entry_bytes, idle_days, updated_at, updated_by)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(project) DO UPDATE SET enabled = excluded.enabled, max_project_bytes = excluded.max_project_bytes,
			max_entry_bytes = excluded.max_entry_bytes, idle_days = excluded.idle_days, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			project, boolInt(l.enabled), l.maxProject, l.maxEntry, p.IdleDays, now, actor); err != nil {
			return err
		}
		a, err := e.readCacheLimits(tx, project)
		after = a.view()
		return err
	})
	return before, after, err
}

// ClearCachePolicy removes a project's override, so the installation's
// policy applies to it again.
func (e *Engine) ClearCachePolicy(project, actor string) (before, after CachePolicyView, err error) {
	if !e.knows(schemaCache) {
		return before, after, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	if project == "" {
		return before, after, fmt.Errorf("%w: the installation's policy cannot be removed, only changed", ErrInvalid)
	}
	err = e.runWrite(func(tx *txx) error {
		b, err := e.readCacheLimits(tx, project)
		if err != nil {
			return err
		}
		before = b.view()
		if _, err := tx.Exec(`DELETE FROM cache_policy WHERE project = ?`, project); err != nil {
			return err
		}
		a, err := e.readCacheLimits(tx, project)
		after = a.view()
		return err
	})
	return before, after, err
}
