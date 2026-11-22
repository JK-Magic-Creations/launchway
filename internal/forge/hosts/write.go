package hosts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// CloneAuth is what a Station needs to check out the Flight's repository: the
// address, and the credential to send to that address only.
type CloneAuth struct {
	URL      string
	Username string
	Secret   string
}

// Clone derives the checkout credential of a Flight from its connection (a
// read credential: the connection's token, delivered to the run and never
// stored with it). ErrNotFound when the project is not attached.
func (s *Service) Clone(ctx context.Context, project string) (CloneAuth, error) {
	p, _, repo, err := s.repoOf(ctx, project)
	if err != nil {
		return CloneAuth{}, err
	}
	cred, err := p.Access().Credential(ctx, repo, false)
	if err != nil {
		return CloneAuth{}, err
	}
	return CloneAuth{URL: repo.Clone.HTTPS, Username: cred.Username, Secret: cred.Secret}, nil
}

// ReportRun publishes a commit status for a run or a required step, when the
// Flight's statuses are switched on. It returns false (and does nothing) when
// the project is not attached or has statuses off.
func (s *Service) ReportRun(ctx context.Context, project string, r forge.Result) (bool, error) {
	p, f, repo, err := s.repoOf(ctx, project)
	if errors.Is(err, ErrNotAttached) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !f.ReportStatuses {
		return false, nil
	}
	return true, p.Results().Publish(ctx, repo, r)
}

// ReportSummary posts the one summary comment of a pull request, or updates
// it in place, when the Flight's comment switch is on.
func (s *Service) ReportSummary(ctx context.Context, project string, number int, body string) (bool, error) {
	p, f, repo, err := s.repoOf(ctx, project)
	if errors.Is(err, ErrNotAttached) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !f.ReportComment {
		return false, nil
	}
	key, err := s.fl.OutsideSummaryComment(project, number)
	if err != nil {
		return false, err
	}
	got, err := p.Releases().UpsertSummary(ctx, repo, number, key, body)
	if err != nil {
		return false, err
	}
	if err := s.fl.PutOutsideSummaryComment(project, number, got); err != nil {
		return true, err
	}
	return true, nil
}

// CutRelease creates the annotated tag at a commit on the host and the
// release for it, with its notes and files: "Cut a release" for a Flight whose
// repository is outside. It returns the release's address on the host.
func (s *Service) CutRelease(ctx context.Context, project, commit string, rel forge.ReleaseSpec, tagMessage string) (string, error) {
	p, _, repo, err := s.repoOf(ctx, project)
	if err != nil {
		return "", err
	}
	if _, err := p.Releases().CreateTag(ctx, repo, forge.TagSpec{Name: rel.Tag, Commit: commit, Message: tagMessage}); err != nil {
		return "", err
	}
	return p.Releases().PublishRelease(ctx, repo, rel)
}

// Writes is how a file change reaches the repository.
type Writes struct {
	// Branch is the branch to commit to, or to cut the pull request's branch from.
	Branch string
	// PullRequest opens a pull request from a new branch instead of committing to Branch.
	PullRequest bool
	NewBranch   string
	Path        string
	Content     []byte
	Message     string
	Title       string
	Body        string
}

// WriteResult says what was done.
type WriteResult struct {
	Commit      string `json:"commit"`
	PullRequest int    `json:"pull_request,omitempty"`
	Branch      string `json:"branch"`
}

// ErrProtected is wrapped when a direct write is refused because the branch
// is protected on the host.
var ErrProtected = errors.New("the branch is protected on the host")

// WriteFile commits a file to a branch or opens a pull request with it. A
// direct commit to a branch the host protects is refused with a message that
// names the pull-request mode, before anything is sent.
func (s *Service) WriteFile(ctx context.Context, project string, w Writes) (WriteResult, error) {
	p, _, repo, err := s.repoOf(ctx, project)
	if err != nil {
		return WriteResult{}, err
	}
	if !w.PullRequest {
		rules, err := p.Rules().List(ctx, repo)
		if err == nil && w.Branch == repo.DefaultBranch && protectedBranch(rules, w.Branch) {
			return WriteResult{}, fmt.Errorf("%w: %s on %s needs a pull request (required reviews or checks); use the pull_request mode, which opens one", ErrProtected, w.Branch, repo.Path)
		}
	}
	ch := forge.FileChange{Branch: w.Branch, Path: w.Path, Content: w.Content, Message: w.Message}
	if w.PullRequest {
		ch.NewBranch = w.NewBranch
		if ch.NewBranch == "" {
			return WriteResult{}, fmt.Errorf("%w: a pull request needs a branch name", forge.ErrInvalid)
		}
	}
	sha, err := p.Releases().CommitFile(ctx, repo, ch)
	if err != nil {
		return WriteResult{}, err
	}
	out := WriteResult{Commit: sha, Branch: w.Branch}
	if w.PullRequest {
		pr, err := p.Proposals().Create(ctx, repo, forge.NewProposal{Title: w.Title, Body: w.Body,
			Source: forge.Ref{Name: "refs/heads/" + ch.NewBranch}, Target: forge.Ref{Name: "refs/heads/" + w.Branch}})
		if err != nil {
			return out, err
		}
		out.PullRequest, out.Branch = pr.Number, ch.NewBranch
	}
	return out, nil
}

func protectedBranch(rs []forge.RefRule, branch string) bool {
	for _, r := range rs {
		if r.Pattern == "refs/heads/"+branch && (r.ProposalsOnly || len(r.RequiredResults) > 0) {
			return true
		}
	}
	return false
}

// Delivery is what a webhook turned out to be.
type Delivery struct {
	// Fresh is false for a delivery seen before; it changes nothing.
	Fresh   bool
	Events  []forge.Event
	Flights []Flight
}

// ErrNoSecret is returned when the connection has no webhook secret yet.
var ErrNoSecret = errors.New("this connection has no webhook secret")

// Webhook verifies a delivery with the connection's secret (constant-time),
// drops a replay by its delivery ID, and finds the Flights the events are
// for. Nothing in the payload is trusted beyond what the signature covers.
func (s *Service) Webhook(ctx context.Context, name string, r *http.Request) (Delivery, error) {
	p, c, err := s.Provider(ctx, name)
	if err != nil {
		return Delivery{}, err
	}
	if c.WebhookSecret.IsZero() {
		return Delivery{}, ErrNoSecret
	}
	secret, err := c.WebhookSecret.Resolve()
	if err != nil {
		return Delivery{}, err
	}
	evs, err := p.Events().Verify(r, []byte(secret))
	if err != nil {
		return Delivery{}, err
	}
	d := Delivery{Fresh: true, Events: evs}
	id := r.Header.Get("X-GitHub-Delivery")
	if id != "" {
		fresh, err := s.st.SeeDelivery(ctx, c.ID, id)
		if err != nil {
			return Delivery{}, err
		}
		if !fresh {
			return Delivery{Fresh: false}, nil
		}
	}
	seen := map[string]bool{}
	for i := range d.Events {
		d.Events[i].Connection = c.ID
		fl, err := s.flightsOf(ctx, c.ID, d.Events[i].Repository)
		if err != nil {
			return d, err
		}
		for _, f := range fl {
			if !seen[f.Project] {
				seen[f.Project] = true
				d.Flights = append(d.Flights, f)
			}
		}
	}
	return d, nil
}

// PullRequestURL is a convenience for messages.
func PullRequestURL(base, repo string, n int) string {
	u, err := url.Parse(base)
	if err != nil || base == "" {
		return "https://github.com/" + repo + "/pull/" + strconv.Itoa(n)
	}
	return strings.TrimSuffix(u.String(), "/") + "/" + repo + "/pull/" + strconv.Itoa(n)
}
