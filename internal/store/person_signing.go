package store

import (
	"context"
	"database/sql"
	"errors"
)

// The tables of verified commits and tags (schema 61): signing_keys,
// signature_cache and signing_installation. Like release_outside they are
// pending (stage S2); the statements run on the connection the caller hands
// in. Rows never hold a private key in the clear: the installation's is sealed
// by the caller before it is given here.

// CtxDB is a database as the person-facing tables use it.
type CtxDB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// SigningKeyRow is one row of signing_keys.
type SigningKeyRow struct {
	ID, Owner, Kind, Name, Fingerprint, PublicText string
	CreatedAt, ExpiresAt, RevokedAt                int64
}

const signingKeyCols = `id, owner, kind, name, fingerprint, public_text, created_at, expires_at, revoked_at`

func scanSigningKey(r interface{ Scan(...any) error }) (SigningKeyRow, error) {
	var k SigningKeyRow
	err := r.Scan(&k.ID, &k.Owner, &k.Kind, &k.Name, &k.Fingerprint, &k.PublicText, &k.CreatedAt, &k.ExpiresAt, &k.RevokedAt)
	return k, err
}

// CountSigningKeys counts a person's keys.
func CountSigningKeys(ctx context.Context, db CtxDB, owner string) (n int, err error) {
	err = db.QueryRow(ctx, `SELECT count(*) FROM signing_keys WHERE owner = ? AND kind IN ('ssh', 'openpgp')`, owner).Scan(&n)
	return
}

// InsertSigningKey stores a key.
func InsertSigningKey(ctx context.Context, db CtxDB, k SigningKeyRow) error {
	_, err := db.Exec(ctx, `INSERT INTO signing_keys(`+signingKeyCols+`) VALUES(?,?,?,?,?,?,?,?,0)`, k.ID, k.Owner, k.Kind, k.Name, k.Fingerprint, k.PublicText, k.CreatedAt, k.ExpiresAt)
	return err
}

// SigningKey finds a key by ID; ok is false when there is none.
func SigningKey(ctx context.Context, db CtxDB, id string) (SigningKeyRow, bool, error) {
	k, err := scanSigningKey(db.QueryRow(ctx, `SELECT `+signingKeyCols+` FROM signing_keys WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return k, false, nil
	}
	return k, err == nil, err
}

// SigningKeyByFingerprint finds a key by its fingerprint.
func SigningKeyByFingerprint(ctx context.Context, db CtxDB, fp string) (SigningKeyRow, bool, error) {
	k, err := scanSigningKey(db.QueryRow(ctx, `SELECT `+signingKeyCols+` FROM signing_keys WHERE fingerprint = ?`, fp))
	if errors.Is(err, sql.ErrNoRows) {
		return k, false, nil
	}
	return k, err == nil, err
}

// SigningKeysOf lists a person's keys, oldest first.
func SigningKeysOf(ctx context.Context, db CtxDB, owner string) ([]SigningKeyRow, error) {
	rows, err := db.Query(ctx, `SELECT `+signingKeyCols+` FROM signing_keys WHERE owner = ? AND kind <> 'openpgp-subkey' ORDER BY created_at, id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SigningKeyRow
	for rows.Next() {
		k, err := scanSigningKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeSigningKey marks a key revoked from a time, if it is not yet; it
// reports whether a row changed.
func RevokeSigningKey(ctx context.Context, db CtxDB, id string, at int64) (bool, error) {
	res, err := db.Exec(ctx, `UPDATE signing_keys SET revoked_at = ? WHERE (id = ? OR (kind = 'openpgp-subkey' AND EXISTS (SELECT 1 FROM signing_keys p WHERE p.id = ? AND p.owner = signing_keys.owner AND p.public_text = signing_keys.public_text))) AND revoked_at = 0`, at, id, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteSigningKey removes a key; it reports whether there was one.
func DeleteSigningKey(ctx context.Context, db CtxDB, id string) (bool, error) {
	res, err := db.Exec(ctx, `DELETE FROM signing_keys WHERE id = ? OR (kind = 'openpgp-subkey' AND EXISTS (SELECT 1 FROM signing_keys p WHERE p.id = ? AND p.owner = signing_keys.owner AND p.public_text = signing_keys.public_text))`, id, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SignatureCacheRow is the cryptographic verdict on one object.
type SignatureCacheRow struct {
	Object, SigType, Fingerprint, Reason string
	CryptoOK                             bool
}

// SignatureCached returns the cached verdict on an object.
func SignatureCached(ctx context.Context, db CtxDB, object string) (SignatureCacheRow, bool) {
	r := SignatureCacheRow{Object: object}
	var ok int
	if err := db.QueryRow(ctx, `SELECT sig_type, key_fingerprint, crypto_ok, reason FROM signature_cache WHERE object_sha = ?`, object).Scan(&r.SigType, &r.Fingerprint, &ok, &r.Reason); err != nil {
		return SignatureCacheRow{}, false
	}
	r.CryptoOK = ok != 0
	return r, true
}

// PutSignatureCache remembers a verdict; the first one stays.
func PutSignatureCache(ctx context.Context, db CtxDB, r SignatureCacheRow, at int64) {
	ok := 0
	if r.CryptoOK {
		ok = 1
	}
	_, _ = db.Exec(ctx, `INSERT INTO signature_cache(object_sha, sig_type, key_fingerprint, crypto_ok, reason, checked_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(object_sha) DO NOTHING`, r.Object, r.SigType, r.Fingerprint, ok, r.Reason, at)
}

// InstallationKeyRow is the installation's own signing key as stored.
type InstallationKeyRow struct {
	PublicText, Fingerprint, Sealed string
	CreatedAt                       int64
}

// InstallationKey returns the installation's key row, if it has one.
func InstallationKey(ctx context.Context, db CtxDB, id string) (InstallationKeyRow, bool, error) {
	var r InstallationKeyRow
	err := db.QueryRow(ctx, `SELECT public_text, fingerprint, sealed, created_at FROM signing_installation WHERE id = ?`, id).Scan(&r.PublicText, &r.Fingerprint, &r.Sealed, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// RowSealer seals a secret bound to its row (internal/sealing's Box).
type RowSealer interface {
	Seal(ctx context.Context, row string, plain []byte) (string, error)
}

// InsertInstallationKey seals the installation's private key (its seed) and
// stores it with its public half; the first writer wins.
func InsertInstallationKey(ctx context.Context, db CtxDB, seal RowSealer, id, algo string, pub, fingerprint string, seed []byte, at int64) error {
	sealed, err := seal.Seal(ctx, id, seed)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO signing_installation(id, algo, public_text, fingerprint, sealed, created_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(id) DO NOTHING`, id, algo, pub, fingerprint, sealed, at)
	return err
}
