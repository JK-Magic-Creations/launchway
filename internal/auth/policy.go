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
	// Environments, if not empty, limits what the binding grants on
	// deployments (commands on a deployment's rollout, and approvals that
	// name a deployment) to those environments (path.Match patterns). The
	// binding grants its role on its projects as usual otherwise.
	Environments []string
}

// Action is something a person asks to do. Each needs a role.
type Action int

const (
	Read              Action = iota // see one project and its runs: viewer on it
	Operate                         // change its runs: operator on it
	ReadAny                         // see something that belongs to no project, such as the runners: viewer on any project
	Administer                      // see the audit trail: admin on every project
	AdministerProject               // override checks on one project, such as saving an invalid pipeline: admin on it
	// RunDraft starts a draft run, of pipeline text that is not in Git, on
	// a project. It needs the role a DraftRule asks for, so Policy alone
	// cannot decide it: Allows always refuses it.
	RunDraft
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
		for _, e := range b.Environments {
			if _, err := path.Match(e, ""); err != nil || e == "" {
				errs = append(errs, fmt.Errorf("%s: environment pattern %q is not valid", where, e))
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

// RoleOnEnvironment returns the highest role a person has on deployments of
// a project to an environment: from the bindings that cover the project
// and either name no environments or name this one.
func (p *Policy) RoleOnEnvironment(user string, groups []string, project, env string) Role {
	if p == nil {
		return NoRole
	}
	best := NoRole
	for i := range p.bindings {
		b := &p.bindings[i]
		if b.Role > best && b.applies(user, groups) && b.covers(project) && b.coversEnvironment(env) {
			best = b.Role
		}
	}
	return best
}

// coversEnvironment reports whether b grants its role on deployments to env.
func (b *Binding) coversEnvironment(env string) bool {
	if len(b.Environments) == 0 {
		return true
	}
	for _, e := range b.Environments {
		if ok, _ := path.Match(e, env); ok {
			return true
		}
	}
	return false
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
	case AdministerProject:
		return p.RoleOn(user, groups, project) >= Admin
	}
	return false
}

// Draft runs run pipeline text that is not in Git: steps nobody reviewed,
// with whatever the runners can reach. So who may start them is a setting of
// its own, per project.

// DraftRule says who may start draft runs on a project, and whether their
// steps may read secrets.
type DraftRule struct {
	// Allow is the role needed on the project: Operator (the default) or
	// Admin. NoRole means nobody may.
	Allow Role
	// Secrets lets the steps of draft runs read secrets. Off by default: a
	// step that declares secrets then fails before any runner sees it.
	Secrets bool
}

// DefaultDraftRule is the rule when nothing is configured.
var DefaultDraftRule = DraftRule{Allow: Operator}

// DraftRules is the server's rule and the projects that override it.
type DraftRules struct {
	Default  DraftRule
	Projects map[string]DraftRule // by project name
}

// For returns the rule for a project. A nil DraftRules gives the default.
func (d *DraftRules) For(project string) DraftRule {
	if d == nil {
		return DefaultDraftRule
	}
	if r, ok := d.Projects[project]; ok {
		return r
	}
	return d.Default
}

// AllowsDraft reports whether someone with role on a project may start a
// draft run there under rule.
func (r DraftRule) AllowsDraft(role Role) bool {
	return r.Allow != NoRole && role >= r.Allow
}

// ParseDraftAllow reads the drafts.allow setting: admin, operator or none.
func ParseDraftAllow(s string) (Role, error) {
	switch s {
	case "admin":
		return Admin, nil
	case "operator":
		return Operator, nil
	case "none":
		return NoRole, nil
	}
	return NoRole, fmt.Errorf("allow %q is not one of admin, operator, none", s)
}
