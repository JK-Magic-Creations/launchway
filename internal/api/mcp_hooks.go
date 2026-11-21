package api

// The MCP tools for webhooks of repositories and owners. Each calls the same
// route as the API. A hook belongs to a repository (owner and name) or to an
// owner (name left out). A secret is write-only: a tool never returns one,
// except the one Launchway makes when none is given, shown once at creation.

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addHookTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_webhooks", Annotations: readOnly("List webhooks"),
		Description: "Repository administrators only (owner hooks: administrators). The webhooks of one repository (owner and name) or, with name left out, of an owner's repositories: address, events, whether on, failures in a row and why it was turned off. Secrets are never shown."},
		mcpCallTimeout, listWebhooks)
	addTool(srv, c, &mcp.Tool{Name: "list_webhook_deliveries", Annotations: readOnly("List the deliveries of a webhook"),
		Description: "The newest deliveries of a webhook: event, state, attempts, response status, duration, the error in words, and the request body."},
		mcpCallTimeout, listWebhookDeliveries)
	if c.mayOperate {
		addTool(srv, c, &mcp.Tool{Name: "add_webhook", Annotations: changes("Add a webhook", false, false),
			Description: "Send events (push, create, delete, repository, pull_request, pull_request_review) of one repository, or of every repository of an owner (name left out), to an address. Each delivery is signed with HMAC-SHA256 in X-Launchway-Signature-256 under the secret you give or one Launchway makes and shows once. Internal addresses need the installation's egress allow-list. Audited."},
			mcpCallTimeout, addWebhook)
		addTool(srv, c, &mcp.Tool{Name: "update_webhook", Annotations: changes("Change a webhook", false, true),
			Description: "Change a webhook's name, address, events or secret, or turn it on (clears the failure count and the notice) or off. A field left out keeps its value. Audited."},
			mcpCallTimeout, updateWebhook)
		addTool(srv, c, &mcp.Tool{Name: "delete_webhook", Annotations: changes("Delete a webhook", true, true),
			Description: "Delete a webhook and its delivery log. Audited."},
			mcpCallTimeout, deleteWebhook)
		addTool(srv, c, &mcp.Tool{Name: "test_webhook", Annotations: changes("Send a test delivery", false, false),
			Description: "Send a test delivery (event ping) now, once, and say what the address answered. Audited."},
			mcpCallTimeout, testWebhook)
		addTool(srv, c, &mcp.Tool{Name: "redeliver_webhook", Annotations: changes("Send a delivery again", false, false),
			Description: "Send an earlier delivery's request again, now, once, as a new delivery. Audited."},
			mcpCallTimeout, redeliverWebhook)
	}
}

func hooksPath(owner, name string) (string, error) {
	switch {
	case owner == "":
		return "", errors.New("say whose webhooks: owner, and name for one repository")
	case name == "":
		return "/api/v1/owners/" + url.PathEscape(owner) + "/hooks", nil
	}
	return apiPath("/api/v1/repositories/%s/%s/hooks", owner, name), nil
}

type hookScopeIn struct {
	Owner string `json:"owner" jsonschema:"the repository's owner, or the owner whose repositories the hooks cover"`
	Name  string `json:"name,omitempty" jsonschema:"the repository's name; leave out for an owner's hooks"`
}

type hookOut struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Events         []string `json:"events"`
	Active         bool     `json:"active"`
	HasSecret      bool     `json:"has_secret"`
	Failures       int      `json:"failures"`
	DisabledReason string   `json:"disabled_reason,omitempty"`
}

type hooksOut struct {
	Summary string    `json:"summary"`
	Hooks   []hookOut `json:"webhooks"`
}

func hookView(h hookOut) hookOut {
	h.Name, h.URL, h.DisabledReason = untrusted(h.Name, 100), untrusted(h.URL, 500), untrusted(h.DisabledReason, 500)
	return h
}

func listWebhooks(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in hookScopeIn) (hooksOut, error) {
	p, err := hooksPath(in.Owner, in.Name)
	if err != nil {
		return hooksOut{}, err
	}
	var page struct {
		Items []hookOut `json:"items"`
	}
	if err := c.apiJSON(ctx, "list_webhooks", http.MethodGet, p, nil, &page); err != nil {
		return hooksOut{}, err
	}
	out := hooksOut{Hooks: []hookOut{}}
	for _, h := range page.Items {
		out.Hooks = append(out.Hooks, hookView(h))
	}
	out.Summary = pluralN(len(out.Hooks), "webhook")
	return out, nil
}

type hookIDIn struct {
	Owner string `json:"owner" jsonschema:"the repository's owner, or the owner whose repositories the hook covers"`
	Name  string `json:"name,omitempty" jsonschema:"the repository's name; leave out for an owner's hook"`
	ID    string `json:"id" jsonschema:"the webhook's ID, from the list"`
}

type deliveryOut struct {
	ID         string `json:"id"`
	Event      string `json:"event"`
	State      string `json:"state"`
	Attempts   int    `json:"attempts"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
	Test       bool   `json:"test,omitempty"`
	Payload    string `json:"payload,omitempty"`
}

type deliveriesOut struct {
	Summary    string        `json:"summary"`
	Deliveries []deliveryOut `json:"deliveries"`
}

func deliveryView(d deliveryOut) deliveryOut {
	d.Error, d.Payload = untrusted(d.Error, 500), untrusted(d.Payload, 2000)
	return d
}

func (in hookIDIn) path(suffix string) (string, error) {
	base, err := hooksPath(in.Owner, in.Name)
	if err != nil {
		return "", err
	}
	if in.ID == "" {
		return "", errors.New("say which webhook: its id")
	}
	return base + "/" + url.PathEscape(in.ID) + suffix, nil
}

func listWebhookDeliveries(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in hookIDIn) (deliveriesOut, error) {
	p, err := in.path("/deliveries")
	if err != nil {
		return deliveriesOut{}, err
	}
	var page struct {
		Items []struct {
			deliveryOut
			Payload any `json:"payload"`
		} `json:"items"`
	}
	if err := c.apiJSON(ctx, "list_webhook_deliveries", http.MethodGet, p, nil, &page); err != nil {
		return deliveriesOut{}, err
	}
	out := deliveriesOut{Deliveries: []deliveryOut{}}
	for _, d := range page.Items {
		d.deliveryOut.Payload = ""
		out.Deliveries = append(out.Deliveries, deliveryView(d.deliveryOut))
	}
	out.Summary = pluralN(len(out.Deliveries), "delivery")
	return out, nil
}

type addWebhookIn struct {
	Owner  string   `json:"owner" jsonschema:"the repository's owner, or the owner whose repositories the hook covers"`
	Name   string   `json:"name,omitempty" jsonschema:"the repository's name; leave out for an owner's hook"`
	Label  string   `json:"label,omitempty" jsonschema:"a name for the hook (default: its address)"`
	URL    string   `json:"url" jsonschema:"the address that receives the deliveries, https:// recommended"`
	Events []string `json:"events" jsonschema:"push, create, delete, repository, pull_request, pull_request_review"`
	Secret string   `json:"secret,omitempty" jsonschema:"the secret that signs each delivery; left out, Launchway makes one and shows it once"`
}

type addedWebhookOut struct {
	Webhook hookOut `json:"webhook"`
	Secret  string  `json:"secret,omitempty"`
	Note    string  `json:"note,omitempty"`
}

func addWebhook(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in addWebhookIn) (addedWebhookOut, error) {
	p, err := hooksPath(in.Owner, in.Name)
	if err != nil {
		return addedWebhookOut{}, err
	}
	var got struct {
		Hook   hookOut `json:"hook"`
		Secret string  `json:"secret"`
		Note   string  `json:"secret_note"`
	}
	body := map[string]any{"name": in.Label, "url": in.URL, "events": in.Events}
	if in.Secret != "" {
		body["secret"] = in.Secret
	}
	if err := c.apiJSON(ctx, "add_webhook", http.MethodPost, p, body, &got); err != nil {
		return addedWebhookOut{}, err
	}
	return addedWebhookOut{Webhook: hookView(got.Hook), Secret: got.Secret, Note: got.Note}, nil
}

type updateWebhookIn struct {
	hookIDIn
	Label  *string  `json:"label,omitempty" jsonschema:"a new name"`
	URL    *string  `json:"url,omitempty" jsonschema:"a new address"`
	Events []string `json:"events,omitempty" jsonschema:"a new list of events"`
	Active *bool    `json:"active,omitempty" jsonschema:"turn it on (clears the notice) or off"`
	Secret *string  `json:"secret,omitempty" jsonschema:"a new secret; an empty string removes signing"`
}

func updateWebhook(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in updateWebhookIn) (hookOut, error) {
	p, err := in.path("")
	if err != nil {
		return hookOut{}, err
	}
	body := map[string]any{}
	if in.Label != nil {
		body["name"] = *in.Label
	}
	if in.URL != nil {
		body["url"] = *in.URL
	}
	if in.Events != nil {
		body["events"] = in.Events
	}
	if in.Active != nil {
		body["active"] = *in.Active
	}
	if in.Secret != nil {
		body["secret"] = *in.Secret
	}
	if len(body) == 0 {
		return hookOut{}, errors.New("say what to change: label, url, events, active or secret")
	}
	var out hookOut
	err = c.apiJSON(ctx, "update_webhook", http.MethodPatch, p, body, &out)
	return hookView(out), err
}

func deleteWebhook(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in hookIDIn) (deletedKeyOut, error) {
	p, err := in.path("")
	if err != nil {
		return deletedKeyOut{}, err
	}
	if err := c.apiJSON(ctx, "delete_webhook", http.MethodDelete, p, nil, nil); err != nil {
		return deletedKeyOut{}, err
	}
	return deletedKeyOut{Deleted: in.ID}, nil
}

func testWebhook(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in hookIDIn) (deliveryOut, error) {
	p, err := in.path("/test")
	if err != nil {
		return deliveryOut{}, err
	}
	var d deliveryOut
	err = c.apiJSON(ctx, "test_webhook", http.MethodPost, p, map[string]any{}, &d)
	return deliveryView(d), err
}

type redeliverIn struct {
	hookIDIn
	Delivery string `json:"delivery" jsonschema:"the delivery's ID, from the list of deliveries"`
}

func redeliverWebhook(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in redeliverIn) (deliveryOut, error) {
	if in.Delivery == "" {
		return deliveryOut{}, errors.New("say which delivery: its id")
	}
	p, err := in.path("/deliveries/" + url.PathEscape(in.Delivery) + "/redeliver")
	if err != nil {
		return deliveryOut{}, err
	}
	var d deliveryOut
	err = c.apiJSON(ctx, "redeliver_webhook", http.MethodPost, p, map[string]any{}, &d)
	return deliveryView(d), err
}
