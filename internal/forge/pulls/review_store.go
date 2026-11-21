package pulls

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// Verdicts of a review.
const (
	VerdictComment        = "comment"
	VerdictApprove        = "approve"
	VerdictRequestChanges = "request_changes"
)

// Comment is a comment of the conversation (no Path) or a review comment
// anchored to a line, or a range of lines, of a file at a commit.
type Comment struct {
	ID       string `json:"id"`
	ReviewID string `json:"review_id,omitempty"`
	// ThreadID groups a review comment and its replies; it is the ID of the
	// first. Conversation comments have none.
	ThreadID string `json:"thread_id,omitempty"`
	Author   string `json:"author"`
	// Body is Markdown as written; clients render it with the sanitizer.
	Body string `json:"body"`
	Path string `json:"path,omitempty"`
	// CommitSHA is the commit the line numbers refer to. When the code moves,
	// the anchor is moved with it (Line changes, CommitSHA becomes the new
	// head) or, when the commented lines themselves changed, the comment is
	// marked Outdated and keeps where it was written (OrigLine).
	CommitSHA string `json:"commit_sha,omitempty"`
	// Side is "new" (the line of the file after the change) or "old" (a
	// removed line, numbered in the file before).
	Side      string `json:"side,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	Line      int    `json:"line,omitempty"`
	OrigLine  int    `json:"original_line,omitempty"`
	// Suggestion is the text that would replace the commented lines; it is
	// set when the comment holds a suggestion block.
	Suggestion string `json:"suggestion,omitempty"`
	// HasSuggestion tells an empty suggestion (delete the lines) from none.
	HasSuggestion bool `json:"has_suggestion,omitempty"`
	// AppliedBy is the commit that applied the suggestion.
	AppliedSHA string    `json:"applied_commit,omitempty"`
	Outdated   bool      `json:"outdated,omitempty"`
	Resolved   bool      `json:"resolved,omitempty"`
	ResolvedBy string    `json:"resolved_by,omitempty"`
	Deleted    bool      `json:"deleted,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Review is a verdict on the pull request as it was at a commit.
type Review struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Verdict   string `json:"verdict"`
	Body      string `json:"body,omitempty"`
	CommitSHA string `json:"commit_sha"`
	// State is submitted or dismissed.
	State           string    `json:"state"`
	DismissedBy     string    `json:"dismissed_by,omitempty"`
	DismissedReason string    `json:"dismissed_reason,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	// Stale says the review is of an older head than the pull request has now.
	Stale bool `json:"stale,omitempty"`
}

// ErrForbidden is returned when the person may not do something to a comment
// or review (not theirs).
var ErrForbidden = errors.New("not allowed")

const commentCols = `id, review_id, thread_id, author, body, path, commit_sha, side, start_line, line, orig_line, suggestion, has_suggestion, applied_sha, outdated, resolved, resolved_by, deleted, created_at, updated_at`

func scanComment(sc interface{ Scan(...any) error }) (Comment, error) {
	var c Comment
	var hs, out, res, del int
	var created, updated int64
	if err := sc.Scan(&c.ID, &c.ReviewID, &c.ThreadID, &c.Author, &c.Body, &c.Path, &c.CommitSHA, &c.Side, &c.StartLine, &c.Line, &c.OrigLine, &c.Suggestion, &hs,
		&c.AppliedSHA, &out, &res, &c.ResolvedBy, &del, &created, &updated); err != nil {
		return c, err
	}
	c.HasSuggestion, c.Outdated, c.Resolved, c.Deleted = hs != 0, out != 0, res != 0, del != 0
	c.CreatedAt, c.UpdatedAt = fromMS(created), fromMS(updated)
	if c.Deleted {
		c.Body, c.Suggestion, c.HasSuggestion = "", "", false
	}
	return c, nil
}

// AddComment stores a comment. A review comment names its file and line; a
// reply carries the thread of the comment it answers.
func (s *Store) AddComment(ctx context.Context, pullID string, c Comment) (Comment, error) {
	c.ID = ids.New("pcm")
	now := s.now()
	if c.Path != "" && c.ThreadID == "" {
		c.ThreadID = c.ID
	}
	if c.Path != "" && c.OrigLine == 0 {
		c.OrigLine = c.Line
	}
	_, err := s.db.Exec(ctx, `INSERT INTO pull_comments(id, pull_id, review_id, thread_id, author, body, path, commit_sha, side, start_line, line, orig_line, suggestion, has_suggestion, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, pullID, c.ReviewID, c.ThreadID, c.Author, c.Body, c.Path, c.CommitSHA, c.Side, c.StartLine, c.Line, c.OrigLine,
		c.Suggestion, b2i(c.HasSuggestion), ms(now), ms(now))
	if err != nil {
		return c, err
	}
	return s.Comment(ctx, pullID, c.ID)
}

// Comment finds a comment of a pull request.
func (s *Store) Comment(ctx context.Context, pullID, id string) (Comment, error) {
	c, err := scanComment(s.db.QueryRow(ctx, `SELECT `+commentCols+` FROM pull_comments WHERE pull_id = ? AND id = ?`, pullID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// Comments lists every comment of a pull request, oldest first.
func (s *Store) Comments(ctx context.Context, pullID string) ([]Comment, error) {
	rows, err := s.db.Query(ctx, `SELECT `+commentCols+` FROM pull_comments WHERE pull_id = ? ORDER BY created_at, id`, pullID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EditComment changes a comment's text; only its author may.
func (s *Store) EditComment(ctx context.Context, pullID, id, by, body string, sugg string, hasSugg bool) (Comment, error) {
	c, err := s.Comment(ctx, pullID, id)
	if err != nil {
		return c, err
	}
	if c.Author != by {
		return c, ErrForbidden
	}
	if c.AppliedSHA != "" {
		return c, conflict("this suggestion was already applied; write a new comment")
	}
	if _, err := s.db.Exec(ctx, `UPDATE pull_comments SET body = ?, suggestion = ?, has_suggestion = ?, updated_at = ? WHERE id = ?`, body, sugg, b2i(hasSugg), ms(s.now()), id); err != nil {
		return c, err
	}
	return s.Comment(ctx, pullID, id)
}

// DeleteComment removes a comment's text (the thread keeps its shape).
func (s *Store) DeleteComment(ctx context.Context, pullID, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE pull_comments SET deleted = 1, updated_at = ? WHERE pull_id = ? AND id = ?`, ms(s.now()), pullID, id)
	return err
}

// Resolve marks every comment of a thread resolved (or open again).
func (s *Store) Resolve(ctx context.Context, pullID, thread, by string, on bool) error {
	by2 := by
	if !on {
		by2 = ""
	}
	res, err := s.db.Exec(ctx, `UPDATE pull_comments SET resolved = ?, resolved_by = ?, updated_at = ? WHERE pull_id = ? AND thread_id = ?`, b2i(on), by2, ms(s.now()), pullID, thread)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Reanchor moves a review comment's anchor.
func (s *Store) Reanchor(ctx context.Context, id string, line, start int, sha string, outdated bool) error {
	_, err := s.db.Exec(ctx, `UPDATE pull_comments SET line = ?, start_line = ?, commit_sha = ?, outdated = ? WHERE id = ?`, line, start, sha, b2i(outdated), id)
	return err
}

// MarkApplied records the commit that applied a suggestion and resolves its thread.
func (s *Store) MarkApplied(ctx context.Context, pullID, id, sha, by string) error {
	c, err := s.Comment(ctx, pullID, id)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE pull_comments SET applied_sha = ? WHERE id = ?`, sha, id); err != nil {
		return err
	}
	return s.Resolve(ctx, pullID, c.ThreadID, by, true)
}

const reviewCols = `id, author, verdict, body, commit_sha, state, dismissed_by, dismissed_reason, created_at`

// AddReview stores a review.
func (s *Store) AddReview(ctx context.Context, pullID string, r Review) (Review, error) {
	r.ID = ids.New("prv")
	if _, err := s.db.Exec(ctx, `INSERT INTO pull_reviews(id, pull_id, author, verdict, body, commit_sha, created_at) VALUES(?,?,?,?,?,?,?)`,
		r.ID, pullID, r.Author, r.Verdict, r.Body, r.CommitSHA, ms(s.now())); err != nil {
		return r, err
	}
	return s.Review(ctx, pullID, r.ID)
}

func scanReview(sc interface{ Scan(...any) error }) (Review, error) {
	var r Review
	var at int64
	err := sc.Scan(&r.ID, &r.Author, &r.Verdict, &r.Body, &r.CommitSHA, &r.State, &r.DismissedBy, &r.DismissedReason, &at)
	r.CreatedAt = fromMS(at)
	return r, err
}

// Review finds a review of a pull request.
func (s *Store) Review(ctx context.Context, pullID, id string) (Review, error) {
	r, err := scanReview(s.db.QueryRow(ctx, `SELECT `+reviewCols+` FROM pull_reviews WHERE pull_id = ? AND id = ?`, pullID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// Reviews lists the reviews of a pull request, oldest first.
func (s *Store) Reviews(ctx context.Context, pullID string) ([]Review, error) {
	rows, err := s.db.Query(ctx, `SELECT `+reviewCols+` FROM pull_reviews WHERE pull_id = ? ORDER BY created_at, id`, pullID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Review{}
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DismissReview marks a review dismissed, with who and why.
func (s *Store) DismissReview(ctx context.Context, pullID, id, by, reason string) error {
	res, err := s.db.Exec(ctx, `UPDATE pull_reviews SET state = 'dismissed', dismissed_by = ?, dismissed_reason = ? WHERE pull_id = ? AND id = ? AND state = 'submitted'`, by, reason, pullID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Mentions lists the users a text mentions with @name, once each, in order.
func Mentions(body string) []string {
	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(body); i++ {
		if body[i] != '@' || (i > 0 && isNameByte(body[i-1])) {
			continue
		}
		j := i + 1
		for j < len(body) && isNameByte(body[j]) {
			j++
		}
		if n := strings.TrimRight(body[i+1:j], ".-_"); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
		i = j - 1
	}
	return out
}

func isNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.'
}
