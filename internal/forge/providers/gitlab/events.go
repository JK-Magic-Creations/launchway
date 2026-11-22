package gitlab

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
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type events struct{ p *Provider }

func (events) Mode() forge.EventMode { return forge.EventsWebhook }

// maxDelivery bounds what is read of one delivery; the documentation names no
// payload cap, so this is Launchway's own.
const maxDelivery = 25 << 20

// ErrBadSignature is returned for a delivery whose secret token does not match.
var ErrBadSignature = errors.New("the webhook secret token does not match")

// DeliveryHeader is the header that carries a delivery's unique ID: "webhook-id",
// "a unique message ID that stays the same across retries" (equal to
// Idempotency-Key, which the documentation keeps for legacy reasons;
// https://docs.gitlab.com/user/project/integrations/webhooks/). X-Gitlab-Event-UUID is not
// used: webhooks triggered by earlier webhooks share it.
const DeliveryHeader = "webhook-id"

// Verify checks a delivery and normalises it. Headers (documented at
// https://docs.gitlab.com/user/project/integrations/webhooks/): X-Gitlab-Token,
// "the secret token for the webhook, sent as plain text", which is compared
// here in constant time; and X-Gitlab-Event ("Push Hook", "Tag Push Hook",
// "Merge Request Hook": https://docs.gitlab.com/user/project/integrations/webhook_events/).
// A delivery without the token is refused: a connection always has a webhook
// secret. A delivery with no unique ID header is given one made from its
// body, so that replaying the same bytes is dropped too.
func (e events) Verify(r *http.Request, secret []byte) ([]forge.Event, error) {
	if len(secret) == 0 {
		return nil, errors.New("the connection has no webhook secret")
	}
	got := r.Header.Get("X-Gitlab-Token")
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256(secret)
	if got == "" || subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		return nil, ErrBadSignature
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDelivery+1))
	if err != nil || len(body) > maxDelivery {
		return nil, errors.New("the webhook body cannot be read or is too large")
	}
	delivery := r.Header.Get(DeliveryHeader)
	if delivery == "" {
		delivery = r.Header.Get("Idempotency-Key")
	}
	if delivery == "" {
		sum := sha256.Sum256(body)
		delivery = "body-" + hex.EncodeToString(sum[:16])
	}
	now := e.p.c.Now().UTC()
	switch ev := r.Header.Get("X-Gitlab-Event"); ev {
	case "Push Hook", "Tag Push Hook":
		var in struct {
			Ref      string `json:"ref"`
			Before   string `json:"before"`
			After    string `json:"after"`
			Checkout string `json:"checkout_sha"`
			User     string `json:"user_username"`
			Project  struct {
				Path string `json:"path_with_namespace"`
			} `json:"project"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.Ref == "" || in.Project.Path == "" {
			return nil, errors.New("the push webhook is not what the host sends")
		}
		return []forge.Event{{DeliveryID: delivery, Kind: forge.EventRefUpdated, Repository: in.Project.Path, Ref: in.Ref,
			Old: in.Before, Commit: in.After, Actor: forge.Principal{Login: in.User, Unlinked: true}, At: now}}, nil
	case "Merge Request Hook":
		var in struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
			Project struct {
				Path string `json:"path_with_namespace"`
			} `json:"project"`
			Attrs struct {
				IID          int    `json:"iid"`
				Title        string `json:"title"`
				Action       string `json:"action"`
				Draft        bool   `json:"draft"`
				SourceBranch string `json:"source_branch"`
				TargetBranch string `json:"target_branch"`
				SourceID     int    `json:"source_project_id"`
				TargetID     int    `json:"target_project_id"`
				URL          string `json:"url"`
				Last         struct {
					ID string `json:"id"`
				} `json:"last_commit"`
			} `json:"object_attributes"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.Attrs.IID == 0 || in.Project.Path == "" {
			return nil, errors.New("the merge request webhook is not what the host sends")
		}
		var kind forge.EventKind
		switch in.Attrs.Action {
		case "open", "reopen":
			kind = forge.EventProposalOpened
		case "update":
			kind = forge.EventProposalRevised
		case "close":
			kind = forge.EventProposalClosed
		case "merge":
			kind = forge.EventProposalLanded
		default:
			return nil, nil // approvals and the rest do not start anything
		}
		n := in.Attrs.IID
		return []forge.Event{{DeliveryID: delivery, Kind: kind, Repository: in.Project.Path, Number: n,
			Ref: "refs/pull/" + fmt.Sprint(n) + "/head", Commit: in.Attrs.Last.ID, Head: in.Attrs.Last.ID, Base: in.Attrs.TargetBranch,
			Title: in.Attrs.Title, Draft: in.Attrs.Draft, Fork: in.Attrs.SourceID != in.Attrs.TargetID, Author: in.User.Username,
			URL: in.Attrs.URL, Proposal: fmt.Sprint(n), Actor: forge.Principal{Login: in.User.Username, Unlinked: true}, At: now}}, nil
	}
	return nil, nil
}

// Poll finds what changed on merge requests when no webhook arrives: it lists
// the open merge requests (GET /projects/:id/merge_requests?state=opened, one
// page of 50) and returns proposal.opened for one not seen and
// proposal.revised for one whose head moved. The cursor is the numbers and
// heads seen, as text; "" is the first call, which reports every open merge
// request as opened. Pushes and tags are found by Launchway's own watcher of
// the repository's clone.
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
