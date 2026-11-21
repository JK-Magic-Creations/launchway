package pipeline

// Who may decide an approval, and how many distinct people must approve.
//
//	approve-prod:
//	  kind: approval
//	  needs: smoke-test
//	  message: Deploy to production?
//	  approvers: { groups: [release-managers], users: [], count: 2 }
//
// groups and users only narrow who may decide: the caller must still have
// the operator role on the project (and, for an approval that names a
// deployment, on its environment). count is how many distinct people must
// approve (1 to MaxApprovers); anyone allowed may reject, which ends the
// step at once.

import (
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaxApprovers is the most distinct approvals one step may ask for.
const MaxApprovers = 5

// maxApproverName bounds a group or user name.
const maxApproverName = 200

// Approvers limits who may decide an approval and asks for more than one
// approval.
type Approvers struct {
	Groups []string `json:"groups,omitempty"`
	Users  []string `json:"users,omitempty"`
	// Count is how many distinct people must approve; 1 when not written.
	Count int `json:"count"`
}

// Restricts reports whether the rule limits who may decide.
func (a *Approvers) Restricts() bool { return a != nil && (len(a.Groups) > 0 || len(a.Users) > 0) }

// Allows reports whether a person, with the groups the identity provider
// gave them, may decide: always when the rule names nobody.
func (a *Approvers) Allows(user string, groups []string) bool {
	if !a.Restricts() {
		return true
	}
	return slices.Contains(a.Users, user) || slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(a.Groups, g) })
}

// Describe says who may decide, for messages.
func (a *Approvers) Describe() string {
	var parts []string
	if len(a.Groups) > 0 {
		parts = append(parts, "members of the group "+strings.Join(a.Groups, " or "))
	}
	if len(a.Users) > 0 {
		parts = append(parts, "the user "+strings.Join(a.Users, " or "))
	}
	return strings.Join(parts, ", or ")
}

// parseApprovers reads approvers: { groups, users, count }.
func (ps *parser) parseApprovers(n *yaml.Node, at string) *Approvers {
	a := &Approvers{Count: 1}
	if n.Kind != yaml.MappingNode {
		ps.add(n, at, "approvers is a mapping, for example: approvers: { groups: [release-managers], count: 2 }")
		return a
	}
	ps.eachKey(n, at, []string{"groups", "users", "count"}, func(k, v *yaml.Node) {
		w := at + "." + k.Value
		switch k.Value {
		case "groups":
			a.Groups = ps.approverNames(v, w)
		case "users":
			a.Users = ps.approverNames(v, w)
		case "count":
			a.Count = ps.int(v, w)
			if a.Count < 1 || a.Count > MaxApprovers {
				ps.add(v, w, "count is between 1 and %d", MaxApprovers)
			}
		}
	})
	if len(a.Groups) == 0 && len(a.Users) > 0 && a.Count > len(a.Users) && a.Count <= MaxApprovers {
		ps.add(n, at+".count", "count is %d but only %d user(s) may approve, so the step could never be approved; name more users, add a group, or lower count", a.Count, len(a.Users))
	}
	return a
}

// approverNames reads a list of group or user names, as bindings and the
// identity provider write them.
func (ps *parser) approverNames(n *yaml.Node, at string) []string {
	var out []string
	for _, name := range ps.strList(n, at) {
		switch {
		case strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\r\n\t") || len(name) > maxApproverName:
			ps.add(n, at, "%q is not a name: write a group or user as it is in the bindings, on one line, at most %d characters", clipText(name, 40), maxApproverName)
		case slices.Contains(out, name):
			ps.add(n, at, "%q is listed twice", name)
		default:
			out = append(out, name)
		}
	}
	return out
}
