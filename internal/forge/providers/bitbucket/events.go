package bitbucket

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type events struct{ p *Provider }

func (events) Mode() forge.EventMode { return forge.EventsWebhook }

const maxDelivery = 25 << 20

// DeliveryHeader carries "the UUID of the request" (X-Request-UUID). The
// documentation does not say whether a retry (X-Attempt-Number) reuses it; if
// it does not, a retried delivery is simply processed again, which only makes
// the Flight poll once more.
const DeliveryHeader = "X-Request-UUID"

// ErrBadSignature is returned for a delivery whose signature does not match.
var ErrBadSignature = errors.New("the webhook signature does not match")

type account struct {
	Nickname    string `json:"nickname"`
	DisplayName string `json:"display_name"`
}

func (a account) login() string {
	return apiUser{Nickname: a.Nickname, DisplayName: a.DisplayName}.login()
}

// Verify checks a delivery and normalises it. The signature is the
// X-Hub-Signature header, "method=signature" with sha256 and the hex HMAC of
// the raw body (https://support.atlassian.com/bitbucket-cloud/docs/manage-webhooks/),
// compared in constant time; a delivery with none is refused (a connection
// always has a webhook secret). The event is the X-Event-Key header
// (https://support.atlassian.com/bitbucket-cloud/docs/event-payloads/):
// repo:push (one event for each branch or tag the push changed), and
// pullrequest:created, :updated, :fulfilled (merged) and :rejected (declined).
func (e events) Verify(r *http.Request, secret []byte) ([]forge.Event, error) {
	if len(secret) == 0 {
		return nil, errors.New("the connection has no webhook secret")
	}
	method, sig, _ := strings.Cut(r.Header.Get("X-Hub-Signature"), "=")
	got, err := hex.DecodeString(sig)
	if err != nil || method != "sha256" || sig == "" {
		return nil, ErrBadSignature
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDelivery+1))
	if err != nil || len(body) > maxDelivery {
		return nil, errors.New("the webhook body cannot be read or is too large")
	}
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	if !hmac.Equal(got, m.Sum(nil)) {
		return nil, ErrBadSignature
	}
	delivery := r.Header.Get(DeliveryHeader)
	if delivery == "" {
		sum := sha256.Sum256(body)
		delivery = "body-" + hex.EncodeToString(sum[:16])
	}
	now := e.p.c.Now().UTC()
	switch key := r.Header.Get("X-Event-Key"); key {
	case "repo:push":
		var in struct {
			Actor      account `json:"actor"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
			Push struct {
				Changes []struct {
					New *ref `json:"new"`
					Old *ref `json:"old"`
				} `json:"changes"`
			} `json:"push"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.Repository.FullName == "" {
			return nil, errors.New("the push webhook is not what the host sends")
		}
		var out []forge.Event
		for i, ch := range in.Push.Changes {
			ev := forge.Event{DeliveryID: fmt.Sprintf("%s-%d", delivery, i), Kind: forge.EventRefUpdated, Repository: in.Repository.FullName,
				Old: strings.Repeat("0", 40), Commit: strings.Repeat("0", 40), Actor: forge.Principal{Login: in.Actor.login(), Unlinked: true}, At: now}
			switch {
			case ch.New != nil:
				ev.Ref, ev.Commit = ch.New.full(), ch.New.Target.Hash
			case ch.Old != nil:
				ev.Ref = ch.Old.full()
			default:
				continue
			}
			if ch.Old != nil {
				ev.Old = ch.Old.Target.Hash
			}
			out = append(out, ev)
		}
		return out, nil
	case "pullrequest:created", "pullrequest:updated", "pullrequest:fulfilled", "pullrequest:rejected":
		var in struct {
			Actor       account `json:"actor"`
			PullRequest apiPull `json:"pullrequest"`
			Repository  struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.PullRequest.ID == 0 || in.Repository.FullName == "" {
			return nil, errors.New("the pull request webhook is not what the host sends")
		}
		kind := map[string]forge.EventKind{"pullrequest:created": forge.EventProposalOpened, "pullrequest:updated": forge.EventProposalRevised,
			"pullrequest:fulfilled": forge.EventProposalLanded, "pullrequest:rejected": forge.EventProposalClosed}[key]
		pr := in.PullRequest
		return []forge.Event{{DeliveryID: delivery, Kind: kind, Repository: in.Repository.FullName, Number: pr.ID, Ref: "refs/pull/" + fmt.Sprint(pr.ID) + "/head",
			Commit: pr.Source.Commit.Hash, Head: pr.Source.Commit.Hash, Base: pr.Dest.Branch.Name, Title: pr.Title, Draft: pr.Draft, Fork: pr.fork(),
			Author: pr.Author.login(), URL: pr.Links.HTML.Href, Proposal: fmt.Sprint(pr.ID), Actor: forge.Principal{Login: in.Actor.login(), Unlinked: true}, At: now}}, nil
	}
	return nil, nil
}

// ref is a reference of a push change: its type is branch or tag (annotated_tag for an old one).
type ref struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Target struct {
		Hash string `json:"hash"`
	} `json:"target"`
}

func (r ref) full() string {
	if r.Type == "branch" {
		return "refs/heads/" + r.Name
	}
	return "refs/tags/" + r.Name
}

// Poll finds what changed on pull requests when no webhook arrives (the open
// pull requests, one page of 50): proposal.opened for one not seen and
// proposal.revised for one whose head moved. The cursor is the numbers and
// heads seen; "" reports every open pull request as opened.
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
	drafts, err := e.p.Proposals().List(ctx, repo, forge.ProposalDraft, "")
	if err != nil {
		return nil, cursor, err
	}
	items := append(open.Items, drafts.Items...)
	out := []forge.Event{}
	next := map[int]string{}
	for _, pr := range items {
		next[pr.Number] = pr.Head
		kind := forge.EventProposalOpened
		if old, ok := seen[pr.Number]; ok {
			if old == pr.Head {
				continue
			}
			kind = forge.EventProposalRevised
		}
		fork, _ := pr.Ext["fork"].(bool)
		prURL, _ := pr.Ext["url"].(string)
		out = append(out, forge.Event{DeliveryID: fmt.Sprintf("poll-%d-%s", pr.Number, pr.Head), Kind: kind, Repository: repo.Path, Number: pr.Number,
			Ref: "refs/pull/" + fmt.Sprint(pr.Number) + "/head", Commit: pr.Head, Head: pr.Head, Base: strings.TrimPrefix(pr.Target.Name, "refs/heads/"),
			Title: pr.Title, Proposal: fmt.Sprint(pr.Number), Draft: pr.State == forge.ProposalDraft, Fork: fork, Author: pr.Author.Login, URL: prURL,
			Actor: forge.Principal{Login: pr.Author.Login, Unlinked: true}, At: e.p.c.Now().UTC()})
	}
	b, _ := json.Marshal(next)
	return out, base64.RawURLEncoding.EncodeToString(b), nil
}
