package hosts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// CloneAuth is what a Station needs to check out the Flight's repository: the
// address, and the credential to send to that address only.
type CloneAuth struct {
	URL      string
	Username string
	Secret   string
	// Resolve pins git to the addresses the egress check saw
	// (http.curloptResolve entries); set by CheckedClone.
	Resolve []string
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
	if !f.ReportStatuses || s.via(p, f.connID, repo.Path) != ViaStatus {
		return false, nil // a result that goes to the comment is sent by ReportResult
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
	return s.upsertSummary(ctx, p, f, repo, number, body)
}

// upsertSummary posts or edits the one summary comment of a pull request.
func (s *Service) upsertSummary(ctx context.Context, p forge.Provider, f Flight, repo forge.Repository, number int, body string) (bool, error) {
	key, err := s.fl.OutsideSummaryComment(f.Project, number)
	if err != nil {
		return false, err
	}
	got, err := p.Releases().UpsertSummary(ctx, repo, number, key, body)
	if err != nil {
		return false, err
	}
	if err := s.fl.PutOutsideSummaryComment(f.Project, number, got); err != nil {
		return true, err
	}
	return true, nil
}

// IsAttached says whether a project keeps its code on an outside host.
func (s *Service) IsAttached(project string) bool {
	_, ok, _ := s.fl.OutsideFlightOf(project)
	return ok
}

// CreateTag makes the annotated tag of a release at a commit on the host.
func (s *Service) CreateTag(ctx context.Context, project, tag, commit, message string) (string, error) {
	p, _, repo, err := s.repoOf(ctx, project)
	if err != nil {
		return "", err
	}
	ref, err := p.Releases().CreateTag(ctx, repo, forge.TagSpec{Name: tag, Commit: commit, Message: message})
	return ref.Commit, err
}

// PublishRelease creates or updates the release of an existing tag on the host with its notes and files.
func (s *Service) PublishRelease(ctx context.Context, project string, rel forge.ReleaseSpec) (string, error) {
	p, _, repo, err := s.repoOf(ctx, project)
	if err != nil {
		return "", err
	}
	return p.Releases().PublishRelease(ctx, repo, rel)
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
	if !s.allowHook(name) {
		return Delivery{}, fmt.Errorf("%w: too many webhook calls for this connection; the cap is %d a minute", forge.ErrRateLimited, s.hookCap())
	}
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
	id := ""
	if e, ok := entryOf(c.Kind); ok && e.DeliveryHeader != "" {
		id = r.Header.Get(e.DeliveryHeader)
	}
	if id == "" && len(evs) > 0 {
		id = evs[0].DeliveryID // a host with no delivery header names a delivery by what the provider made of it
	}
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

// DefaultHookCap is how many webhook calls one connection accepts a minute,
// verified or not, before the rest are answered 429. A host sends a few a
// second at most; the cap stops a flood that would cost a signature check each.
const DefaultHookCap = 300

func (s *Service) hookCap() int {
	if s.HookCap > 0 {
		return s.HookCap
	}
	return DefaultHookCap
}

// allowHook counts a call in the connection's current minute.
func (s *Service) allowHook(name string) bool {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	now := s.now()
	win := now.Truncate(time.Minute)
	if s.hookWin == nil || !s.hookWin.Equal(win) {
		s.hookWin, s.hookN = &win, map[string]int{}
	}
	s.hookN[name]++
	return s.hookN[name] <= s.hookCap()
}

// CheckedClone is Clone for a server that reads the repository itself (the
// source poller and the mirror fetch): it also checks the clone address
// against the egress allow-list, so that a connection can only ever send its
// credential to the host it was added for. ok is false when the project is
// not attached.
func (s *Service) CheckedClone(ctx context.Context, project string) (CloneAuth, bool, error) {
	ca, err := s.Clone(ctx, project)
	if errors.Is(err, ErrNotAttached) {
		return CloneAuth{}, false, nil
	}
	if err != nil {
		return CloneAuth{}, false, err
	}
	pin, err := s.guard.Pin(ctx, ca.URL)
	if err != nil {
		return CloneAuth{}, false, err
	}
	ca.Resolve = pin
	return ca, true, nil
}

// PullRequest reads one pull request of a Flight's repository from its host;
// ok is false when the project is not attached.
func (s *Service) PullRequest(ctx context.Context, project string, number int) (forge.Proposal, bool, error) {
	p, _, repo, err := s.repoOf(ctx, project)
	if errors.Is(err, ErrNotAttached) {
		return forge.Proposal{}, false, nil
	}
	if err != nil {
		return forge.Proposal{}, false, err
	}
	pr, err := p.Proposals().Get(ctx, repo, number)
	return pr, err == nil, err
}

// QuietFlights lists the Flights whose connection has received no webhook
// delivery since cutoff: the ones the watcher must poll (a host that cannot
// deliver to this server, or a webhook that was never set up, still works).
func (s *Service) QuietFlights(ctx context.Context, cutoff time.Time) ([]Flight, error) {
	conns, err := s.st.Connections(ctx)
	if err != nil {
		return nil, err
	}
	var out []Flight
	for _, c := range conns {
		if !c.Enabled {
			continue
		}
		if at, ok, err := s.fl.OutsideLastDelivery(c.ID); err != nil {
			return nil, err
		} else if ok && time.UnixMilli(at).After(cutoff) {
			continue
		}
		rows, err := s.fl.OutsideFlightsOfConnection(c.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			f, err := s.flightOf(ctx, r)
			if err != nil {
				return nil, err
			}
			out = append(out, f)
		}
	}
	return out, nil
}

// PollEvents asks the host what changed on the pull requests of a Flight's
// repository since the cursor (the provider's Poll); it returns the events and
// the next cursor.
func (s *Service) PollEvents(ctx context.Context, project, cursor string) ([]forge.Event, string, error) {
	p, f, repo, err := s.repoOf(ctx, project)
	if err != nil {
		return nil, cursor, err
	}
	evs, next, err := p.Events().Poll(ctx, repo, cursor)
	for i := range evs {
		evs[i].Connection = f.connID
	}
	return evs, next, err
}

// LastDelivery is when a connection last received a webhook delivery.
func (s *Service) LastDelivery(ctx context.Context, name string) (time.Time, bool, error) {
	c, err := s.conn(ctx, name)
	if err != nil {
		return time.Time{}, false, err
	}
	at, ok, err := s.fl.OutsideLastDelivery(c.ID)
	return time.UnixMilli(at).UTC(), ok, err
}

// Releases is the write-back group of a Flight's connection, with the
// Flight's repository on it: what "Cut a release" writes through.
func (s *Service) Releases(ctx context.Context, project string) (forge.Releases, forge.Repository, error) {
	p, _, repo, err := s.repoOf(ctx, project)
	if err != nil {
		return nil, repo, err
	}
	return p.Releases(), repo, nil
}

// ProposalRefs is where the Flight's host publishes the head of each pull
// request as a ref; ok is false when the project is not attached.
func (s *Service) ProposalRefs(ctx context.Context, project string) (forge.RefLayout, bool) {
	f, err := s.Flight(ctx, project)
	if err != nil {
		return forge.RefLayout{}, false
	}
	c, err := s.conn(ctx, f.Connection)
	if err != nil {
		return forge.RefLayout{}, false
	}
	p, err := s.newProvider(c)
	if err != nil {
		return forge.RefLayout{}, false
	}
	return p.ProposalRefs(), true
}
