package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Errors the interfaces map to their own answers.
var (
	ErrNotFound  = errors.New("not found")
	ErrInvalid   = errors.New("invalid")
	ErrConflict  = errors.New("conflict")
	ErrForbidden = errors.New("forbidden")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}
func notFound(what string) error { return fmt.Errorf("%w: %s", ErrNotFound, what) }
func conflict(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, a...))
}
func forbidden(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrForbidden, fmt.Sprintf(format, a...))
}

// Scope kinds. A project's id is the project's name (a hosted repository's
// owner/name); a unit's is the unit's name; the others have no id.
const (
	ScopeProject      = "project"
	ScopeUnit         = "unit"
	ScopeOrganization = "organization"
	ScopeInstallation = "installation"
)

// Scope is where an item lives: one project, or a level above it.
type Scope struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

func (s Scope) String() string {
	if s.ID == "" {
		return s.Kind
	}
	return s.Kind + ":" + s.ID
}

// Validate checks a scope.
func (s Scope) Validate() error {
	switch s.Kind {
	case ScopeProject, ScopeUnit:
		if s.ID == "" || len(s.ID) > 200 {
			return invalid("a %s scope needs its name", s.Kind)
		}
	case ScopeOrganization, ScopeInstallation:
		if s.ID != "" {
			return invalid("the %s scope has no name", s.Kind)
		}
	default:
		return invalid("scope kind %q is none of project, unit, organization, installation", s.Kind)
	}
	return nil
}

// Item types.
const (
	TypeIssue = "issue"
	TypeBug   = "bug"
	TypeStory = "story"
	TypeTask  = "task"
	TypeEpic  = "epic"
	// TypeDecision is a decision record (decisions.go).
	TypeDecision = "decision"
)

// Types are the item types, in the order the interface lists them.
var Types = []string{TypeIssue, TypeBug, TypeStory, TypeTask, TypeEpic, TypeDecision}

// Priorities, most urgent first.
var Priorities = []string{"urgent", "high", "normal", "low"}

// PriorityWeight orders priorities (0 is most urgent).
func PriorityWeight(p string) int {
	for i, x := range Priorities {
		if x == p {
			return i
		}
	}
	return 2
}

// Actor is who does something: a person, an agent or the system.
type Actor struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // person, agent or system
	Via  string `json:"via"`  // ui, api, cli, mcp, git, import or system
}

// Actor kinds.
const (
	KindPerson = "person"
	KindAgent  = "agent"
	KindSystem = "system"
)

// Human reports whether a person is acting.
func (a Actor) Human() bool { return a.Kind == KindPerson }

// Assignee is a person or an agent an item is assigned to.
type Assignee struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Criterion is one line of an item's acceptance checklist.
type Criterion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// Claim says an agent holds an item.
type Claim struct {
	Agent      string `json:"agent"`
	ClaimedAt  string `json:"claimed_at"`
	Heartbeat  string `json:"heartbeat_at"`
	LeaseUntil string `json:"lease_until"`
	Note       string `json:"note,omitempty"`
	Expired    bool   `json:"expired,omitempty"`
}

// Progress is the roll-up of an item's children.
type Progress struct {
	Total       int     `json:"total"`
	Done        int     `json:"done"`
	Points      float64 `json:"points"`
	PointsDone  float64 `json:"points_done"`
	InProgress  int     `json:"in_progress"`
	BlockedKids int     `json:"blocked,omitempty"`
}

// Item is one piece of work.
type Item struct {
	ID           string         `json:"id"`
	Ref          string         `json:"ref"` // KEY-12
	Scope        Scope          `json:"scope"`
	Number       int            `json:"number"`
	Type         string         `json:"type"`
	Title        string         `json:"title"`
	Body         string         `json:"body,omitempty"`
	State        string         `json:"state"`
	StateName    string         `json:"state_name,omitempty"`
	Category     string         `json:"category,omitempty"`
	Resolution   string         `json:"resolution,omitempty"`
	Priority     string         `json:"priority"`
	Estimate     float64        `json:"estimate,omitempty"`
	ParentID     string         `json:"parent_id,omitempty"`
	ParentRef    string         `json:"parent_ref,omitempty"`
	MilestoneID  string         `json:"milestone_id,omitempty"`
	Milestone    string         `json:"milestone,omitempty"`
	SprintID     string         `json:"sprint_id,omitempty"`
	Sprint       string         `json:"sprint,omitempty"`
	Rank         int64          `json:"rank"`
	StartAt      string         `json:"start_at,omitempty"`
	DueAt        string         `json:"due_at,omitempty"`
	Reporter     string         `json:"reporter,omitempty"`
	ReporterKind string         `json:"reporter_kind,omitempty"`
	Confidential bool           `json:"confidential,omitempty"`
	Difficulty   string         `json:"difficulty,omitempty"` // small, medium, large or empty
	Custom       map[string]any `json:"custom,omitempty"`
	Labels       []string       `json:"labels"`
	Assignees    []Assignee     `json:"assignees"`
	Watchers     []string       `json:"watchers,omitempty"`
	Criteria     []Criterion    `json:"criteria,omitempty"`
	Blocked      bool           `json:"blocked,omitempty"`
	BlockedBy    []string       `json:"blocked_by,omitempty"` // refs of the open items that block it
	Claim        *Claim         `json:"claim,omitempty"`
	Children     *Progress      `json:"children,omitempty"`
	SourceKey    string         `json:"source_key,omitempty"`
	Version      int            `json:"version"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	ClosedAt     string         `json:"closed_at,omitempty"`
	Comments     int            `json:"comment_count,omitempty"`
	// Reactions are the counts of each reaction and whether the caller made it (filled for the API).
	Reactions []ReactionCount `json:"reactions,omitempty"`
}

// Limits.
const (
	MaxTitle     = 300
	MaxBody      = 256 << 10
	MaxComment   = 64 << 10
	MaxLabels    = 50
	MaxLabelLen  = 60
	MaxAssignees = 20
	MaxCriteria  = 200
	MaxCustom    = 50
)

func ms(t time.Time) int64 { return t.UnixMilli() }

func iso(m int64) string {
	if m <= 0 {
		return ""
	}
	return time.UnixMilli(m).UTC().Format(time.RFC3339)
}

// ParseTime reads an RFC 3339 time or a plain date (as midnight UTC).
func ParseTime(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli(), nil
		}
	}
	return 0, invalid("%q is not a date (use 2026-12-31 or an RFC 3339 time)", s)
}

// cleanTitle removes control characters from a one-line text.
func cleanTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func cleanBody(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.ReplaceAll(s, "\x00", "")
}

func cleanLabel(s string) (string, error) {
	s = strings.ToLower(cleanTitle(s))
	if s == "" || utf8.RuneCountInString(s) > MaxLabelLen {
		return "", invalid("a label is 1 to %d characters", MaxLabelLen)
	}
	if strings.ContainsAny(s, ",\"") {
		return "", invalid("label %q holds a comma or a quote", s)
	}
	return s, nil
}

// estimate conversions: thousandths.
func toMilli(f float64) int64   { return int64(f*1000 + 0.5*sign(f)) }
func fromMilli(m int64) float64 { return float64(m) / 1000 }
func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

func itoa(n int) string { return strconv.Itoa(n) }

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
