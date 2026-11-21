package pulls

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// State is where a pull request is in its life.
type State string

// The states of a pull request.
const (
	Open   State = "open"
	Merged State = "merged"
	Closed State = "closed"
)

// MergeState is the server's judgment of whether the branches can be merged.
type MergeState string

// The merge states. Checking means a judgment is being made (or the last one
// was made for another head or base); it is never a refusal.
const (
	MergeUnknown   MergeState = "unknown"
	MergeChecking  MergeState = "checking"
	MergeClean     MergeState = "clean"
	MergeConflicts MergeState = "conflicts"
)

// Mergeability is the judgment, with what it was made for.
type Mergeability struct {
	State MergeState `json:"state"`
	// Files are the conflicted files when State is conflicts.
	Files []string `json:"conflicting_files,omitempty"`
	// Behind is how many commits the target has that the pull request's head
	// lacks; 0 means the pull request is up to date with its target.
	Behind int `json:"behind"`
	// Ahead is how many commits the head has that the target lacks.
	Ahead     int       `json:"ahead"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// Reason says, for State unknown, why no judgment could be made.
	Reason string `json:"reason,omitempty"`
}

// PullRequest is a pull request.
type PullRequest struct {
	ID     string `json:"id"`
	RepoID string `json:"-"`
	// Repository is the repository's path (owner/name), filled by the API.
	Repository string `json:"repository,omitempty"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	// Body is the description, in Markdown, exactly as written; clients render
	// it with the product's sanitizer, never as raw HTML.
	Body   string `json:"body"`
	Author string `json:"author"`
	// SourceRepo is reserved for a pull request from a fork: the path of the
	// repository the source branch lives in. Empty: the same repository.
	SourceRepo   string   `json:"source_repository,omitempty"`
	SourceBranch string   `json:"source_branch"`
	TargetBranch string   `json:"target_branch"`
	State        State    `json:"state"`
	Draft        bool     `json:"draft"`
	Labels       []string `json:"labels"`
	Assignees    []string `json:"assignees"`
	Reviewers    []string `json:"requested_reviewers"`
	// HeadSHA and BaseSHA are the tips of the source and target as last seen;
	// MergeBase is where they diverge. After a merge they stay at what was
	// merged.
	HeadSHA      string       `json:"head_sha"`
	BaseSHA      string       `json:"base_sha"`
	MergeBase    string       `json:"merge_base,omitempty"`
	Mergeability Mergeability `json:"mergeability"`
	MergeCommit  string       `json:"merge_commit,omitempty"`
	MergeMethod  string       `json:"merge_method,omitempty"`
	MergedBy     string       `json:"merged_by,omitempty"`
	// Reverts is the number of the pull request this one reverts (0: none).
	Reverts int `json:"reverts,omitempty"`
	// AutoMerge is the method a pull request will be merged with once its
	// rules pass ("" when auto-merge is off).
	AutoMerge   string    `json:"auto_merge,omitempty"`
	AutoMergeBy string    `json:"auto_merge_by,omitempty"`
	LinkedRuns  []string  `json:"linked_runs"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	MergedAt    time.Time `json:"merged_at,omitzero"`
	ClosedAt    time.Time `json:"closed_at,omitzero"`
	ClosedBy    string    `json:"closed_by,omitempty"`
	// Revisions is how many times the head or base moved, the first being the
	// pull request's opening.
	Revisions int `json:"revisions"`
}

// Revision is the head and base at one update of a pull request.
type Revision struct {
	Seq     int    `json:"seq"`
	HeadSHA string `json:"head_sha"`
	BaseSHA string `json:"base_sha"`
	// Forced says the new head did not contain the old one: a force-push or a
	// rebase.
	Forced    bool      `json:"forced"`
	Actor     string    `json:"actor,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Errors the API turns into answers.
var (
	ErrNotFound = errors.New("no such pull request")
	ErrInvalid  = errors.New("invalid pull request")
	ErrConflict = errors.New("pull request conflict")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func conflict(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, a...))
}

// New is what opening a pull request needs.
type New struct {
	Title        string
	Body         string
	SourceBranch string
	TargetBranch string
	// SourceRepo is reserved for forks and refused while forks do not exist.
	SourceRepo string
	Draft      bool
	Labels     []string
	Assignees  []string
	Reviewers  []string
	Reverts    int
}

const (
	maxTitle = 256
	maxBody  = 256 * 1024
)

// Validate checks what a person typed.
func (n *New) Validate() error {
	n.Title = strings.TrimSpace(n.Title)
	switch {
	case n.Title == "":
		return invalid("a pull request needs a title")
	case len(n.Title) > maxTitle:
		return invalid("the title is %d characters, more than the %d allowed", len(n.Title), maxTitle)
	case len(n.Body) > maxBody:
		return invalid("the description is %d KiB, more than the %d KiB allowed", len(n.Body)/1024, maxBody/1024)
	case n.SourceBranch == "" || n.TargetBranch == "":
		return invalid("name the branch with the changes and the branch they go to")
	case n.SourceRepo != "":
		return invalid("pull requests from a fork are not available yet; push a branch to this repository")
	case n.SourceBranch == n.TargetBranch:
		return invalid("the branch %s is both the source and the target; choose two different branches", n.SourceBranch)
	}
	labels, err := cleanNames(n.Labels, "label")
	if err != nil {
		return err
	}
	n.Labels = labels
	return nil
}

func cleanNames(in []string, what string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		switch {
		case s == "":
			continue
		case len(s) > 64 || strings.ContainsAny(s, "\x00\n\r"):
			return nil, invalid("%q is not a valid %s", s, what)
		case seen[s]:
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) > 50 {
		return nil, invalid("at most 50 %ss", what)
	}
	return out, nil
}

// Filter narrows a list.
type Filter struct {
	State string // open, closed, merged or "" (all); "closed" here means closed or merged when All is set
	// Author, Assignee and Reviewer match one person.
	Author, Assignee, Reviewer string
	Label                      string
	Source, Target             string
	Query                      string // matches the title
	Limit, Offset              int
}
