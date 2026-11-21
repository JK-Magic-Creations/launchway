package forge

import (
	"errors"
	"fmt"
	"sort"
)

// Support says how a backend provides a feature.
type Support string

const (
	// Native: the backend does it itself.
	Native Support = "native"
	// Emulated: Launchway does it another way, and Workaround says how.
	Emulated Support = "emulated"
	// None: it cannot be done; calls return ErrUnsupported.
	None Support = "none"
)

// Capability names a feature, one for each group of the Provider and a few
// finer ones.
type Capability string

const (
	CapIdentity     Capability = "identity"
	CapRepositories Capability = "repositories"
	CapRepoCreate   Capability = "repositories.create"
	CapContent      Capability = "content"
	CapEvents       Capability = "events"
	CapAccess       Capability = "access"
	CapProposals    Capability = "proposals"
	CapProposalRef  Capability = "proposals.fetchable_ref"
	CapDrafts       Capability = "proposals.drafts"
	CapReviews      Capability = "reviews"
	CapResults      Capability = "results"
	CapResultsGate  Capability = "results.gate"
	CapLand         Capability = "land"
	CapAutoLand     Capability = "land.auto"
	CapRules        Capability = "rules"
	CapForks        Capability = "forks"
	CapLFS          Capability = "lfs"
)

// AllCapabilities is every capability, in a fixed order, so the generated
// matrix of capabilities is stable.
var AllCapabilities = []Capability{
	CapIdentity, CapRepositories, CapRepoCreate, CapContent, CapEvents, CapAccess,
	CapProposals, CapProposalRef, CapDrafts, CapReviews, CapResults, CapResultsGate,
	CapLand, CapAutoLand, CapRules, CapForks, CapLFS,
}

// Declaration is one capability of one provider: how it is provided, why it
// is not (when it is not), and what a person can do instead.
type Declaration struct {
	Support Support `json:"support"`
	// Reason is one plain sentence, shown when Support is not Native.
	Reason string `json:"reason,omitempty"`
	// Workaround is plain text for the UI. Required when Support is None or
	// Emulated.
	Workaround string `json:"workaround,omitempty"`
}

// Capabilities is what a provider declares. It is data: the matrix of what
// each kind can do is generated from it.
type Capabilities map[Capability]Declaration

// Has reports whether the feature is available, natively or emulated.
func (c Capabilities) Has(feat Capability) bool {
	d, ok := c[feat]
	return ok && d.Support != None
}

// Err returns the ErrUnsupported to give for a feature the provider does not
// have, or nil when it has it.
func (c Capabilities) Err(feat Capability) error {
	d, ok := c[feat]
	if ok && d.Support != None {
		return nil
	}
	if !ok {
		d = Declaration{Support: None, Reason: "this provider does not declare the feature", Workaround: "use a provider that supports it"}
	}
	return &ErrUnsupported{Capability: feat, Reason: d.Reason, Workaround: d.Workaround}
}

// Validate checks the declarations are complete: every capability is
// declared, each support value is known, and every feature that is not native
// says what to do instead.
func (c Capabilities) Validate() error {
	for _, feat := range AllCapabilities {
		d, ok := c[feat]
		if !ok {
			return fmt.Errorf("capability %q is not declared", feat)
		}
		switch d.Support {
		case Native:
		case Emulated, None:
			if d.Workaround == "" || d.Reason == "" {
				return fmt.Errorf("capability %q is %s and must have a reason and a workaround", feat, d.Support)
			}
		default:
			return fmt.Errorf("capability %q has unknown support %q", feat, d.Support)
		}
	}
	var extra []string
	for feat := range c {
		found := false
		for _, a := range AllCapabilities {
			found = found || a == feat
		}
		if !found {
			extra = append(extra, string(feat))
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		return fmt.Errorf("unknown capabilities %v", extra)
	}
	return nil
}

// ErrUnsupported is what a group or operation a backend lacks returns. The
// workaround is plain text meant for people, for example "CodeCommit has no
// commit statuses; Launchway reports results as a comment and an approval by
// the CI role".
type ErrUnsupported struct {
	Capability Capability `json:"capability"`
	Reason     string     `json:"reason"`
	Workaround string     `json:"workaround"`
}

// Error describes the missing feature and what to do instead.
func (e *ErrUnsupported) Error() string {
	s := fmt.Sprintf("not supported by this forge: %s", e.Capability)
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	if e.Workaround != "" {
		s += ". " + e.Workaround
	}
	return s
}

// IsUnsupported reports whether err is, or wraps, an ErrUnsupported, and
// returns it.
func IsUnsupported(err error) (*ErrUnsupported, bool) {
	var u *ErrUnsupported
	if errors.As(err, &u) {
		return u, true
	}
	return nil, false
}
