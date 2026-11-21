package pulls

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// The merge queue. A protected branch whose rule asks for it takes pull
// requests only through the queue: a pull request that is ready is queued, and
// the queue handles one at a time, in order. For the entry at the front it
// brings the pull request's branch up to date with the target (a merge commit
// of the target into it, which is a push like any other, so the branch's real
// checks run on exactly what will land), waits for the required checks of that
// head, and then merges it. A pull request that fails, conflicts or changes
// while it waits is taken out, with the reason, and the next one goes on.
//
// Not built yet: testing several entries at once on top of each other and
// splitting the batch when one fails.

// Queue entry states.
const (
	QueueWaiting = "waiting"
	QueueTesting = "testing"
	QueueRemoved = "removed"
)

// QueueEntry is a pull request in a merge queue.
type QueueEntry struct {
	PullNumber int    `json:"pull_number"`
	PullID     string `json:"-"`
	Title      string `json:"title,omitempty"`
	Branch     string `json:"branch"`
	// Position is 1 for the entry at the front; 0 for one that was removed.
	Position int    `json:"position"`
	State    string `json:"state"`
	Method   string `json:"method"`
	QueuedBy string `json:"queued_by"`
	Priority int    `json:"priority"`
	// HeadSHA is the pull request's head when it was queued; TestedSHA the head
	// the queue brought it to (it contains the target's tip then).
	HeadSHA   string    `json:"head_sha"`
	TestedSHA string    `json:"tested_sha,omitempty"`
	Runs      []string  `json:"runs"`
	Reason    string    `json:"reason,omitempty"`
	QueuedAt  time.Time `json:"queued_at"`
	UpdatedAt time.Time `json:"updated_at"`
	seq       int64
}

const queueCols = `q.seq, q.pull_id, pr.number, pr.title, q.branch, q.state, q.method, q.queued_by, q.priority, q.head_sha, q.tested_sha, q.run_ids, q.reason, q.queued_at, q.updated_at`

func scanQueue(sc interface{ Scan(...any) error }) (QueueEntry, error) {
	var e QueueEntry
	var runs string
	var qa, ua int64
	if err := sc.Scan(&e.seq, &e.PullID, &e.PullNumber, &e.Title, &e.Branch, &e.State, &e.Method, &e.QueuedBy, &e.Priority, &e.HeadSHA, &e.TestedSHA, &runs, &e.Reason, &qa, &ua); err != nil {
		return e, err
	}
	_ = json.Unmarshal([]byte(runs), &e.Runs)
	if e.Runs == nil {
		e.Runs = []string{}
	}
	e.QueuedAt, e.UpdatedAt = fromMS(qa), fromMS(ua)
	return e, nil
}

// Queue lists a branch's queue: those waiting and being tested, in the order
// they will be taken (priority first, then who queued first), with positions.
func (s *Store) Queue(ctx context.Context, repoID, branch string) ([]QueueEntry, error) {
	rows, err := s.db.Query(ctx, `SELECT `+queueCols+` FROM pull_queue q JOIN pull_requests pr ON pr.id = q.pull_id
		WHERE q.repo_id = ? AND q.branch = ? AND q.state <> 'removed' ORDER BY q.priority DESC, q.seq`, repoID, branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueueEntry{}
	for rows.Next() {
		e, err := scanQueue(rows)
		if err != nil {
			return nil, err
		}
		e.Position = len(out) + 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// QueueEntryOf finds a pull request's entry (a removed one too, with its
// reason); nil when it was never queued.
func (s *Store) QueueEntryOf(ctx context.Context, pullID string) (*QueueEntry, error) {
	e, err := scanQueue(s.db.QueryRow(ctx, `SELECT `+queueCols+` FROM pull_queue q JOIN pull_requests pr ON pr.id = q.pull_id WHERE q.pull_id = ?`, pullID))
	if err != nil {
		return nil, nil //nolint:nilerr // no row: not queued
	}
	if e.State != QueueRemoved {
		e.Position, _ = s.queueOf(ctx, pullID)
	}
	return &e, nil
}

// queueOf is a pull request's place in its branch's queue (0: not in it).
func (s *Store) queueOf(ctx context.Context, pullID string) (int, error) {
	var repoID, branch string
	if err := s.db.QueryRow(ctx, `SELECT repo_id, branch FROM pull_queue WHERE pull_id = ? AND state <> 'removed'`, pullID).Scan(&repoID, &branch); err != nil {
		return 0, err
	}
	q, err := s.Queue(ctx, repoID, branch)
	if err != nil {
		return 0, err
	}
	for _, e := range q {
		if e.PullID == pullID {
			return e.Position, nil
		}
	}
	return 0, nil
}

// QueuedBranches lists every repository and branch that has entries to handle.
func (s *Store) QueuedBranches(ctx context.Context) ([][2]string, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT repo_id, branch FROM pull_queue WHERE state <> 'removed'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var r [2]string
		if err := rows.Scan(&r[0], &r[1]); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) setQueue(ctx context.Context, pullID, state, tested, reason string) error {
	_, err := s.db.Exec(ctx, `UPDATE pull_queue SET state = ?, tested_sha = ?, reason = ?, updated_at = ? WHERE pull_id = ?`, state, tested, reason, ms(s.now()), pullID)
	return err
}

// queueRuns records the runs that tested a queued head.
func (s *Store) queueRuns(ctx context.Context, pullID string, runs []string) error {
	b, _ := json.Marshal(runs)
	_, err := s.db.Exec(ctx, `UPDATE pull_queue SET run_ids = ?, updated_at = ? WHERE pull_id = ?`, string(b), ms(s.now()), pullID)
	return err
}

func (s *Store) enqueue(ctx context.Context, repoID, branch string, p PullRequest, method, by string, priority int) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM pull_queue WHERE pull_id = ?`, p.ID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO pull_queue(pull_id, repo_id, branch, method, queued_by, queued_at, priority, state, head_sha, tested_sha, run_ids, reason, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, repoID, branch, method, by, ms(s.now()), priority, QueueWaiting, p.HeadSHA, "", "[]", "", ms(s.now()))
	return err
}

// hardBlockers are the reasons a pull request may not enter the queue or stay
// in it: everything but the checks that have not finished (the queue waits for
// those) and being behind the target (the queue brings the branch up to date).
// A required check that failed or was canceled is hard.
func hardBlockers(r Readiness) []string {
	var out []string
	for _, b := range r.Blockers {
		switch b.Code {
		case "behind", "checking":
			continue
		case "check":
			continue // judged below, by the check's own status
		}
		out = append(out, b.Why)
	}
	for _, c := range r.Checks {
		if c.Required && (c.Status == CheckFailed || c.Status == CheckCanceled) {
			out = append(out, "`"+c.Name+"` "+map[string]string{CheckFailed: "failed", CheckCanceled: "was canceled"}[c.Status])
		}
	}
	return out
}

// Enqueue puts a ready pull request in the queue of its target branch.
// priority orders the queue before the order of arrival; the caller decides who
// may give one.
func (s *Service) Enqueue(ctx context.Context, repoID, repoPath string, p PullRequest, method, by, role string, priority int) (QueueEntry, error) {
	p, err := s.Store.ByID(ctx, p.ID)
	if err != nil {
		return QueueEntry{}, err
	}
	rule, covered := RuleFor(mustRules(ctx, s.Store, repoID), p.TargetBranch)
	switch {
	case p.State != Open:
		return QueueEntry{}, conflict("this pull request is %s", p.State)
	case !covered || !rule.MergeQueue:
		return QueueEntry{}, invalid("%s has no merge queue; merge the pull request directly", p.TargetBranch)
	}
	set, err := s.Store.Settings(ctx, repoID)
	if err != nil {
		return QueueEntry{}, err
	}
	if method == "" {
		method = set.DefaultMethod
	}
	if !set.allows(method) {
		return QueueEntry{}, invalid("this repository does not allow merging by %s; it allows %s", method, strings.Join(set.Allowed(), ", "))
	}
	ready, err := s.Readiness(ctx, repoID, repoPath, p, role)
	if err != nil {
		return QueueEntry{}, err
	}
	if hard := hardBlockers(ready); len(hard) > 0 {
		return QueueEntry{}, &NotReadyError{Readiness: Readiness{Why: strings.Join(hard, "; "), Blockers: ready.Blockers}}
	}
	if err := s.Store.enqueue(ctx, repoID, p.TargetBranch, p, method, by, priority); err != nil {
		return QueueEntry{}, err
	}
	e, _ := s.Store.QueueEntryOf(ctx, p.ID)
	s.emit(ctx, "queued", repoID, p, by, map[string]string{"method": method, "position": fmt.Sprint(e.Position)})
	s.AdvanceQueue(ctx, repoID, p.TargetBranch)
	e, _ = s.Store.QueueEntryOf(ctx, p.ID)
	if e == nil { // it was at the front, everything was ready, and it has merged already
		return QueueEntry{PullNumber: p.Number, Branch: p.TargetBranch, State: "merged", Method: method, QueuedBy: by}, nil
	}
	return *e, nil
}

// Dequeue takes a pull request out of the queue because a person asked.
func (s *Service) Dequeue(ctx context.Context, repoID string, p PullRequest, by string) error {
	e, _ := s.Store.QueueEntryOf(ctx, p.ID)
	if e == nil || e.State == QueueRemoved {
		return fmt.Errorf("%w: this pull request is not in a merge queue", ErrNotFound)
	}
	s.removeFromQueue(ctx, repoID, p, "taken out of the queue by "+by, by, false)
	s.AdvanceQueue(ctx, repoID, p.TargetBranch)
	return nil
}

// removeFromQueue ends an entry, with the reason, and says so (a failure
// notifies the person who queued it; a person's own request does not).
func (s *Service) removeFromQueue(ctx context.Context, repoID string, p PullRequest, reason, actor string, failure bool) {
	if err := s.Store.setQueue(ctx, p.ID, QueueRemoved, "", reason); err != nil {
		return
	}
	action := "dequeued"
	if failure {
		action = "queue_failed"
	}
	s.emit(ctx, action, repoID, p, actor, map[string]string{"reason": reason})
}

// queueLocks serializes the handling of one branch's queue in this server.
func (s *Service) queueLock(key string) func() {
	s.mu.Lock()
	if s.qlocks == nil {
		s.qlocks = map[string]chan struct{}{}
	}
	ch, ok := s.qlocks[key]
	if !ok {
		ch = make(chan struct{}, 1)
		s.qlocks[key] = ch
	}
	s.mu.Unlock()
	ch <- struct{}{}
	return func() { <-ch }
}

// SweepQueue handles every queue that has entries.
func (s *Service) SweepQueue(ctx context.Context) {
	bs, err := s.Store.QueuedBranches(ctx)
	if err != nil {
		return
	}
	for _, b := range bs {
		s.AdvanceQueue(ctx, b[0], b[1])
	}
}

// AdvanceQueue does what can be done for the front of a branch's queue, then
// goes on to the next while entries merge or are removed. It never waits for a
// run: an entry whose checks are still running is looked at again by the next
// sweep or by whatever changes it (a run's end, a push, a review).
func (s *Service) AdvanceQueue(ctx context.Context, repoID, branch string) {
	defer s.queueLock(repoID + "\x00" + branch)()
	path := ""
	if s.Paths != nil {
		path, _ = s.Paths(ctx, repoID)
	}
	for range 50 {
		q, err := s.Store.Queue(ctx, repoID, branch)
		if err != nil || len(q) == 0 {
			return
		}
		// Whatever changed under an entry that waits is noticed now, not when its turn comes.
		changed := false
		for _, e := range q {
			if p, err := s.Store.ByID(ctx, e.PullID); err == nil {
				if why := staleReason(p, e, branch); why != "" {
					s.removeFromQueue(ctx, repoID, p, why, "merge queue", true)
					changed = true
				}
			}
		}
		if changed {
			continue
		}
		if !s.advanceFront(ctx, repoID, path, branch, q[0]) {
			return
		}
	}
}

// advanceFront handles the entry at the front; true means it is finished (merged or removed) and the next should be looked at.
func (s *Service) advanceFront(ctx context.Context, repoID, path, branch string, e QueueEntry) bool {
	p, err := s.Store.ByID(ctx, e.PullID)
	if err != nil {
		return false
	}
	fail := func(reason string) bool {
		s.removeFromQueue(ctx, repoID, p, reason, "merge queue", true)
		return true
	}
	rule, covered := RuleFor(mustRules(ctx, s.Store, repoID), branch)
	if !covered || !rule.MergeQueue {
		return fail("the merge queue of " + branch + " was turned off")
	}
	if why := staleReason(p, e, branch); why != "" {
		return fail(why)
	}
	m, err := s.Judge(ctx, repoID, p)
	if err != nil {
		return false
	}
	p.Mergeability = m
	if m.State == MergeConflicts {
		return fail("it conflicts with " + branch + " in " + strings.Join(m.Files, ", ") + "; update the branch, resolve the conflicts and queue it again")
	}
	ready, err := s.Readiness(ctx, repoID, path, p, RoleNone)
	if err != nil {
		return false
	}
	if hard := hardBlockers(ready); len(hard) > 0 {
		return fail(strings.Join(hard, "; "))
	}
	if m.State == MergeClean && m.Behind > 0 {
		if s.ProtectedBranch != nil && s.ProtectedBranch(ctx, repoID, p.SourceBranch) {
			return fail("its branch " + p.SourceBranch + " is protected, so the queue cannot bring it up to date; update it yourself and queue it again")
		}
		head, err := s.UpdateBranch(ctx, repoID, p, "merge", "merge queue")
		if err != nil {
			return fail("the queue could not bring the branch up to date: " + trimErr(err))
		}
		_ = s.Store.setQueue(ctx, p.ID, QueueTesting, head, "")
		return false
	}
	if m.State != MergeClean {
		return false // still being judged
	}
	if !ready.CanMerge {
		_ = s.Store.setQueue(ctx, p.ID, QueueTesting, p.HeadSHA, "")
		runs := []string{}
		for _, c := range ready.Checks {
			if c.RunID != "" && !slices.Contains(runs, c.RunID) {
				runs = append(runs, c.RunID)
			}
		}
		_ = s.Store.queueRuns(ctx, p.ID, runs)
		return false // the required checks of this head have not finished
	}
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if _, err := s.Merge(mctx, repoID, p, MergeInput{Method: e.Method, ExpectHead: p.HeadSHA, Actor: e.QueuedBy, RepoPath: path, Queue: true}); err != nil {
		return fail("the merge failed: " + trimErr(err))
	}
	_, _ = s.Store.db.Exec(ctx, `DELETE FROM pull_queue WHERE pull_id = ?`, p.ID)
	if s.AfterMerge != nil {
		np, _ := s.Store.ByID(mctx, p.ID)
		s.AfterMerge(mctx, repoID, np)
	}
	return true
}

// staleReason says why an entry can no longer stand, from the pull request alone ("" if it can).
func staleReason(p PullRequest, e QueueEntry, branch string) string {
	switch {
	case p.State != Open:
		return "the pull request is " + string(p.State)
	case p.TargetBranch != branch:
		return "the pull request now targets " + p.TargetBranch
	case p.HeadSHA != e.HeadSHA && p.HeadSHA != e.TestedSHA:
		return "new commits were pushed while it waited, so what was approved and tested is not what would merge; queue it again"
	}
	return ""
}

func trimErr(err error) string {
	return strings.TrimPrefix(err.Error(), ErrConflict.Error()+": ")
}

// UpdateBranch brings a pull request's source branch up to date with its
// target: a merge commit of the target into it (the default), or the branch's
// commits replayed on the target's tip (rebase, which rewrites the branch). It
// is a push to the source branch, so the pull request follows and its checks
// run on the new head. It returns the new head. The caller checks that the
// person may push to the branch.
func (s *Service) UpdateBranch(ctx context.Context, repoID string, p PullRequest, method, by string) (string, error) {
	switch {
	case p.State != Open:
		return "", conflict("this pull request is %s", p.State)
	case p.SourceRepo != "":
		return "", conflict("a branch of another repository cannot be updated here")
	case method == "":
		method = "merge"
	case method != "merge" && method != "rebase":
		return "", invalid("update the branch by merge or rebase")
	}
	sref, tref := branchRef(p.SourceBranch), branchRef(p.TargetBranch)
	var made string
	_, err := s.Git.Mutate(ctx, repoID, func(ctx context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		head, tip := refs[sref], refs[tref]
		if head == "" || tip == "" {
			return nil, conflict("the branch %s or %s no longer exists", p.SourceBranch, p.TargetBranch)
		}
		if head != p.HeadSHA {
			return nil, conflict("the branch %s moved since the pull request last looked; refresh and try again", p.SourceBranch)
		}
		g := s.runner(scratch, true)
		if up, err := g.isAncestor(ctx, tip, head); err != nil {
			return nil, err
		} else if up {
			return nil, conflict("%s already contains everything in %s", p.SourceBranch, p.TargetBranch)
		}
		var err error
		if method == "rebase" {
			made, err = s.rebase(ctx, g, tip, head, by)
			if err != nil {
				return nil, err
			}
		} else {
			tree, files, clean, err := g.trial(ctx, head, tip)
			if err != nil {
				return nil, err
			}
			if !clean {
				return nil, conflict("merging %s into %s conflicts in %s; resolve them on your computer", p.TargetBranch, p.SourceBranch, strings.Join(files, ", "))
			}
			g.env = ident(by, s.now())
			made, err = g.out(ctx, "commit-tree", tree, "-p", head, "-p", tip, "-m", fmt.Sprintf("Merge branch '%s' into %s", p.TargetBranch, p.SourceBranch))
			if err != nil {
				return nil, err
			}
		}
		return []storage.RefUpdate{{Name: sref, Old: head, New: made}}, nil
	})
	if err != nil {
		return "", err
	}
	ups := []RefUpdate{{Name: sref, Old: p.HeadSHA, New: made}}
	s.Pushed(ctx, repoID, by, ups)
	if s.AfterBranchMoved != nil {
		s.AfterBranchMoved(ctx, repoID, by, ups)
	}
	return made, nil
}
