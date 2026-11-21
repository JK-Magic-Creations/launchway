package propose

import (
	"context"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// ForgeHost is a Host over any forge provider that supports proposals: the
// built-in hosting, or a connected host. Branch names tell which proposals
// are the updater's; labels and the closing note are used when the provider
// offers them (optional interfaces below), and skipped when it does not.
type ForgeHost struct {
	Provider forge.Provider
	Repo     forge.Repository
}

type labeler interface {
	SetLabels(ctx context.Context, repo forge.Repository, number int, labels []string) error
}

type closingNoter interface {
	ClosingNote(ctx context.Context, repo forge.Repository, number int) (string, error)
}

// DefaultBranch is the repository's default branch.
func (h *ForgeHost) DefaultBranch(context.Context) (string, error) {
	if h.Repo.DefaultBranch == "" {
		return "", fmt.Errorf("repository %s has no default branch yet: push one first", h.Repo.Path)
	}
	return h.Repo.DefaultBranch, nil
}

func (h *ForgeHost) list(ctx context.Context, st forge.ProposalState, prefix string) ([]PR, error) {
	if err := h.Provider.Capabilities().Err(forge.CapProposals); err != nil {
		return nil, err
	}
	pg, err := h.Provider.Proposals().List(ctx, h.Repo, st, "")
	if err != nil {
		return nil, err
	}
	var out []PR
	for _, p := range pg.Items {
		b := strings.TrimPrefix(p.Source.Name, "refs/heads/")
		if !strings.HasPrefix(b, strings.TrimRight(prefix, "/")+"/") {
			continue
		}
		out = append(out, PR{Number: p.Number, Branch: b, Title: p.Title, Body: p.Body, Merged: p.State == forge.ProposalLanded})
	}
	return out, nil
}

// Open lists the updater's open proposals (drafts count as open).
func (h *ForgeHost) Open(ctx context.Context, prefix string) ([]PR, error) {
	a, err := h.list(ctx, forge.ProposalOpen, prefix)
	if err != nil {
		return nil, err
	}
	b, err := h.list(ctx, forge.ProposalDraft, prefix)
	return append(a, b...), err
}

// Closed lists the updater's closed proposals that were not merged, with
// the closing note where the provider has one. The updater's own closings
// say so in their comment and carry no ignore request.
func (h *ForgeHost) Closed(ctx context.Context, prefix string) ([]PR, error) {
	cl, err := h.list(ctx, forge.ProposalClosed, prefix)
	if err != nil {
		return nil, err
	}
	n, ok := h.Provider.Proposals().(closingNoter)
	for i := range cl {
		if ok {
			if note, err := n.ClosingNote(ctx, h.Repo, cl[i].Number); err == nil {
				cl[i].Reason = note
			}
		}
	}
	return cl, nil
}

// Create opens a proposal and sets its labels when the provider can.
func (h *ForgeHost) Create(ctx context.Context, n NewPR) (PR, error) {
	p, err := h.Provider.Proposals().Create(ctx, h.Repo, forge.NewProposal{Title: n.Title, Body: n.Body,
		Source: forge.Ref{Name: "refs/heads/" + n.Branch}, Target: forge.Ref{Name: "refs/heads/" + n.Base}})
	if err != nil {
		return PR{}, err
	}
	if l, ok := h.Provider.Proposals().(labeler); ok && len(n.Labels) > 0 {
		_ = l.SetLabels(ctx, h.Repo, p.Number, n.Labels)
	}
	return PR{Number: p.Number, Branch: n.Branch, Title: p.Title, Body: p.Body}, nil
}

// Update changes the title and body in place.
func (h *ForgeHost) Update(ctx context.Context, number int, title, body string) error {
	_, err := h.Provider.Proposals().Update(ctx, h.Repo, number, forge.ProposalPatch{Title: &title, Body: &body})
	return err
}

// Close closes a proposal. The comment is not posted where the provider has
// no way to comment; the audit trail has it.
func (h *ForgeHost) Close(ctx context.Context, number int, _ string) error {
	return h.Provider.Proposals().Close(ctx, h.Repo, number)
}
