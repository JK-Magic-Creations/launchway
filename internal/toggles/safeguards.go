package toggles

import "slices"

// Safeguards are the protections around changing a toggle in an environment.
// They are on by default and the team in charge may relax them, at the level
// they own and below, with the change audited; a level above may lock one so
// that it cannot be relaxed below (the only way a team loses the choice).
type Safeguards struct {
	// RequireReason: a change needs a sentence saying why.
	RequireReason bool `json:"require_reason"`
	// RequireApproval: a change waits until someone other than the person
	// who asked has approved it. On by default in a protected environment.
	RequireApproval bool `json:"require_approval"`
	// Approvals is how many different people must approve (1 or more).
	Approvals int `json:"approvals"`
	// WatchMinutes is how long after a change the environment is marked as
	// being watched, so that the timeline, the rollback page and the people
	// on call know a toggle changed recently; 0 is no watch.
	WatchMinutes int `json:"watch_minutes"`
}

// The names of the safeguards, as they are written and locked.
const (
	GuardReason    = "require_reason"
	GuardApproval  = "require_approval"
	GuardApprovals = "approvals"
	GuardWatch     = "watch_minutes"
)

// GuardNames lists them.
var GuardNames = []string{GuardReason, GuardApproval, GuardApprovals, GuardWatch}

// Setting is one level's say about the safeguards: what it sets (a pointer
// left nil is not set) and which of them it locks for everything below.
type Setting struct {
	RequireReason   *bool    `json:"require_reason,omitempty"`
	RequireApproval *bool    `json:"require_approval,omitempty"`
	Approvals       *int     `json:"approvals,omitempty"`
	WatchMinutes    *int     `json:"watch_minutes,omitempty"`
	Locked          []string `json:"locked,omitempty"`
}

// Layer is a Setting with where it came from, for explaining the result.
type Layer struct {
	Where string // for example "organization" or "project pay-api, production"
	Setting
}

// Resolved is what applies, and for each safeguard where it came from.
type Resolved struct {
	Safeguards
	// From says, for each safeguard, which layer decided it ("default" when none).
	From map[string]string `json:"from"`
	// LockedBy says which layer locked a safeguard, when one did.
	LockedBy map[string]string `json:"locked_by,omitempty"`
}

// Resolve applies layers, widest first, to the defaults. A layer cannot change
// a safeguard that an earlier layer locked.
func Resolve(defaults Safeguards, layers []Layer) Resolved {
	r := Resolved{Safeguards: defaults, From: map[string]string{}, LockedBy: map[string]string{}}
	for _, g := range GuardNames {
		r.From[g] = "default"
	}
	for _, l := range layers {
		set := func(name string, apply func()) {
			if _, locked := r.LockedBy[name]; locked {
				return
			}
			apply()
			r.From[name] = l.Where
		}
		if l.RequireReason != nil {
			set(GuardReason, func() { r.RequireReason = *l.RequireReason })
		}
		if l.RequireApproval != nil {
			set(GuardApproval, func() { r.RequireApproval = *l.RequireApproval })
		}
		if l.Approvals != nil {
			set(GuardApprovals, func() { r.Approvals = max(1, *l.Approvals) })
		}
		if l.WatchMinutes != nil {
			set(GuardWatch, func() { r.WatchMinutes = max(0, *l.WatchMinutes) })
		}
		for _, g := range l.Locked {
			if slices.Contains(GuardNames, g) {
				if _, locked := r.LockedBy[g]; !locked {
					r.LockedBy[g] = l.Where
				}
			}
		}
	}
	if r.Approvals < 1 {
		r.Approvals = 1
	}
	return r
}

// Relaxes lists the safeguards that a setting turns down from what would
// otherwise apply: the changes that deserve a line in the audit trail's detail.
func (s Setting) Relaxes(current Safeguards) []string {
	var out []string
	if s.RequireReason != nil && current.RequireReason && !*s.RequireReason {
		out = append(out, GuardReason)
	}
	if s.RequireApproval != nil && current.RequireApproval && !*s.RequireApproval {
		out = append(out, GuardApproval)
	}
	if s.Approvals != nil && *s.Approvals < current.Approvals {
		out = append(out, GuardApprovals)
	}
	if s.WatchMinutes != nil && *s.WatchMinutes < current.WatchMinutes {
		out = append(out, GuardWatch)
	}
	return out
}
