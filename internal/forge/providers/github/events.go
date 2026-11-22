package github

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

// maxDelivery is the host's documented payload cap (25 MB):
// https://docs.github.com/en/webhooks/webhook-events-and-payloads#payload-cap
const maxDelivery = 25 << 20

// ErrBadSignature is returned for a delivery whose signature does not match.
var ErrBadSignature = errors.New("the webhook signature does not match")

// Verify checks a delivery and normalises it. Headers (documented at
// https://docs.github.com/en/webhooks/webhook-events-and-payloads#delivery-headers):
// X-GitHub-Event, X-GitHub-Delivery and X-Hub-Signature-256, which is
// "sha256=" and the hex HMAC-SHA256 of the raw body keyed with the secret
// (https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries).
// The comparison is constant time. A delivery without a signature is refused:
// a connection always has a webhook secret.
func (e events) Verify(r *http.Request, secret []byte) ([]forge.Event, error) {
	if len(secret) == 0 {
		return nil, errors.New("the connection has no webhook secret")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDelivery+1))
	if err != nil || len(body) > maxDelivery {
		return nil, errors.New("the webhook body cannot be read or is too large")
	}
	sig := strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=")
	got, err := hex.DecodeString(sig)
	if err != nil || r.Header.Get("X-Hub-Signature-256") == "" {
		return nil, ErrBadSignature
	}
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	if !hmac.Equal(got, m.Sum(nil)) {
		return nil, ErrBadSignature
	}
	delivery := r.Header.Get("X-GitHub-Delivery")
	if delivery == "" {
		return nil, errors.New("the webhook has no delivery id")
	}
	now := e.p.c.Now().UTC()
	switch ev := r.Header.Get("X-GitHub-Event"); ev {
	case "ping":
		return nil, nil
	case "push":
		var in struct {
			Ref        string `json:"ref"`
			Before     string `json:"before"`
			After      string `json:"after"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
			Sender struct {
				Login string `json:"login"`
			} `json:"sender"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.Ref == "" || in.Repository.FullName == "" {
			return nil, errors.New("the push webhook is not what the host sends")
		}
		return []forge.Event{{DeliveryID: delivery, Kind: forge.EventRefUpdated, Repository: in.Repository.FullName, Ref: in.Ref,
			Old: in.Before, Commit: in.After, Actor: forge.Principal{Login: in.Sender.Login, Unlinked: true}, At: now}}, nil
	case "pull_request":
		var in struct {
			Action      string  `json:"action"`
			Number      int     `json:"number"`
			PullRequest apiPull `json:"pull_request"`
			Repository  struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
			Sender struct {
				Login string `json:"login"`
			} `json:"sender"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.Number == 0 || in.Repository.FullName == "" {
			return nil, errors.New("the pull request webhook is not what the host sends")
		}
		var kind forge.EventKind
		switch in.Action {
		case "opened", "reopened":
			kind = forge.EventProposalOpened
		case "synchronize", "edited", "ready_for_review", "converted_to_draft":
			kind = forge.EventProposalRevised
		case "closed":
			kind = forge.EventProposalClosed
			if in.PullRequest.MergedAt != nil {
				kind = forge.EventProposalLanded
			}
		default:
			return nil, nil // labelled, assigned and the rest do not start anything
		}
		fork := in.PullRequest.Head.Repo == nil || in.PullRequest.Head.Repo.FullName != in.Repository.FullName
		return []forge.Event{{DeliveryID: delivery, Kind: kind, Repository: in.Repository.FullName, Number: in.Number,
			Ref: "refs/pull/" + fmt.Sprint(in.Number) + "/head", Commit: in.PullRequest.Head.SHA, Head: in.PullRequest.Head.SHA,
			Base: in.PullRequest.Base.Ref, Title: in.PullRequest.Title, Draft: in.PullRequest.Draft, Fork: fork,
			Author: in.PullRequest.User.Login, URL: in.PullRequest.HTMLURL, Proposal: fmt.Sprint(in.Number), Actor: forge.Principal{Login: in.Sender.Login, Unlinked: true}, At: now}}, nil
	}
	return nil, nil
}

// Poll finds what changed on pull requests when no webhook arrives: it lists
// the open pull requests (GET /repos/{o}/{r}/pulls?state=open, one page of 50,
// https://docs.github.com/en/rest/pulls/pulls#list-pull-requests) and returns
// proposal.opened for one not seen and proposal.revised for one whose head
// moved. The cursor is the numbers and heads seen, as text; "" is the first
// call, which reports every open pull request as opened. Pushes and tags are
// found by Launchway's own watcher of the repository's clone.
func (e events) Poll(ctx context.Context, repo forge.Repository, cursor string) ([]forge.Event, string, error) {
	seen := map[int]string{}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(raw, &seen) != nil {
			return nil, cursor, fmt.Errorf("%w: bad cursor", forge.ErrInvalid)
		}
	}
	page, err := e.p.Proposals().List(ctx, repo, forge.ProposalOpen, "")
	if err != nil {
		return nil, cursor, err
	}
	drafts, err := e.p.Proposals().List(ctx, repo, forge.ProposalDraft, "")
	if err != nil {
		return nil, cursor, err
	}
	page.Items = append(page.Items, drafts.Items...)
	out := []forge.Event{}
	next := map[int]string{}
	for _, pr := range page.Items {
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
