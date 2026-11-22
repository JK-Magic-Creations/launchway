package api

// The MCP tools for work tracking (internal/work). Each calls the same route
// as the API, in process, as the caller, so a tool does exactly what the route
// lets that caller do. Everything in an item, comment or criterion was written
// by someone else, so every string in an answer is cleaned and clipped before
// it is handed on. People are offered the person tools; an agent's token is
// offered only the calls an agent makes (and the four it shares with people),
// never the person tools.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// workOut is what a work tool answers: the route's object with its text cleaned.
type workOut = map[string]any

const workTextMax = 20000

// clipAll cleans every string of a decoded JSON value.
func clipAll(v any) any {
	switch x := v.(type) {
	case string:
		return untrusted(x, workTextMax)
	case []any:
		for i := range x {
			x[i] = clipAll(x[i])
		}
	case map[string]any:
		for k, e := range x {
			x[k] = clipAll(e)
		}
	}
	return v
}

// workCall calls a work route as the caller and returns its answer cleaned.
func workCall(ctx context.Context, c *mcpCall, tool, method, path string, body any) (workOut, error) {
	var raw any
	if err := c.apiJSON(ctx, tool, method, path, body, &raw); err != nil {
		return nil, err
	}
	if m, ok := clipAll(raw).(map[string]any); ok {
		return m, nil
	}
	return workOut{"result": clipAll(raw)}, nil
}

// bodyOf is a tool's input as a request body, without the fields the route
// takes elsewhere (in the path or the query).
func bodyOf(in any, drop ...string) (map[string]any, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for _, k := range drop {
		delete(m, k)
	}
	return m, nil
}

// workGet makes a tool that reads one route.
func workGet[In any](tool string, path func(In) (string, error)) toolFunc[In, workOut] {
	return func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in In) (workOut, error) {
		p, err := path(in)
		if err != nil {
			return nil, err
		}
		return workCall(ctx, c, tool, http.MethodGet, p, nil)
	}
}

// workSend makes a tool that changes something through one route; body makes
// the request body from the input (nil for none).
func workSend[In any](tool, method string, path func(In) (string, error), body func(In) (any, error)) toolFunc[In, workOut] {
	return func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in In) (workOut, error) {
		p, err := path(in)
		if err != nil {
			return nil, err
		}
		var b any
		if body != nil {
			if b, err = body(in); err != nil {
				return nil, err
			}
		}
		return workCall(ctx, c, tool, method, p, b)
	}
}

// fixed is a path that needs nothing from the input.
func fixed[In any](p string) func(In) (string, error) {
	return func(In) (string, error) { return p, nil }
}

// whole sends the input as the request body.
func whole[In any](in In) (any, error) { return in, nil }

// without sends the input as the request body, less the named fields.
func without[In any](drop ...string) func(In) (any, error) {
	return func(in In) (any, error) { return bodyOf(in, drop...) }
}

func needItem(item string) error {
	if item == "" {
		return errors.New("say which item: its reference, such as KEY-12")
	}
	return nil
}

// itemPath is a route under an item.
func itemPath[In interface{ itemRef() string }](suffix string) func(In) (string, error) {
	return func(in In) (string, error) {
		if err := needItem(in.itemRef()); err != nil {
			return "", err
		}
		return apiPath("/api/v1/work/items/%s", in.itemRef()) + suffix, nil
	}
}

// itemIn is the input of the tools that name one item.
type itemIn struct {
	Item string `json:"item" jsonschema:"the item's reference, such as KEY-12"`
}

func (i itemIn) itemRef() string { return i.Item }

// scopeIn is the input of the tools that work at one level.
type scopeIn struct {
	Scope string `json:"scope" jsonschema:"where: project:owner/name (a bare owner/name is a project), unit:NAME for a Squadron, organization for the Wing, or installation for Base"`
}

func (s scopeIn) scopeQuery() (url.Values, error) {
	if _, err := parseWorkScope(s.Scope); err != nil {
		return nil, err
	}
	return url.Values{"scope": {s.Scope}}, nil
}

func scopedPath[In interface{ scopeQuery() (url.Values, error) }](p string, more func(In, url.Values)) func(In) (string, error) {
	return func(in In) (string, error) {
		q, err := in.scopeQuery()
		if err != nil {
			return "", err
		}
		if more != nil {
			more(in, q)
		}
		return p + "?" + q.Encode(), nil
	}
}

func setS(q url.Values, k, v string) {
	if v != "" {
		q.Set(k, v)
	}
}

func setN[N ~int | ~int64](q url.Values, k string, n N) {
	if n > 0 {
		q.Set(k, strconv.FormatInt(int64(n), 10))
	}
}

// addWorkTools offers the work tools this caller may use.
func addWorkTools(srv *mcp.Server, c *mcpCall) {
	if c.id != nil && c.id.agent != nil {
		addWorkAgentTools(srv, c)
		return
	}
	addWorkReadTools(srv, c)
	addWorkPlanReadTools(srv, c)
	addWorkAgentReadTools(srv, c)
	addWorkProposalReadTools(srv, c)
	if c.mayOperate {
		addWorkChangeTools(srv, c)
		addWorkPlanChangeTools(srv, c)
		addWorkAgentManageTools(srv, c)
		addWorkImportTools(srv, c)
		addWorkProposalChangeTools(srv, c)
	}
}

// ---- files ----

type attachIn struct {
	Item          string `json:"item" jsonschema:"the item's reference, such as KEY-12"`
	Name          string `json:"name" jsonschema:"the file's name"`
	Kind          string `json:"kind,omitempty" jsonschema:"what the file is, such as screenshot or log"`
	ContentType   string `json:"content_type,omitempty" jsonschema:"the media type, such as text/plain (default application/octet-stream)"`
	Text          string `json:"text,omitempty" jsonschema:"the file's content, when it is text"`
	ContentBase64 string `json:"content_base64,omitempty" jsonschema:"the file's content, base64, when it is not text"`
}

func attachWorkFile(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in attachIn) (workOut, error) {
	if err := needItem(in.Item); err != nil {
		return nil, err
	}
	if in.Name == "" || (in.Text == "") == (in.ContentBase64 == "") {
		return nil, errors.New("give the file's name and its content, as text or as content_base64")
	}
	data := []byte(in.Text)
	if in.ContentBase64 != "" {
		var err error
		if data, err = base64.StdEncoding.DecodeString(in.ContentBase64); err != nil {
			return nil, errors.New("content_base64 is not base64")
		}
	}
	ct := in.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	q := url.Values{"name": {in.Name}}
	setS(q, "kind", in.Kind)
	ans, err := c.apiWithHeaders(ctx, "attach_work_file", http.MethodPost, apiPath("/api/v1/work/items/%s/attachments", in.Item)+"?"+q.Encode(), 0, data, "Content-Type", ct)
	if err != nil {
		return nil, err
	}
	if ans.Status >= 400 {
		return nil, answerError(ans)
	}
	var out any
	if err := json.Unmarshal(ans.Body, &out); err != nil {
		return nil, err
	}
	m, _ := clipAll(out).(map[string]any)
	return m, nil
}

type attachmentIn struct {
	ID string `json:"id" jsonschema:"the attachment's id, from get_work_item"`
}

// getWorkAttachment reads at most 1 MiB of a file: its text when it is text,
// otherwise base64.
func getWorkAttachment(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in attachmentIn) (workOut, error) {
	if in.ID == "" {
		return nil, errors.New("say which attachment: its id")
	}
	ans, err := c.api(ctx, "get_work_attachment", http.MethodGet, apiPath("/api/v1/work/attachments/%s", in.ID)+"?download=1", nil, 1<<20)
	if err != nil {
		return nil, err
	}
	if ans.Status >= 400 {
		return nil, answerError(ans)
	}
	if ans.Truncated {
		return nil, errors.New("the file is larger than 1 MiB; download it with the API")
	}
	out := workOut{"size": len(ans.Body), "content_type": ans.Header.Get("Content-Type")}
	if utf8.Valid(ans.Body) && !containsNUL(ans.Body) {
		out["text"] = untrusted(string(ans.Body), workTextMax)
	} else {
		out["content_base64"] = base64.StdEncoding.EncodeToString(ans.Body)
	}
	return out, nil
}

func containsNUL(b []byte) bool {
	for _, x := range b {
		if x == 0 {
			return true
		}
	}
	return false
}
