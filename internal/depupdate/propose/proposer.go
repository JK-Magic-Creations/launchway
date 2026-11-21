package propose

import (
	"context"
	"time"

	"github.com/jk-magic-creations/launchway/internal/depupdate/plan"
)

// Event is one thing the updater did, for the audit trail.
type Event struct {
	Action string // opened, updated, closed, kept, queued, failed
	Key    string
	Number int
	Detail string
}

// Proposer carries out decisions.
type Proposer struct {
	Host      Host
	WS        *Workspace
	Runner    StepRunner
	Policy    plan.Policy
	Toolchain Toolchains
	Scratch   string
	Now       func() time.Time
	// Audit is told of every change (nil: nobody).
	Audit func(Event)
	// DryRun makes no change: it reports what would be done.
	DryRun bool
}

// Outcome of one decision.
type Outcome struct {
	Decision plan.Decision
	Event    Event
	Err      error
}

func (p *Proposer) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Proposer) audit(e Event) {
	if p.Audit != nil {
		p.Audit(e)
	}
}

// Execute does what the decisions say: open, update in place, close, and
// leave the rest. A failure of one is recorded and does not stop the others.
func (p *Proposer) Execute(ctx context.Context, decisions []plan.Decision) []Outcome {
	var out []Outcome
	base, err := p.Host.DefaultBranch(ctx)
	if err != nil {
		for _, d := range decisions {
			out = append(out, Outcome{Decision: d, Err: err})
		}
		return out
	}
	cloned := false
	prefix := p.Policy.Commit.BranchPrefix
	if prefix == "" {
		prefix = plan.DefaultBranchPrefix
	}
	for _, d := range decisions {
		o := Outcome{Decision: d, Event: Event{Key: d.Key, Number: d.Number, Detail: d.Why}}
		switch d.Action {
		case plan.ActionKeep:
			o.Event.Action = "kept"
		case plan.ActionQueue:
			o.Event.Action = "queued"
		case plan.ActionClose:
			o.Event.Action = "closed"
			if !p.DryRun {
				o.Err = p.Host.Close(ctx, d.Number, UpdaterClosing+": "+d.Why)
			}
		case plan.ActionOpen, plan.ActionUpdate:
			o.Event.Action = map[string]string{plan.ActionOpen: "opened", plan.ActionUpdate: "updated"}[d.Action]
			if p.DryRun {
				break
			}
			if !cloned {
				p.WS.Base = base
				if _, err := p.WS.Clone(ctx); err != nil {
					o.Err = err
					break
				}
				cloned = true
			}
			o.Err = p.one(ctx, d, base, prefix, &o.Event)
		}
		if o.Err != nil {
			o.Event.Action, o.Event.Detail = "failed", o.Err.Error()
		}
		if !p.DryRun && o.Event.Action != "kept" && o.Event.Action != "queued" {
			p.audit(o.Event)
		}
		out = append(out, o)
	}
	return out
}

func (p *Proposer) one(ctx context.Context, d plan.Decision, base, prefix string, ev *Event) error {
	work, err := Build(d.Proposal, p.WS.Read, p.Toolchain)
	if err != nil {
		return err
	}
	if iso, ok := p.Runner.(interface{ Isolated() bool }); ok {
		work.Isolated = iso.Isolated()
	}
	if _, err := Apply(ctx, p.WS, d.Proposal, work, p.Runner, prefix, p.Scratch); err != nil {
		return err
	}
	body := Body(d.Proposal, work, p.now(), p.Policy)
	if d.Action == plan.ActionUpdate {
		return p.Host.Update(ctx, d.Number, d.Title, body)
	}
	pr, err := p.Host.Create(ctx, NewPR{Title: d.Title, Body: body, Branch: d.Branch, Base: base,
		Labels: labelsFor(p.Policy, d.Proposal), Reviewers: p.Policy.Reviewers, Assignees: p.Policy.Assignees})
	if err != nil {
		return err
	}
	ev.Number = pr.Number
	return nil
}

func labelsFor(p plan.Policy, pr plan.Proposal) []string {
	l := append([]string(nil), p.Labels...)
	if pr.Security {
		l = append(l, "security")
	}
	return append(l, pr.Ecosystem)
}
