package sealing

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/keyring"
)

// batch is how many rows of a column are read and sealed at a time.
const batch = 200

// leaseFor is how long the server that is sealing the old plaintext holds
// the lease; it renews it with every batch, so a server that dies hands the
// work on within this time.
const leaseFor = 30 * time.Second

// Result is what a pass of the migration did.
type Result struct {
	Sealed   int      `json:"sealed"`
	Resealed int      `json:"resealed"`
	Failed   int      `json:"failed"`
	Failures []string `json:"failures,omitempty"`
}

// acquire takes the migration lease if nobody holds it (or its holder let it
// expire). One server wins; the others wait or carry on.
func (s *Service) acquire(ctx context.Context) (bool, error) {
	mine := s.instance + "|" + millis(s.now().Add(leaseFor))
	res, err := s.kdb.Exec(ctx, `INSERT INTO encryption_state(key, value) VALUES('migration_lease', ?) ON CONFLICT(key) DO NOTHING`, mine)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, nil
	}
	cur, ok := s.state(ctx, "migration_lease")
	if !ok {
		return false, nil
	}
	if cur != "" {
		_, exp, _ := strings.Cut(cur, "|")
		if ms, err := strconv.ParseInt(exp, 10, 64); err == nil && ms > s.now().UnixMilli() {
			return false, nil // held, and alive
		}
	}
	res, err = s.kdb.Exec(ctx, `UPDATE encryption_state SET value = ? WHERE key = 'migration_lease' AND value = ?`, mine, cur)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Service) renew(ctx context.Context) {
	_ = s.setState(ctx, "migration_lease", s.instance+"|"+millis(s.now().Add(leaseFor)))
}

func (s *Service) release(ctx context.Context) {
	_, _ = s.kdb.Exec(ctx, `DELETE FROM encryption_state WHERE key = 'migration_lease'`)
}

// migrateAtStart seals what is stored in the clear before the server serves.
// Servers starting together: one takes the lease and does the work in
// batches; the others wait (up to Wait) and see the result. Every row is
// changed by a compare-and-swap, so even a second server that takes over
// from one that died cannot damage a row.
func (s *Service) migrateAtStart(ctx context.Context) error {
	deadline := s.now().Add(s.wait)
	for {
		rem, err := s.remaining(ctx)
		if err != nil {
			return err
		}
		if rem == 0 {
			if !s.complete(ctx) {
				_ = s.setState(ctx, "sealed_complete", "1")
				_ = s.setState(ctx, "migration_completed_at", millis(s.now()))
			}
			return nil
		}
		got, err := s.acquire(ctx)
		if err != nil {
			return err
		}
		if got {
			err := s.runLocked(ctx, false)
			s.release(ctx)
			if err != nil {
				return err
			}
			continue
		}
		if !s.now().Before(deadline) {
			s.log.Warn("another server is still sealing the stored secrets; this one carries on (what is not yet sealed stays readable)")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *Service) runLocked(ctx context.Context, reseal bool) error {
	if _, ok := s.state(ctx, "migration_started_at"); !ok {
		_ = s.setState(ctx, "migration_started_at", millis(s.now()))
	}
	s.passes.Add(1)
	s.scrubStart(ctx)
	res, err := s.pass(ctx, reseal)
	s.scrubEnd(ctx)
	s.sealedRows.Add(int64(res.Sealed))
	if err != nil {
		_ = s.setState(ctx, "migration_last_error", err.Error())
		return err
	}
	if res.Failed > 0 {
		msg := strings.Join(res.Failures, "; ")
		_ = s.setState(ctx, "migration_last_error", msg)
		return fmt.Errorf("%d stored secrets could not be sealed, so this server will not start: %s", res.Failed, msg)
	}
	_, _ = s.kdb.Exec(ctx, `DELETE FROM encryption_state WHERE key = 'migration_last_error'`)
	if s.Encrypted() && (res.Sealed > 0 || res.Resealed > 0) {
		s.audit(ctx, keyring.Event{Action: "encryption.migrated", Target: "stored secrets", Outcome: "ok",
			Detail: fmt.Sprintf("%d stored secrets were sealed and %d re-sealed under a newer data key", res.Sealed, res.Resealed)})
	}
	return nil
}

func (s *Service) audit(ctx context.Context, e keyring.Event) {
	if s.auditFn != nil {
		s.auditFn(ctx, e)
	}
}

// Migrate runs one pass: seals every secret that is stored in the clear, in
// batches, and (with reseal) re-seals those sealed under an older data key
// version. It is safe to repeat and to run beside other servers.
func (s *Service) Migrate(ctx context.Context, reseal bool) (Result, error) {
	if !s.Encrypted() {
		return Result{}, keyring.ErrNoRootKey
	}
	for {
		got, err := s.acquire(ctx)
		if err != nil {
			return Result{}, err
		}
		if got {
			break
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	defer s.release(ctx)
	s.passes.Add(1)
	s.scrubStart(ctx)
	res, err := s.pass(ctx, reseal)
	s.scrubEnd(ctx)
	s.sealedRows.Add(int64(res.Sealed))
	if err == nil && res.Failed == 0 {
		if rem, rerr := s.remaining(ctx); rerr == nil && rem == 0 {
			_ = s.setState(ctx, "sealed_complete", "1")
		}
	}
	return res, err
}

// pass walks the registry once.
func (s *Service) pass(ctx context.Context, reseal bool) (Result, error) {
	var res Result
	for _, c := range Registry {
		cursor := ""
		for {
			type row struct{ key, val string }
			where := ""
			if c.Where != "" {
				where = " AND (" + c.Where + ")"
			}
			rs, err := s.db.Query(ctx, fmt.Sprintf(`SELECT %s, %s FROM %s WHERE %s > ?%s ORDER BY %s LIMIT %d`,
				c.Key, c.Column, c.Table, c.Key, where, c.Key, batch), cursor)
			if err != nil {
				return res, err
			}
			var rows []row
			for rs.Next() {
				var r row
				if err := rs.Scan(&r.key, &r.val); err != nil {
					rs.Close()
					return res, err
				}
				rows = append(rows, r)
			}
			rs.Close()
			if err := rs.Err(); err != nil {
				return res, err
			}
			for _, r := range rows {
				cursor = r.key
				if err := s.sealRow(ctx, c, r.key, r.val, reseal, &res); err != nil {
					return res, err
				}
			}
			s.renew(ctx)
			if len(rows) < batch {
				break
			}
		}
	}
	// The sealing keys of the previous releases have done their work once no
	// row is left that needs them.
	if res.Failed == 0 {
		if err := s.dropLegacyKeys(ctx); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (s *Service) sealRow(ctx context.Context, c Column, key, val string, reseal bool, res *Result) error {
	if val == "" {
		return nil
	}
	var plain []byte
	switch {
	case keyring.IsSealed(val):
		if !reseal {
			return nil
		}
		v, err := keyring.SealedVersion(val)
		if err != nil {
			return nil
		}
		newest, err := s.kr.NewestVersion(ctx, c.Purpose, "")
		if err != nil || v >= newest {
			return err
		}
		if plain, err = s.keys.Open(ctx, c.binding(key), val); err != nil {
			res.Failed++
			res.Failures = append(res.Failures, fmt.Sprintf("%s.%s %q: %v", c.Table, c.Column, key, err))
			return nil
		}
	default:
		var err error
		if plain, err = c.legacyOpen(ctx, s, val); err != nil {
			res.Failed++
			res.Failures = append(res.Failures, fmt.Sprintf("%s.%s %q: %v", c.Table, c.Column, key, err))
			return nil
		}
	}
	sealed, err := s.keys.Seal(ctx, c.binding(key), plain)
	keyring.Zero(plain)
	if err != nil {
		return err
	}
	r, err := s.kdb.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = ? WHERE %s = ? AND %s = ?`, c.Table, c.Column, c.Key, c.Column), sealed, key, val)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n > 0 {
		if keyring.IsSealed(val) {
			res.Resealed++
		} else {
			res.Sealed++
		}
	}
	return nil
}

// dropLegacyKeys deletes the sealing keys of the previous releases (they sat
// beside the data they sealed) once nothing is sealed with them.
func (s *Service) dropLegacyKeys(ctx context.Context) error {
	for _, c := range []struct {
		col  Column
		name string
	}{{hostKeysColumn, "ssh_host_key_seal"}, {webhooksColumn, "webhook_secret_seal"}} {
		var n int
		if err := s.db.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s <> '' AND %s NOT LIKE ?`, c.col.Table, c.col.Column, c.col.Column), keyring.Prefix+"%").Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := s.kdb.Exec(ctx, `DELETE FROM meta WHERE key = ?`, metaRowPrefix+c.name); err != nil {
				return err
			}
		}
	}
	return nil
}

// remaining counts the stored values that are not sealed, across the registry.
func (s *Service) remaining(ctx context.Context) (int, error) {
	n := 0
	for _, c := range Registry {
		where := ""
		if c.Where != "" {
			where = " AND (" + c.Where + ")"
		}
		var k int
		err := s.db.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s <> '' AND %s NOT LIKE ?%s`, c.Table, c.Column, c.Column, where), keyring.Prefix+"%").Scan(&k)
		if err != nil && !isMissingTable(err) {
			return 0, err
		}
		n += k
	}
	return n, nil
}

func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "no such table") || strings.Contains(m, "does not exist")
}

// scrubStart and scrubEnd keep the plaintext that sealing replaces from
// lingering in a SQLite file: freed content is zeroed as it is freed, and
// the write-ahead log, which still holds the old pages, is folded in and
// emptied afterwards. (PostgreSQL keeps old row versions until vacuumed; the
// guide says so.)
func (s *Service) scrubStart(ctx context.Context) {
	if !s.db.Postgres() {
		_, _ = s.kdb.Exec(ctx, `PRAGMA secure_delete = ON`)
	}
}

func (s *Service) scrubEnd(ctx context.Context) {
	if !s.db.Postgres() {
		_, _ = s.kdb.Exec(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	}
}
