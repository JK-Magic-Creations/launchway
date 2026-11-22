package forge

import "time"

// PermissionCheck is one permission a feature needs and whether the credential has it.
type PermissionCheck struct {
	Feature string `json:"feature"`
	// Needs is the permission to grant, in the host's words.
	Needs string `json:"needs"`
	// OK is true only for a permission the test verified. NotChecked says the test could not tell (the host's
	// API names no scopes and Launchway writes nothing to test): then OK is false and the permission is neither
	// claimed nor reported missing. Three states: ok, lacking (both false), not checked.
	OK         bool   `json:"ok"`
	NotChecked bool   `json:"not_checked,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// The three states of a permission check, as the API and the pages name them.
const (
	CheckOK         = "ok"
	CheckLacking    = "lacking"
	CheckNotChecked = "not_checked"
)

// State is the check's state: CheckOK, CheckLacking or CheckNotChecked.
func (c PermissionCheck) State() string {
	switch {
	case c.OK && !c.NotChecked:
		return CheckOK
	case c.NotChecked:
		return CheckNotChecked
	}
	return CheckLacking
}

// HostReport is what "test connection" says, in words for people.
type HostReport struct {
	Account       string            `json:"account"`
	TokenExpires  *time.Time        `json:"token_expires,omitempty"`
	RateRemaining int               `json:"rate_remaining"`
	Checks        []PermissionCheck `json:"checks"`
	// Repositories is how many the credential reaches on the first page, narrowed to the allow-list.
	Repositories int `json:"repositories"`
}
