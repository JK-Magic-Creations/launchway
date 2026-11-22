package pulls

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// DB is what the store needs of a database.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// Store is the pull request tables.
type Store struct {
	db  DB
	now func() time.Time
}

// NewStore makes a store over a database.
func NewStore(db DB) *Store { return &Store{db: db, now: time.Now} }

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

const pullCols = `id, repo_id, number, title, body, author, source_repo_id, source_branch, target_branch, state, draft, labels,
 head_sha, base_sha, merge_base, merge_state, merge_files, merge_behind, merge_ahead, merge_checked_head, merge_checked_base, merge_checked_at, merge_reason,
 merge_commit, merge_method, merged_by, reverts, auto_merge, auto_merge_by, created_at, updated_at, merged_at, closed_at, closed_by,
 allow_maintainer_edits, run_approved_head`

// pulled is a row with what the merge judgment was made for.
type pulled struct {
	PullRequest
	checkedHead, checkedBase string
}

func scanPull(sc interface{ Scan(...any) error }) (pulled, error) {
	var p pulled
	var draft, edits int
	var labels, files, state, mstate string
	var created, updated, merged, closed, checked int64
	if err := sc.Scan(&p.ID, &p.RepoID, &p.Number, &p.Title, &p.Body, &p.Author, &p.SourceRepo, &p.SourceBranch, &p.TargetBranch, &state, &draft, &labels,
		&p.HeadSHA, &p.BaseSHA, &p.MergeBase, &mstate, &files, &p.Mergeability.Behind, &p.Mergeability.Ahead, &p.checkedHead, &p.checkedBase, &checked, &p.Mergeability.Reason,
		&p.MergeCommit, &p.MergeMethod, &p.MergedBy, &p.Reverts, &p.AutoMerge, &p.AutoMergeBy, &created, &updated, &merged, &closed, &p.ClosedBy,
		&edits, &p.RunApprovedHead); err != nil {
		return p, err
	}
	p.State, p.Draft, p.AllowMaintainerEdits = State(state), draft != 0, edits != 0
	_ = json.Unmarshal([]byte(labels), &p.Labels)
	_ = json.Unmarshal([]byte(files), &p.Mergeability.Files)
	if p.Labels == nil {
		p.Labels = []string{}
	}
	p.Mergeability.State = MergeState(mstate)
	p.Mergeability.CheckedAt = fromMS(checked)
	p.CreatedAt, p.UpdatedAt, p.MergedAt, p.ClosedAt = fromMS(created), fromMS(updated), fromMS(merged), fromMS(closed)
	// A judgment made for another head or base is not a judgment of this one.
	if p.State == Open && (p.checkedHead != p.HeadSHA || p.checkedBase != p.BaseSHA) && p.Mergeability.State != MergeUnknown {
		p.Mergeability.State = MergeChecking
	}
	return p, nil
}

// nextNumber hands out the next number of a repository, atomically.
func (s *Store) nextNumber(ctx context.Context, repoID string) (int, error) {
	var next int
	err := s.db.QueryRow(ctx, `INSERT INTO pull_counters(repo_id, next_number) VALUES(?, 2)
		ON CONFLICT(repo_id) DO UPDATE SET next_number = pull_counters.next_number + 1 RETURNING next_number`, repoID).Scan(&next)
	return next - 1, err
}

// Create records a pull request opened by author on a repository whose
// branches are at head and base. The first revision is recorded with it.
func (s *Store) Create(ctx context.Context, repoID, author string, n New, head, base, mergeBase string) (PullRequest, error) {
	if err := n.Validate(); err != nil {
		return PullRequest{}, err
	}
	num := n.Number
	if num == 0 {
		var err error
		if num, err = s.nextNumber(ctx, repoID); err != nil {
			return PullRequest{}, err
		}
	}
	now := s.now()
	labels, _ := json.Marshal(n.Labels)
	id := ids.New("pul")
	if _, err := s.db.Exec(ctx, `INSERT INTO pull_requests(id, repo_id, number, title, body, author, source_branch, target_branch, state, draft, labels,
		head_sha, base_sha, merge_base, reverts, created_at, updated_at, source_repo_id, allow_maintainer_edits) VALUES(?,?,?,?,?,?,?,?, 'open', ?,?,?,?,?,?,?,?,?,?)`,
		id, repoID, num, n.Title, n.Body, author, n.SourceBranch, n.TargetBranch, b2i(n.Draft), string(labels), head, base, mergeBase, n.Reverts, ms(now), ms(now), n.SourceRepo, b2i(n.AllowMaintainerEdits)); err != nil {
		return PullRequest{}, err
	}
	for _, a := range n.Assignees {
		if err := s.AddPerson(ctx, id, "assignee", a, author); err != nil {
			return PullRequest{}, err
		}
	}
	for _, r := range n.Reviewers {
		if err := s.AddPerson(ctx, id, "reviewer", r, author); err != nil {
			return PullRequest{}, err
		}
	}
	if err := s.AddRevision(ctx, id, head, base, false, author); err != nil {
		return PullRequest{}, err
	}
	return s.Get(ctx, repoID, num)
}

// Get finds a pull request by its number.
func (s *Store) Get(ctx context.Context, repoID string, number int) (PullRequest, error) {
	p, err := scanPull(s.db.QueryRow(ctx, `SELECT `+pullCols+` FROM pull_requests WHERE repo_id = ? AND number = ?`, repoID, number))
	if errors.Is(err, sql.ErrNoRows) {
		return PullRequest{}, ErrNotFound
	}
	if err != nil {
		return PullRequest{}, err
	}
	return s.fill(ctx, p.PullRequest)
}

// ByID finds a pull request by its ID.
func (s *Store) ByID(ctx context.Context, id string) (PullRequest, error) {
	p, err := scanPull(s.db.QueryRow(ctx, `SELECT `+pullCols+` FROM pull_requests WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return PullRequest{}, ErrNotFound
	}
	if err != nil {
		return PullRequest{}, err
	}
	return s.fill(ctx, p.PullRequest)
}

// fill adds the people, revisions count and runs.
func (s *Store) fill(ctx context.Context, p PullRequest) (PullRequest, error) {
	p.Assignees, p.Reviewers, p.ReviewerGroups, p.LinkedRuns = []string{}, []string{}, []string{}, []string{}
	if err := s.people(ctx, &p); err != nil {
		return p, err
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM pull_revisions WHERE pull_id = ?`, p.ID).Scan(&p.Revisions); err != nil {
		return p, err
	}
	rr, err := s.db.Query(ctx, `SELECT run_id FROM pull_runs WHERE pull_id = ? ORDER BY created_at, run_id`, p.ID)
	if err != nil {
		return p, err
	}
	defer rr.Close()
	for rr.Next() {
		var id string
		if err := rr.Scan(&id); err != nil {
			return p, err
		}
		p.LinkedRuns = append(p.LinkedRuns, id)
	}
	return p, rr.Err()
}

// people reads the assignees and requested reviewers into p.
func (s *Store) people(ctx context.Context, p *PullRequest) error {
	rows, err := s.db.Query(ctx, `SELECT kind, name FROM pull_people WHERE pull_id = ? ORDER BY added_at, name`, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k, n string
		if err := rows.Scan(&k, &n); err != nil {
			return err
		}
		switch k {
		case "assignee":
			p.Assignees = append(p.Assignees, n)
		case "reviewer_group":
			p.ReviewerGroups = append(p.ReviewerGroups, n)
		default:
			p.Reviewers = append(p.Reviewers, n)
		}
	}
	return rows.Err()
}

// pullRows reads rows of pullCols into filled pull requests.
func (s *Store) pullRows(ctx context.Context, q string, args ...any) ([]PullRequest, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var got []PullRequest
	for rows.Next() {
		p, err := scanPull(rows)
		if err != nil {
			return nil, err
		}
		got = append(got, p.PullRequest)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range got {
		if got[i], err = s.fill(ctx, got[i]); err != nil {
			return nil, err
		}
	}
	if got == nil {
		got = []PullRequest{}
	}
	return got, nil
}

// List returns a repository's pull requests, newest first, and how many match.
func (s *Store) List(ctx context.Context, repoID string, f Filter) ([]PullRequest, int, error) {
	where, args := []string{"p.repo_id = ?"}, []any{repoID}
	switch f.State {
	case "open", "merged", "closed":
		where, args = append(where, "p.state = ?"), append(args, f.State)
	case "", "all":
	default:
		return nil, 0, invalid("state %q is not open, closed, merged or all", f.State)
	}
	add := func(cond string, v ...any) { where, args = append(where, cond), append(args, v...) }
	if f.Author != "" {
		add("p.author = ?", f.Author)
	}
	if f.Assignee != "" {
		add("EXISTS (SELECT 1 FROM pull_people q WHERE q.pull_id = p.id AND q.kind = 'assignee' AND q.name = ?)", f.Assignee)
	}
	if f.Reviewer != "" {
		add("EXISTS (SELECT 1 FROM pull_people q WHERE q.pull_id = p.id AND q.kind = 'reviewer' AND q.name = ?)", f.Reviewer)
	}
	if f.Label != "" {
		add("p.labels LIKE ?", `%`+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(`"`+strings.ReplaceAll(f.Label, `"`, `\"`)+`"`)+`%`)
	}
	if f.Source != "" {
		add("p.source_branch = ?", f.Source)
	}
	if f.Target != "" {
		add("p.target_branch = ?", f.Target)
	}
	if f.Query != "" {
		add(`LOWER(p.title) LIKE ? ESCAPE '\'`, `%`+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(f.Query))+`%`)
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM pull_requests p WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + prefixed("p.", pullCols) + ` FROM pull_requests p WHERE ` + cond + ` ORDER BY p.number DESC LIMIT ? OFFSET ?`
	out, err := s.pullRows(ctx, q, append(args, limit, max(f.Offset, 0))...)
	return out, total, err
}

func prefixed(pre, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = pre + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// OpenFromSource returns the open pull requests whose source is a branch.
func (s *Store) OpenFromSource(ctx context.Context, repoID, branch string) ([]PullRequest, error) {
	return s.open(ctx, `repo_id = ? AND source_branch = ? AND state = 'open'`, repoID, branch)
}

// OpenToTarget returns the open pull requests that go into a branch.
func (s *Store) OpenToTarget(ctx context.Context, repoID, branch string) ([]PullRequest, error) {
	return s.open(ctx, `repo_id = ? AND target_branch = ? AND state = 'open'`, repoID, branch)
}

// OpenAt returns the open pull requests whose head is a commit.
func (s *Store) OpenAt(ctx context.Context, repoID, sha string) ([]PullRequest, error) {
	return s.open(ctx, `repo_id = ? AND head_sha = ? AND state = 'open'`, repoID, sha)
}

func (s *Store) open(ctx context.Context, cond string, args ...any) ([]PullRequest, error) {
	return s.pullRows(ctx, `SELECT `+pullCols+` FROM pull_requests WHERE `+cond+` ORDER BY number`, args...)
}

// Edit changes what a person may change on a pull request; nil stays.
type Edit struct {
	Title  *string
	Body   *string
	Draft  *bool
	Labels *[]string
	Target *string
}

// Apply writes an edit.
func (s *Store) Apply(ctx context.Context, id string, e Edit) error {
	sets, args := []string{"updated_at = ?"}, []any{ms(s.now())}
	if e.Title != nil {
		t := strings.TrimSpace(*e.Title)
		if t == "" || len(t) > maxTitle {
			return invalid("the title must be between 1 and %d characters", maxTitle)
		}
		sets, args = append(sets, "title = ?"), append(args, t)
	}
	if e.Body != nil {
		if len(*e.Body) > maxBody {
			return invalid("the description is more than %d KiB", maxBody/1024)
		}
		sets, args = append(sets, "body = ?"), append(args, *e.Body)
	}
	if e.Draft != nil {
		sets, args = append(sets, "draft = ?"), append(args, b2i(*e.Draft))
	}
	if e.Labels != nil {
		l, err := cleanNames(*e.Labels, "label")
		if err != nil {
			return err
		}
		j, _ := json.Marshal(l)
		sets, args = append(sets, "labels = ?"), append(args, string(j))
	}
	if e.Target != nil {
		sets, args = append(sets, "target_branch = ?"), append(args, *e.Target)
	}
	_, err := s.db.Exec(ctx, `UPDATE pull_requests SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...)
	return err
}

// AddPerson adds an assignee or a requested reviewer (kind "assignee" or
// "reviewer"); adding one again changes nothing.
func (s *Store) AddPerson(ctx context.Context, pullID, kind, name, by string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return invalid("%q is not a user name", name)
	}
	_, err := s.db.Exec(ctx, `INSERT INTO pull_people(pull_id, kind, name, added_by, added_at) VALUES(?,?,?,?,?) ON CONFLICT(pull_id, kind, name) DO NOTHING`,
		pullID, kind, name, by, ms(s.now()))
	return err
}

// RemovePerson removes one.
func (s *Store) RemovePerson(ctx context.Context, pullID, kind, name string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM pull_people WHERE pull_id = ? AND kind = ? AND name = ?`, pullID, kind, name)
	return err
}

// AddRevision records the head and base at an update.
func (s *Store) AddRevision(ctx context.Context, pullID, head, base string, forced bool, actor string) error {
	var seq int
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM pull_revisions WHERE pull_id = ?`, pullID).Scan(&seq); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO pull_revisions(pull_id, seq, head_sha, base_sha, forced, actor, created_at) VALUES(?,?,?,?,?,?,?)`,
		pullID, seq, head, base, b2i(forced), actor, ms(s.now()))
	return err
}

// Revisions lists a pull request's revisions, oldest first.
func (s *Store) Revisions(ctx context.Context, pullID string) ([]Revision, error) {
	rows, err := s.db.Query(ctx, `SELECT seq, head_sha, base_sha, forced, actor, created_at FROM pull_revisions WHERE pull_id = ? ORDER BY seq`, pullID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		var r Revision
		var forced int
		var at int64
		if err := rows.Scan(&r.Seq, &r.HeadSHA, &r.BaseSHA, &forced, &r.Actor, &at); err != nil {
			return nil, err
		}
		r.Forced, r.CreatedAt = forced != 0, fromMS(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MoveTips records the tips a pull request now has (after a push to either
// branch); the judgment of mergeability becomes stale by itself because it
// remembers what it was made for.
func (s *Store) MoveTips(ctx context.Context, id, head, base, mergeBase string) error {
	_, err := s.db.Exec(ctx, `UPDATE pull_requests SET head_sha = ?, base_sha = ?, merge_base = ?, updated_at = ? WHERE id = ?`, head, base, mergeBase, ms(s.now()), id)
	return err
}

// SetMergeability stores a judgment with the tips it was made for. It is
// kept only if the pull request is still at those tips.
func (s *Store) SetMergeability(ctx context.Context, id, head, base string, m Mergeability) error {
	files, _ := json.Marshal(m.Files)
	if m.Files == nil {
		files = []byte("[]")
	}
	_, err := s.db.Exec(ctx, `UPDATE pull_requests SET merge_state = ?, merge_files = ?, merge_behind = ?, merge_ahead = ?, merge_checked_head = ?, merge_checked_base = ?,
		merge_checked_at = ?, merge_reason = ? WHERE id = ? AND head_sha = ? AND base_sha = ?`,
		string(m.State), string(files), m.Behind, m.Ahead, head, base, ms(s.now()), m.Reason, id, head, base)
	return err
}

// SetState moves a pull request to closed or open again.
func (s *Store) SetState(ctx context.Context, id string, st State, by string) error {
	now := ms(s.now())
	switch st {
	case Closed:
		_, err := s.db.Exec(ctx, `UPDATE pull_requests SET state = 'closed', closed_at = ?, closed_by = ?, auto_merge = '', updated_at = ? WHERE id = ? AND state = 'open'`, now, by, now, id)
		return err
	case Open:
		_, err := s.db.Exec(ctx, `UPDATE pull_requests SET state = 'open', closed_at = 0, closed_by = '', updated_at = ? WHERE id = ? AND state = 'closed'`, now, id)
		return err
	}
	return invalid("a pull request is merged by merging it")
}

// SetMerged records that a pull request landed.
func (s *Store) SetMerged(ctx context.Context, id, commit, method, by string) error {
	now := ms(s.now())
	res, err := s.db.Exec(ctx, `UPDATE pull_requests SET state = 'merged', merge_commit = ?, merge_method = ?, merged_by = ?, merged_at = ?, auto_merge = '', updated_at = ?
		WHERE id = ? AND state = 'open'`, commit, method, by, now, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return conflict("the pull request is not open")
	}
	return nil
}

// SetAutoMerge turns auto-merge on (with a method) or off ("").
func (s *Store) SetAutoMerge(ctx context.Context, id, method, by string) error {
	_, err := s.db.Exec(ctx, `UPDATE pull_requests SET auto_merge = ?, auto_merge_by = ?, updated_at = ? WHERE id = ? AND state = 'open'`, method, by, ms(s.now()), id)
	return err
}

// LinkRun records that a run belongs to a pull request.
func (s *Store) LinkRun(ctx context.Context, pullID, runID, head string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO pull_runs(pull_id, run_id, head_sha, created_at) VALUES(?,?,?,?) ON CONFLICT(pull_id, run_id) DO NOTHING`, pullID, runID, head, ms(s.now()))
	return err
}

// Candidate is a pull request waiting for its rules to pass.
type Candidate struct{ RepoID, ID string }

// AutoMergeCandidates lists the open pull requests that asked for auto-merge.
func (s *Store) AutoMergeCandidates(ctx context.Context) ([]Candidate, error) {
	rows, err := s.db.Query(ctx, `SELECT repo_id, id FROM pull_requests WHERE state = 'open' AND auto_merge <> '' ORDER BY updated_at LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.RepoID, &c.ID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
