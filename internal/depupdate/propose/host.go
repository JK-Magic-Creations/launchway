package propose

import (
	"context"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate/plan"
)

// PR is a pull request of the updater as the host knows it.
type PR struct {
	Number int
	Branch string
	Title  string
	Body   string
	URL    string
	Merged bool
	// Reason is, for a closed one, what the person who closed it said.
	Reason string
}

// NewPR is what is opened.
type NewPR struct {
	Title, Body, Branch, Base    string
	Labels, Reviewers, Assignees []string
}

// Host is the pull-request side of a repository: the built-in hosting, or any
// connected forge that supports proposals.
type Host interface {
	DefaultBranch(ctx context.Context) (string, error)
	// Open lists the open pull requests whose branch starts with prefix.
	Open(ctx context.Context, prefix string) ([]PR, error)
	// Closed lists the closed, unmerged ones a person closed (most recent first, at most 200).
	Closed(ctx context.Context, prefix string) ([]PR, error)
	Create(ctx context.Context, n NewPR) (PR, error)
	Update(ctx context.Context, number int, title, body string) error
	Close(ctx context.Context, number int, comment string) error
}

// UpdaterClosing starts the comment the updater leaves when it closes its own
// pull request, so that closing is never read as a person asking to ignore.
const UpdaterClosing = "Closed by Launchway's dependency updater"

// IgnoreKeys turns what people closed into the keys the resolver honors:
// "this version" (the default), "this major", "this dependency".
func IgnoreKeys(closed []PR, ecosystemOf func(branchKey string) string) map[string]bool {
	out := map[string]bool{}
	for _, c := range closed {
		_, targets, ok := ParseMarker(c.Body)
		if !ok || c.Merged {
			continue
		}
		key, _, _ := ParseMarker(c.Body)
		eco := ecosystemOf(key)
		r := strings.ToLower(c.Reason)
		for dep, target := range targets {
			switch {
			case strings.Contains(r, "ignore this dependency"):
				out[eco+"|"+dep+"|*"] = true
			case strings.Contains(r, "ignore this major"):
				out[eco+"|"+dep+"|major-"+majorOfTarget(target)] = true
			default:
				out[eco+"|"+dep+"|"+target] = true
			}
		}
	}
	return out
}

func majorOfTarget(v string) string {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, ".-"); i > 0 {
		v = v[:i]
	}
	return v
}

// OpenFrom reads the open pull requests' keys and targets back from their
// bodies, for the planner. A pull request without the marker is not the
// updater's and is left out.
func OpenFrom(prs []PR) (open []OpenPR) {
	for _, p := range prs {
		if key, targets, ok := ParseMarker(p.Body); ok {
			open = append(open, OpenPR{Key: key, Number: p.Number, Targets: targets})
		}
	}
	return open
}

// OpenPR is what the planner needs of an open pull request.
type OpenPR = plan.Open
