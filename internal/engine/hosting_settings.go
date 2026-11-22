package engine

import "github.com/jk-magic-creations/launchway/internal/store"

// The installation's few hosting settings (table hosting_settings, schema 59): a name and a value. The API's
// typed view of them is in internal/api/hosting_settings.go; this is only the way to them.

// HostingSetting returns a setting's value, and whether it was ever set.
func (e *Engine) HostingSetting(name string) (string, bool, error) {
	if !e.knows(schemaBadges) {
		return "", false, nil
	}
	return store.HostingSettingOf(e.db, name)
}

// SetHostingSetting sets a setting.
func (e *Engine) SetHostingSetting(name, value, actor string) error {
	if !e.knows(schemaBadges) {
		return ErrConflict
	}
	return store.SetHostingSettingOf(e.db, name, value, e.now().UnixMilli(), actor)
}
