package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/toggles"
	"github.com/jk-magic-creations/launchway/internal/togglestore"
)

// The evaluation protocol: the routes applications call, with an SDK key and
// nothing else (no session, no API token). They are documented, for people who
// write an SDK in another language, in docs/reference/toggles-protocol.md.
//
//	GET  /api/sdk/v1/ruleset    server key. The environment's ruleset, for
//	                            evaluating in the application. ETag, and 304 for
//	                            If-None-Match.
//	GET  /api/sdk/v1/stream     server key. Server-sent events: the ruleset as
//	                            event "ruleset", whose id is its ETag, whenever
//	                            it changes; Last-Event-ID resumes (nothing is
//	                            sent if the client already has the current one).
//	POST /api/sdk/v1/evaluate   client or server key. The values for a context.
//	                            A client key gets values of client-visible toggles
//	                            only, never a rule or a segment.
//	POST /api/sdk/v1/telemetry  server key. Aggregated counts of evaluations.
//
// What is never done: the context of an evaluation is not logged, stored or
// put in the audit trail; a key reads, and nothing it holds can change a toggle.

func (s *Server) toggleSDKRoutes() []route {
	return []route{
		{"GET /api/sdk/v1/ruleset", public, "", s.sdkRuleset},
		{"GET /api/sdk/v1/stream", public, "", s.sdkStream},
		{"POST /api/sdk/v1/evaluate", public, "", s.sdkEvaluate},
		{"OPTIONS /api/sdk/v1/evaluate", public, "", s.sdkPreflight},
		{"POST /api/sdk/v1/telemetry", public, "", s.sdkTelemetry},
	}
}

const (
	maxEvalBody     = 64 << 10
	maxContextAttrs = 100
)

// sdkKey reads and checks the key of a request. want is the kinds it may be.
// A key that is not valid is a 401 that says nothing about why.
func (s *Server) sdkKey(w http.ResponseWriter, r *http.Request, serverOnly bool) (*togglestore.KeyInfo, *togglestore.Service, bool) {
	svc := s.toggleService(w)
	if svc == nil {
		return nil, nil, false
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		token = r.Header.Get("X-Launchway-Client-Id")
	}
	// Guessing keys is slowed down: an address that has sent many keys that
	// were not valid is refused for a while, whatever it sends next.
	addr := s.clientAddr(r)
	if svc.Limits().TooManyFailures(addr) {
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "too many keys that were not valid: wait a minute")
		return nil, nil, false
	}
	info, err := svc.Authenticate(r.Context(), token)
	if err != nil {
		if !errors.Is(err, togglestore.ErrBadKey) {
			s.fail(w, err)
			return nil, nil, false
		}
		svc.Limits().Failed(addr)
		w.Header().Set("WWW-Authenticate", `Bearer realm="launchway-toggles"`)
		writeErr(w, http.StatusUnauthorized, "the SDK key is missing or not valid")
		return nil, nil, false
	}
	if serverOnly && info.Kind != togglestore.KindServer {
		writeErr(w, http.StatusForbidden, "this call needs a server key: a client key can only ask for values")
		return nil, nil, false
	}
	return info, svc, true
}

func (s *Server) sdkRuleset(w http.ResponseWriter, r *http.Request) {
	info, svc, ok := s.sdkKey(w, r, true)
	if !ok {
		return
	}
	if !svc.Limits().Allow(info.ID, "ruleset", 120, 60) {
		w.Header().Set("Retry-After", "5")
		writeErr(w, http.StatusTooManyRequests, "too many requests: use the stream, or poll less often")
		return
	}
	sn, err := svc.Current(r.Context(), info.Project, info.Environment)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("ETag", sn.ETag)
	w.Header().Set("Cache-Control", "private, no-cache")
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, sn.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(sn.Body)
}

func etagMatches(header, etag string) bool {
	for _, p := range strings.Split(header, ",") {
		if p = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p), "W/")); p == etag || p == "*" {
			return true
		}
	}
	return false
}

func (s *Server) sdkStream(w http.ResponseWriter, r *http.Request) {
	info, svc, ok := s.sdkKey(w, r, true)
	if !ok {
		return
	}
	fl, can := w.(http.Flusher)
	if !can {
		writeErr(w, http.StatusInternalServerError, "this connection cannot stream")
		return
	}
	if !svc.Limits().OpenStream(info.ID) {
		writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("a key may have %d streams open", togglestore.MaxStreamsPerKey))
		return
	}
	defer svc.Limits().CloseStream(info.ID)
	ctx, cancel := s.longLived(r)
	defer cancel()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// What the client says it has: the id of the last event it saw.
	last := strings.Trim(r.Header.Get("Last-Event-ID"), `"`)
	ping := time.NewTicker(s.StreamPing)
	look := time.NewTicker(time.Second)
	defer ping.Stop()
	defer look.Stop()
	fmt.Fprint(w, "retry: 3000\n\n")
	fl.Flush()
	for {
		changed := svc.Changed()
		sn, err := svc.Current(ctx, info.Project, info.Environment) //nolint:contextcheck // ctx is the request's own, ended by Stop
		if err != nil {
			return
		}
		if id := strings.Trim(sn.ETag, `"`); id != last {
			// Every event is the whole ruleset, so a client that missed some
			// needs nothing but this one: there is no history to replay.
			if _, err := fmt.Fprintf(w, "id: %s\nevent: ruleset\ndata: %s\n\n", id, sn.Body); err != nil {
				return
			}
			fl.Flush()
			last = id
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-look.C:
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// allowOrigin sets the CORS answer for a client key: its origins when it names
// some, any origin when it names none (the key is public anyway).
func allowOrigin(w http.ResponseWriter, r *http.Request, info *togglestore.KeyInfo) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if info != nil && len(info.Origins) > 0 {
		for _, o := range info.Origins {
			if o == origin {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
				return true
			}
		}
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	return true
}

func (s *Server) sdkPreflight(w http.ResponseWriter, r *http.Request) {
	// A preflight carries no key: it only says which request may follow.
	if o := r.Header.Get("Origin"); o != "" {
		w.Header().Set("Access-Control-Allow-Origin", o)
		w.Header().Add("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Launchway-Client-Id")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

type evalRequest struct {
	Context toggles.Context `json:"context"`
	Toggles []string        `json:"toggles"`
}

func (s *Server) sdkEvaluate(w http.ResponseWriter, r *http.Request) {
	info, svc, ok := s.sdkKey(w, r, false)
	if !ok {
		return
	}
	if !allowOrigin(w, r, info) {
		writeErr(w, http.StatusForbidden, "this key is not for use from that website")
		return
	}
	if !svc.Limits().Allow(info.ID, "evaluate", 600, 200) {
		w.Header().Set("Retry-After", "5")
		writeErr(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	var in evalRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxEvalBody))
	if err := dec.Decode(&in); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "the request body is not valid JSON (or is over 64 KiB): "+err.Error())
		return
	}
	if len(in.Context) > maxContextAttrs || len(in.Toggles) > 200 {
		writeErr(w, http.StatusBadRequest, "a context has at most 100 attributes, and a request asks for at most 200 toggles")
		return
	}
	client := info.Kind == togglestore.KindClient
	values, err := svc.Evaluate(r.Context(), info.Project, info.Environment, in.Context, in.Toggles, client)
	if err != nil {
		s.fail(w, err)
		return
	}
	if client { // never a rule: not even which rule matched
		for k, v := range values {
			v.RuleID = ""
			values[k] = v
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"environment": info.Environment, "values": values})
}

type telemetryRequest struct {
	Counts []struct {
		Toggle string `json:"toggle"`
		Value  any    `json:"value"`
		Reason string `json:"reason"`
		Count  int64  `json:"count"`
	} `json:"counts"`
}

func (s *Server) sdkTelemetry(w http.ResponseWriter, r *http.Request) {
	info, svc, ok := s.sdkKey(w, r, true)
	if !ok {
		return
	}
	if !svc.Limits().Allow(info.ID, "telemetry", 12, 6) {
		w.Header().Set("Retry-After", "30")
		writeErr(w, http.StatusTooManyRequests, "too many requests: report once a minute")
		return
	}
	var in telemetryRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxEvalBody)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "the request body is not valid JSON: "+err.Error())
		return
	}
	if len(in.Counts) > 500 {
		writeErr(w, http.StatusBadRequest, "at most 500 counts in one report")
		return
	}
	sn, err := svc.Current(r.Context(), info.Project, info.Environment)
	if err != nil {
		s.fail(w, err)
		return
	}
	taken := 0
	for _, c := range in.Counts {
		if _, known := sn.Ruleset.Toggles[c.Toggle]; !known || c.Count <= 0 || c.Count > 1_000_000_000 || len(c.Reason) > 40 {
			continue
		}
		svc.Count(info.Project, info.Environment, c.Toggle, c.Value, c.Reason, c.Count)
		taken++
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": taken})
}
