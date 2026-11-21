// Package refrules keeps the protected branches of hosted repositories (table
// branch_rules, schema 31) and judges a push against them.
//
// A protected branch takes no direct push except from an administrator of the
// repository, and cannot be deleted. Force pushes are refused for every
// branch by the storage itself. Rules are patterns on the branch name, where
// * matches any run of characters except a slash. Status checks, approvals
// and merge rules arrive with pull requests; this is the part that is needed
// to keep a main branch safe today.
package refrules

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// DB is what the store needs of a database.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
}

// Store is the rules' table.
type Store struct {
	db  DB
	now func() time.Time
}

// New makes a store over a database.
func New(db DB) *Store { return &Store{db: db, now: time.Now} }

// Patterns returns a repository's protected branch patterns, sorted.
func (s *Store) Patterns(ctx context.Context, repoID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT pattern FROM branch_rules WHERE repo_id = ? ORDER BY pattern`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Validate checks a pattern.
func Validate(p string) error {
	switch {
	case p == "" || len(p) > 200 || strings.ContainsAny(p, " \\[]?\x00") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "refs/"):
		return fmt.Errorf("%q is not a branch name or pattern: use names such as main or release/*", p)
	}
	if _, err := path.Match(p, "x"); err != nil {
		return fmt.Errorf("%q is not a valid pattern", p)
	}
	return nil
}

// Set replaces a repository's protected branches with patterns.
func (s *Store) Set(ctx context.Context, repoID string, patterns []string, by string) error {
	seen := map[string]bool{}
	var clean []string
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if err := Validate(p); err != nil {
			return err
		}
		if !seen[p] {
			seen[p] = true
			clean = append(clean, p)
		}
	}
	if len(clean) > 100 {
		return fmt.Errorf("at most 100 protected branch patterns")
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM branch_rules WHERE repo_id = ?`, repoID); err != nil {
		return err
	}
	for _, p := range clean {
		if _, err := s.db.Exec(ctx, `INSERT INTO branch_rules(id, repo_id, pattern, push_role, created_at, created_by) VALUES(?,?,?,?,?,?)`,
			ids.New("rul"), repoID, p, "admin", s.now().UnixMilli(), by); err != nil {
			return err
		}
	}
	return nil
}

// Update is one ref a push moves.
type Update struct{ Name, Old, New string }

// Match reports whether a ref is a branch that a pattern protects.
func Match(patterns []string, ref string) (string, bool) {
	br, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok {
		return "", false
	}
	for _, p := range patterns {
		if m, _ := path.Match(p, br); m {
			return br, true
		}
	}
	return "", false
}

// Check judges a push: nil if it may go on, otherwise the sentence the pusher
// is told. isAdmin says whether the pusher administers the repository.
func Check(patterns []string, updates []Update, isAdmin bool) error {
	for _, u := range updates {
		br, protected := Match(patterns, u.Name)
		if !protected {
			continue
		}
		if strings.Trim(u.New, "0") == "" {
			return fmt.Errorf("the branch %s is protected and cannot be deleted; an administrator can remove its protection first", br)
		}
		if !isAdmin {
			return fmt.Errorf("the branch %s is protected: only an administrator of the repository may push to it directly; push to another branch and ask an administrator to merge it", br)
		}
	}
	return nil
}
