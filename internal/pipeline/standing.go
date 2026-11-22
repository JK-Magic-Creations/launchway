package pipeline

import (
	"encoding/json"
	"fmt"
)

// Steps that Standing orders add to a run (internal/standards). They are
// ordinary steps (a tool in a pinned image, writing a findings report) plus
// one step of kind gate, which the server itself settles from the findings
// those steps reported. A pipeline file never writes either: the server
// adds them when it creates a run, and they are shown in the graph as
// added by Standing orders.

// SchemaStandings is the server schema that brought Standing orders (45):
// a run that has the steps below carries it as "requires", so that older
// servers, which would not know the gate step, leave the run alone.
const SchemaStandings = 45

// KindGate is the Kind of the step that gives the Inspection's verdict.
const KindGate = "gate"

// AddedByStandingOrders is Step.Added.By for steps the server added.
const AddedByStandingOrders = "standing-orders"

// Added says who put a step in the pipeline, when it was not written in the
// repository. Everything here is for display and audit.
type Added struct {
	By    string `json:"by"`              // AddedByStandingOrders
	Check string `json:"check,omitempty"` // the check it belongs to
	Why   string `json:"why,omitempty"`   // one sentence: what was found that makes this check apply
}

// Gate is what a gate step judges by: the effective Standing orders as they
// were when the run was created, kept with the run so that the verdict can
// be explained later, whatever the settings are by then.
type Gate struct {
	Policy json.RawMessage `json:"policy"`
}

// IsGate reports whether the step is the Inspection's verdict.
func (s *Step) IsGate() bool { return s.Kind == KindGate }

// AppendSteps adds steps the server made to the end of the pipeline, after
// the pipeline's own. It refuses an ID that is already used, so a pipeline
// that has its own step of the same name keeps it and the server's is not
// added (the caller checks Step(id) first to decide that).
func (p *Pipeline) AppendSteps(steps []*Step, requires int) error {
	for _, s := range steps {
		if s == nil || s.ID == "" {
			return fmt.Errorf("pipeline: a step to add has no ID")
		}
		if p.Step(s.ID) != nil {
			return fmt.Errorf("pipeline: step %q already exists", s.ID)
		}
		p.Steps = append(p.Steps, s)
	}
	if requires > p.Requires {
		p.Requires = requires
	}
	return nil
}

// Clone returns a copy of the pipeline that can be changed without
// changing the original: the step list and the steps are copied, the rest
// is shared (it is never changed after parsing).
func (p *Pipeline) Clone() *Pipeline {
	c := *p
	c.Steps = make([]*Step, len(p.Steps))
	for i, s := range p.Steps {
		cp := *s
		c.Steps[i] = &cp
	}
	return &c
}
