package keyring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Orphan is a key-encryption key the configured root key cannot open.
type Orphan struct {
	Version   uint32 `json:"version"`
	Holder    string `json:"wrapped_by"`
	CreatedAt int64  `json:"created_at"`
	DataKeys  int    `json:"data_keys_under_it"`
}

// FindOrphans lists the key-encryption keys (not retired) that neither the
// primary holder nor a previous one can open. It reads only. A database with
// no installation id has no keys, so nothing is orphaned.
func FindOrphans(ctx context.Context, db DB, primary Holder, previous []Holder) ([]Orphan, error) {
	if primary == nil {
		return nil, ErrNoRootKey
	}
	k := &Keyring{db: db, primary: primary, previous: previous}
	err := db.QueryRow(ctx, `SELECT value FROM encryption_state WHERE key = 'installation_id'`).Scan(&k.installation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := k.listKEKs(ctx)
	if err != nil {
		return nil, err
	}
	var out []Orphan
	for _, r := range rows {
		if r.Retired != 0 {
			continue
		}
		var wrong *ErrWrongKey
		if _, _, err := k.unwrapKEK(ctx, r); err == nil {
			continue
		} else if !errors.As(err, &wrong) {
			return nil, err
		}
		o := Orphan{Version: r.Version, Holder: r.Holder, CreatedAt: r.CreatedAt}
		var n int64
		if err := db.QueryRow(ctx, `SELECT count(*) FROM encryption_deks WHERE kek_version = ?`, int64(r.Version)).Scan(&n); err != nil {
			return nil, err
		}
		o.DataKeys = int(n)
		out = append(out, o)
	}
	return out, nil
}

// DiscardOrphan removes a key-encryption key and the data keys wrapped by it.
// The caller has proved that nothing is sealed under them.
func DiscardOrphan(ctx context.Context, db DB, version uint32) error {
	if _, err := db.Exec(ctx, `DELETE FROM encryption_deks WHERE kek_version = ?`, int64(version)); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `DELETE FROM encryption_keks WHERE version = ?`, int64(version)); err != nil {
		return fmt.Errorf("removing key-encryption key %d: %w", version, err)
	}
	return nil
}
