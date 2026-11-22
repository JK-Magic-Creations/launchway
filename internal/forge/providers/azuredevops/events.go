package azuredevops

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type events struct{ p *Provider }

func (events) Mode() forge.EventMode { return forge.EventsWebhook }

const maxDelivery = 25 << 20

// HookAuthHeader is the HTTP header a subscription may carry the connection's
// webhook secret in. The web hooks consumer's "HTTP headers" input takes "header
// keys and values separated by a colon ... each key-value-pair appearing on its
// own line of text" (https://learn.microsoft.com/en-us/rest/api/azure/devops/hooks/consumers/list,
// consumer webHooks, action httpRequest, input httpHeaders).
const HookAuthHeader = "X-Launchway-Webhook-Secret"

// ErrBadCredential is returned for a delivery whose secret is missing or wrong.
var ErrBadCredential = errors.New("the webhook's secret is missing or does not match")

// authentic checks the delivery's secret in constant time, both ways the web
// hooks consumer can send one: the password of HTTP Basic (inputs
// basicAuthUsername and basicAuthPassword of the same action; the user name
// is free) or the HookAuthHeader. Both are always evaluated, and each is
// compared as a SHA-256 digest so that neither the length nor a prefix of the
// secret shows in the time taken. A service hook has no signature of the body,
// so this is weaker than the other hosts' checks (see Capabilities).
func authentic(r *http.Request, secret []byte) bool {
	want := sha256.Sum256(secret)
	_, pass, hasBasic := r.BasicAuth()
	gotBasic := sha256.Sum256([]byte(pass))
	gotHeader := sha256.Sum256([]byte(r.Header.Get(HookAuthHeader)))
	okBasic := subtle.ConstantTimeCompare(want[:], gotBasic[:]) == 1 && hasBasic && pass != ""
	okHeader := subtle.ConstantTimeCompare(want[:], gotHeader[:]) == 1 && r.Header.Get(HookAuthHeader) != ""
	return okBasic || okHeader
}

// envelope is the common part of a service hook's JSON payload
// (https://learn.microsoft.com/en-us/azure/devops/service-hooks/events): id,
// eventType, resource and resourceContainers.
type envelope struct {
	ID                string          `json:"id"`
	NotificationID    int             `json:"notificationId"`
	EventType         string          `json:"eventType"`
	CreatedDate       string          `json:"createdDate"`
	Resource          json.RawMessage `json:"resource"`
	ResourceContainer struct {
		Collection struct {
			BaseURL string `json:"baseUrl"`
		} `json:"collection"`
		Account struct {
			BaseURL string `json:"baseUrl"`
		} `json:"account"`
	} `json:"resourceContainers"`
}

// orgOf is the organization (or collection) the payload says it comes from:
// the last element of the collection's base address, or the first label of a
// "{org}.visualstudio.com" address.
func (e envelope) orgOf() string {
	for _, raw := range []string{e.ResourceContainer.Collection.BaseURL, e.ResourceContainer.Account.BaseURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		if segs := strings.Split(strings.Trim(u.Path, "/"), "/"); segs[0] != "" {
			return segs[len(segs)-1]
		}
		if h, _, ok := strings.Cut(u.Host, ".visualstudio.com"); ok && h != "" {
			return h
		}
	}
	return ""
}

// delivery names a delivery for the replay check: the payload's own id (the
// notification's), else its notification number with the event type, else a
// hash of the body. The host sends no header that names a delivery.
func (e envelope) delivery(body []byte) string {
	switch {
	case e.ID != "":
		return e.ID
	case e.NotificationID != 0:
		return "n" + strconv.Itoa(e.NotificationID) + "-" + e.EventType
	}
	sum := sha256.Sum256(body)
	return "body-" + hex.EncodeToString(sum[:16])
}

type apiRepoRef struct {
	Name    string `json:"name"`
	Project struct {
		Name string `json:"name"`
	} `json:"project"`
}

// Verify checks a delivery and normalises it. The secret is checked first (see
// authentic); a delivery without it is refused and its body is not read. The
// event is the payload's eventType
// (https://learn.microsoft.com/en-us/azure/devops/service-hooks/events):
// git.push (one event for each ref the push updated), git.pullrequest.created,
// git.pullrequest.updated (a completed or abandoned status is a landed or
// closed pull request) and git.pullrequest.merged (a merge was attempted: a
// revision). Any other event starts nothing.
func (e events) Verify(r *http.Request, secret []byte) ([]forge.Event, error) {
	if len(secret) == 0 {
		return nil, errors.New("the connection has no webhook secret")
	}
	if !authentic(r, secret) {
		return nil, ErrBadCredential
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDelivery+1))
	if err != nil || len(body) > maxDelivery {
		return nil, errors.New("the webhook body cannot be read or is too large")
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil || env.EventType == "" {
		return nil, errors.New("the webhook is not what the host sends")
	}
	id := env.delivery(body)
	switch env.EventType {
	case "git.push":
		return e.push(env, id)
	case "git.pullrequest.created", "git.pullrequest.updated", "git.pullrequest.merged":
		return e.pull(env, id)
	}
	return nil, nil
}

func (e events) push(env envelope, id string) ([]forge.Event, error) {
	var in struct {
		RefUpdates []struct {
			Name        string `json:"name"`
			OldObjectID string `json:"oldObjectId"`
			NewObjectID string `json:"newObjectId"`
		} `json:"refUpdates"`
		Repository apiRepoRef  `json:"repository"`
		PushedBy   apiIdentity `json:"pushedBy"`
	}
	org := env.orgOf()
	if err := json.Unmarshal(env.Resource, &in); err != nil || org == "" || in.Repository.Name == "" || in.Repository.Project.Name == "" {
		return nil, errors.New("the push webhook is not what the host sends")
	}
	at := parseTime(env.CreatedDate)
	if at.IsZero() {
		at = e.p.c.Now().UTC()
	}
	var out []forge.Event
	for i, u := range in.RefUpdates {
		ev := forge.Event{DeliveryID: fmt.Sprintf("%s-%d", id, i), Kind: forge.EventRefUpdated, Repository: org + "/" + in.Repository.Project.Name + "/" + in.Repository.Name,
			Ref: u.Name, Old: orZeros(u.OldObjectID), Commit: orZeros(u.NewObjectID), Actor: forge.Principal{Login: in.PushedBy.login(), Unlinked: true}, At: at}
		out = append(out, ev)
	}
	return out, nil
}

func orZeros(s string) string {
	if s == "" {
		return zeros
	}
	return s
}

func (e events) pull(env envelope, id string) ([]forge.Event, error) {
	var pr apiPull
	org := env.orgOf()
	if err := json.Unmarshal(env.Resource, &pr); err != nil || org == "" || pr.ID == 0 || pr.Repository.Name == "" || pr.Repository.Project.Name == "" {
		return nil, errors.New("the pull request webhook is not what the host sends")
	}
	kind := forge.EventProposalRevised
	switch {
	case env.EventType == "git.pullrequest.created":
		kind = forge.EventProposalOpened
	case env.EventType == "git.pullrequest.updated" && pr.Status == "completed":
		kind = forge.EventProposalLanded
	case env.EventType == "git.pullrequest.updated" && pr.Status == "abandoned":
		kind = forge.EventProposalClosed
	}
	at := parseTime(env.CreatedDate)
	if at.IsZero() {
		at = e.p.c.Now().UTC()
	}
	head := pr.LastMergeSourceCommit.CommitID
	return []forge.Event{{DeliveryID: id, Kind: kind, Repository: org + "/" + pr.Repository.Project.Name + "/" + pr.Repository.Name, Number: pr.ID,
		Ref: pr.SourceRef, Commit: head, Head: head, Base: trimRef(pr.TargetRef), Title: pr.Title, Draft: pr.IsDraft, Fork: pr.fork(),
		Author: pr.CreatedBy.login(), URL: pr.webURL(), Proposal: strconv.Itoa(pr.ID), Actor: forge.Principal{Login: pr.CreatedBy.login(), Unlinked: true}, At: at}}, nil
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
			Ref: pr.Source.Name, Commit: pr.Head, Head: pr.Head, Base: trimRef(pr.Target.Name), Title: pr.Title, Proposal: strconv.Itoa(pr.Number),
			Draft: pr.State == forge.ProposalDraft, Fork: fork, Author: pr.Author.Login, URL: prURL,
			Actor: forge.Principal{Login: pr.Author.Login, Unlinked: true}, At: e.p.c.Now().UTC()})
	}
	b, _ := json.Marshal(next)
	return out, base64.RawURLEncoding.EncodeToString(b), nil
}
