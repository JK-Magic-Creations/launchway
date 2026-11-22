package engine

import (
	"database/sql"
	"errors"
)

// The installation's few hosting settings (table hosting_settings, schema 59): a name and a value. The API's
// typed view of them is in internal/api/hosting_settings.go; this is only the storage.

// HostingSetting returns a setting's value, and whether it was ever set.
func (e *Engine) HostingSetting(name string) (string, bool, error) {
	if !e.knows(schemaBadges) {
		return "", false, nil
	}
	var v string
	err := e.db.QueryRow(`SELECT value FROM hosting_settings WHERE name = ?`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// SetHostingSetting sets a setting.
func (e *Engine) SetHostingSetting(name, value, actor string) error {
	if !e.knows(schemaBadges) {
		return ErrConflict
	}
	_, err := e.db.Exec(`INSERT INTO hosting_settings(name, value, updated_at, updated_by) VALUES(?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		name, value, e.now().UnixMilli(), actor)
	return err
}
