package api

// The MCP tools for notifications people receive: the unified inbox, the
// person's choices, and (for administrators) the mail settings' status and a
// test message. Each calls the same route as the API.

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addNoticeTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "get_inbox", Annotations: readOnly("Your inbox"),
		Description: "Your one inbox: notices (a review asked of you, mentions, comments, merges, failed and fixed runs, approvals waiting, deployments, releases you watch, credentials about to expire), work entries and the pull requests that wait on you, newest first, with how many are unread and how many wait on you. Filter by state (unread, open, done, all), kind or source (notice, work, pull)."},
		mcpCallTimeout, getInbox)
	addTool(srv, c, &mcp.Tool{Name: "mark_inbox", Annotations: changes("Mark inbox entries", false, true),
		Description: "Mark your inbox entries (by id, from get_inbox) read or unread, or done or open again. Pull requests that wait on you cannot be marked: they leave when dealt with."},
		mcpCallTimeout, markInbox)
	addTool(srv, c, &mcp.Tool{Name: "get_notice_preferences", Annotations: readOnly("Your notification choices"),
		Description: "For each kind of notice, whether you get it in the inbox only (in_app), by e-mail too (email) or not at all (off); your watch level (all, participating, none) per repository and Flight; your quiet hours and daily digest; and whether mail is on and your address known."},
		mcpCallTimeout, getNoticePrefs)
	addTool(srv, c, &mcp.Tool{Name: "set_notice_preferences", Annotations: changes("Set your notification choices", false, true),
		Description: "Change your choices: kinds (kind to in_app, email or off), watches (scope repo:owner/name or flight:name, level all, participating or none), settings (quiet_start and quiet_end in minutes after midnight or -1 for none, zone, digest, digest_hour). What you leave out is unchanged."},
		mcpCallTimeout, setNoticePrefs)
	if c.mayAdminister {
		addTool(srv, c, &mcp.Tool{Name: "get_mail_status", Annotations: readOnly("Mail settings and outbox"),
			Description: "Administrators only. Whether the installation sends mail, to which server and how it is secured, the sender, the limits, and how many messages wait, were sent and failed. Credentials are references and are never shown."},
			mcpCallTimeout, getMailStatus)
		addTool(srv, c, &mcp.Tool{Name: "send_test_mail", Annotations: changes("Send a test message", false, false),
			Description: "Administrators only. Send one test message through the mail settings, to the address given or else your own, and say how it went. Audited."},
			mcpCallTimeout, sendTestMail)
	}
}

type noticeInboxIn struct {
	State  string `json:"state,omitempty" jsonschema:"unread, open (the default), done or all"`
	Kind   string `json:"kind,omitempty" jsonschema:"only one kind of notice, such as review_requested"`
	Source string `json:"source,omitempty" jsonschema:"notice, work or pull"`
}

type noticeInboxOut struct {
	Summary string          `json:"summary"`
	Unread  int             `json:"unread"`
	Waiting int             `json:"waiting_on_you"`
	Items   []noticeItemOut `json:"items"`
}

type noticeItemOut struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Detail     string `json:"detail,omitempty"`
	Repository string `json:"repository,omitempty"`
	Link       string `json:"link,omitempty"`
	Actor      string `json:"actor,omitempty"`
	At         string `json:"at"`
	Read       bool   `json:"read"`
	Done       bool   `json:"done"`
}

func getInbox(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in noticeInboxIn) (noticeInboxOut, error) {
	q := url.Values{}
	for k, v := range map[string]string{"state": in.State, "kind": in.Kind, "source": in.Source} {
		if v != "" {
			q.Set(k, v)
		}
	}
	var page struct {
		Items   []noticeItemOut `json:"items"`
		Unread  int             `json:"unread"`
		Waiting int             `json:"waiting"`
	}
	if err := c.apiJSON(ctx, "get_inbox", http.MethodGet, "/api/v1/inbox?"+q.Encode(), nil, &page); err != nil {
		return noticeInboxOut{}, err
	}
	for i := range page.Items {
		it := &page.Items[i]
		it.Title, it.Detail, it.Actor = untrusted(it.Title, 300), untrusted(it.Detail, 1000), untrusted(it.Actor, 100)
	}
	return noticeInboxOut{Summary: pluralN(len(page.Items), "entry") + ", " + pluralN(page.Unread, "unread") + ", " + pluralN(page.Waiting, "waiting on you"),
		Unread: page.Unread, Waiting: page.Waiting, Items: orEmpty(page.Items)}, nil
}

type markInboxIn struct {
	IDs    []string `json:"ids" jsonschema:"entry ids from get_inbox, up to 200"`
	Action string   `json:"action" jsonschema:"read, unread, done or open"`
}

type changedOut struct {
	Changed int `json:"changed"`
}

func markInbox(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in markInboxIn) (changedOut, error) {
	path, value := "", true
	switch in.Action {
	case "read":
		path = "/api/v1/inbox/read"
	case "unread":
		path, value = "/api/v1/inbox/read", false
	case "done":
		path = "/api/v1/inbox/done"
	case "open":
		path, value = "/api/v1/inbox/done", false
	default:
		return changedOut{}, errors.New("action is read, unread, done or open")
	}
	var out changedOut
	err := c.apiJSON(ctx, "mark_inbox", http.MethodPut, path, map[string]any{"ids": in.IDs, "value": value}, &out)
	return out, err
}

func getNoticePrefs(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (map[string]any, error) {
	var out map[string]any
	err := c.apiJSON(ctx, "get_notice_preferences", http.MethodGet, "/api/v1/notices/preferences", nil, &out)
	return out, err
}

type setPrefsIn struct {
	Kinds    map[string]string `json:"kinds,omitempty" jsonschema:"kind to in_app, email or off"`
	Watches  []watchIn         `json:"watches,omitempty" jsonschema:"levels per repository or Flight; participating removes a watch"`
	Settings map[string]any    `json:"settings,omitempty" jsonschema:"quiet_start, quiet_end, zone, digest, digest_hour; give all the quiet and digest fields you want to keep"`
}

type watchIn struct {
	Scope string `json:"scope" jsonschema:"repo:owner/name or flight:name"`
	Level string `json:"level" jsonschema:"all, participating or none"`
}

func setNoticePrefs(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in setPrefsIn) (map[string]any, error) {
	body := map[string]any{}
	if len(in.Kinds) > 0 {
		body["kinds"] = in.Kinds
	}
	if len(in.Watches) > 0 {
		body["watches"] = in.Watches
	}
	if len(in.Settings) > 0 {
		body["settings"] = in.Settings
	}
	var out map[string]any
	err := c.apiJSON(ctx, "set_notice_preferences", http.MethodPut, "/api/v1/notices/preferences", body, &out)
	return out, err
}

func getMailStatus(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (mailOut, error) {
	var out mailOut
	err := c.apiJSON(ctx, "get_mail_status", http.MethodGet, "/api/v1/mail", nil, &out)
	return out, err
}

type testMailIn struct {
	To string `json:"to,omitempty" jsonschema:"the address to send to (default: your own)"`
}

func sendTestMail(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in testMailIn) (map[string]any, error) {
	var out map[string]any
	err := c.apiJSON(ctx, "send_test_mail", http.MethodPost, "/api/v1/mail/test", map[string]any{"to": in.To}, &out)
	return out, err
}
