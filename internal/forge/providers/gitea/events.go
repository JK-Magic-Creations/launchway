package gitea

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

// maxDelivery bounds what is read of one delivery; the documentation names no cap.
const maxDelivery = 25 << 20

// ErrBadSignature is returned for a delivery whose signature does not match.
var ErrBadSignature = errors.New("the webhook signature does not match")

// headerNames are the delivery headers of a kind. Gitea documents
// X-Gitea-Signature ("hex-encoded HMAC-SHA256 of the raw request body, without
// a prefix"), X-Gitea-Event and X-Gitea-Delivery
// (https://docs.gitea.com/usage/webhooks); Forgejo's page shows its own
// X-Forgejo-Delivery and X-Forgejo-Event and reads X-Forgejo-Signature
// (https://forgejo.org/docs/latest/user/webhooks/), and lists the X-Gitea-*
// variants beside them, which are tried second.
func headerNames(k forge.Kind, what string) []string {
	if k == forge.KindForgejo {
		return []string{"X-Forgejo-" + what, "X-Gitea-" + what}
	}
	return []string{"X-Gitea-" + what}
}

// DeliveryHeader is the header of a kind that carries a delivery's unique ID.
func DeliveryHeader(k forge.Kind) string { return headerNames(k, "Delivery")[0] }

func header(r *http.Request, names []string) string {
	for _, n := range names {
		if v := r.Header.Get(n); v != "" {
			return v
		}
	}
	return ""
}

type payloadRepo struct {
	FullName string `json:"full_name"`
}

type payloadUser struct {
	Login string `json:"login"`
}

// Verify checks a delivery and normalises it: the signature is compared in
// constant time; a delivery with none is refused (a connection always has a
// webhook secret, and the host sends an empty digest when it has none). The
// events are push (a branch or a tag), create (a tag), and pull_request and
// pull_request_sync, whose actions are opened, reopened, edited, closed and
// synchronized. The head of a pull request and where it comes from are read from the payload's pull_request
// when it carries them, and are otherwise asked of the API by the caller (an
// event without a head starts nothing by itself).
func (e events) Verify(r *http.Request, secret []byte) ([]forge.Event, error) {
	if len(secret) == 0 {
		return nil, errors.New("the connection has no webhook secret")
	}
	sig := header(r, headerNames(e.p.conn.Kind, "Signature"))
	got, err := hex.DecodeString(sig)
	if err != nil || sig == "" {
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
	delivery := header(r, headerNames(e.p.conn.Kind, "Delivery"))
	if delivery == "" {
		return nil, errors.New("the webhook has no delivery id")
	}
	now := e.p.c.Now().UTC()
	switch ev := header(r, headerNames(e.p.conn.Kind, "Event")); ev {
	case "push":
		var in struct {
			Ref        string      `json:"ref"`
			Before     string      `json:"before"`
			After      string      `json:"after"`
			Repository payloadRepo `json:"repository"`
			Sender     payloadUser `json:"sender"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.Ref == "" || in.Repository.FullName == "" {
			return nil, errors.New("the push webhook is not what the host sends")
		}
		return []forge.Event{{DeliveryID: delivery, Kind: forge.EventRefUpdated, Repository: in.Repository.FullName, Ref: in.Ref,
			Old: in.Before, Commit: in.After, Actor: forge.Principal{Login: in.Sender.Login, Unlinked: true}, At: now}}, nil
	case "create":
		var in struct {
			Ref        string      `json:"ref"`
			RefType    string      `json:"ref_type"`
			SHA        string      `json:"sha"`
			Repository payloadRepo `json:"repository"`
			Sender     payloadUser `json:"sender"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.Ref == "" || in.Repository.FullName == "" {
			return nil, errors.New("the create webhook is not what the host sends")
		}
		if in.RefType != "tag" {
			return nil, nil // a new branch arrives as a push
		}
		return []forge.Event{{DeliveryID: delivery, Kind: forge.EventRefUpdated, Repository: in.Repository.FullName, Ref: "refs/tags/" + in.Ref,
			Old: strings.Repeat("0", 40), Commit: in.SHA, Actor: forge.Principal{Login: in.Sender.Login, Unlinked: true}, At: now}}, nil
	case "pull_request", "pull_request_sync":
		var in struct {
			Action      string      `json:"action"`
			Number      int         `json:"number"`
			Repository  payloadRepo `json:"repository"`
			Sender      payloadUser `json:"sender"`
			PullRequest *apiPull    `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.Number == 0 || in.Repository.FullName == "" {
			return nil, errors.New("the pull request webhook is not what the host sends")
		}
		var kind forge.EventKind
		switch in.Action {
		case "opened", "reopened":
			kind = forge.EventProposalOpened
		case "synchronized", "edited":
			kind = forge.EventProposalRevised
		case "closed":
			kind = forge.EventProposalClosed
			if in.PullRequest != nil && in.PullRequest.Merged {
				kind = forge.EventProposalLanded
			}
		default:
			return nil, nil
		}
		ev := forge.Event{DeliveryID: delivery, Kind: kind, Repository: in.Repository.FullName, Number: in.Number,
			Ref: "refs/pull/" + fmt.Sprint(in.Number) + "/head", Proposal: fmt.Sprint(in.Number), Actor: forge.Principal{Login: in.Sender.Login, Unlinked: true}, At: now}
		if pr := in.PullRequest; pr != nil && pr.Head.SHA != "" {
			ev.Commit, ev.Head, ev.Base, ev.Title, ev.Draft = pr.Head.SHA, pr.Head.SHA, pr.Base.Ref, pr.Title, pr.Draft
			ev.Fork, ev.Author, ev.URL = pr.fork(in.Repository.FullName), pr.User.Login, pr.HTMLURL
		}
		return []forge.Event{ev}, nil
	}
	return nil, nil
}

// Poll finds what changed on pull requests when no webhook arrives (GET
// /repos/{o}/{r}/pulls?state=open, one page of 50): proposal.opened for one not
// seen and proposal.revised for one whose head moved. The cursor is the numbers
// and heads seen; "" reports every open pull request as opened.
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
