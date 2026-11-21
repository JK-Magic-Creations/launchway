package auth

import "strings"

// LocalGroupPrefix starts the name of the groups that carry the role of a
// built-in account. Providers' groups never start with it: sign-in through
// an identity provider drops any group that does (see StripReservedGroups),
// so no provider can grant itself a built-in role.
const LocalGroupPrefix = "launchway:local:"

// LocalGroup is the group a built-in account of that role belongs to.
func LocalGroup(r Role) string { return LocalGroupPrefix + r.String() }

// StripReservedGroups removes the groups only built-in accounts may have.
func StripReservedGroups(groups []string) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		if !strings.HasPrefix(g, LocalGroupPrefix) {
			out = append(out, g)
		}
	}
	return out
}
