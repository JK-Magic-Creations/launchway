package forge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SecretRef says where Launchway reads a credential. It never holds the
// credential: a Connection with a literal secret is refused.
type SecretRef struct {
	// Env is the name of an environment variable of the server.
	Env string `json:"env,omitempty"`
	// File is the absolute path of a file that holds the secret.
	File string `json:"file,omitempty"`
}

// IsZero reports whether no reference is given.
func (s SecretRef) IsZero() bool { return s.Env == "" && s.File == "" }

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// Validate checks the reference is a reference and not a value.
func (s SecretRef) Validate() error {
	if s.Env != "" && s.File != "" {
		return errors.New("a credential is read from an environment variable or a file, not both")
	}
	if s.Env != "" && !envName.MatchString(s.Env) {
		return fmt.Errorf("credential.env must be the name of an environment variable (upper case, digits and _), not a secret value (%d characters given)", len(s.Env))
	}
	if s.File != "" && (!filepath.IsAbs(s.File) || strings.ContainsRune(s.File, 0)) {
		return errors.New("credential.file must be an absolute path")
	}
	return nil
}

// EventMode is how events arrive from a forge.
type EventMode string

const (
	// EventsInProcess: the built-in forge, exact, no network.
	EventsInProcess EventMode = "in_process"
	// EventsWebhook: a signed or token-checked webhook, treated as a hint.
	EventsWebhook EventMode = "webhook"
	// EventsPolling: Launchway polls; the source of truth for every kind.
	EventsPolling EventMode = "polling"
)

// Connection is a configured link to a forge.
type Connection struct {
	ID      string `json:"id"` // con_...
	Name    string `json:"name"`
	Kind    Kind   `json:"kind"`
	BaseURL string `json:"base_url,omitempty"`
	// Credential is a reference, never a value.
	Credential SecretRef `json:"credential,omitzero"`
	Events     EventMode `json:"events,omitempty"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ErrInvalid is wrapped by every validation failure of the model.
var ErrInvalid = errors.New("invalid")

// Validate checks a connection before it is stored: a known kind, a
// well-formed base address without credentials in it, and a credential that
// is a reference.
func (c Connection) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: connection: %s", ErrInvalid, fmt.Sprintf(format, a...))
	}
	if !nameRE.MatchString(c.Name) {
		return bad("the name must be 1 to 63 lower-case letters, digits, '.', '_' or '-'")
	}
	if !c.Kind.Valid() {
		return bad("unknown kind %q", c.Kind)
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return bad("base_url must be an http or https address")
		}
		if u.User != nil {
			return bad("base_url must not contain a user name or password; give the credential as a reference")
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return bad("base_url must not contain a query or fragment")
		}
	}
	if err := c.Credential.Validate(); err != nil {
		return bad("%v", err)
	}
	switch c.Events {
	case "", EventsInProcess, EventsWebhook, EventsPolling:
	default:
		return bad("unknown event mode %q", c.Events)
	}
	return nil
}

// Visibility of a repository.
type Visibility string

const (
	Private  Visibility = "private"
	Internal Visibility = "internal"
	Public   Visibility = "public"
)

// CloneAddresses are where a repository is cloned from. Never credentials.
type CloneAddresses struct {
	HTTPS string `json:"https,omitempty"`
	SSH   string `json:"ssh,omitempty"`
}

// Repository is a repository on a connection.
type Repository struct {
	ID            string         `json:"id"` // rep_...
	Connection    string         `json:"connection"`
	Home          string         `json:"home,omitempty"`
	Name          string         `json:"name"`
	Path          string         `json:"path"` // as the forge names it, e.g. "home/name"
	DefaultBranch string         `json:"default_branch,omitempty"`
	Visibility    Visibility     `json:"visibility"`
	Archived      bool           `json:"archived,omitempty"`
	Clone         CloneAddresses `json:"clone"`
	// Mirror says Launchway keeps this repository current from another
	// address; it takes no pushes.
	Mirror    bool           `json:"mirror,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Ext       map[string]any `json:"ext,omitempty"`
}

// Validate checks a repository before it is stored.
func (r Repository) Validate() error {
	if r.Connection == "" {
		return fmt.Errorf("%w: repository: connection is required", ErrInvalid)
	}
	if !validPath(r.Path) {
		return fmt.Errorf("%w: repository: path %q is not a plain path", ErrInvalid, r.Path)
	}
	switch r.Visibility {
	case Private, Internal, Public:
	default:
		return fmt.Errorf("%w: repository: unknown visibility %q", ErrInvalid, r.Visibility)
	}
	for _, a := range []string{r.Clone.HTTPS, r.Clone.SSH} {
		if u, err := url.Parse(a); a != "" && (err != nil || u.User != nil && u.User.String() != "git" && u.Scheme != "ssh") {
			return fmt.Errorf("%w: repository: a clone address must not carry credentials", ErrInvalid)
		}
	}
	return nil
}

func validPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.Contains(p, "//") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." || seg == "" || strings.ContainsAny(seg, "\x00\\") {
			return false
		}
	}
	return true
}

// Ref is a branch, tag or other ref, and the commit it points at.
type Ref struct {
	// Name is the full name, such as "refs/heads/main".
	Name string `json:"name"`
	// Commit is the full commit ID.
	Commit string `json:"commit"`
	// Repository is set when the ref lives in a repository other than the one
	// asked about (the source of a proposal from a fork).
	Repository string `json:"repository,omitempty"`
}

// State of a result.
type State string

const (
	Pending State = "pending"
	Passing State = "passing"
	Failing State = "failing"
	Errored State = "error"
)

// Valid reports whether s is a known state.
func (s State) Valid() bool { return s == Pending || s == Passing || s == Failing || s == Errored }

// Details are optional rich parts of a result: tests, coverage, findings,
// the verdict of a policy.
type Details struct {
	Tests    *TestCounts `json:"tests,omitempty"`
	Coverage *float64    `json:"coverage,omitempty"`
	Findings int         `json:"findings,omitempty"`
	Policy   string      `json:"policy,omitempty"`
}

// TestCounts summarises a test run.
type TestCounts struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// Result is a named verdict on a commit by a producer.
type Result struct {
	Producer string `json:"producer"`
	// Name is what ref rules require, for example "pipeline:ci".
	Name    string         `json:"name"`
	SHA     string         `json:"sha"`
	State   State          `json:"state"`
	Summary string         `json:"summary,omitempty"`
	URL     string         `json:"url,omitempty"`
	Details *Details       `json:"details,omitempty"`
	Ext     map[string]any `json:"ext,omitempty"`
}

// Validate checks a result before it is published.
func (r Result) Validate() error {
	switch {
	case r.Producer == "" || r.Name == "":
		return fmt.Errorf("%w: result: producer and name are required", ErrInvalid)
	case len(r.SHA) != 40 && len(r.SHA) != 64:
		return fmt.Errorf("%w: result: sha must be a full commit ID", ErrInvalid)
	case !r.State.Valid():
		return fmt.Errorf("%w: result: unknown state %q", ErrInvalid, r.State)
	}
	return nil
}

// Principal is a person or automation on a forge, as that forge knows them.
type Principal struct {
	// User is the Launchway user this maps to, when known.
	User string `json:"user,omitempty"`
	// Login is the name on the forge.
	Login string `json:"login"`
	Name  string `json:"name,omitempty"`
	// Unlinked is true for a forge identity that has no Launchway account.
	Unlinked bool `json:"unlinked,omitempty"`
}

// Mergeability says whether a proposal can land and, when it cannot, why, in
// plain words.
type Mergeability struct {
	Ready   bool     `json:"ready"`
	Reasons []string `json:"reasons,omitempty"`
}

// ProposalState of a proposal.
type ProposalState string

const (
	ProposalOpen   ProposalState = "open"
	ProposalDraft  ProposalState = "draft"
	ProposalLanded ProposalState = "landed"
	ProposalClosed ProposalState = "closed"
)

// Authority says which side holds the record of a proposal.
type Authority string

const (
	// AuthorityLaunchway: Launchway holds the record.
	AuthorityLaunchway Authority = "launchway"
	// AuthorityProvider: the forge does, and this is a cache.
	AuthorityProvider Authority = "provider"
)

// Proposal is a request to bring the commits of one ref into another. This is
// the shape that later stories fill in; the fields are final, the behaviour
// is not built yet.
type Proposal struct {
	ID         string         `json:"id"` // prp_...
	Repository string         `json:"repository"`
	Number     int            `json:"number"`
	Title      string         `json:"title"`
	Body       string         `json:"body,omitempty"`
	Author     Principal      `json:"author"`
	Source     Ref            `json:"source"`
	Target     Ref            `json:"target"`
	State      ProposalState  `json:"state"`
	Head       string         `json:"head,omitempty"`
	Base       string         `json:"base,omitempty"`
	Mergeable  Mergeability   `json:"mergeability"`
	Authority  Authority      `json:"authority"`
	SyncedAt   time.Time      `json:"synced_at,omitzero"`
	Ext        map[string]any `json:"ext,omitempty"`
}

// Revision is one head of a proposal.
type Revision struct {
	Proposal  string    `json:"proposal"`
	Number    int       `json:"number"`
	Head      string    `json:"head"`
	Base      string    `json:"base"`
	CreatedAt time.Time `json:"created_at"`
}

// Verdict of a review.
type Verdict string

const (
	Approve        Verdict = "approve"
	RequestChanges Verdict = "request_changes"
	Comment        Verdict = "comment"
)

// Review is a person's verdict on a revision.
type Review struct {
	ID        string    `json:"id"`
	Proposal  string    `json:"proposal"`
	Revision  int       `json:"revision"`
	Author    Principal `json:"author"`
	Verdict   Verdict   `json:"verdict"`
	Body      string    `json:"body,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Thread is a conversation on a proposal, or on a line of a revision.
type Thread struct {
	ID       string `json:"id"`
	Proposal string `json:"proposal"`
	// Path and Line are set for a thread on a line of a revision.
	Path     string    `json:"path,omitempty"`
	Line     int       `json:"line,omitempty"`
	Resolved bool      `json:"resolved"`
	Messages []Message `json:"messages"`
}

// Message is one post in a thread.
type Message struct {
	Author    Principal `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Commit is a commit as the content group reports it.
type Commit struct {
	ID        string    `json:"id"`
	Parents   []string  `json:"parents,omitempty"`
	Author    Principal `json:"author"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// TreeEntry is one entry of a directory.
type TreeEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // file, dir, link, submodule
	Mode string `json:"mode"`
	ID   string `json:"id"`
	Size int64  `json:"size,omitempty"`
}

// Comparison of two commits.
type Comparison struct {
	Base    string   `json:"base"`
	Head    string   `json:"head"`
	Ahead   int      `json:"ahead"`
	Behind  int      `json:"behind"`
	Commits []Commit `json:"commits,omitempty"`
}

// Strategy of landing a proposal.
type Strategy string

const (
	StrategyMerge       Strategy = "merge"
	StrategySquash      Strategy = "squash"
	StrategyReplay      Strategy = "replay"
	StrategyFastForward Strategy = "fast_forward"
)

// RefRule is a rule for refs matching a pattern.
type RefRule struct {
	Pattern         string     `json:"pattern"`
	PushersOnly     []string   `json:"pushers_only,omitempty"`
	ProposalsOnly   bool       `json:"proposals_only,omitempty"`
	RequiredReviews int        `json:"required_reviews,omitempty"`
	RequiredResults []string   `json:"required_results,omitempty"`
	AllowedStrategy []Strategy `json:"allowed_strategies,omitempty"`
	Immutable       bool       `json:"immutable,omitempty"`
}

// DecodeConnection reads a connection from JSON. A field the model does not
// have, such as "token" or "password", is refused rather than ignored, so a
// literal credential can never be stored by accident.
func DecodeConnection(data []byte) (Connection, error) {
	var c Connection
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Connection{}, fmt.Errorf("%w: connection: %v (credentials are given as a reference: credential.env or credential.file)", ErrInvalid, err)
	}
	return c, c.Validate()
}
