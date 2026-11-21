// Package togglestore keeps feature toggles (schema 47) and does what is done
// with them: changing them under the safeguards, the history and its revert,
// approvals, scheduled changes, the in-memory snapshot of each environment's
// ruleset that applications read, SDK keys, evaluation counts and the stale
// nudge. The rules and the evaluator are in internal/toggles; who may call
// what is decided above this package, in internal/api, by the same levels and
// roles as variables.
package togglestore

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/toggles"
)

// Org is the organization everything belongs to until there is more than one.
const Org = "default"

// Level is where a toggle, a segment or a safeguard is set: the same four
// levels as variables (internal/settings), widest first.
type Level string

const (
	LevelInstallation Level = "installation"
	LevelOrganization Level = "organization"
	LevelUnit         Level = "unit"
	LevelProject      Level = "project"
)

// Valid reports whether l is a level.
func (l Level) Valid() bool {
	return l == LevelInstallation || l == LevelOrganization || l == LevelUnit || l == LevelProject
}

// Where is a level and its scope: the unit's or the project's name, empty for
// the two above.
type Where struct {
	Level Level  `json:"level"`
	Scope string `json:"scope,omitempty"`
}

func (w Where) String() string {
	if w.Scope == "" {
		return string(w.Level)
	}
	return string(w.Level) + " " + w.Scope
}

// Check says whether the level and scope make sense together.
func (w Where) Check() error {
	if !w.Level.Valid() {
		return invalid("level is installation, organization, unit or project")
	}
	if (w.Level == LevelUnit || w.Level == LevelProject) && w.Scope == "" {
		return invalid("say which %s", w.Level)
	}
	if (w.Level == LevelInstallation || w.Level == LevelOrganization) && w.Scope != "" {
		return invalid("the %s level has no scope", w.Level)
	}
	return nil
}

// Key is a toggle's place: where it is defined and its key.
type Key struct {
	Where
	Key string `json:"key"`
}

func (k Key) String() string {
	if k.Scope == "" {
		return fmt.Sprintf("%s/%s", k.Level, k.Key)
	}
	return fmt.Sprintf("%s/%s/%s", k.Level, k.Scope, k.Key)
}

func invalid(f string, a ...any) error {
	return fmt.Errorf("%w: %s", engine.ErrInvalid, fmt.Sprintf(f, a...))
}
func missing(f string, a ...any) error {
	return fmt.Errorf("%w: %s", engine.ErrNotFound, fmt.Sprintf(f, a...))
}
func conflict(f string, a ...any) error {
	return fmt.Errorf("%w: %s", engine.ErrConflict, fmt.Sprintf(f, a...))
}
func refused(f string, a ...any) error {
	return fmt.Errorf("%w: %s", engine.ErrForbidden, fmt.Sprintf(f, a...))
}

// ErrReasonRequired is returned when a safeguard asks for a reason and there
// is none; the message says which setting asks.
var ErrReasonRequired = fmt.Errorf("%w: a reason is required", engine.ErrInvalid)

// Definition is a toggle as defined, apart from what it does in any
// environment.
type Definition struct {
	Key
	Type          toggles.ValueType `json:"type"`
	Kind          toggles.Kind      `json:"kind"`
	Description   string            `json:"description"`
	Salt          string            `json:"salt"`
	Default       any               `json:"default"`
	SafeValue     any               `json:"safe_value"`
	ClientVisible bool              `json:"client_visible"`
	Owner         string            `json:"owner"`
	Tags          []string          `json:"tags"`
	ExpiresAt     int64             `json:"expires_at,omitempty"`
	Origin        string            `json:"origin,omitempty"`
	OriginChange  int64             `json:"origin_change,omitempty"`
	Lock          *Lock             `json:"lock,omitempty"`
	Version       int               `json:"version"`
	CreatedAt     int64             `json:"created_at"`
	CreatedBy     string            `json:"created_by"`
	UpdatedAt     int64             `json:"updated_at"`
	UpdatedBy     string            `json:"updated_by"`
}

// Lock stops a nearer level from defining a toggle of the same key.
type Lock struct {
	Reason string `json:"reason"`
	By     string `json:"by"`
}

// EnvState is a toggle's state in one environment, as stored.
type EnvState struct {
	Environment  string        `json:"environment"`
	State        toggles.State `json:"state"`
	Settled      string        `json:"settled,omitempty"`
	SettledSince int64         `json:"settled_since,omitempty"`
	Version      int           `json:"version"`
	UpdatedAt    int64         `json:"updated_at"`
	UpdatedBy    string        `json:"updated_by"`
}

// SegmentRow is a segment as stored.
type SegmentRow struct {
	Where
	Name      string          `json:"name"`
	Segment   toggles.Segment `json:"segment"`
	Version   int             `json:"version"`
	UpdatedAt int64           `json:"updated_at"`
	UpdatedBy string          `json:"updated_by"`
}
