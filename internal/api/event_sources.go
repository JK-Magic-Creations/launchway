package api

// Event sources: incoming webhook sources made in the product.
//
// A source belongs to a project and has a signing secret that is generated
// when it is made, shown once, and kept sealed under the installation's root
// key. A caller signs the body with HMAC-SHA256 over "<timestamp>.<body>"
// (headers X-Launchway-Timestamp and X-Launchway-Signature: sha256=<hex>); a
// signature is accepted within five minutes and only once. What the call does
// after that is decided by internal/source (ReceiveEvent) and recorded in the
// event log. A caller that cannot prove itself, and a source that does not
// exist, get the same 404.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/source"
)

func (s *Server) eventSourceRoutes() []route {
	return []route{
		{"GET /api/v1/projects/{name}/event-sources", projectRead, "", s.listEventSources},
		{"POST /api/v1/projects/{name}/event-sources", projectOperate, "event_source.create", s.createEventSource},
		{"POST /api/v1/projects/{name}/event-sources/{source}", projectOperate, "event_source.change", s.changeEventSource},
		{"DELETE /api/v1/projects/{name}/event-sources/{source}", projectOperate, "event_source.delete", s.deleteEventSource},
		{"POST /api/v1/projects/{name}/event-sources/{source}/test", projectOperate, "event_source.test", s.testEventSource},
		{"POST /api/v1/projects/{name}/sources/{source}", public, "", s.callEventSource},
	}
}

// sourceView is a source as the API shows it: never with its secret.
type sourceView struct {
	engine.EventSource
	URL string `json:"url"`
}

func (s *Server) viewOf(r *http.Request, src engine.EventSource) sourceView {
	return sourceView{src, s.baseURL(r) + "/api/v1/projects/" + url.PathEscape(src.Project) + "/sources/" + url.PathEscape(src.Name)}
}

func (s *Server) listEventSources(w http.ResponseWriter, r *http.Request) {
	list, err := s.Engine.EventSources(r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]sourceView, len(list))
	for i, x := range list {
		out[i] = s.viewOf(r, x)
	}
	writeJSON(w, http.StatusOK, out)
}

// eventSourceIn is what a person sends to make or change a source.
type eventSourceIn struct {
	Name          string            `json:"name"`
	Schema        *string           `json:"schema"`
	Mapping       map[string]string `json:"mapping"`
	Filter        *string           `json:"filter"`
	DedupeKey     *string           `json:"dedupe_key"`
	Ref           *string           `json:"ref"`
	DebounceSecs  *int              `json:"debounce_seconds"`
	BatchSecs     *int              `json:"batch_seconds"`
	MaxBody       *int              `json:"max_body"`
	RatePerMinute *int              `json:"rate_per_minute"`
	Enabled       *bool             `json:"enabled"`
	RotateSecret  bool              `json:"rotate_secret"`
}

func (in eventSourceIn) apply(x *engine.EventSource) {
	if in.Schema != nil {
		x.Schema = *in.Schema
	}
	if in.Mapping != nil {
		x.Mapping = in.Mapping
	}
	if in.Filter != nil {
		x.Filter = *in.Filter
	}
	if in.DedupeKey != nil {
		x.DedupeKey = *in.DedupeKey
	}
	if in.Ref != nil {
		x.Ref = *in.Ref
	}
	if in.DebounceSecs != nil {
		x.DebounceSecs = *in.DebounceSecs
	}
	if in.BatchSecs != nil {
		x.BatchSecs = *in.BatchSecs
	}
	if in.MaxBody != nil {
		x.MaxBody = *in.MaxBody
	}
	if in.RatePerMinute != nil {
		x.RatePerMinute = *in.RatePerMinute
	}
	if in.Enabled != nil {
		x.Enabled = *in.Enabled
	}
}

// newSourceSecret makes a signing secret: 32 random bytes as text.
func newSourceSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "lwes_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) sealSourceSecret(ctx context.Context, id, secret string) (string, error) {
	svc, err := s.secretStore()
	if err != nil {
		return "", err
	}
	return svc.Webhooks().Seal(ctx, id, []byte(secret))
}

func (s *Server) createEventSource(w http.ResponseWriter, r *http.Request) {
	var in eventSourceIn
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, in.Name, "")
	svc, err := s.secretStore()
	if err != nil {
		s.fail(w, err)
		return
	}
	if !svc.Webhooks().Enabled() {
		writeNoRootKey(w, "Event sources")
		return
	}
	x := engine.EventSource{ID: ids.New("evs"), Project: r.PathValue("name"), Name: in.Name, Mapping: map[string]string{}, MaxBody: engine.DefaultMaxBody,
		RatePerMinute: engine.DefaultRatePerMinute, Enabled: true, CreatedBy: identityFrom(r).User}
	in.apply(&x)
	if err := source.CheckSourceExpressions(x); err != nil {
		s.fail(w, err)
		return
	}
	if err := engine.CheckEventSource(&x); err != nil {
		s.fail(w, err)
		return
	}
	secret, err := newSourceSecret()
	if err != nil {
		s.fail(w, err)
		return
	}
	sealed, err := s.sealSourceSecret(r.Context(), x.ID, secret)
	if err != nil {
		s.fail(w, err)
		return
	}
	made, err := s.Engine.CreateEventSource(x, sealed)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, made.Name, "event source "+made.Name+" of "+made.Project+"; the secret was shown once")
	writeJSON(w, http.StatusCreated, map[string]any{"source": s.viewOf(r, *made), "secret": secret,
		"note": "This is the only time the signing secret is shown. Sign each call with HMAC-SHA256 over \"<timestamp>.<body>\"; headers X-Launchway-Timestamp (Unix seconds) and X-Launchway-Signature: sha256=<hex>."})
}

func (s *Server) changeEventSource(w http.ResponseWriter, r *http.Request) {
	var in eventSourceIn
	if !decode(w, r, &in) {
		return
	}
	project, name := r.PathValue("name"), r.PathValue("source")
	setAudit(r, name, "")
	x, _, err := s.Engine.EventSourceNamed(project, name)
	if err != nil {
		s.fail(w, err)
		return
	}
	in.apply(x)
	if err := source.CheckSourceExpressions(*x); err != nil {
		s.fail(w, err)
		return
	}
	var secret, sealed string
	if in.RotateSecret {
		svc, err := s.secretStore()
		if err != nil {
			s.fail(w, err)
			return
		}
		if !svc.Webhooks().Enabled() {
			writeNoRootKey(w, "Event sources")
			return
		}
		if secret, err = newSourceSecret(); err != nil {
			s.fail(w, err)
			return
		}
		if sealed, err = s.sealSourceSecret(r.Context(), x.ID, secret); err != nil {
			s.fail(w, err)
			return
		}
	}
	if err := s.Engine.ChangeEventSource(*x, sealed); err != nil {
		s.fail(w, err)
		return
	}
	detail := "settings changed"
	if in.RotateSecret {
		detail += "; the secret was replaced and shown once"
	}
	setAudit(r, name, detail)
	out := map[string]any{"source": s.viewOf(r, *x)}
	if secret != "" {
		out["secret"] = secret
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteEventSource(w http.ResponseWriter, r *http.Request) {
	setAudit(r, r.PathValue("source"), "")
	if err := s.Engine.DeleteEventSource(r.PathValue("name"), r.PathValue("source")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// testEventSource judges a body as a call would be, and records and starts
// nothing: {"body": {...}, "headers": {...}}.
func (s *Server) testEventSource(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Body    json.RawMessage   `json:"body"`
		Headers map[string]string `json:"headers"`
	}
	if !decode(w, r, &in) {
		return
	}
	src, _, err := s.Engine.EventSourceNamed(r.PathValue("name"), r.PathValue("source"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(in.Body) == 0 {
		writeErr(w, http.StatusBadRequest, `give the body to test with: {"body": {...}}`)
		return
	}
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no projects are configured")
		return
	}
	hs := map[string]string{}
	for k, v := range in.Headers {
		hs[strings.ToLower(k)] = v
	}
	res, err := s.Projects.ReceiveEvent(r.Context(), src.Project, source.SourceCall{Source: *src, Body: in.Body, Headers: hs}, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- the call ----

// sourceRates counts calls to each source in the current minute, on this
// server (with several servers the allowance is per server).
var sourceRates = struct {
	sync.Mutex
	at map[string]int64
	n  map[string]int
}{at: map[string]int64{}, n: map[string]int{}}

func sourceRateOK(id string, limit int, now time.Time) bool {
	sourceRates.Lock()
	defer sourceRates.Unlock()
	minute := now.Unix() / 60
	if sourceRates.at[id] != minute {
		sourceRates.at[id], sourceRates.n[id] = minute, 0
	}
	sourceRates.n[id]++
	if len(sourceRates.at) > 10000 {
		clear(sourceRates.at)
		clear(sourceRates.n)
	}
	return sourceRates.n[id] <= limit
}

// callHeaders are the request's headers an expression may read: none that
// carries a credential.
func callHeaders(r *http.Request) map[string]string {
	out := map[string]string{}
	for k, v := range r.Header {
		l := strings.ToLower(k)
		if len(v) == 0 || strings.Contains(l, "authorization") || strings.Contains(l, "cookie") || strings.Contains(l, "signature") ||
			strings.Contains(l, "secret") || strings.Contains(l, "token") || l == "x-launchway-timestamp" {
			continue
		}
		out[l] = clip(v[0], 500)
	}
	return out
}

func (s *Server) callEventSource(w http.ResponseWriter, r *http.Request) {
	if s.tooManyFailures(w, r) {
		return
	}
	project, name := r.PathValue("name"), r.PathValue("source")
	who := "event-source:" + clip(name, 63)
	var src *engine.EventSource
	var sealed string
	if s.Projects != nil && engine.ValidEventSourceName(name) {
		if x, sl, err := s.Engine.EventSourceNamed(project, name); err == nil && x.Enabled {
			src, sealed = x, sl
		}
	}
	if src == nil {
		// The same answer as a wrong signature; nothing can be attributed.
		s.hookDenied(r, "event_source.call", who, "a call to an event source that does not exist or is off")
		hookNotFound(w)
		return
	}
	refuse := func(summary, reason string, code int) {
		_, _ = s.Engine.RecordEvent(engine.EventRecord{Project: project, Source: engine.SourceEventSource, SourceName: name, Verified: false, Summary: summary},
			engine.EventDecision{Trigger: "source:" + name, Decision: engine.DecRefused, Reason: reason})
		if code == http.StatusNotFound {
			hookNotFound(w)
			return
		}
		writeErr(w, code, reason)
	}
	if !sourceRateOK(src.ID, src.RatePerMinute, s.Now()) {
		w.Header().Set("Retry-After", "60")
		refuse("Call to event source "+name, fmt.Sprintf("over the source's limit of %d calls a minute", src.RatePerMinute), http.StatusTooManyRequests)
		return
	}
	if r.ContentLength > int64(src.MaxBody) {
		refuse("Call to event source "+name, fmt.Sprintf("the body is larger than the source's limit of %d bytes", src.MaxBody), http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(src.MaxBody)))
	if err != nil {
		refuse("Call to event source "+name, fmt.Sprintf("the body is larger than the source's limit of %d bytes", src.MaxBody), http.StatusRequestEntityTooLarge)
		return
	}
	signature, why := s.checkSourceSignature(r, src, sealed, body)
	if why != "" {
		s.hookDenied(r, "event_source.call", who, why)
		refuse("Call to event source "+name+" that did not prove who sent it", why, http.StatusNotFound)
		return
	}
	if fresh, _, err := s.Engine.ClaimKey(project, "sig:"+src.ID, signature, "", time.Hour); err != nil {
		s.fail(w, err)
		return
	} else if !fresh {
		_, _ = s.Engine.RecordEvent(engine.EventRecord{Project: project, Source: engine.SourceEventSource, SourceName: name, Verified: true, Summary: "Repeat of a signed call to event source " + name},
			engine.EventDecision{Trigger: "source:" + name, Decision: engine.DecRefused, Reason: "this signed request was already received; sign a new one"})
		writeErr(w, http.StatusConflict, "this signed request was already received; sign a new one")
		return
	}
	s.Engine.TouchEventSource(src.ID)
	res, err := s.Projects.ReceiveEvent(r.Context(), project, source.SourceCall{Source: *src, Body: body, Headers: callHeaders(r)}, false)
	if err != nil {
		s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: who, Source: s.clientAddr(r), RequestID: reqID(r), Action: "event_source.call", Project: project, Outcome: "failed", Detail: err.Error()})
		s.fail(w, err)
		return
	}
	entry := &engine.AuditEntry{At: s.Now().UnixMilli(), User: who, Source: s.clientAddr(r), RequestID: reqID(r), Action: "event_source.call", Project: project,
		Target: res.EventID, Outcome: "ok", Detail: res.Status + ": " + clip(res.Reason, 300)}
	if res.Status == engine.DecError || res.Status == engine.DecRefused {
		entry.Outcome = "failed"
	}
	s.record(entry)
	code := map[string]int{engine.DecStarted: http.StatusCreated, engine.DecHeld: http.StatusCreated, engine.DecWaiting: http.StatusAccepted,
		engine.DecRefused: http.StatusUnprocessableEntity, engine.DecError: http.StatusUnprocessableEntity}[res.Status]
	if code == 0 {
		code = http.StatusOK
	}
	writeJSON(w, code, res)
}

// checkSourceSignature opens the source's secret and checks the call's
// signature and timestamp. It returns the signature, or why the call is
// refused.
func (s *Server) checkSourceSignature(r *http.Request, src *engine.EventSource, sealed string, body []byte) (signature, why string) {
	svc, err := s.secretStore()
	if err != nil || sealed == "" {
		return "", "the source has no secret the server can read"
	}
	secret, err := svc.Webhooks().Open(r.Context(), src.ID, sealed)
	if err != nil {
		s.Log.Error("opening an event source's secret", "project", src.Project, "source", src.Name, "err", err)
		return "", "the server could not read the source's secret"
	}
	ts := r.Header.Get("X-Launchway-Timestamp")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return "", "no X-Launchway-Timestamp"
	}
	if d := s.Now().Sub(time.Unix(sec, 0)); d > signatureTolerance || d < -signatureTolerance {
		return "", "a signature whose timestamp is more than five minutes away"
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	sig := r.Header.Get("X-Launchway-Signature")
	if !macEqual(mac, "sha256=", sig) {
		return "", "a wrong or missing signature"
	}
	return strings.ToLower(strings.TrimPrefix(sig, "sha256=")), ""
}

// ---- MCP ----

type listSourcesIn struct {
	Project string `json:"project" jsonschema:"the project's name"`
}

type sourceBrief struct {
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	Enabled   bool              `json:"enabled"`
	Mapping   map[string]string `json:"mapping"`
	Filter    string            `json:"filter,omitempty"`
	DedupeKey string            `json:"dedupe_key,omitempty"`
	Debounce  int               `json:"debounce_seconds,omitempty"`
	Batch     int               `json:"batch_seconds,omitempty"`
}

type listSourcesOut struct {
	Sources []sourceBrief `json:"sources" jsonschema:"the project's event sources; the signing secret is never shown"`
}

type testSourceIn struct {
	Project string         `json:"project" jsonschema:"the project's name"`
	Source  string         `json:"source" jsonschema:"the event source's name"`
	Body    map[string]any `json:"body" jsonschema:"the JSON object to try, as a sender would post it"`
}

type testSourceOut struct {
	Status   string            `json:"status" jsonschema:"ready, filtered, duplicate, refused or error"`
	Reason   string            `json:"reason"`
	Problems []string          `json:"problems,omitempty"`
	Inputs   map[string]string `json:"inputs,omitempty" jsonschema:"the inputs the run would be given"`
	Key      string            `json:"dedupe_key,omitempty"`
}

type deleteSourceIn struct {
	Project string `json:"project" jsonschema:"the project's name"`
	Source  string `json:"source" jsonschema:"the event source's name"`
}

func addEventSourceTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_event_sources", Annotations: readOnly("List event sources"),
		Description: "A project's incoming webhook sources: each one's address, what it maps from the body to the pipeline's inputs, its filter, dedupe key and window. The signing secret is never shown. Making a source is done on the console or in the web interface, because its secret is shown once."},
		mcpCallTimeout, listEventSources)
	if !c.mayOperate {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "test_event_source", Annotations: readOnly("Try a body against an event source"),
		Description: "Judge a JSON body as a call to the source would be: schema, filter, dedupe key and the mapping to inputs, checked against the pipeline's inputs. It records and starts nothing."},
		mcpCallTimeout, testEventSource)
	addTool(srv, c, &mcp.Tool{Name: "delete_event_source", Annotations: changes("Delete an event source", true, true),
		Description: "Remove an event source; calls to its address are answered not found from then on. Events it recorded stay in the event log."},
		mcpCallTimeout, deleteEventSource)
}

func listEventSources(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in listSourcesIn) (listSourcesOut, error) {
	var list []sourceView
	if err := c.apiJSON(ctx, "list_event_sources", http.MethodGet, apiPath("/api/v1/projects/%s/event-sources", in.Project), nil, &list); err != nil {
		return listSourcesOut{}, err
	}
	out := listSourcesOut{Sources: []sourceBrief{}}
	for _, x := range list {
		out.Sources = append(out.Sources, sourceBrief{Name: x.Name, URL: x.URL, Enabled: x.Enabled, Mapping: x.Mapping, Filter: x.Filter, DedupeKey: x.DedupeKey,
			Debounce: x.DebounceSecs, Batch: x.BatchSecs})
	}
	return out, nil
}

func testEventSource(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in testSourceIn) (testSourceOut, error) {
	var res source.SourceResult
	if err := c.apiJSON(ctx, "test_event_source", http.MethodPost, apiPath("/api/v1/projects/%s/event-sources/%s/test", in.Project, in.Source),
		map[string]any{"body": in.Body}, &res); err != nil {
		return testSourceOut{}, err
	}
	return testSourceOut{Status: res.Status, Reason: untrusted(res.Reason, 400), Problems: res.Problems, Inputs: res.Inputs, Key: res.Key}, nil
}

func deleteEventSource(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in deleteSourceIn) (okOut, error) {
	if err := c.apiJSON(ctx, "delete_event_source", http.MethodDelete, apiPath("/api/v1/projects/%s/event-sources/%s", in.Project, in.Source), nil, nil); err != nil {
		return okOut{}, err
	}
	return okOut{OK: true, Summary: "Deleted the event source " + in.Source + "."}, nil
}
