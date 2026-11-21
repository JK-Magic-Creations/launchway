package sealing

import (
	"context"

	"github.com/jk-magic-creations/launchway/internal/keyring"
)

// Rotation is what `launchway encryption rotate` did.
type Rotation struct {
	Keys keyring.Rotation `json:"keys"`
	// DataKeys is the number of data key versions made (with data rotation).
	DataKeys int `json:"data_keys_made"`
	// Resealed is the number of stored values re-sealed under the newest data key.
	Resealed int `json:"values_resealed"`
}

// Rotate makes a new key-encryption key version and re-wraps every data key
// under it, online: nothing a running server reads or writes is blocked or
// changed. With data it also makes new data key versions and re-seals every
// stored value under them (in batches; old versions stay readable until the
// pass has finished).
func (s *Service) Rotate(ctx context.Context, data bool) (Rotation, error) {
	var out Rotation
	if !s.Encrypted() {
		return out, keyring.ErrNoRootKey
	}
	var err error
	if out.Keys, err = s.kr.RotateKEK(ctx); err != nil {
		return out, err
	}
	if !data {
		return out, nil
	}
	if out.DataKeys, err = s.kr.RotateDataKeys(ctx); err != nil {
		return out, err
	}
	res, err := s.Migrate(ctx, true)
	out.Resealed = res.Resealed
	return out, err
}

// Rewrap makes the configured root key the one that wraps the key-encryption
// keys (root key replacement; see keyring.Rewrap).
func (s *Service) Rewrap(ctx context.Context) (int, error) {
	if !s.Encrypted() {
		return 0, keyring.ErrNoRootKey
	}
	return s.kr.Rewrap(ctx)
}

// Drill is the result of VerifyAll.
type Drill struct {
	Opened int      `json:"opened"`
	Failed int      `json:"failed"`
	Errors []string `json:"errors,omitempty"`
}

// VerifyAll opens every sealed secret in the registry, reading only. It
// proves that this root key and this database belong together: the restore
// drill (`launchway doctor encryption --restore-drill`). Nothing is changed.
func (s *Service) VerifyAll(ctx context.Context) (Drill, error) {
	var d Drill
	if !s.Encrypted() {
		return d, keyring.ErrNoRootKey
	}
	for _, c := range Registry {
		where := ""
		if c.Where != "" {
			where = " AND (" + c.Where + ")"
		}
		rs, err := s.db.Query(ctx, "SELECT "+c.Key+", "+c.Column+" FROM "+c.Table+" WHERE "+c.Column+" LIKE ?"+where, keyring.Prefix+"%")
		if err != nil {
			if isMissingTable(err) {
				continue
			}
			return d, err
		}
		type row struct{ k, v string }
		var rows []row
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.k, &r.v); err != nil {
				rs.Close()
				return d, err
			}
			rows = append(rows, r)
		}
		rs.Close()
		for _, r := range rows {
			plain, err := s.keys.Open(ctx, c.binding(r.k), r.v)
			keyring.Zero(plain)
			if err != nil {
				d.Failed++
				d.Errors = append(d.Errors, c.Table+"."+c.Column+" "+r.k+": "+err.Error())
				continue
			}
			d.Opened++
		}
	}
	return d, nil
}
