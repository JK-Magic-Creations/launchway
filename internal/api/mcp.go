package api

// The Model Context Protocol endpoint, /mcp: agents connect to it with the
// same API tokens as scripts and do through it exactly what their token
// allows. It speaks the Streamable HTTP transport statelessly (protocol
// revision 2026-07-28, and the earlier revisions back to 2025-03-26 without
// sessions), so any server behind a load balancer can answer any request.
//
// Every tool is carried out by calling this server's own HTTP API in
// process, with the caller's credentials: the same guard decides who may do
// what, the same handlers mask secrets and remove credentials from
// addresses, and every call that changes something is in the audit trail,
// marked as made through MCP with the token's name. There is no second
// permission check to keep in step.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// MCPOptions configure the MCP endpoint. A nil *MCPOptions on Server turns
// it off.
type MCPOptions struct {
	// AllowedOrigins are the browser origins (scheme://host[:port]) that may
	// call /mcp, besides this server's own. Requests without an Origin
	// header (every non-browser client) are not affected.
	AllowedOrigins []string
	// RequestsPerMinute bounds the requests one token (or, without sign-in,
	// one address) makes to /mcp on one server; default 120.
	RequestsPerMinute int
	// Concurrent bounds the requests one token has in progress on one
	// server at once; default 4.
	Concurrent int
	// Total bounds the MCP requests in progress on one server at once, so
	// that agents cannot crowd out people; default 32.
	Total int
	// OAuth, if set, also accepts access tokens of the sign-in provider that
	// were issued for this endpoint (see mcp_oauth.go).
	OAuth *MCPOAuth
}

const (
	mcpPath              = "/mcp"
	mcpDefaultRate       = 120
	mcpDefaultConcurrent = 4
	mcpDefaultTotal      = 32
	// mcpMaxBody bounds a request to /mcp. A pipeline to validate is at most
	// 256 KiB; JSON escaping can double it.
	mcpMaxBody = 1 << 20
	// mcpCallTimeout bounds one tool call, except wait_for_run, which says
	// how long it may wait.
	mcpCallTimeout = 60 * time.Second
	// mcpMaxRead bounds what one internal API call may read into memory.
	mcpMaxRead = 8 << 20
)

// mcpSchemas caches the tools' JSON schemas, which every request's server
// would otherwise infer again.
var mcpSchemas = mcp.NewSchemaCache()

// ---- the endpoint ----

// mcpEndpoint serves /mcp. It checks the origin, who is calling and their
// limits, and then hands the request to the SDK's stateless Streamable
// HTTP handler with a server made for this caller.
func (s *Server) mcpEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.MCP == nil {
		writeErr(w, http.StatusNotFound, "the MCP endpoint is turned off on this server (mcp.enabled: false)")
		return
	}
	if r.Method != http.MethodPost {
		// This server keeps no sessions and offers no stream of its own: every
		// message is a POST.
		w.Header().Set("Allow", http.MethodPost)
		writeRPCErr(w, http.StatusMethodNotAllowed, "send each message as a POST; this server keeps no sessions and has no GET stream")
		return
	}
	if msg := s.mcpOriginRefusal(r); msg != "" {
		writeRPCErr(w, http.StatusForbidden, msg)
		return
	}
	caller, ok := s.mcpCaller(w, r)
	if !ok {
		return
	}
	release, ok := s.mcpLimits().admit(w, caller.limitKey, s.MCP)
	if !ok {
		return
	}
	defer release()
	srv := s.mcpServerFor(caller)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Stateless: true,
		// This handler checks the origin itself (above), and allows a proxy
		// on the same machine to pass on a public Host.
		DisableLocalhostProtection:   true,
		MaxRequestBodyBytes:          mcpMaxBody,
		PropagateRequestCancellation: true,
		Logger:                       nil,
	})
	h.ServeHTTP(w, r)
}

// writeRPCErr answers with a JSON-RPC error that has no id, as the MCP
// transport describes for requests refused before they are read.
func writeRPCErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": -32000, "message": msg}})
}

// mcpOriginRefusal says why a request from a browser page must be refused,
// or "" if it may go on. A request without Origin is not from a page. A
// page may call only from this server's own origin, as its sign-in address
// gives it, from a loopback address to a server on a loopback address, or
// from an origin the configuration allows. The Host header is never trusted
// for this, since a page that rebinds its DNS name to this server sends
// its own name there.
func (s *Server) mcpOriginRefusal(r *http.Request) string {
	o := r.Header.Get("Origin")
	if o == "" {
		return ""
	}
	u, err := url.Parse(o)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "the Origin header is not an origin"
	}
	origin := strings.ToLower(u.Scheme + "://" + u.Host)
	for _, a := range s.MCP.AllowedOrigins {
		if strings.EqualFold(strings.TrimRight(a, "/"), origin) {
			return ""
		}
	}
	if s.Auth != nil && s.Auth.OIDC != nil {
		if own, err := url.Parse(s.Auth.OIDC.RedirectURL()); err == nil && strings.EqualFold(own.Scheme+"://"+own.Host, origin) {
			return ""
		}
	}
	if isLoopbackHost(u.Hostname()) && isLoopbackHost(hostOnly(r.Host)) && u.Port() == portOf(r.Host) {
		return "" // this server's own pages, on this machine
	}
	return "requests from the web page at " + origin + " may not use this server's MCP endpoint (mcp.allowed_origins lists the pages that may)"
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

func portOf(hostport string) string {
	if _, p, err := net.SplitHostPort(hostport); err == nil {
		return p
	}
	return ""
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ---- who is calling ----

// mcpCall is one request to /mcp: who made it, and what is needed to make
// calls to the API on their behalf.
type mcpCall struct {
	s     *Server
	outer *http.Request
	id    *identity
	// limitKey is what the limits count: the token, or the person, or
	// without sign-in the address.
	limitKey string
	// mayOperate says whether the caller may change anything anywhere; if
	// not, the tools that change things are not listed.
	mayOperate bool
	// mayAdminister says whether the caller is an administrator of every
	// project; if not, the tools that change the installation's settings
	// are not listed.
	mayAdminister bool
	baseURL       string // this server as the caller reaches it, for links
}

// mcpCaller works out who is calling /mcp and answers 401 or 429 if
// nobody valid is. Only bearer tokens are accepted: a browser's session
// cookie is not, so that no page can act through /mcp with a visitor's
// session.
func (s *Server) mcpCaller(w http.ResponseWriter, r *http.Request) (*mcpCall, bool) {
	c := &mcpCall{s: s, outer: r, baseURL: s.baseURL(r)}
	if s.Auth == nil {
		c.id = &identity{User: s.Actor, Via: viaNone}
		c.limitKey, c.mayOperate, c.mayAdminister = "addr:"+s.clientAddr(r), true, true
		return c, true
	}
	h := r.Header.Get("Authorization")
	if h == "" {
		s.mcpChallenge(w, "", "send an API token: Authorization: Bearer lw_… (make one on the API tokens page)")
		return nil, false
	}
	if s.tooManyFailures(w, r) {
		return nil, false
	}
	var id *identity
	var err error
	if tok, _ := strings.CutPrefix(h, "Bearer "); s.MCP.OAuth != nil && !strings.HasPrefix(tok, tokenPrefix) {
		id, err = s.oauthIdentity(r, tok)
	} else {
		id, err = s.identify(r, true)
	}
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	if id == nil {
		s.mcpChallenge(w, "invalid_token", "the token was refused: it is unknown, revoked or expired")
		return nil, false
	}
	c.id = id
	c.limitKey = "user:" + id.User
	if id.tokenID != "" {
		c.limitKey = "token:" + id.tokenID
	}
	c.mayOperate = s.mayOperateSomewhere(id)
	c.mayAdminister = s.authorize(id, auth.Administer, "")
	return c, true
}

// mcpChallenge answers 401 with the challenge RFC 6750 and the MCP
// authorization rules describe: where the protected resource's metadata
// is, when this server accepts the sign-in provider's tokens.
func (s *Server) mcpChallenge(w http.ResponseWriter, code, msg string) {
	parts := []string{`Bearer realm="launchway"`}
	if s.MCP.OAuth != nil {
		parts = append(parts, `resource_metadata="`+s.MCP.OAuth.metadataURL()+`"`)
		if sc := s.MCP.OAuth.Scopes; len(sc) > 0 {
			parts = append(parts, `scope="`+strings.Join(sc, " ")+`"`)
		}
	}
	if code != "" {
		parts = append(parts, `error="`+code+`"`, `error_description="`+strings.ReplaceAll(msg, `"`, `'`)+`"`)
	}
	w.Header().Set("WWW-Authenticate", strings.Join(parts, ", "))
	writeRPCErr(w, http.StatusUnauthorized, msg)
}

// mayOperateSomewhere reports whether the identity may change anything on
// some project: start, cancel or approve runs.
func (s *Server) mayOperateSomewhere(id *identity) bool {
	if s.Auth == nil {
		return true
	}
	if id.capRole > auth.NoRole && id.capRole < auth.Operator {
		return false
	}
	known, err := s.knownProjects()
	if err != nil {
		return false
	}
	for _, p := range known {
		if s.authorize(id, auth.Operate, p) {
			return true
		}
	}
	return false
}

// baseURL is this server as the request reached it.
func (s *Server) baseURL(r *http.Request) string {
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---- limits ----

// mcpLimiter bounds MCP requests per caller and per server. Each server
// counts on its own.
type mcpLimiter struct {
	mu      sync.Mutex
	callers map[string]*mcpBucket
	total   int
}

type mcpBucket struct {
	rate     *rate.Limiter
	inFlight int
	lastSeen time.Time
}

const mcpMaxCallers = 10000

var mcpLimiters sync.Map // *Server → *mcpLimiter

func (s *Server) mcpLimits() *mcpLimiter {
	if l, ok := mcpLimiters.Load(s); ok {
		return l.(*mcpLimiter)
	}
	l, _ := mcpLimiters.LoadOrStore(s, &mcpLimiter{callers: map[string]*mcpBucket{}})
	return l.(*mcpLimiter)
}

// admit lets a request in, or answers 429 and says no. The caller must call
// release when the request is done.
func (l *mcpLimiter) admit(w http.ResponseWriter, key string, o *MCPOptions) (release func(), ok bool) {
	perMinute, concurrent, total := or(o.RequestsPerMinute, mcpDefaultRate), or(o.Concurrent, mcpDefaultConcurrent), or(o.Total, mcpDefaultTotal)
	now := time.Now()
	l.mu.Lock()
	c := l.callers[key]
	if c == nil {
		if len(l.callers) >= mcpMaxCallers {
			l.forgetIdle(now)
		}
		c = &mcpBucket{rate: rate.NewLimiter(rate.Limit(float64(perMinute)/60), max(perMinute/4, 5))}
		l.callers[key] = c
	}
	c.lastSeen = now
	var refusal string
	retry := 1
	switch {
	case c.inFlight >= concurrent:
		refusal = fmt.Sprintf("this token already has %d MCP requests in progress on this server; wait for one to finish", concurrent)
	case l.total >= total:
		refusal = "this server is busy with other agents' requests; try again in a moment"
	default:
		if res := c.rate.ReserveN(now, 1); !res.OK() || res.DelayFrom(now) > 0 {
			if res.OK() {
				retry = int(res.DelayFrom(now).Seconds()) + 1
				res.CancelAt(now)
			}
			refusal = fmt.Sprintf("this token made more than %d MCP requests a minute; slow down", perMinute)
		}
	}
	if refusal != "" {
		l.mu.Unlock()
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeRPCErr(w, http.StatusTooManyRequests, refusal)
		return nil, false
	}
	c.inFlight++
	l.total++
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			c.inFlight--
			l.total--
			l.mu.Unlock()
		})
	}, true
}

// forgetIdle drops callers with nothing in progress that have not been
// seen for a minute, or, if none, the one seen longest ago.
func (l *mcpLimiter) forgetIdle(now time.Time) {
	var oldest string
	var at time.Time
	for k, c := range l.callers {
		if c.inFlight > 0 {
			continue
		}
		if now.Sub(c.lastSeen) > time.Minute {
			delete(l.callers, k)
			continue
		}
		if oldest == "" || c.lastSeen.Before(at) {
			oldest, at = k, c.lastSeen
		}
	}
	if len(l.callers) >= mcpMaxCallers && oldest != "" {
		delete(l.callers, oldest)
	}
}

func or(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// ---- calling the API on the caller's behalf ----

// mcpMark goes with every internal API call a tool makes, so that the
// audit trail can say the call came through MCP, and which tool made it.
type mcpMark struct {
	Tool   string
	Client string // the MCP client's name, as it gave it
}

type mcpCtxKey int

const (
	mcpMarkKey mcpCtxKey = iota
	mcpIdentityKey
)

// apiAnswer is what an internal API call answered.
type apiAnswer struct {
	Status    int
	Header    http.Header
	Body      []byte
	Total     int64 // bytes the handler wrote; more than len(Body) if cut
	Truncated bool
}

// apiError is a refusal or failure the API answered, in words for an agent.
type apiError struct {
	Status  int
	Message string
	Detail  any // problems or input errors, when the API gave them
}

func (e *apiError) Error() string {
	switch e.Status {
	case http.StatusNotFound:
		return "not found (or not visible to this token): " + e.Message
	case http.StatusForbidden:
		return "refused: " + e.Message
	case http.StatusUnauthorized:
		return "the token was refused: " + e.Message
	case http.StatusConflict:
		return "not possible now: " + e.Message
	case http.StatusTooManyRequests:
		return "too many requests: " + e.Message
	}
	if e.Status >= 500 {
		return "the server failed: " + e.Message
	}
	return e.Message
}

// api calls this server's own HTTP API as the caller, through the same
// handler a client's request reaches, guard included. It reads at most max
// bytes of the answer (mcpMaxRead if 0).
func (c *mcpCall) api(ctx context.Context, tool, method, path string, body any, max int) (*apiAnswer, error) {
	return c.apiWithHeaders(ctx, tool, method, path, max, body)
}

// apiWithHeaders is api with a body and extra request headers, given as
// name, value pairs after the body.
func (c *mcpCall) apiWithHeaders(ctx context.Context, tool, method, path string, max int, body any, hdr ...string) (*apiAnswer, error) {
	var rd io.Reader = http.NoBody
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(data)
	}
	ctx = context.WithValue(ctx, mcpMarkKey, &mcpMark{Tool: tool, Client: clientName(ctx)})
	if c.id.Via == viaOAuth {
		ctx = context.WithValue(ctx, mcpIdentityKey, c.id)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, rd)
	if err != nil {
		return nil, err
	}
	req.RemoteAddr, req.Host, req.TLS = c.outer.RemoteAddr, c.outer.Host, c.outer.TLS
	for _, h := range []string{"Authorization", "X-Forwarded-For", "X-Forwarded-Proto", "User-Agent"} {
		if v := c.outer.Header.Values(h); len(v) > 0 && !(h == "Authorization" && c.id.Via == viaOAuth) {
			req.Header[h] = v
		}
	}
	req.Header.Set("Content-Type", "application/json")
	// Not a browser's request: /mcp checked the origin, and accepts no
	// cookie. Without sign-in the API asks every change for this header.
	req.Header.Set(csrfHeader, "1")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if max <= 0 {
		max = mcpMaxRead
	}
	rec := &boundedRecorder{header: http.Header{}, max: max}
	c.s.mux.ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return &apiAnswer{Status: rec.status, Header: rec.header, Body: rec.buf.Bytes(), Total: rec.total, Truncated: rec.total > int64(rec.buf.Len())}, nil
}

// apiJSON calls the API and decodes a successful answer into out; an
// answer of 400 and over is an *apiError.
func (c *mcpCall) apiJSON(ctx context.Context, tool, method, path string, body, out any) error {
	ans, err := c.api(ctx, tool, method, path, body, 0)
	if err != nil {
		return err
	}
	if ans.Status >= 400 {
		return answerError(ans)
	}
	if ans.Truncated {
		return &apiError{Status: 500, Message: "the answer was too large to read"}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(ans.Body, out); err != nil {
		return fmt.Errorf("reading the answer: %w", err)
	}
	return nil
}

func answerError(ans *apiAnswer) *apiError {
	var body struct {
		Error    string          `json:"error"`
		Problems json.RawMessage `json:"problems"`
		Inputs   json.RawMessage `json:"inputs"`
	}
	_ = json.Unmarshal(ans.Body, &body)
	e := &apiError{Status: ans.Status, Message: body.Error}
	if e.Message == "" {
		e.Message = http.StatusText(ans.Status)
	}
	if len(body.Problems) > 0 {
		e.Detail = body.Problems
	} else if len(body.Inputs) > 0 {
		e.Detail = body.Inputs
	}
	return e
}

// boundedRecorder keeps the status, headers and the first max bytes of an
// answer, and counts the rest.
type boundedRecorder struct {
	header http.Header
	status int
	buf    bytes.Buffer
	max    int
	total  int64
}

func (b *boundedRecorder) Header() http.Header { return b.header }
func (b *boundedRecorder) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}
func (b *boundedRecorder) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	b.total += int64(len(p))
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
func (b *boundedRecorder) Flush() {}

// clientName is the MCP client's name, as the tool call's context carries
// it (see withClient).
func clientName(ctx context.Context) string {
	n, _ := ctx.Value(mcpClientKey{}).(string)
	return n
}

type mcpClientKey struct{}

// withClient notes the MCP client's name in the context of a tool call.
func withClient(ctx context.Context, info *mcp.Implementation) context.Context {
	if info == nil || info.Name == "" {
		return ctx
	}
	return context.WithValue(ctx, mcpClientKey{}, clip(info.Name, 60))
}

// markMCP notes on an audit entry that the call came through MCP: which
// token, which tool and which client.
func markMCP(r *http.Request, id *identity, e *engine.AuditEntry) {
	m, ok := r.Context().Value(mcpMarkKey).(*mcpMark)
	if !ok || e == nil {
		return
	}
	via := "via MCP"
	switch {
	case id != nil && id.Via == viaToken:
		via += fmt.Sprintf(", token %q", id.tokenTag)
	case id != nil && id.Via == viaOAuth:
		via += ", access token of " + id.tokenTag
	}
	via += ", tool " + m.Tool
	if m.Client != "" {
		via += fmt.Sprintf(", client %q", m.Client)
	}
	if e.Detail == "" {
		e.Detail = via
	} else {
		e.Detail = via + "; " + e.Detail
	}
}
