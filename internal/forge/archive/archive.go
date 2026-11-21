package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// Format is the value of a document's "format".
const Format = "launchway-archive/1"

// Limits of one import request, so that one call cannot hold the server.
const (
	MaxIssuesPerImport = 500
	MaxCommentsPerItem = 5000
	MaxBodyBytes       = 1 << 20
	MaxTitleBytes      = 1000
	MaxLabels          = 100
	DefaultPerPage     = 50
	MaxPerPage         = 200
)

// Comment is one comment of an imported issue.
type Comment struct {
	ID        string `json:"id,omitempty"` // the source's identifier, as text
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at,omitempty"` // RFC 3339
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Issue is one imported issue (or, with IsPullRequest, the source's record of
// a pull request that it kept in its issue list).
type Issue struct {
	Number        int       `json:"number"`
	Title         string    `json:"title"`
	State         string    `json:"state"` // open or closed
	Author        string    `json:"author"`
	Body          string    `json:"body"`
	Labels        []string  `json:"labels"`
	Milestone     string    `json:"milestone,omitempty"`
	Assignees     []string  `json:"assignees"`
	URL           string    `json:"url,omitempty"` // where it was at the source
	IsPullRequest bool      `json:"is_pull_request,omitempty"`
	CreatedAt     string    `json:"created_at,omitempty"`
	UpdatedAt     string    `json:"updated_at,omitempty"`
	ClosedAt      string    `json:"closed_at,omitempty"`
	CommentCount  int       `json:"comment_count"`
	Comments      []Comment `json:"comments,omitempty"` // only in a detail and in an import
}

// Document is what an import reads: a source's exported issues.
type Document struct {
	Format     string  `json:"format,omitempty"`
	Source     Source  `json:"source,omitempty"`
	ExportedAt string  `json:"exported_at,omitempty"`
	Issues     []Issue `json:"issues"`
}

// Source says where the document's issues came from.
type Source struct {
	Name string `json:"name,omitempty"` // such as the old server's name
	URL  string `json:"url,omitempty"`  // the repository's address there
}

// Result of an import.
type Result struct {
	Issues   int `json:"issues"`
	Comments int `json:"comments"`
	Replaced int `json:"replaced"` // issues that had been imported before
	Total    int `json:"total"`    // issues now held for the repository
}

// DB is what the store needs of a database.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// Tx is a transaction of the database.
type Tx interface {
	DB
	Commit() error
	Rollback() error
}

// Beginner is a database that can start a transaction.
type Beginner interface {
	DB
	BeginTx(ctx context.Context) (Tx, error)
}

// Store is the archive's tables.
type Store struct {
	db  DB
	now func() time.Time
}

// New makes a store. A database that can begin a transaction (Beginner) makes
// each issue and its comments one change.
func New(db DB) *Store { return &Store{db: db, now: time.Now} }

// ErrNotFound is returned for an issue that was not imported.
var ErrNotFound = fmt.Errorf("%w: imported issue", forge.ErrNotFound)

// Validate checks and tidies a document; nothing is stored if it fails.
func Validate(d *Document) error {
	if d.Format != "" && d.Format != Format {
		return fmt.Errorf("%w: format is %q; this server reads %q", forge.ErrInvalid, d.Format, Format)
	}
	if len(d.Issues) == 0 {
		return fmt.Errorf("%w: the document holds no issues", forge.ErrInvalid)
	}
	if len(d.Issues) > MaxIssuesPerImport {
		return fmt.Errorf("%w: %d issues in one import; send at most %d at a time", forge.ErrInvalid, len(d.Issues), MaxIssuesPerImport)
	}
	seen := map[int]bool{}
	for i := range d.Issues {
		is := &d.Issues[i]
		switch {
		case is.Number <= 0:
			return fmt.Errorf("%w: issue %d has no number", forge.ErrInvalid, i+1)
		case seen[is.Number]:
			return fmt.Errorf("%w: issue #%d appears twice", forge.ErrInvalid, is.Number)
		case len(is.Title) > MaxTitleBytes || len(is.Body) > MaxBodyBytes:
			return fmt.Errorf("%w: issue #%d has a title or body that is too long", forge.ErrInvalid, is.Number)
		case len(is.Comments) > MaxCommentsPerItem:
			return fmt.Errorf("%w: issue #%d has more than %d comments", forge.ErrInvalid, is.Number, MaxCommentsPerItem)
		case len(is.Labels) > MaxLabels || len(is.Assignees) > MaxLabels:
			return fmt.Errorf("%w: issue #%d has too many labels or assignees", forge.ErrInvalid, is.Number)
		}
		seen[is.Number] = true
		is.State = strings.ToLower(is.State)
		if is.State != "open" && is.State != "closed" {
			return fmt.Errorf("%w: issue #%d: state %q is neither open nor closed", forge.ErrInvalid, is.Number, is.State)
		}
		for _, c := range is.Comments {
			if len(c.Body) > MaxBodyBytes {
				return fmt.Errorf("%w: a comment of issue #%d is too long", forge.ErrInvalid, is.Number)
			}
		}
		for _, t := range append([]string{is.CreatedAt, is.UpdatedAt, is.ClosedAt}, commentTimes(is.Comments)...) {
			if t == "" {
				continue
			}
			if _, err := time.Parse(time.RFC3339, t); err != nil {
				return fmt.Errorf("%w: issue #%d: %q is not an RFC 3339 time", forge.ErrInvalid, is.Number, t)
			}
		}
		is.Title, is.Body = cleanText(is.Title), strings.ToValidUTF8(is.Body, "�")
		if is.Labels == nil {
			is.Labels = []string{}
		}
		if is.Assignees == nil {
			is.Assignees = []string{}
		}
	}
	return nil
}

func commentTimes(cs []Comment) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.CreatedAt, c.UpdatedAt)
	}
	return out
}

func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return ' '
		}
		return r
	}, s)
}

func millis(t string) int64 {
	if t == "" {
		return 0
	}
	p, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return 0
	}
	return p.UnixMilli()
}

func iso(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// Import stores a validated document for a repository. Each issue is replaced
// as a whole (its comments too), so importing the same export again changes
// nothing, and a corrected export fixes what it corrects.
func (s *Store) Import(ctx context.Context, repoID, by string, d Document) (Result, error) {
	var res Result
	if err := Validate(&d); err != nil {
		return res, err
	}
	db := s.db
	var tx Tx
	if b, ok := s.db.(Beginner); ok {
		t, err := b.BeginTx(ctx)
		if err != nil {
			return res, err
		}
		tx, db = t, t
		defer func() { _ = tx.Rollback() }()
	}
	now := s.now().UnixMilli()
	for _, is := range d.Issues {
		var had int
		if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM imported_issues WHERE repo_id = ? AND number = ?`, repoID, is.Number).Scan(&had); err != nil {
			return res, err
		}
		if had > 0 {
			res.Replaced++
			if _, err := db.Exec(ctx, `DELETE FROM imported_comments WHERE repo_id = ? AND issue_number = ?`, repoID, is.Number); err != nil {
				return res, err
			}
			if _, err := db.Exec(ctx, `DELETE FROM imported_issues WHERE repo_id = ? AND number = ?`, repoID, is.Number); err != nil {
				return res, err
			}
		}
		labels, _ := json.Marshal(is.Labels)
		assignees, _ := json.Marshal(is.Assignees)
		isPull := 0
		if is.IsPullRequest {
			isPull = 1
		}
		if _, err := db.Exec(ctx, `INSERT INTO imported_issues(repo_id, number, title, state, author, body, labels, milestone, assignees, url, is_pull,
			created_at, updated_at, closed_at, comment_count, source, imported_at, imported_by) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			repoID, is.Number, is.Title, is.State, cleanText(is.Author), is.Body, string(labels), cleanText(is.Milestone), string(assignees), cleanText(is.URL), isPull,
			millis(is.CreatedAt), millis(is.UpdatedAt), millis(is.ClosedAt), len(is.Comments), cleanText(d.Source.Name), now, by); err != nil {
			return res, err
		}
		for i, c := range is.Comments {
			if _, err := db.Exec(ctx, `INSERT INTO imported_comments(repo_id, issue_number, position, source_id, author, body, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)`,
				repoID, is.Number, i, cleanText(c.ID), cleanText(c.Author), strings.ToValidUTF8(c.Body, "�"), millis(c.CreatedAt), millis(c.UpdatedAt)); err != nil {
				return res, err
			}
			res.Comments++
		}
		res.Issues++
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return res, err
		}
	}
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM imported_issues WHERE repo_id = ?`, repoID).Scan(&res.Total)
	return res, err
}

// Query narrows a list.
type Query struct {
	State   string // open, closed or "" for both
	Text    string // matched in titles, bodies and comments, ignoring case
	Page    int    // from 1
	PerPage int
}

// Page is one page of a list, with the totals the interface shows.
type Page struct {
	Items   []Issue `json:"items"`
	Total   int     `json:"total"`  // matching the query
	Open    int     `json:"open"`   // of the repository's imported issues, whatever the query
	Closed  int     `json:"closed"` // likewise
	Page    int     `json:"page"`
	PerPage int     `json:"per_page"`
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

const issueCols = `number, title, state, author, body, labels, milestone, assignees, url, is_pull, created_at, updated_at, closed_at, comment_count`

func scanIssue(sc interface{ Scan(...any) error }) (Issue, error) {
	var is Issue
	var labels, assignees string
	var isPull int
	var c, u, cl int64
	if err := sc.Scan(&is.Number, &is.Title, &is.State, &is.Author, &is.Body, &labels, &is.Milestone, &assignees, &is.URL, &isPull, &c, &u, &cl, &is.CommentCount); err != nil {
		return is, err
	}
	_ = json.Unmarshal([]byte(labels), &is.Labels)
	_ = json.Unmarshal([]byte(assignees), &is.Assignees)
	if is.Labels == nil {
		is.Labels = []string{}
	}
	if is.Assignees == nil {
		is.Assignees = []string{}
	}
	is.IsPullRequest = isPull != 0
	is.CreatedAt, is.UpdatedAt, is.ClosedAt = iso(c), iso(u), iso(cl)
	return is, nil
}

// List returns a page of a repository's imported issues, newest number first.
// A list leaves out each issue's text: the detail has it.
func (s *Store) List(ctx context.Context, repoID string, q Query) (Page, error) {
	if q.PerPage <= 0 {
		q.PerPage = DefaultPerPage
	}
	q.PerPage = min(q.PerPage, MaxPerPage)
	q.Page = max(q.Page, 1)
	pg := Page{Items: []Issue{}, Page: q.Page, PerPage: q.PerPage}
	where, args := []string{"repo_id = ?"}, []any{repoID}
	switch q.State {
	case "open", "closed":
		where, args = append(where, "state = ?"), append(args, q.State)
	case "", "all":
	default:
		return pg, fmt.Errorf("%w: state is open, closed or all", forge.ErrInvalid)
	}
	if t := strings.ToLower(strings.TrimSpace(q.Text)); t != "" {
		if len(t) > 200 {
			return pg, fmt.Errorf("%w: search text is longer than 200 characters", forge.ErrInvalid)
		}
		pat := "%" + escapeLike(t) + "%"
		where = append(where, `(LOWER(title) LIKE ? ESCAPE '\' OR LOWER(body) LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM imported_comments c
			WHERE c.repo_id = imported_issues.repo_id AND c.issue_number = imported_issues.number AND LOWER(c.body) LIKE ? ESCAPE '\'))`)
		args = append(args, pat, pat, pat)
	}
	w := strings.Join(where, " AND ")
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM imported_issues WHERE `+w, args...).Scan(&pg.Total); err != nil {
		return pg, err
	}
	var err error
	if pg.Open, pg.Closed, err = s.stateCounts(ctx, repoID); err != nil {
		return pg, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+issueCols+` FROM imported_issues WHERE `+w+` ORDER BY number DESC LIMIT ? OFFSET ?`,
		append(args, q.PerPage, (q.Page-1)*q.PerPage)...)
	if err != nil {
		return pg, err
	}
	defer rows.Close()
	for rows.Next() {
		is, err := scanIssue(rows)
		if err != nil {
			return pg, err
		}
		is.Body = "" // a list is short; the detail has the text
		pg.Items = append(pg.Items, is)
	}
	return pg, rows.Err()
}

// Get returns one imported issue with its comments in the order they had.
func (s *Store) Get(ctx context.Context, repoID string, number int) (Issue, error) {
	is, err := scanIssue(s.db.QueryRow(ctx, `SELECT `+issueCols+` FROM imported_issues WHERE repo_id = ? AND number = ?`, repoID, number))
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, ErrNotFound
	}
	if err != nil {
		return Issue{}, err
	}
	rows, err := s.db.Query(ctx, `SELECT source_id, author, body, created_at, updated_at FROM imported_comments WHERE repo_id = ? AND issue_number = ? ORDER BY position`, repoID, number)
	if err != nil {
		return Issue{}, err
	}
	defer rows.Close()
	is.Comments = []Comment{}
	for rows.Next() {
		var c Comment
		var cr, up int64
		if err := rows.Scan(&c.ID, &c.Author, &c.Body, &cr, &up); err != nil {
			return Issue{}, err
		}
		c.CreatedAt, c.UpdatedAt = iso(cr), iso(up)
		is.Comments = append(is.Comments, c)
	}
	return is, rows.Err()
}

// stateCounts is how many of a repository's imported issues are open and closed.
func (s *Store) stateCounts(ctx context.Context, repoID string) (open, closed int, err error) {
	rows, err := s.db.Query(ctx, `SELECT state, COUNT(*) FROM imported_issues WHERE repo_id = ? GROUP BY state`, repoID)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return 0, 0, err
		}
		if st == "open" {
			open = n
		} else {
			closed = n
		}
	}
	return open, closed, rows.Err()
}
