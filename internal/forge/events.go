package forge

import "time"

// EventKind is a normalised event. Every backend reduces what it hears to
// these, however it hears it.
type EventKind string

const (
	EventRefUpdated      EventKind = "ref.updated"
	EventProposalOpened  EventKind = "proposal.opened"
	EventProposalRevised EventKind = "proposal.revised"
	EventProposalLanded  EventKind = "proposal.landed"
	EventProposalClosed  EventKind = "proposal.closed"
	EventReviewSubmitted EventKind = "review.submitted"
	EventThreadPosted    EventKind = "thread.posted"
	EventResultReported  EventKind = "result.reported"
)

// EventKinds is the whole set, in a fixed order.
var EventKinds = []EventKind{
	EventRefUpdated, EventProposalOpened, EventProposalRevised, EventProposalLanded,
	EventProposalClosed, EventReviewSubmitted, EventThreadPosted, EventResultReported,
}

// Event is one normalised event.
type Event struct {
	// DeliveryID identifies the delivery so a replay is ignored.
	DeliveryID string    `json:"delivery_id,omitempty"`
	Kind       EventKind `json:"kind"`
	Connection string    `json:"connection"`
	Repository string    `json:"repository,omitempty"`
	// Ref and Commit are set for ref.updated; Old is the previous commit
	// ("" for a new ref, all zeros is also accepted).
	Ref      string    `json:"ref,omitempty"`
	Old      string    `json:"old,omitempty"`
	Commit   string    `json:"commit,omitempty"`
	Proposal string    `json:"proposal,omitempty"`
	Actor    Principal `json:"actor,omitzero"`
	At       time.Time `json:"at"`
	// The proposal fields are set for proposal.* events from a forge that
	// reports them.
	Number int    `json:"number,omitempty"`
	Head   string `json:"head,omitempty"`
	Base   string `json:"base,omitempty"`
	Title  string `json:"title,omitempty"`
	Draft  bool   `json:"draft,omitempty"`
	// Fork says the proposal's source is another repository: its run follows
	// the restricted pull-request rule.
	Fork bool `json:"fork,omitempty"`
}
