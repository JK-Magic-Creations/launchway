package webhooks

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Events a hook can ask for. pull_request is sent for every change of a pull
// request (opened, edited, synchronize, closed, reopened, merged, ...);
// pull_request_review for a review submitted or dismissed.
const (
	EventPush              = "push"
	EventCreate            = "create"
	EventDelete            = "delete"
	EventRepository        = "repository"
	EventPullRequest       = "pull_request"
	EventPullRequestReview = "pull_request_review"
	EventPing              = "ping" // a test delivery; always delivered
)

// Events is the whole set, in a fixed order.
var Events = []string{EventPush, EventCreate, EventDelete, EventRepository, EventPullRequest, EventPullRequestReview}

// Scopes of a hook.
const (
	ScopeRepository = "repository"
	ScopeOwner      = "owner"
)

// Hook is a configured webhook. The secret is never part of it.
type Hook struct {
	ID    string `json:"id"`
	Scope string `json:"scope"`
	// Repository is the repository's path for a repository hook; Owner is the
	// owner whose repositories an owner hook covers.
	Repository     string    `json:"repository,omitempty"`
	RepoID         string    `json:"-"`
	Owner          string    `json:"owner,omitempty"`
	Name           string    `json:"name"`
	URL            string    `json:"url"`
	Events         []string  `json:"events"`
	Active         bool      `json:"active"`
	HasSecret      bool      `json:"has_secret"`
	Failures       int       `json:"failures"`
	DisabledReason string    `json:"disabled_reason,omitempty"`
	DisabledAt     time.Time `json:"disabled_at,omitzero"`
	CreatedAt      time.Time `json:"created_at"`
	CreatedBy      string    `json:"created_by,omitempty"`
}

// MaskURL is how a hook's address is shown once it is stored: the scheme and
// host, with the path, query and anything after elided, because the token of a
// chat service's incoming webhook sits in the path. To change the address, the
// whole of it is entered again.
func MaskURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Path == "" && u.RawQuery == "" && u.Fragment == "" {
		return u.Scheme + "://" + u.Host
	}
	return u.Scheme + "://" + u.Host + "/..."
}

// Wants reports whether the hook asked for an event.
func (h Hook) Wants(ev string) bool {
	if ev == EventPing {
		return true
	}
	for _, e := range h.Events {
		if e == ev {
			return true
		}
	}
	return false
}

// Delivery is one delivery and what came of it.
type Delivery struct {
	ID           string          `json:"id"`
	HookID       string          `json:"hook_id"`
	Event        string          `json:"event"`
	State        string          `json:"state"` // pending, delivered or failed
	Attempts     int             `json:"attempts"`
	Status       int             `json:"status"` // the last response's HTTP status, 0 if none
	DurationMS   int64           `json:"duration_ms"`
	Error        string          `json:"error,omitempty"`
	Response     string          `json:"response,omitempty"` // the start of the response body
	Test         bool            `json:"test,omitempty"`
	RedeliveryOf string          `json:"redelivery_of,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	NextAttempt  time.Time       `json:"next_attempt,omitzero"`
	Payload      json.RawMessage `json:"payload"`
}

// Delivery states.
const (
	Pending   = "pending"
	Delivered = "delivered"
	Failed    = "failed"
)

// Normalise sorts an event list, drops duplicates and refuses unknown names.
func Normalise(events []string) ([]string, string) {
	seen := map[string]bool{}
	var out []string
	for _, e := range events {
		e = strings.TrimSpace(e)
		ok := false
		for _, k := range Events {
			ok = ok || k == e
		}
		if !ok {
			return nil, "unknown event " + e + "; the events are " + strings.Join(Events, ", ")
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, "choose at least one event: " + strings.Join(Events, ", ")
	}
	sort.Strings(out)
	return out, ""
}

// CheckURL refuses an address a hook cannot use, with the reason. Whether the
// address may be reached (internal addresses) is judged by the egress guard
// when a delivery connects.
func CheckURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "the address must be a web address that starts with https:// (or http://)"
	}
	if u.User != nil {
		return "the address must not carry a user name or password; the secret signs each delivery instead"
	}
	if len(raw) > 2000 {
		return "the address is too long"
	}
	return ""
}

// Payload types: the JSON body of a delivery.

// Repo is the repository a payload is about.
type Repo struct {
	Path          string `json:"path"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Visibility    string `json:"visibility,omitempty"`
	CloneHTTPS    string `json:"clone_https,omitempty"`
	CloneSSH      string `json:"clone_ssh,omitempty"`
	// The names GitHub's push payload uses for the same addresses, so that
	// a receiver written for GitHub's format (Argo CD's /api/webhook, which
	// matches them to its applications' repoURL) reads this payload as it is.
	FullName string `json:"full_name,omitempty"`
	HTMLURL  string `json:"html_url,omitempty"`
	CloneURL string `json:"clone_url,omitempty"`
	SSHURL   string `json:"ssh_url,omitempty"`
}

// Sender is who did it.
type Sender struct {
	Name string `json:"name"`
}

// Payload is the body of a delivery. Fields that do not apply to an event are
// left out.
type Payload struct {
	Event      string    `json:"event"`
	Delivery   string    `json:"delivery"`
	At         time.Time `json:"at"`
	Repository Repo      `json:"repository"`
	Sender     Sender    `json:"sender"`
	// push, create and delete
	Ref     string `json:"ref,omitempty"`
	RefType string `json:"ref_type,omitempty"` // branch or tag (create, delete)
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
	Created bool   `json:"created,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
	// repository: created, renamed or deleted; Changes says what (renamed: the old path)
	Action  string            `json:"action,omitempty"`
	Changes map[string]string `json:"changes,omitempty"`
	// pull_request and pull_request_review: Action says what happened
	// (opened, synchronize, closed, merged, review_submitted, ...), Number and
	// PullRequest say which pull request; Changes carries what is useful (the
	// review's verdict, the merge method).
	Number      int            `json:"number,omitempty"`
	PullRequest map[string]any `json:"pull_request,omitempty"`
	// ping
	Zen string `json:"zen,omitempty"`
}
