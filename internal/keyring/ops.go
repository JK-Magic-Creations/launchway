package keyring

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
)

// Rotation is what rotating a key-encryption key did.
type Rotation struct {
	KEKVersion uint32 `json:"kek_version"`
	Rewrapped  int    `json:"data_keys_rewrapped"`
	Retired    int    `json:"key_encryption_keys_retired"`
}

// maxVersion is the newest key-encryption key version in the database.
func (k *Keyring) maxKEKVersion(ctx context.Context) (uint32, error) {
	var v sql.NullInt64
	if err := k.db.QueryRow(ctx, `SELECT MAX(version) FROM encryption_keks WHERE retired_at = 0`).Scan(&v); err != nil {
		return 0, err
	}
	return u32(v.Int64), nil
}

// RotateKEK creates the next key-encryption key version (wrapped by the
// root key) and re-wraps every data key under it. It runs online: values are
// not touched, readers and writers on any server carry on, and the data
// keys they already hold stay valid because only their wrapping changes.
// Run twice at once, each makes its own version and both finish.
func (k *Keyring) RotateKEK(ctx context.Context) (Rotation, error) {
	next, err := k.maxKEKVersion(ctx)
	if err != nil {
		return Rotation{}, err
	}
	next++
	if err := k.createKEK(ctx, next); err != nil {
		return Rotation{}, err
	}
	nk, err := k.kekByVersion(ctx, next)
	if err != nil {
		return Rotation{}, err
	}
	k.mu.Lock()
	k.curKEK = next
	k.mu.Unlock()
	res := Rotation{KEKVersion: next}
	deks, err := k.DataKeys(ctx)
	if err != nil {
		return res, err
	}
	for _, d := range deks {
		if d.KEKVersion == next {
			continue
		}
		key, err := k.dek(ctx, d.Purpose, d.Org, d.Version)
		if err != nil {
			return res, fmt.Errorf("data key %s/%s version %d: %w", d.Purpose, d.Org, d.Version, err)
		}
		wrapped, err := (&localHolder{key: nk}).Wrap(ctx, key, k.dekAAD(d.Purpose, d.Org, d.Version))
		if err != nil {
			return res, err
		}
		r, err := k.db.Exec(ctx, `UPDATE encryption_deks SET kek_version = ?, wrapped = ? WHERE purpose = ? AND org = ? AND version = ? AND kek_version = ?`,
			int64(next), base64.RawStdEncoding.EncodeToString(wrapped), d.Purpose, d.Org, int64(d.Version), int64(d.KEKVersion))
		if err != nil {
			return res, err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			res.Rewrapped++
		}
	}
	r, err := k.db.Exec(ctx, `UPDATE encryption_keks SET retired_at = ? WHERE version < ? AND retired_at = 0
		AND NOT EXISTS (SELECT 1 FROM encryption_deks WHERE encryption_deks.kek_version = encryption_keks.version)`, k.now().UnixMilli(), int64(next))
	if err != nil {
		return res, err
	}
	if n, _ := r.RowsAffected(); n > 0 {
		res.Retired = int(n)
	}
	k.emit(ctx, Event{Action: "encryption.kek_rotated", Target: fmt.Sprintf("kek/%d", next), Outcome: "ok",
		Detail: fmt.Sprintf("a new key-encryption key was made and %d data keys re-wrapped under it", res.Rewrapped)})
	return res, nil
}

// RotateDataKeys makes the next data key version for every purpose and
// organization that has one. New values are sealed with it at once; the
// stored ones are re-sealed by the engine's re-seal pass.
func (k *Keyring) RotateDataKeys(ctx context.Context) (int, error) {
	deks, err := k.DataKeys(ctx)
	if err != nil {
		return 0, err
	}
	newest := map[[2]string]uint32{}
	for _, d := range deks {
		lk := [2]string{d.Purpose, d.Org}
		if d.Version > newest[lk] {
			newest[lk] = d.Version
		}
	}
	n := 0
	for lk, v := range newest {
		if err := k.createDEK(ctx, lk[0], lk[1], v+1); err != nil {
			return n, err
		}
		k.mu.Lock()
		delete(k.latest, lk)
		k.mu.Unlock()
		n++
	}
	k.emit(ctx, Event{Action: "encryption.dek_rotated", Target: "data keys", Outcome: "ok", Detail: fmt.Sprintf("%d data key versions were made", n)})
	return n, nil
}

// Rewrap makes the primary holder the one that wraps every key-encryption
// key: the procedure for replacing the root key (the primary is the new
// key, a previous holder the old one) or moving to another holder. Each key
// is re-wrapped by itself, and it only changes if it still has the wrapping
// that was read, so servers keep running and a second run finishes what a
// stopped one left. Nothing but the wrapping changes: no value and no data
// key is touched.
func (k *Keyring) Rewrap(ctx context.Context) (int, error) {
	rows, err := k.listKEKs(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		wrapped, err := base64.RawStdEncoding.DecodeString(r.Wrapped)
		if err != nil {
			return n, errors.New("a stored key is damaged")
		}
		aad := k.kekAAD(r.Version)
		if r.Holder == k.primary.Kind() {
			if _, err := k.primary.Unwrap(ctx, wrapped, aad); err == nil {
				continue // already wrapped by the primary
			}
		}
		key, _, err := k.unwrapKEK(ctx, r)
		if err != nil {
			k.emit(ctx, Event{Action: "encryption.rewrap", Target: fmt.Sprintf("kek/%d", r.Version), Outcome: "failed", Detail: err.Error()})
			return n, err
		}
		nw, err := k.primary.Wrap(ctx, key, aad)
		if err != nil {
			return n, err
		}
		res, err := k.db.Exec(ctx, `UPDATE encryption_keks SET holder = ?, wrapped = ? WHERE version = ? AND wrapped = ?`,
			k.primary.Kind(), base64.RawStdEncoding.EncodeToString(nw), int64(r.Version), r.Wrapped)
		if err != nil {
			return n, err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	k.mu.Lock()
	k.rewrapNd = false
	k.mu.Unlock()
	k.emit(ctx, Event{Action: "encryption.rewrap", Target: "key-encryption keys", Outcome: "ok",
		Detail: fmt.Sprintf("%d key-encryption keys are now wrapped by the %s root key", n, k.primary.Kind())})
	return n, nil
}

// DataKeys lists the data keys (never their values).
func (k *Keyring) DataKeys(ctx context.Context) ([]DEKRow, error) {
	rs, err := k.db.Query(ctx, `SELECT purpose, org, version, kek_version, created_at FROM encryption_deks ORDER BY purpose, org, version`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []DEKRow
	for rs.Next() {
		var d DEKRow
		var v, kv int64
		if err := rs.Scan(&d.Purpose, &d.Org, &v, &kv, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.Version, d.KEKVersion = u32(v), u32(kv)
		out = append(out, d)
	}
	return out, rs.Err()
}

// NewestVersion is the newest data key version for a purpose and
// organization (0 if there is none yet).
func (k *Keyring) NewestVersion(ctx context.Context, purpose, org string) (uint32, error) {
	var top sql.NullInt64
	err := k.db.QueryRow(ctx, `SELECT MAX(version) FROM encryption_deks WHERE purpose = ? AND org = ?`, purpose, org).Scan(&top)
	return u32(top.Int64), err
}

// KEKInfo describes a key-encryption key without its value.
type KEKInfo struct {
	Version   uint32 `json:"version"`
	Holder    string `json:"holder"`
	CreatedAt int64  `json:"created_at"`
	Retired   bool   `json:"retired"`
}

// Status is the keyring as the doctor and the settings page show it.
type Status struct {
	Holder       HolderStatus `json:"holder"`
	Installation string       `json:"installation"`
	KEKs         []KEKInfo    `json:"key_encryption_keys"`
	CurrentKEK   uint32       `json:"current_key_encryption_key"`
	DataKeys     []DEKRow     `json:"data_keys"`
	NeedsRewrap  bool         `json:"needs_rewrap"`
	// OldestKeyMillis is when the oldest key still in use was made.
	OldestKeyMillis int64 `json:"oldest_key_at"`
}

// Status reads the keyring's state.
func (k *Keyring) Status(ctx context.Context) (Status, error) {
	st := Status{Holder: k.primary.Status(ctx), Installation: k.installation, NeedsRewrap: k.NeedsRewrap()}
	st.CurrentKEK, _ = k.currentKEK()
	rows, err := k.listKEKs(ctx)
	if err != nil {
		return st, err
	}
	for _, r := range rows {
		st.KEKs = append(st.KEKs, KEKInfo{r.Version, r.Holder, r.CreatedAt, r.Retired != 0})
		if r.Retired == 0 && (st.OldestKeyMillis == 0 || r.CreatedAt < st.OldestKeyMillis) {
			st.OldestKeyMillis = r.CreatedAt
		}
	}
	if st.DataKeys, err = k.DataKeys(ctx); err != nil {
		return st, err
	}
	sort.Slice(st.DataKeys, func(i, j int) bool { return st.DataKeys[i].CreatedAt < st.DataKeys[j].CreatedAt })
	return st, nil
}
