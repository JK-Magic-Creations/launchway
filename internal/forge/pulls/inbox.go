package pulls

import (
	"context"
	"sort"
	"time"
)

// Inbox kinds: what a pull request wants of a person.
const (
	InboxReview      = "review_requested"
	InboxChanges     = "changes_requested"
	InboxQueueFailed = "queue_failed"
)

// InboxItem is one pull request that waits on a person.
type InboxItem struct {
	RepoID string `json:"-"`
	// Repository is the repository's path, filled by the API.
	Repository string    `json:"repository"`
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	Kind       string    `json:"kind"`
	Why        string    `json:"why"`
	At         time.Time `json:"at"`
}

// Inbox lists the open pull requests that wait on a person, across the
// repositories (the caller keeps those the person may read): review asked of
// them or of a group of theirs, changes asked on their own, and their own that
// the merge queue took out. Newest first.
func (s *Store) Inbox(ctx context.Context, user string, groups []string) ([]InboxItem, error) {
	var out []InboxItem
	add := func(q string, args []any, kind string, why func(string) string) error {
		rows, err := s.db.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it InboxItem
			var at int64
			var extra string
			if err := rows.Scan(&it.RepoID, &it.Number, &it.Title, &at, &extra); err != nil {
				return err
			}
			it.Kind, it.Why, it.At = kind, why(extra), fromMS(at)
			out = append(out, it)
		}
		return rows.Err()
	}
	if err := add(`SELECT pr.repo_id, pr.number, pr.title, pp.added_at, pp.added_by FROM pull_people pp JOIN pull_requests pr ON pr.id = pp.pull_id
		WHERE pp.kind = 'reviewer' AND pp.name = ? AND pr.state = 'open' AND pr.draft = 0 AND pr.author <> ? ORDER BY pp.added_at DESC LIMIT 100`,
		[]any{user, user}, InboxReview, func(by string) string {
			if by == "" || by == "system" {
				return "you were asked to review it"
			}
			return by + " asked you to review it"
		}); err != nil {
		return nil, err
	}
	for _, g := range groups {
		if err := add(`SELECT pr.repo_id, pr.number, pr.title, pp.added_at, pp.name FROM pull_people pp JOIN pull_requests pr ON pr.id = pp.pull_id
			WHERE pp.kind = 'reviewer_group' AND pp.name = ? AND pr.state = 'open' AND pr.draft = 0 AND pr.author <> ? ORDER BY pp.added_at DESC LIMIT 100`,
			[]any{g, user}, InboxReview, func(name string) string { return "the group " + name + " was asked to review it" }); err != nil {
			return nil, err
		}
	}
	if err := add(`SELECT q.repo_id, pr.number, pr.title, q.updated_at, q.reason FROM pull_queue q JOIN pull_requests pr ON pr.id = q.pull_id
		WHERE q.state = 'removed' AND q.queued_by = ? AND pr.state = 'open' AND q.reason <> '' AND q.reason NOT LIKE 'taken out of the queue by %' ORDER BY q.updated_at DESC LIMIT 100`,
		[]any{user}, InboxQueueFailed, func(reason string) string { return "the merge queue took it out: " + reason }); err != nil {
		return nil, err
	}
	changes, err := s.inboxChanges(ctx, user)
	if err != nil {
		return nil, err
	}
	out = append(out, changes...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// inboxChanges lists a person's own open pull requests on which someone's last verdict is a request for changes.
func (s *Store) inboxChanges(ctx context.Context, user string) ([]InboxItem, error) {
	type mine struct {
		id, repo, title string
		number          int
	}
	own, err := func() ([]mine, error) {
		rows, err := s.db.Query(ctx, `SELECT id, repo_id, number, title FROM pull_requests WHERE author = ? AND state = 'open' ORDER BY updated_at DESC LIMIT 200`, user)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var own []mine
		for rows.Next() {
			var m mine
			if err := rows.Scan(&m.id, &m.repo, &m.number, &m.title); err != nil {
				return nil, err
			}
			own = append(own, m)
		}
		return own, rows.Err()
	}()
	if err != nil {
		return nil, err
	}
	var out []InboxItem
	for _, m := range own {
		rs, err := s.Reviews(ctx, m.id)
		if err != nil {
			return nil, err
		}
		last := map[string]Review{}
		for _, r := range rs {
			if r.State == "submitted" && r.Verdict != VerdictComment {
				last[r.Author] = r
			}
		}
		for who, r := range last {
			if r.Verdict == VerdictRequestChanges {
				out = append(out, InboxItem{RepoID: m.repo, Number: m.number, Title: m.title, Kind: InboxChanges, Why: who + " asked for changes", At: r.CreatedAt})
			}
		}
	}
	return out, nil
}
