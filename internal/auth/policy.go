// Package auth decides who someone is and what they may do: roles and the
// bindings that grant them, OpenID Connect sign-in, and the small
// cryptographic helpers that sessions and API tokens need.
package auth

import (
	"errors"
	"fmt"
	"path"
	"slices"
)

// Role is what someone may do on a project. Each role includes the ones
// before it.
type Role int

const (
	NoRole   Role = iota
	Viewer        // read everything about the project
	Operator      // and start, cancel, resume, approve and reject runs
	Admin         // and read the audit trail, and anything administrative
)

var roleNames = []string{"", "viewer", "operator", "admin"}

func (r Role) String() string {
	if r < NoRole || int(r) >= len(roleNames) {
		return fmt.Sprintf("role(%d)", int(r))
	}
	return roleNames[r]
}

// ParseRole reads viewer, operator or admin.
func ParseRole(s string) (Role, error) {
	for i, n := range roleNames {
		if n != "" && n == s {
			return Role(i), nil
		}
	}
	return NoRole, fmt.Errorf("role %q is not one of viewer, operator, admin", s)
}

// Binding grants a role to one group or one user, on the projects whose
// names match one of Projects, or on every project if Projects is empty.
type Binding struct {
	Group    string
	User     string
	Role     Role
	Projects []string // path.Match patterns
}

// Action is something a person asks to do. Each needs a role.
type Action int

const (
	Read       Action = iota // see one project and its runs: viewer on it
	Operate                  // change its runs: operator on it
	ReadAny                  // see something that belongs to no project, such as the runners: viewer on any project
	Administer               // see the audit trail: admin on every project
)

// Policy holds the bindings from the server's configuration. A nil or
// empty Policy grants nothing.
type Policy struct {
	bindings []Binding
}

// NewPolicy checks the bindings and returns a policy built from them.
func NewPolicy(bs []Binding) (*Policy, error) {
	var errs []error
	for i, b := range bs {
		where := fmt.Sprintf("access[%d]", i)
		switch {
		case (b.Group == "") == (b.User == ""):
			errs = append(errs, fmt.Errorf("%s: give exactly one of group or user", where))
		case b.Role <= NoRole || b.Role > Admin:
			errs = append(errs, fmt.Errorf("%s: a role is required (viewer, operator or admin)", where))
		}
		for _, p := range b.Projects {
			if _, err := path.Match(p, ""); err != nil || p == "" {
				errs = append(errs, fmt.Errorf("%s: project pattern %q is not valid", where, p))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &Policy{bindings: slices.Clone(bs)}, nil
}

// applies reports whether b is for this person.
func (b *Binding) applies(user string, groups []string) bool {
	if b.User != "" {
		return b.User == user
	}
	return slices.Contains(groups, b.Group)
}

// covers reports whether b grants its role on project.
func (b *Binding) covers(project string) bool {
	if len(b.Projects) == 0 {
		return true
	}
	for _, p := range b.Projects {
		if ok, _ := path.Match(p, project); ok {
			return true
		}
	}
	return false
}

// RoleOn returns the highest role a person has on a project.
func (p *Policy) RoleOn(user string, groups []string, project string) Role {
	if p == nil {
		return NoRole
	}
	best := NoRole
	for i := range p.bindings {
		b := &p.bindings[i]
		if b.Role > best && b.applies(user, groups) && b.covers(project) {
			best = b.Role
		}
	}
	return best
}

// RoleEverywhere returns the highest role a person has on every project,
// from bindings that name no projects.
func (p *Policy) RoleEverywhere(user string, groups []string) Role {
	if p == nil {
		return NoRole
	}
	best := NoRole
	for i := range p.bindings {
		b := &p.bindings[i]
		if b.Role > best && len(b.Projects) == 0 && b.applies(user, groups) {
			best = b.Role
		}
	}
	return best
}

// RoleAnywhere returns the highest role a person has on at least one
// project.
func (p *Policy) RoleAnywhere(user string, groups []string) Role {
	if p == nil {
		return NoRole
	}
	best := NoRole
	for i := range p.bindings {
		b := &p.bindings[i]
		if b.Role > best && b.applies(user, groups) {
			best = b.Role
		}
	}
	return best
}

// Allows reports whether a person may do a on project. project is ignored
// for ReadAny and Administer.
func (p *Policy) Allows(user string, groups []string, a Action, project string) bool {
	switch a {
	case Read:
		return p.RoleOn(user, groups, project) >= Viewer
	case Operate:
		return p.RoleOn(user, groups, project) >= Operator
	case ReadAny:
		return p.RoleAnywhere(user, groups) >= Viewer
	case Administer:
		return p.RoleEverywhere(user, groups) >= Admin
	}
	return false
}
