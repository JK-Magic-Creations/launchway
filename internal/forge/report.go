package forge

import "time"

// PermissionCheck is one permission a feature needs and whether the credential has it.
type PermissionCheck struct {
	Feature string `json:"feature"`
	// Needs is the permission to grant, in the host's words.
	Needs  string `json:"needs"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
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
