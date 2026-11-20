// Package aitest is a fake model provider for tests. One Server speaks both
// API shapes the ai package uses, Anthropic Messages (POST /v1/messages) and
// OpenAI-compatible chat completions (POST /v1/chat/completions), streamed
// and not. Tests script its replies and read back the requests it got, so
// no test ever calls a real (or paid) model.
//
// Point an anthropic model's endpoint at AnthropicEndpoint and an openai
// model's at OpenAIEndpoint.
package aitest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Reply scripts one answer. The zero Reply is a successful answer "ok".
type Reply struct {
	Status int    // HTTP status; 0 is 200
	Text   string // the answer text
	JSON   any    // if set, the answer text is this, marshaled

	StopReason string // Anthropic words (end_turn, max_tokens, refusal); for openai, mapped to stop, length, content_filter
	Model      string // the model id to report; default: the one requested

	InputTokens, OutputTokens         int
	CacheReadTokens, CacheWriteTokens int

	// Refuse makes the model decline: stop_reason refusal with
	// stop_details.category RefusalCategory (anthropic), or a message
	// refusal (openai).
	Refuse          bool
	RefusalCategory string

	// For an error status: the error JSON's type and message, and an
	// optional Retry-After header value.
	ErrorType    string
	ErrorMessage string
	RetryAfter   string

	// RawBody is sent as the body exactly, with Status and ContentType.
	RawBody     string
	ContentType string

	Delay time.Duration // wait before answering (until the client gives up)

	// Streaming only.
	ChunkSize      int  // bytes of text per delta; default 8
	MalformedEvent bool // send an event that is not JSON
	CutStream      bool // stop the stream before its end marker
	StreamError    bool // send an error event mid-stream (Anthropic overloaded_error; openai error chunk)
}

// Request is one request the server got.
type Request struct {
	API    string // anthropic or openai
	Path   string
	Header http.Header
	Body   []byte
	JSON   map[string]any // the body, decoded
}

// Stream reports whether the request asked for a stream.
func (r Request) Stream() bool { return r.JSON["stream"] == true }

// Has reports whether the body has the top-level field.
func (r Request) Has(field string) bool { _, ok := r.JSON[field]; return ok }

// Server is the fake provider.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	queue    []Reply
	fallback Reply
	requests []Request
}

// New starts a server, stopped when the test ends.
func New(t testing.TB) *Server {
	s := &Server{}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// AnthropicEndpoint is the endpoint for an anthropic model.
func (s *Server) AnthropicEndpoint() string { return s.URL }

// OpenAIEndpoint is the endpoint for an openai model.
func (s *Server) OpenAIEndpoint() string { return s.URL + "/v1" }

// Enqueue adds replies, served one per request in order. When the queue is
// empty the default reply is served.
func (s *Server) Enqueue(r ...Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, r...)
}

// SetDefault sets the reply served when the queue is empty.
func (s *Server) SetDefault(r Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallback = r
}

// Requests returns the requests so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Last returns the latest request; it fails the test if there is none.
func (s *Server) Last(t testing.TB) Request {
	t.Helper()
	reqs := s.Requests()
	if len(reqs) == 0 {
		t.Fatal("aitest: the fake provider got no request")
	}
	return reqs[len(reqs)-1]
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req := Request{Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	_ = json.Unmarshal(body, &req.JSON)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
		req.API = "anthropic"
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		req.API = "openai"
	default:
		http.Error(w, "aitest: no such endpoint", http.StatusNotFound)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	rep := s.fallback
	if len(s.queue) > 0 {
		rep, s.queue = s.queue[0], s.queue[1:]
	}
	s.mu.Unlock()

	if rep.Delay > 0 {
		select {
		case <-time.After(rep.Delay):
		case <-r.Context().Done():
			return
		}
	}
	if rep.Model == "" {
		rep.Model, _ = req.JSON["model"].(string)
	}
	if rep.JSON != nil {
		b, _ := json.Marshal(rep.JSON)
		rep.Text = string(b)
	} else if rep.Text == "" && rep.RawBody == "" && !rep.Refuse {
		rep.Text = "ok"
	}
	if rep.RetryAfter != "" {
		w.Header().Set("Retry-After", rep.RetryAfter)
	}
	status := rep.Status
	if status == 0 {
		status = http.StatusOK
	}
	if rep.RawBody != "" {
		ct := rep.ContentType
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(status)
		io.WriteString(w, rep.RawBody)
		return
	}
	if status != http.StatusOK {
		writeError(w, req.API, status, rep)
		return
	}
	switch {
	case req.API == "anthropic" && req.Stream():
		anthropicStream(w, rep)
	case req.API == "anthropic":
		writeJSON(w, anthropicMessage(rep))
	case req.Stream():
		openAIStream(w, rep, req)
	default:
		writeJSON(w, openAICompletion(rep))
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, api string, status int, rep Reply) {
	typ, msg := rep.ErrorType, rep.ErrorMessage
	if typ == "" {
		typ = defaultErrorType(status)
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if api == "anthropic" {
		json.NewEncoder(w).Encode(map[string]any{"type": "error",
			"error": map[string]any{"type": typ, "message": msg}})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": typ, "code": nil}})
}

func defaultErrorType(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 429:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	}
	return "api_error"
}

func stopReason(rep Reply) string {
	switch {
	case rep.Refuse:
		return "refusal"
	case rep.StopReason != "":
		return rep.StopReason
	}
	return "end_turn"
}

func anthropicUsage(rep Reply) map[string]any {
	return map[string]any{
		"input_tokens":                rep.InputTokens,
		"output_tokens":               rep.OutputTokens,
		"cache_creation_input_tokens": rep.CacheWriteTokens,
		"cache_read_input_tokens":     rep.CacheReadTokens,
	}
}

func anthropicStopDetails(rep Reply) any {
	if !rep.Refuse {
		return nil
	}
	return map[string]any{"type": "refusal", "category": rep.RefusalCategory, "explanation": "declined"}
}

func anthropicMessage(rep Reply) map[string]any {
	content := []any{map[string]any{"type": "thinking", "thinking": "", "signature": "sig"}}
	if rep.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": rep.Text})
	}
	return map[string]any{
		"id": "msg_fake", "type": "message", "role": "assistant", "model": rep.Model,
		"content": content, "stop_reason": stopReason(rep), "stop_sequence": nil,
		"stop_details": anthropicStopDetails(rep), "usage": anthropicUsage(rep),
	}
}

func chunks(text string, size int) []string {
	if size <= 0 {
		size = 8
	}
	var out []string
	for len(text) > size {
		out = append(out, text[:size])
		text = text[size:]
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

func sse(w http.ResponseWriter, event string, v any) {
	b, _ := json.Marshal(v)
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func anthropicStream(w http.ResponseWriter, rep Reply) {
	w.Header().Set("Content-Type", "text/event-stream")
	start := anthropicUsage(rep)
	start["output_tokens"] = 1
	sse(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_fake", "type": "message", "role": "assistant", "model": rep.Model,
		"content": []any{}, "stop_reason": nil, "usage": start}})
	sse(w, "ping", map[string]any{"type": "ping"})
	sse(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "thinking", "thinking": ""}})
	sse(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "thinking_delta", "thinking": "not the answer"}})
	sse(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	sse(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 1,
		"content_block": map[string]any{"type": "text", "text": ""}})
	for i, c := range chunks(rep.Text, rep.ChunkSize) {
		if rep.MalformedEvent && i == 1 {
			io.WriteString(w, "event: content_block_delta\ndata: {not json\n\n")
		}
		if rep.StreamError && i == 1 {
			sse(w, "error", map[string]any{"type": "error",
				"error": map[string]any{"type": "overloaded_error", "message": "Overloaded"}})
			return
		}
		if rep.CutStream && i == 1 {
			return
		}
		sse(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 1,
			"delta": map[string]any{"type": "text_delta", "text": c}})
	}
	if rep.CutStream {
		return
	}
	sse(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 1})
	sse(w, "message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": stopReason(rep), "stop_sequence": nil, "stop_details": anthropicStopDetails(rep)},
		"usage": anthropicUsage(rep)})
	sse(w, "message_stop", map[string]any{"type": "message_stop"})
}

func finishReason(rep Reply) string {
	switch stopReason(rep) {
	case "max_tokens":
		return "length"
	case "refusal":
		if rep.Refuse {
			return "stop"
		}
		return "content_filter"
	case "end_turn":
		return "stop"
	}
	return rep.StopReason
}

func openAIUsage(rep Reply) map[string]any {
	return map[string]any{
		"prompt_tokens":         rep.InputTokens + rep.CacheReadTokens,
		"completion_tokens":     rep.OutputTokens,
		"total_tokens":          rep.InputTokens + rep.CacheReadTokens + rep.OutputTokens,
		"prompt_tokens_details": map[string]any{"cached_tokens": rep.CacheReadTokens},
	}
}

func openAICompletion(rep Reply) map[string]any {
	msg := map[string]any{"role": "assistant", "content": rep.Text, "refusal": nil}
	if rep.Refuse {
		msg["content"] = nil
		msg["refusal"] = "I can't help with that."
	}
	return map[string]any{
		"id": "chatcmpl-fake", "object": "chat.completion", "created": 1, "model": rep.Model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finishReason(rep)}},
		"usage":   openAIUsage(rep),
	}
}

func openAIStream(w http.ResponseWriter, rep Reply, req Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": "chatcmpl-fake", "object": "chat.completion.chunk", "created": 1,
			"model": rep.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	sse(w, "", chunk(map[string]any{"role": "assistant", "content": ""}, nil))
	if rep.Refuse {
		sse(w, "", chunk(map[string]any{"refusal": "I can't help with that."}, nil))
	}
	for i, c := range chunks(rep.Text, rep.ChunkSize) {
		if rep.MalformedEvent && i == 1 {
			io.WriteString(w, "data: {not json\n\n")
		}
		if rep.StreamError && i == 1 {
			sse(w, "", map[string]any{"error": map[string]any{"message": "the model crashed", "type": "server_error"}})
			return
		}
		if rep.CutStream && i == 1 {
			return
		}
		sse(w, "", chunk(map[string]any{"content": c}, nil))
	}
	if rep.CutStream {
		return
	}
	sse(w, "", chunk(map[string]any{}, finishReason(rep)))
	if opts, _ := req.JSON["stream_options"].(map[string]any); opts["include_usage"] == true {
		sse(w, "", map[string]any{"id": "chatcmpl-fake", "object": "chat.completion.chunk", "created": 1,
			"model": rep.Model, "choices": []any{}, "usage": openAIUsage(rep)})
	}
	io.WriteString(w, "data: [DONE]\n\n")
}

// Text returns the system and user text of a request, joined, so tests can
// check that something (such as a secret) was not sent to the model.
func (r Request) Text() string {
	var b strings.Builder
	if s, ok := r.JSON["system"].(string); ok {
		b.WriteString(s + "\n")
	}
	msgs, _ := r.JSON["messages"].([]any)
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok {
			if c, ok := mm["content"].(string); ok {
				b.WriteString(c + "\n")
			}
		}
	}
	return b.String()
}
