package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// PackState is where a pack is in its life.
type PackState string

const (
	// PackPending: uploaded and registered, no ref points into it yet. A push
	// that dies leaves one; maintenance collects it after a day.
	PackPending PackState = "pending"
	// PackLive: part of the repository.
	PackLive PackState = "live"
	// PackRetired: replaced by a consolidated pack; its blob is deleted after
	// the grace period.
	PackRetired PackState = "retired"
	// PackOrphan: its blob is gone.
	PackOrphan PackState = "orphan"
)

// Pack is one row of the manifest.
type Pack struct {
	ID      string // SHA-256 of the pack file, in hex
	BlobKey string
	Bytes   int64
	Objects int64
	State   PackState
	Born    int64 // the generation that made it live
	Died    int64 // the generation that retired it
}

// RefUpdate is one line of a push. Old and New are full object names; all
// zeros (or empty) means "does not exist".
type RefUpdate struct{ Name, Old, New string }

// ErrStale means a compare-and-swap failed: a ref is not where the pusher said
// it was. Nothing was changed. It is what the loser of a race gets.
type ErrStale struct{ Ref, Have, Want string }

func (e *ErrStale) Error() string {
	return fmt.Sprintf("the branch moved: %s is at %s, not at %s", e.Ref, short(e.Have), short(e.Want))
}

// ErrNotFound is returned for a repository the database does not know.
var ErrNotFound = errors.New("repository not found")

func short(s string) string {
	switch {
	case s == "":
		return "(nothing)"
	case len(s) > 10:
		return s[:10]
	}
	return s
}

func isZero(s string) bool { return strings.Trim(s, "0") == "" }

func norm(s string) string {
	if isZero(s) {
		return ""
	}
	return s
}

func nowMS() int64 { return time.Now().UnixMilli() }

// Ledger is the database side of the storage.
type Ledger struct {
	db *engine.Database
}

// NewLedger wraps the engine's database.
func NewLedger(db *engine.Database) *Ledger { return &Ledger{db: db} }

// Create makes a repository exist: generation 0, no refs. Creating one that
// exists changes nothing.
func (l *Ledger) Create(ctx context.Context, repo string) error {
	_, err := l.db.Exec(ctx, `INSERT INTO repo_generation(repo_id, gen, created_at) VALUES(?, 0, ?) ON CONFLICT(repo_id) DO NOTHING`, repo, nowMS())
	return err
}

// Exists reports whether the repository is known.
func (l *Ledger) Exists(ctx context.Context, repo string) (bool, error) {
	var n int
	err := l.db.QueryRow(ctx, `SELECT count(*) FROM repo_generation WHERE repo_id = ?`, repo).Scan(&n)
	return n > 0, err
}

// Generation reads the repository's generation: the one cheap read a cache
// checks itself against.
func (l *Ledger) Generation(ctx context.Context, repo string) (int64, error) {
	var g int64
	err := l.db.QueryRow(ctx, `SELECT gen FROM repo_generation WHERE repo_id = ?`, repo).Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: %s", ErrNotFound, repo)
	}
	return g, err
}

// RegisterPack records an uploaded pack as pending. Registering a pack that is
// already there (a retried push, the same content pushed twice) changes
// nothing.
func (l *Ledger) RegisterPack(ctx context.Context, repo string, p Pack) error {
	_, err := l.db.Exec(ctx, `INSERT INTO repo_packs(repo_id, pack_id, blob_key, bytes, objects, state, created_at)
		VALUES(?, ?, ?, ?, ?, 'pending', ?) ON CONFLICT(repo_id, pack_id) DO NOTHING`, repo, p.ID, p.BlobKey, p.Bytes, p.Objects, nowMS())
	return err
}

// Commit is the commit point of a push. In one transaction it compares and
// swaps every ref, makes the pack live (if the push had one) and bumps the
// generation. When any ref is not where the pusher said, it changes nothing and
// returns *ErrStale. On PostgreSQL the generation row is locked first, so
// pushes to one repository are serialised; on SQLite the transaction holds the
// write lock. faults.Hit is called between the statements, so tests can end a
// process, or fail the connection, at each of them.
func (l *Ledger) Commit(ctx context.Context, repo string, ups []RefUpdate, pack string, f Faults) (int64, error) {
	tx, err := l.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	lock := ""
	if l.db.Postgres() {
		lock = " FOR UPDATE"
	}
	var gen int64
	if err := tx.QueryRow(ctx, `SELECT gen FROM repo_generation WHERE repo_id = ?`+lock, repo).Scan(&gen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("%w: %s", ErrNotFound, repo)
		}
		return 0, err
	}
	for _, u := range ups {
		var cur string
		err := tx.QueryRow(ctx, `SELECT target FROM repo_refs WHERE repo_id = ? AND name = ?`, repo, u.Name).Scan(&cur)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if cur != norm(u.Old) {
			return 0, &ErrStale{Ref: u.Name, Have: cur, Want: norm(u.Old)}
		}
	}
	if err := f.Hit(PtTxAfterCheck); err != nil {
		return 0, err
	}
	for _, u := range ups {
		if isZero(u.New) {
			_, err = tx.Exec(ctx, `DELETE FROM repo_refs WHERE repo_id = ? AND name = ?`, repo, u.Name)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO repo_refs(repo_id, name, target, updated_gen) VALUES(?, ?, ?, ?)
				ON CONFLICT(repo_id, name) DO UPDATE SET target = excluded.target, updated_gen = excluded.updated_gen`, repo, u.Name, u.New, gen+1)
		}
		if err != nil {
			return 0, err
		}
	}
	if err := f.Hit(PtTxAfterRefs); err != nil {
		return 0, err
	}
	if pack != "" {
		res, err := tx.Exec(ctx, `UPDATE repo_packs SET state = 'live', born_gen = ? WHERE repo_id = ? AND pack_id = ? AND state IN ('pending', 'live')`, gen+1, repo, pack)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return 0, fmt.Errorf("pack %s is not registered; refusing to point refs at objects nobody can fetch", short(pack))
		}
	}
	if err := f.Hit(PtTxAfterPack); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE repo_generation SET gen = ? WHERE repo_id = ?`, gen+1, repo); err != nil {
		return 0, err
	}
	if err := f.Hit(PtTxBeforeCommit); err != nil {
		return 0, err
	}
	if err := f.Drop("before"); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if err := f.Drop("after"); err != nil {
		return gen + 1, err
	}
	if err := f.Hit(PtTxAfterCommit); err != nil {
		return gen + 1, err
	}
	return gen + 1, nil
}

// Snapshot is a consistent reading of a repository.
type Snapshot struct {
	Gen     int64
	Refs    map[string]string
	Packs   []Pack // live, oldest first
	Flagged string // why the repository is flagged, "" if not
}

// Snapshot reads the generation, the refs and the live packs, and reads the
// generation again: if it moved, a push committed in between and the reading
// is taken afresh, so what is returned is one generation's state.
func (l *Ledger) Snapshot(ctx context.Context, repo string) (Snapshot, error) {
	for attempt := 0; attempt < 8; attempt++ {
		s := Snapshot{Refs: map[string]string{}}
		if err := l.db.QueryRow(ctx, `SELECT gen, flagged FROM repo_generation WHERE repo_id = ?`, repo).Scan(&s.Gen, &s.Flagged); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return s, fmt.Errorf("%w: %s", ErrNotFound, repo)
			}
			return s, err
		}
		if err := l.readRefs(ctx, repo, s.Refs); err != nil {
			return s, err
		}
		packs, err := l.readPacks(ctx, repo)
		if err != nil {
			return s, err
		}
		s.Packs = packs
		var again int64
		if err := l.db.QueryRow(ctx, `SELECT gen FROM repo_generation WHERE repo_id = ?`, repo).Scan(&again); err != nil {
			return s, err
		}
		if again == s.Gen {
			return s, nil
		}
	}
	return Snapshot{}, errors.New("the repository kept changing while it was being read")
}

func (l *Ledger) readRefs(ctx context.Context, repo string, into map[string]string) error {
	rows, err := l.db.Query(ctx, `SELECT name, target FROM repo_refs WHERE repo_id = ?`, repo)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n, t string
		if err := rows.Scan(&n, &t); err != nil {
			return err
		}
		into[n] = t
	}
	return rows.Err()
}

func (l *Ledger) readPacks(ctx context.Context, repo string) ([]Pack, error) {
	rows, err := l.db.Query(ctx, `SELECT pack_id, blob_key, bytes, objects, state, born_gen, died_gen FROM repo_packs WHERE repo_id = ? AND state = 'live' ORDER BY born_gen, pack_id`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pack
	for rows.Next() {
		var p Pack
		var st string
		if err := rows.Scan(&p.ID, &p.BlobKey, &p.Bytes, &p.Objects, &st, &p.Born, &p.Died); err != nil {
			return nil, err
		}
		p.State = PackState(st)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Consolidate adds a consolidated pack as live and retires every live pack
// born at or before snapshotGen, in one transaction, bumping the generation so
// caches notice. Packs that arrived after the snapshot stay live: the
// consolidated pack does not contain them.
func (l *Ledger) Consolidate(ctx context.Context, repo string, snapshotGen int64, np Pack) error {
	tx, err := l.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	lock := ""
	if l.db.Postgres() {
		lock = " FOR UPDATE"
	}
	var gen int64
	if err := tx.QueryRow(ctx, `SELECT gen FROM repo_generation WHERE repo_id = ?`+lock, repo).Scan(&gen); err != nil {
		return err
	}
	now := nowMS()
	if _, err := tx.Exec(ctx, `UPDATE repo_packs SET state = 'retired', died_gen = ?, retired_at = ? WHERE repo_id = ? AND state = 'live' AND born_gen <= ? AND pack_id <> ?`,
		gen+1, now, repo, snapshotGen, np.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE repo_packs SET state = 'live', born_gen = ? WHERE repo_id = ? AND pack_id = ? AND state IN ('pending', 'live')`, gen+1, repo, np.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE repo_generation SET gen = ? WHERE repo_id = ?`, gen+1, repo); err != nil {
		return err
	}
	return tx.Commit()
}

// packsWhere lists packs of a repository in a state, older than a cut-off
// (Unix ms) on the given timestamp column.
func (l *Ledger) packsWhere(ctx context.Context, repo string, state PackState, col string, before int64) ([]Pack, error) {
	rows, err := l.db.Query(ctx, `SELECT pack_id, blob_key, bytes, objects FROM repo_packs WHERE repo_id = ? AND state = ? AND `+col+` <= ?`, repo, string(state), before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pack
	for rows.Next() {
		p := Pack{State: state}
		if err := rows.Scan(&p.ID, &p.BlobKey, &p.Bytes, &p.Objects); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Retired lists packs retired at or before the cut-off.
func (l *Ledger) Retired(ctx context.Context, repo string, before int64) ([]Pack, error) {
	return l.packsWhere(ctx, repo, PackRetired, "retired_at", before)
}

// Pending lists pending packs registered at or before the cut-off.
func (l *Ledger) Pending(ctx context.Context, repo string, before int64) ([]Pack, error) {
	return l.packsWhere(ctx, repo, PackPending, "created_at", before)
}

// SetPackState moves a pack from one state to another.
func (l *Ledger) SetPackState(ctx context.Context, repo, pack string, from, to PackState) error {
	_, err := l.db.Exec(ctx, `UPDATE repo_packs SET state = ? WHERE repo_id = ? AND pack_id = ? AND state = ?`, string(to), repo, pack, string(from))
	return err
}

// Lease takes the repository's maintenance lease for d, or reports that
// another holder has it.
func (l *Ledger) Lease(ctx context.Context, repo, holder string, d time.Duration) (bool, error) {
	now := nowMS()
	res, err := l.db.Exec(ctx, `INSERT INTO repo_maintenance(repo_id, holder, until_at) VALUES(?, ?, ?)
		ON CONFLICT(repo_id) DO UPDATE SET holder = excluded.holder, until_at = excluded.until_at
		WHERE repo_maintenance.until_at < ? OR repo_maintenance.holder = excluded.holder`, repo, holder, now+d.Milliseconds(), now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Flag marks a repository as damaged, with the reason, so that people see it
// and maintenance does not make it worse. An empty reason clears the flag.
func (l *Ledger) Flag(ctx context.Context, repo, why string) error {
	_, err := l.db.Exec(ctx, `UPDATE repo_generation SET flagged = ? WHERE repo_id = ?`, why, repo)
	return err
}

// Usage is what a repository takes in the blob store.
type Usage struct {
	LiveBytes    int64
	PendingBytes int64
	RetiredBytes int64
	LivePacks    int
	Objects      int64
}

// Usage sums the manifest.
func (l *Ledger) Usage(ctx context.Context, repo string) (Usage, error) {
	var u Usage
	rows, err := l.db.Query(ctx, `SELECT state, count(*), COALESCE(sum(bytes), 0), COALESCE(sum(objects), 0) FROM repo_packs WHERE repo_id = ? GROUP BY state`, repo)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		var b, o int64
		if err := rows.Scan(&st, &n, &b, &o); err != nil {
			return u, err
		}
		switch PackState(st) {
		case PackLive:
			u.LiveBytes, u.LivePacks, u.Objects = b, n, o
		case PackPending:
			u.PendingBytes = b
		case PackRetired:
			u.RetiredBytes = b
		}
	}
	return u, rows.Err()
}
