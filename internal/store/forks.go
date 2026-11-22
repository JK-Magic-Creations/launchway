package store

// Forks and template repositories (schemas 75 and 76). The tables are those of
// the forge (forge_repositories, repo_refs, repo_packs, repo_generation) and of
// pull requests, so until the Git hosting moves into the store (stage S3) the
// statements run on the connection the caller hands in, like release_outside's.
//
// A fork shares the packs of its parent by reference: each pack is a row of
// repo_packs of the fork that names the same blob as the parent's row. A blob
// is deleted only when no row of any repository still needs it
// (BlobInUse), which is what keeps "delete the parent" and "collect either"
// safe. See internal/forge/storage/fork.go for the design.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CtxDB runs statements written with ? on the database.
type CtxDB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// CtxTx is a transaction on it.
type CtxTx interface {
	CtxDB
	Commit() error
	Rollback() error
}

// BeginFunc starts a transaction.
type BeginFunc func(ctx context.Context) (CtxTx, error)

// ErrForkSource says the parent changed in a way that makes the copy of its
// manifest unsafe to finish (it is gone, or the child is not empty).
var ErrForkSource = errors.New("the fork could not be made from the parent as it is now")

// ForkLedger is what ForkLedgerCopy did.
type ForkLedger struct {
	Gen   int64 // the child's generation after the copy
	Refs  int
	Packs int
}

// ForkLedgerCopy makes the child's manifest from the parent's, in one
// transaction: the branches and tags of the parent (never its pull request or
// imported refs), and a row for every live pack that names the parent's blob.
// The child must exist (generation 0) and be empty. On PostgreSQL the
// parent's generation row is locked first, as a push and a consolidation lock
// it, so refs and packs are read as one generation's state; on SQLite the
// transaction holds the write lock. Objects pushed to the parent afterwards
// are in packs the child has no row for: they are never reachable through it.
func ForkLedgerCopy(ctx context.Context, begin BeginFunc, postgres bool, parent, child string, now int64, hit func(point string) error) (ForkLedger, error) {
	var out ForkLedger
	if hit == nil {
		hit = func(string) error { return nil }
	}
	tx, err := begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	lock := ""
	if postgres {
		lock = " FOR UPDATE"
	}
	var pgen int64
	var flagged string
	if err := tx.QueryRow(ctx, `SELECT gen, flagged FROM repo_generation WHERE repo_id = ?`+lock, parent).Scan(&pgen, &flagged); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, fmt.Errorf("%w: the parent has no storage", ErrForkSource)
		}
		return out, err
	}
	if flagged != "" {
		return out, fmt.Errorf("%w: the parent is flagged as damaged (%s)", ErrForkSource, flagged)
	}
	var cgen int64
	if err := tx.QueryRow(ctx, `SELECT gen FROM repo_generation WHERE repo_id = ?`+lockOf(postgres), child).Scan(&cgen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, fmt.Errorf("%w: the fork has no storage", ErrForkSource)
		}
		return out, err
	}
	var have int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM repo_refs WHERE repo_id = ?) + (SELECT count(*) FROM repo_packs WHERE repo_id = ?)`, child, child).Scan(&have); err != nil {
		return out, err
	}
	if cgen != 0 || have != 0 {
		return out, fmt.Errorf("%w: the fork already has content", ErrForkSource)
	}
	res, err := tx.Exec(ctx, `INSERT INTO repo_refs(repo_id, name, target, updated_gen)
		SELECT ?, name, target, 1 FROM repo_refs WHERE repo_id = ? AND (name LIKE 'refs/heads/%' OR name LIKE 'refs/tags/%')`, child, parent)
	if err != nil {
		return out, err
	}
	n, _ := res.RowsAffected()
	out.Refs = int(n)
	if err := hit("fork:after-refs"); err != nil {
		return out, err
	}
	res, err = tx.Exec(ctx, `INSERT INTO repo_packs(repo_id, pack_id, blob_key, bytes, objects, state, born_gen, died_gen, created_at, retired_at)
		SELECT ?, pack_id, blob_key, bytes, objects, 'live', 1, 0, ?, 0 FROM repo_packs WHERE repo_id = ? AND state = 'live'`, child, now, parent)
	if err != nil {
		return out, err
	}
	n, _ = res.RowsAffected()
	out.Packs = int(n)
	if err := hit("fork:after-packs"); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE repo_generation SET gen = 1 WHERE repo_id = ? AND gen = 0`, child); err != nil {
		return out, err
	}
	if err := hit("fork:before-commit"); err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	out.Gen = 1
	return out, nil
}

func lockOf(postgres bool) string {
	if postgres {
		return " FOR UPDATE"
	}
	return ""
}

// BlobInUse reports whether any row other than (repo, pack) still needs the
// blob: a live pack of any repository, a pending one registered after
// pendingAfter, or a retired one retired after retiredAfter (Unix ms). Only
// when it says no may the blob be deleted.
func BlobInUse(ctx context.Context, db CtxDB, key, repo, pack string, retiredAfter, pendingAfter int64) (bool, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM repo_packs WHERE blob_key = ? AND NOT (repo_id = ? AND pack_id = ?)
		AND (state = 'live' OR (state = 'pending' AND created_at > ?) OR (state = 'retired' AND retired_at > ?))`,
		key, repo, pack, pendingAfter, retiredAfter).Scan(&n)
	return n > 0, err
}

// PurgeLedger ends a repository's storage: its refs are deleted, every live or
// pending pack is retired (its blob is deleted by the next collection that
// finds no other repository needing it), the repository is flagged so that
// nothing repacks or serves it, and its generation moves. Purging twice is the
// same as once.
func PurgeLedger(ctx context.Context, begin BeginFunc, postgres bool, repo string, now int64, hit func(point string) error) (packs int, err error) {
	if hit == nil {
		hit = func(string) error { return nil }
	}
	tx, err := begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var gen int64
	if err := tx.QueryRow(ctx, `SELECT gen FROM repo_generation WHERE repo_id = ?`+lockOf(postgres), repo).Scan(&gen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM repo_refs WHERE repo_id = ?`, repo); err != nil {
		return 0, err
	}
	res, err := tx.Exec(ctx, `UPDATE repo_packs SET state = 'retired', died_gen = ?, retired_at = ? WHERE repo_id = ? AND state IN ('live', 'pending')`, gen+1, now, repo)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := hit("purge:after-packs"); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE repo_generation SET gen = ?, flagged = 'purged' WHERE repo_id = ?`, gen+1, repo); err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}

// ForkInfo is what a repository's row says about forks, templates and the
// switch. Visibility is the repository's own, for the rules that compare it
// with another's.
type ForkInfo struct {
	ID         string
	Path       string
	Home       string
	Visibility string
	ParentID   string
	RootID     string
	ForkPoint  string
	ForkedBy   string
	ForkedAt   int64
	AllowForks int // -1 follow the default, 0 off, 1 on
	IsTemplate bool
	Trashed    bool
}

const forkCols = `id, path, home, visibility, parent_id, root_id, fork_point, forked_by, forked_at, allow_forks, is_template`

func scanFork(sc interface{ Scan(...any) error }) (ForkInfo, error) {
	var f ForkInfo
	var tpl int
	err := sc.Scan(&f.ID, &f.Path, &f.Home, &f.Visibility, &f.ParentID, &f.RootID, &f.ForkPoint, &f.ForkedBy, &f.ForkedAt, &f.AllowForks, &tpl)
	f.IsTemplate = tpl != 0
	f.Trashed = len(f.Path) >= len(TrashPrefix) && f.Path[:len(TrashPrefix)] == TrashPrefix
	return f, err
}

// TrashPrefix is where the path of a deleted repository starts
// (internal/forge/store.TrashPrefix).
const TrashPrefix = ".trash/"

// ErrNoRepository is returned for an ID or a path no repository has.
var ErrNoRepository = errors.New("no such repository")

// ForkInfoOf reads one repository's fork row by ID.
func ForkInfoOf(ctx context.Context, db CtxDB, id string) (ForkInfo, error) {
	f, err := scanFork(db.QueryRow(ctx, `SELECT `+forkCols+` FROM forge_repositories WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNoRepository
	}
	return f, err
}

// ForksOf lists the repositories forked directly from a parent, live ones
// first by path (those in the trash are left out).
func ForksOf(ctx context.Context, db CtxDB, parent string, limit int) ([]ForkInfo, error) {
	rows, err := db.Query(ctx, `SELECT `+forkCols+` FROM forge_repositories WHERE parent_id = ? AND path NOT LIKE ? ORDER BY path LIMIT `+itoa(limit), parent, TrashPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ForkInfo
	for rows.Next() {
		f, err := scanFork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// CountForksOf counts the live forks of a parent and, with root set, of a
// whole root's network.
func CountForksOf(ctx context.Context, db CtxDB, parent string) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM forge_repositories WHERE parent_id = ? AND path NOT LIKE ?`, parent, TrashPrefix+"%").Scan(&n)
	return n, err
}

// RecordFork says a repository is a fork of a parent, at a commit.
func RecordFork(ctx context.Context, db CtxDB, child, parent, root, point, by string, now int64) error {
	res, err := db.Exec(ctx, `UPDATE forge_repositories SET parent_id = ?, root_id = ?, fork_point = ?, forked_by = ?, forked_at = ? WHERE id = ? AND parent_id = ''`,
		parent, root, point, by, now, child)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: repository %s", ErrNoRepository, child)
	}
	return nil
}

// ForksMadeBy counts the forks a person made since a time (Unix ms), for the
// rate limit that holds across servers.
func ForksMadeBy(ctx context.Context, db CtxDB, by string, since int64) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM forge_repositories WHERE forked_by = ? AND forked_at >= ?`, by, since).Scan(&n)
	return n, err
}

// ForksMadeSince counts every fork made since a time.
func ForksMadeSince(ctx context.Context, db CtxDB, since int64) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM forge_repositories WHERE forked_at >= ?`, since).Scan(&n)
	return n, err
}

// SetAllowForks sets a repository's switch (-1 follows the default).
func SetAllowForks(ctx context.Context, db CtxDB, id string, v int) error {
	res, err := db.Exec(ctx, `UPDATE forge_repositories SET allow_forks = ? WHERE id = ?`, v, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: repository %s", ErrNoRepository, id)
	}
	return nil
}

// SetTemplate marks a repository as a template, or not.
func SetTemplate(ctx context.Context, db CtxDB, id string, on bool) error {
	v := 0
	if on {
		v = 1
	}
	res, err := db.Exec(ctx, `UPDATE forge_repositories SET is_template = ? WHERE id = ?`, v, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: repository %s", ErrNoRepository, id)
	}
	return nil
}

// OwnerAllowForks reads an owner's switch: ok is false when the owner has not
// set one.
func OwnerAllowForks(ctx context.Context, db CtxDB, owner string) (allow bool, ok bool, err error) {
	var v int
	err = db.QueryRow(ctx, `SELECT allow_forks FROM owner_fork_settings WHERE owner = ?`, owner).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return v != 0, err == nil, err
}

// SetOwnerAllowForks sets an owner's switch; unset removes it, so the default
// applies again.
func SetOwnerAllowForks(ctx context.Context, db CtxDB, owner string, allow, unset bool, by string, now int64) error {
	if unset {
		_, err := db.Exec(ctx, `DELETE FROM owner_fork_settings WHERE owner = ?`, owner)
		return err
	}
	v := 0
	if allow {
		v = 1
	}
	_, err := db.Exec(ctx, `INSERT INTO owner_fork_settings(owner, allow_forks, updated_by, updated_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(owner) DO UPDATE SET allow_forks = excluded.allow_forks, updated_by = excluded.updated_by, updated_at = excluded.updated_at`, owner, v, by, now)
	return err
}

// SetPullEdits sets whether maintainers of the target repository may push to a
// pull request's source branch in the fork.
func SetPullEdits(ctx context.Context, db CtxDB, repoID string, number int, on bool) error {
	v := 0
	if on {
		v = 1
	}
	res, err := db.Exec(ctx, `UPDATE pull_requests SET allow_maintainer_edits = ? WHERE repo_id = ? AND number = ?`, v, repoID, number)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("no pull request %d", number)
	}
	return nil
}

// ApproveRunHead records the head commit a person approved to run the
// pipeline of a pull request from a fork.
func ApproveRunHead(ctx context.Context, db CtxDB, repoID string, number int, head string) error {
	res, err := db.Exec(ctx, `UPDATE pull_requests SET run_approved_head = ? WHERE repo_id = ? AND number = ?`, head, repoID, number)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("no pull request %d", number)
	}
	return nil
}

// ForkPull is what the gate that starts pipelines for a fork's pull request
// needs of it.
type ForkPull struct {
	Number       int
	Author       string
	SourceRepo   string // the fork's repository ID; empty: not from a fork
	HeadSHA      string
	ApprovedHead string
	State        string
}

// ForkPullOf reads a pull request of a repository by number.
func ForkPullOf(ctx context.Context, db CtxDB, repoID string, number int) (ForkPull, error) {
	var p ForkPull
	err := db.QueryRow(ctx, `SELECT number, author, source_repo_id, head_sha, run_approved_head, state FROM pull_requests WHERE repo_id = ? AND number = ?`,
		repoID, number).Scan(&p.Number, &p.Author, &p.SourceRepo, &p.HeadSHA, &p.ApprovedHead, &p.State)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("no pull request %d", number)
	}
	return p, err
}

// AuthorHasMergedPull reports whether a person has a pull request merged into
// the repository: a contributor whose work was accepted before is not a
// first-timer.
func AuthorHasMergedPull(ctx context.Context, db CtxDB, repoID, author string) (bool, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM pull_requests WHERE repo_id = ? AND author = ? AND state = 'merged'`, repoID, author).Scan(&n)
	return n > 0, err
}

// OpenPullsFromFork lists the open pull requests whose source is a branch of a
// fork: (repository the pull request is in, number).
type ForkSourceRef struct {
	RepoID string
	Number int
}

// OpenPullsFromFork finds the open pull requests that take their head from a
// branch of the fork.
func OpenPullsFromFork(ctx context.Context, db CtxDB, fork, branch string) ([]ForkSourceRef, error) {
	rows, err := db.Query(ctx, `SELECT repo_id, number FROM pull_requests WHERE source_repo_id = ? AND source_branch = ? AND state = 'open'`, fork, branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ForkSourceRef
	for rows.Next() {
		var r ForkSourceRef
		if err := rows.Scan(&r.RepoID, &r.Number); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RepositoryIDByPath finds a live repository's ID by its connection and path.
func RepositoryIDByPath(ctx context.Context, db CtxDB, path string) (string, error) {
	var id string
	err := db.QueryRow(ctx, `SELECT id FROM forge_repositories WHERE path = ?`, path).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoRepository
	}
	return id, err
}
