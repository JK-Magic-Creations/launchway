package codecommit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type events struct{ p *Provider }

// Mode is polling: CodeCommit sends no webhook (see Capabilities).
func (e events) Mode() forge.EventMode { return forge.EventsPolling }

// Verify refuses every delivery: nothing in CodeCommit posts to Launchway, so
// whatever arrives at the webhook address for this connection is not the host's.
func (e events) Verify(*http.Request, []byte) ([]forge.Event, error) {
	return nil, fmt.Errorf("%w: CodeCommit sends no webhooks; this connection is followed by polling", forge.ErrInvalid)
}

// Poll finds what changed on pull requests (the open ones, one page of 50):
// proposal.opened for one not seen and proposal.revised for one whose source
// moved. The cursor is the numbers and heads seen; "" reports every open pull
// request as opened. Pushes are followed from the repository itself, which
// Launchway fetches on the same schedule.
func (e events) Poll(ctx context.Context, repo forge.Repository, cursor string) ([]forge.Event, string, error) {
	seen := map[int]string{}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(raw, &seen) != nil {
			return nil, cursor, fmt.Errorf("%w: bad cursor", forge.ErrInvalid)
		}
	}
	open, err := e.p.Proposals().List(ctx, repo, forge.ProposalOpen, "")
	if err != nil {
		return nil, cursor, err
	}
	out := []forge.Event{}
	next := map[int]string{}
	for _, pr := range open.Items {
		next[pr.Number] = pr.Head
		kind := forge.EventProposalOpened
		if old, ok := seen[pr.Number]; ok {
			if old == pr.Head {
				continue
			}
			kind = forge.EventProposalRevised
		}
		out = append(out, forge.Event{DeliveryID: fmt.Sprintf("poll-%d-%s", pr.Number, pr.Head), Kind: kind, Repository: repo.Path, Number: pr.Number,
			Ref: pr.Source.Name, Commit: pr.Head, Head: pr.Head, Base: trimHeads(pr.Target.Name), Title: pr.Title, Proposal: strconv.Itoa(pr.Number),
			Fork: false, Author: pr.Author.Login, Actor: forge.Principal{Login: pr.Author.Login, Unlinked: true}, At: e.p.now().UTC()})
	}
	b, _ := json.Marshal(next)
	return out, base64.RawURLEncoding.EncodeToString(b), nil
}

func trimHeads(s string) string {
	if len(s) > 11 && s[:11] == "refs/heads/" {
		return s[11:]
	}
	return s
}
