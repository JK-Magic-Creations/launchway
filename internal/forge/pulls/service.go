package pulls

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// Event is something that happened to a pull request, for webhooks, the audit
// trail and notifications (which are left as events: whoever listens decides).
type Event struct {
	// Action is opened, edited, synchronize (new commits), closed, reopened,
	// merged, ready_for_review, converted_to_draft, review_requested,
	// review_submitted, review_dismissed, commented, auto_merge_enabled or
	// auto_merge_disabled.
	Action string
	RepoID string
	Pull   PullRequest
	Actor  string
	// Detail carries what is useful to a receiver (the review's verdict, the
	// old head, the merge method).
	Detail map[string]string
}

// Service is the behaviour of pull requests.
type Service struct {
	Store *Store
	Git   *storage.Store
	// OnEvent is told of every change (nil: nobody).
	OnEvent func(ctx context.Context, e Event)
	Log     *slog.Logger
	// Clock gives the time of commits made by the server (default time.Now).
	Clock func() time.Time

	sem  chan struct{}
	wg   sync.WaitGroup
	mu   sync.Mutex
	jobs map[string]*job
	// background is cancelled by Close.
	background context.Context
	cancel     context.CancelFunc
}

type job struct{ again bool }

// NewService makes the service. Close stops its background work.
func NewService(store *Store, git *storage.Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{Store: store, Git: git, Log: log, sem: make(chan struct{}, 4), jobs: map[string]*job{}, background: ctx, cancel: cancel}
}

// Close stops the background judging and waits for it.
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

// Settle waits until no judgement of mergeability is running or waiting. A
// request never calls it; tests and shutdown do.
func (s *Service) Settle() { s.wg.Wait() }

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Service) emit(ctx context.Context, action, repoID string, p PullRequest, actor string, detail map[string]string) {
	if s.OnEvent != nil {
		s.OnEvent(ctx, Event{Action: action, RepoID: repoID, Pull: p, Actor: actor, Detail: detail})
	}
}

func (s *Service) runner(dir string, scratch bool) gitRunner {
	return gitRunner{dir: dir, limits: s.Git.Limits(), scratch: scratch}
}

// read runs fn with git in the server's cache of the repository.
func (s *Service) read(ctx context.Context, repoID string, fn func(g gitRunner) error) error {
	return s.Git.WithCache(ctx, repoID, func(dir string) error { return fn(s.runner(dir, false)) })
}

func branchRef(b string) string { return "refs/heads/" + b }

// PullRef is the ref that keeps a pull request's head: it can be fetched with
// git fetch origin refs/pull/N/head, and it keeps the commits alive when the
// branch is deleted or force-pushed.
func PullRef(number int) string { return fmt.Sprintf("refs/pull/%d/head", number) }

// ReservedRef reports whether a ref is the server's own: nobody pushes to it.
func ReservedRef(name string) bool { return strings.HasPrefix(name, "refs/pull/") }

// setRefs moves refs of the repository to existing commits through the
// ledger, retrying when another change moved one of them meanwhile.
func (s *Service) setRefs(ctx context.Context, repoID string, want map[string]string) error {
	var err error
	for range 4 {
		_, err = s.Git.Mutate(ctx, repoID, func(_ context.Context, refs map[string]string, _ string) ([]storage.RefUpdate, error) {
			var ups []storage.RefUpdate
			for n, sha := range want {
				ups = append(ups, storage.RefUpdate{Name: n, Old: refs[n], New: sha})
			}
			return ups, nil
		})
		var stale *storage.ErrStale
		if !errors.As(err, &stale) {
			return err
		}
	}
	return err
}

// Open opens a pull request from one branch of the repository to another.
func (s *Service) Open(ctx context.Context, repoID, author string, n New) (PullRequest, error) {
	if err := n.Validate(); err != nil {
		return PullRequest{}, err
	}
	refs, err := s.Git.Refs(ctx, repoID)
	if err != nil {
		return PullRequest{}, err
	}
	head, base := refs[branchRef(n.SourceBranch)], refs[branchRef(n.TargetBranch)]
	switch {
	case head == "":
		return PullRequest{}, invalid("the branch %s does not exist; push it first", n.SourceBranch)
	case base == "":
		return PullRequest{}, invalid("the branch %s does not exist; choose a branch of this repository to merge into", n.TargetBranch)
	}
	var mb string
	var contained bool
	if err := s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		if contained, err = g.isAncestor(ctx, head, base); err != nil {
			return err
		}
		mb, err = g.mergeBase(ctx, base, head)
		return err
	}); err != nil {
		return PullRequest{}, err
	}
	if contained {
		return PullRequest{}, invalid("%s has no commits that %s lacks; there is nothing to merge", n.SourceBranch, n.TargetBranch)
	}
	if dup, _ := s.Store.OpenFromSource(ctx, repoID, n.SourceBranch); len(dup) > 0 {
		for _, d := range dup {
			if d.TargetBranch == n.TargetBranch && d.SourceRepo == "" {
				return PullRequest{}, conflict("pull request #%d already goes from %s to %s", d.Number, n.SourceBranch, n.TargetBranch)
			}
		}
	}
	p, err := s.Store.Create(ctx, repoID, author, n, head, base, mb)
	if err != nil {
		return PullRequest{}, err
	}
	if err := s.setRefs(ctx, repoID, map[string]string{PullRef(p.Number): head, RevisionRef(p.Number, 1): head}); err != nil {
		return p, fmt.Errorf("keeping the head of pull request #%d: %w", p.Number, err)
	}
	s.schedule(repoID, p.ID)
	s.emit(ctx, "opened", repoID, p, author, nil)
	return p, nil
}

// RefUpdate is one ref a push moved.
type RefUpdate struct{ Name, Old, New string }

func zero(sha string) bool { return strings.Trim(sha, "0") == "" }

// Pushed is called after refs of the repository moved (a push, a merge, a
// mirror): every open pull request whose source or target moved is brought up
// to date: its head and base are recorded as a new revision, its pull ref is
// moved, a merge by other means is noticed, and the judgement of
// mergeability is redone in the background.
func (s *Service) Pushed(ctx context.Context, repoID, actor string, ups []RefUpdate) {
	for _, u := range ups {
		br, ok := strings.CutPrefix(u.Name, "refs/heads/")
		if !ok || zero(u.New) {
			continue
		}
		if srcs, err := s.Store.OpenFromSource(ctx, repoID, br); err == nil {
			for _, p := range srcs {
				if p.SourceRepo == "" {
					s.headMoved(ctx, repoID, p, u.New, actor)
				}
			}
		}
		if tgts, err := s.Store.OpenToTarget(ctx, repoID, br); err == nil {
			for _, p := range tgts {
				s.baseMoved(ctx, repoID, p, u.New, actor)
			}
		}
	}
}

func (s *Service) headMoved(ctx context.Context, repoID string, p PullRequest, head, actor string) {
	if p.HeadSHA == head {
		return
	}
	var forced bool
	var mb string
	err := s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		if forced, err = g.isAncestor(ctx, p.HeadSHA, head); err != nil {
			return err
		}
		forced = !forced
		mb, err = g.mergeBase(ctx, p.BaseSHA, head)
		return err
	})
	if err != nil {
		s.Log.Warn("a pull request's head moved and could not be compared", "pull", p.Number, "err", err)
		forced = true
	}
	if err := s.Store.MoveTips(ctx, p.ID, head, p.BaseSHA, mb); err != nil {
		s.Log.Warn("recording a pull request's new head", "pull", p.Number, "err", err)
		return
	}
	if err := s.Store.AddRevision(ctx, p.ID, head, p.BaseSHA, forced, actor); err != nil {
		s.Log.Warn("recording a pull request's revision", "pull", p.Number, "err", err)
	}
	seq := 0
	if revs, err := s.Store.Revisions(ctx, p.ID); err == nil && len(revs) > 0 {
		seq = revs[len(revs)-1].Seq
	}
	if err := s.setRefs(ctx, repoID, map[string]string{PullRef(p.Number): head, RevisionRef(p.Number, seq): head}); err != nil {
		s.Log.Warn("moving a pull request's head ref", "pull", p.Number, "err", err)
	}
	s.afterNewCommits(ctx, repoID, p, head, actor)
	s.schedule(repoID, p.ID)
	np, _ := s.Store.ByID(ctx, p.ID)
	s.emit(ctx, "synchronize", repoID, np, actor, map[string]string{"before": p.HeadSHA, "after": head, "forced": fmt.Sprint(forced)})
}

func (s *Service) baseMoved(ctx context.Context, repoID string, p PullRequest, base, actor string) {
	if p.BaseSHA == base {
		return
	}
	var contained bool
	var mb string
	err := s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		if contained, err = g.isAncestor(ctx, p.HeadSHA, base); err != nil {
			return err
		}
		mb, err = g.mergeBase(ctx, base, p.HeadSHA)
		return err
	})
	if err != nil {
		s.Log.Warn("a pull request's target moved and could not be compared", "pull", p.Number, "err", err)
	}
	if err := s.Store.MoveTips(ctx, p.ID, p.HeadSHA, base, mb); err != nil {
		s.Log.Warn("recording a pull request's new base", "pull", p.Number, "err", err)
		return
	}
	if contained {
		// Everything in the pull request is now in its target: it was merged by hand.
		if err := s.Store.SetMerged(ctx, p.ID, base, "manual", actor); err == nil {
			np, _ := s.Store.ByID(ctx, p.ID)
			s.emit(ctx, "merged", repoID, np, actor, map[string]string{"method": "manual", "commit": base})
		}
		return
	}
	if err := s.Store.AddRevision(ctx, p.ID, p.HeadSHA, base, false, actor); err != nil {
		s.Log.Warn("recording a pull request's revision", "pull", p.Number, "err", err)
	}
	s.schedule(repoID, p.ID)
}

// afterNewCommits is where reviews react to new commits (see review.go).
func (s *Service) afterNewCommits(ctx context.Context, repoID string, p PullRequest, head, actor string) {
	s.reanchorComments(ctx, repoID, p, head)
	s.dismissStale(ctx, repoID, p, head)
}

// schedule asks for the judgement of a pull request to be (re)made in the
// background: at most four run at once, and a request for one already
// running makes it run once more when it finishes.
func (s *Service) schedule(repoID, id string) {
	s.mu.Lock()
	if j, ok := s.jobs[id]; ok {
		j.again = true
		s.mu.Unlock()
		return
	}
	s.jobs[id] = &job{}
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case s.sem <- struct{}{}:
			case <-s.background.Done():
				s.finish(id, true)
				return
			}
			ctx, cancel := context.WithTimeout(s.background, 2*time.Minute)
			s.judge(ctx, repoID, id)
			cancel()
			<-s.sem
			if s.finish(id, false) {
				return
			}
		}
	}()
}

// finish ends a job unless it was asked to run again.
func (s *Service) finish(id string, force bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j != nil && j.again && !force {
		j.again = false
		return false
	}
	delete(s.jobs, id)
	return true
}

// Judge makes the judgement of a pull request now, for callers that must
// have it (a merge about to be made). Requests use schedule instead.
func (s *Service) Judge(ctx context.Context, repoID string, p PullRequest) (Mergeability, error) {
	return s.judgement(ctx, repoID, p)
}

func (s *Service) judge(ctx context.Context, repoID, id string) {
	p, err := s.Store.ByID(ctx, id)
	if err != nil || p.State != Open {
		return
	}
	m, err := s.judgement(ctx, repoID, p)
	if err != nil {
		m = Mergeability{State: MergeUnknown, Reason: err.Error()}
		s.Log.Warn("judging whether a pull request can merge", "pull", p.Number, "err", err)
	}
	if err := s.Store.SetMergeability(ctx, p.ID, p.HeadSHA, p.BaseSHA, m); err != nil {
		s.Log.Warn("storing the judgement of a pull request", "pull", p.Number, "err", err)
		return
	}
	s.autoMerge(ctx, repoID, p.ID)
}

// judgement asks git whether the head can be merged into the base.
func (s *Service) judgement(ctx context.Context, repoID string, p PullRequest) (Mergeability, error) {
	m := Mergeability{State: MergeUnknown}
	err := s.Git.Scratch(ctx, repoID, func(ctx context.Context, _ map[string]string, scratch string) error {
		g := s.runner(scratch, true)
		var err error
		if m.Behind, m.Ahead, err = g.counts(ctx, p.BaseSHA, p.HeadSHA); err != nil {
			return err
		}
		_, files, clean, err := g.trial(ctx, p.BaseSHA, p.HeadSHA)
		if err != nil {
			return err
		}
		m.State = MergeClean
		if !clean {
			m.State, m.Files = MergeConflicts, files
		}
		return nil
	})
	m.CheckedAt = s.now().UTC()
	return m, err
}

// RevisionRef is the ref that keeps the head a pull request had at one of its
// revisions, so "changes since your last review" survives a force-push.
func RevisionRef(number, seq int) string { return fmt.Sprintf("refs/pull/%d/rev/%d", number, seq) }
