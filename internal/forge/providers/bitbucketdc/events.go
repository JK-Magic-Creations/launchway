package bitbucketdc

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
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type events struct{ p *Provider }

func (events) Mode() forge.EventMode { return forge.EventsWebhook }

const maxDelivery = 25 << 20

// DeliveryHeader carries "a unique UUID for each webhook request"
// (X-Request-Id, https://confluence.atlassian.com/bitbucketserver/event-payload-938025882.html).
// The page does not say whether a retry reuses it; if it does not, a retried
// delivery is only processed again, which makes the Flight poll once more.
const DeliveryHeader = "X-Request-Id"

// ErrBadSignature is returned for a delivery whose signature does not match.
var ErrBadSignature = errors.New("the webhook signature does not match")

type actor struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

func (a actor) principal() forge.Principal {
	return forge.Principal{Login: a.Name, Name: a.DisplayName, Unlinked: true}
}

// Verify checks a delivery and normalises it. With a secret set, "each request
// is signed via a Hash-based Message Authentication Code (HMAC)": the header
// X-Hub-Signature, whose default algorithm is HMAC-SHA256, in the form
// sha256=<hex digest> (https://confluence.atlassian.com/bitbucketserver/manage-webhooks-938025878.html),
// compared in constant time over the raw body; a delivery with none is refused
// (a connection always has a webhook secret). The event is the X-Event-Key
// header (https://confluence.atlassian.com/bitbucketserver/event-payload-938025882.html):
// repo:refs_changed (one event for each branch or tag the push changed) and
// pr:opened, pr:from_ref_updated, pr:modified (title, description, draft or
// target changed), pr:merged, pr:declined and pr:deleted.
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
	case "repo:refs_changed":
		return e.pushed(body, delivery, now)
	case "pr:opened", "pr:from_ref_updated", "pr:modified", "pr:merged", "pr:declined", "pr:deleted":
		return e.pull(key, body, delivery, now)
	}
	return nil, nil
}

const zeros = "0000000000000000000000000000000000000000"

// pushed: the repo:refs_changed payload has repository (slug, project.key),
// actor and changes, each with ref (id, type), fromHash, toHash and type
// (ADD, UPDATE or DELETE).
func (e events) pushed(body []byte, delivery string, now time.Time) ([]forge.Event, error) {
	var in struct {
		Actor      actor      `json:"actor"`
		Repository apiRepoRef `json:"repository"`
		Changes    []struct {
			Ref struct {
				ID string `json:"id"`
			} `json:"ref"`
			RefID    string `json:"refId"`
			FromHash string `json:"fromHash"`
			ToHash   string `json:"toHash"`
			Type     string `json:"type"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(body, &in); err != nil || in.Repository.full() == "" {
		return nil, errors.New("the push webhook is not what the host sends")
	}
	var out []forge.Event
	for i, ch := range in.Changes {
		ref := ch.Ref.ID
		if ref == "" {
			ref = ch.RefID
		}
		if ref == "" {
			continue
		}
		old, cur := ch.FromHash, ch.ToHash
		if old == "" || ch.Type == "ADD" {
			old = zeros
		}
		if cur == "" || ch.Type == "DELETE" {
			cur = zeros
		}
		out = append(out, forge.Event{DeliveryID: fmt.Sprintf("%s-%d", delivery, i), Kind: forge.EventRefUpdated, Repository: in.Repository.full(),
			Ref: ref, Old: old, Commit: cur, Actor: in.Actor.principal(), At: now})
	}
	return out, nil
}

// pull: the pull request events carry the actor and the pullRequest (id, title,
// draft, state, fromRef.latestCommit and repository, toRef.id and repository).
func (e events) pull(key string, body []byte, delivery string, now time.Time) ([]forge.Event, error) {
	var in struct {
		Actor       actor   `json:"actor"`
		PullRequest apiPull `json:"pullRequest"`
	}
	if err := json.Unmarshal(body, &in); err != nil || in.PullRequest.ID == 0 || in.PullRequest.ToRef.Repository.full() == "" {
		return nil, errors.New("the pull request webhook is not what the host sends")
	}
	kind := map[string]forge.EventKind{"pr:opened": forge.EventProposalOpened, "pr:from_ref_updated": forge.EventProposalRevised, "pr:modified": forge.EventProposalRevised,
		"pr:merged": forge.EventProposalLanded, "pr:declined": forge.EventProposalClosed, "pr:deleted": forge.EventProposalClosed}[key]
	pr := in.PullRequest
	return []forge.Event{{DeliveryID: delivery, Kind: kind, Repository: pr.ToRef.Repository.full(), Number: pr.ID, Ref: "refs/pull/" + fmt.Sprint(pr.ID) + "/head",
		Commit: pr.FromRef.LatestCommit, Head: pr.FromRef.LatestCommit, Base: strings.TrimPrefix(pr.ToRef.ID, "refs/heads/"), Title: pr.Title, Draft: pr.Draft, Fork: pr.fork(),
		Author: pr.Author.User.Name, URL: pr.url(), Proposal: fmt.Sprint(pr.ID), Actor: in.Actor.principal(), At: now}}, nil
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
