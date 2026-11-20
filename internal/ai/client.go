package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Limits on what a provider may send back.
const (
	MaxResponseBytes = 4 << 20 // the whole HTTP body, streamed or not
	MaxOutputBytes   = 1 << 20 // the answer's text
	MaxRetries       = 2       // tries after the first, for errors worth retrying
)

// Request is one call to a model.
type Request struct {
	System      string          // instructions; may be empty
	Prompt      string          // the user message
	Schema      json.RawMessage // optional JSON schema (an object); the answer must match it
	MaxTokens   int             // output cap; 0 is the model's cap, more is an error
	Temperature *float64        // only for models that SupportsTemperature
}

// Usage counts the tokens of one call. InputTokens is the input that was
// neither read from nor written to a prompt cache.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}

// Total is every token the call used, which is what daily limits count.
func (u Usage) Total() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// Response is a model's answer. Its Text is untrusted: see the package
// documentation.
type Response struct {
	Text       string          `json:"text"`
	JSON       json.RawMessage `json:"json,omitempty"` // with a schema: the checked object, compacted
	StopReason string          `json:"stop_reason"`    // end_turn, max_tokens, stop_sequence, refusal, tool_use, or the provider's own word
	Usage      Usage           `json:"usage"`
	CostUSD    float64         `json:"cost_usd"` // estimated from the model's price; 0 if unknown
	Model      string          `json:"model"`    // the model id the provider reported
	DurationMS int64           `json:"duration_ms"`
}

// ErrorKind sorts call errors by what the operator or author can do.
type ErrorKind string

const (
	ErrRequest       ErrorKind = "request"        // the request breaks the model's limits or settings; nothing was sent
	ErrAuth          ErrorKind = "auth"           // the provider refused the key or access (401, 403)
	ErrRejected      ErrorKind = "rejected"       // the provider refused the request (400, 404 and other 4xx)
	ErrRateLimited   ErrorKind = "rate_limited"   // 429 after every retry
	ErrUnavailable   ErrorKind = "unavailable"    // 5xx, 529, an unreachable or garbled provider, after every retry
	ErrTimeout       ErrorKind = "timeout"        // no answer within the model's timeout
	ErrCanceled      ErrorKind = "canceled"       // the caller stopped the call
	ErrRefused       ErrorKind = "refused"        // the model declined to answer
	ErrOversized     ErrorKind = "oversized"      // the answer is over the size limits
	ErrInvalidOutput ErrorKind = "invalid_output" // the answer does not match the schema
)

// Error is a failed call. Its message never contains the API key.
type Error struct {
	Model      string        // the registry name
	Kind       ErrorKind     //
	Status     int           // the provider's HTTP status, if there was one
	RetryAfter time.Duration // what the provider asked to wait, if it said
	Msg        string        // what happened, in words
}

func (e *Error) Error() string { return "model " + e.Model + ": " + e.Msg }

// Client calls models. The zero value is ready to use.
type Client struct {
	// HTTP sends the requests; nil uses a client without its own timeout
	// (each call has the model's).
	HTTP *http.Client

	// Backoff is the wait before the first retry when the provider does not
	// say how long to wait; it doubles for each further retry. Default 1s.
	Backoff time.Duration

	// MaxWait caps any wait between tries, a provider's retry-after
	// included. Default 30s.
	MaxWait time.Duration
}

// CheckRequest reports whether req is within the model's limits and
// settings. Call checks the same before sending anything; callers may check
// earlier, such as when a pipeline is parsed.
func (m Model) CheckRequest(req Request) error {
	_, _, err := m.checkRequest(req)
	return err
}

func (m Model) checkRequest(req Request) (*schema, int, error) {
	fail := func(format string, args ...any) (*schema, int, error) {
		return nil, 0, &Error{Model: m.Name, Kind: ErrRequest, Msg: fmt.Sprintf(format, args...)}
	}
	if _, ok := providers[m.Provider]; !ok {
		return fail("provider %q is not known", m.Provider)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return fail("the prompt is empty")
	}
	limit := m.MaxInputBytes
	if limit <= 0 {
		limit = DefaultMaxInputBytes
	}
	if n := len(req.System) + len(req.Prompt); n > limit {
		return fail("the prompt and instructions are %d bytes, over the model's limit of %d (max_input_bytes); shorten the input", n, limit)
	}
	maxTokens := m.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	switch {
	case req.MaxTokens < 0:
		return fail("max_tokens must not be negative")
	case req.MaxTokens > maxTokens:
		return fail("max_tokens %d is over the model's cap of %d", req.MaxTokens, maxTokens)
	case req.MaxTokens > 0:
		maxTokens = req.MaxTokens
	}
	if req.Temperature != nil {
		if !m.SupportsTemperature() {
			return fail("temperature cannot be set for %s models: the API refuses sampling settings; leave it out", m.Provider)
		}
		if t := *req.Temperature; t < 0 || t > 2 {
			return fail("temperature %v must be between 0 and 2", t)
		}
	}
	var s *schema
	if req.Schema != nil {
		var err error
		if s, err = parseSchema(req.Schema); err != nil {
			return fail("%v", err)
		}
	}
	return s, maxTokens, nil
}

// Call sends one request to the model and returns its answer. key is the API
// key looked up from m.Key, or empty for a model without one.
//
// It retries rate limits, overloads, server errors and dropped connections
// up to MaxRetries times, honoring the provider's retry-after, and gives up
// at the model's timeout. Every error is an *Error. When the model answered
// but the answer is unusable (refused, oversized, not matching the schema),
// Call returns the Response too, so its Usage can still be counted.
func (c *Client) Call(ctx context.Context, m Model, key string, req Request) (Response, error) {
	start := time.Now()
	s, maxTokens, err := m.checkRequest(req)
	if err != nil {
		return Response{}, err
	}
	p := providers[m.Provider]
	prep := &Prepared{
		Model: m, Key: key, System: req.System, Prompt: req.Prompt,
		MaxTokens: maxTokens, Temperature: req.Temperature, SchemaMode: m.SchemaMode,
		Stream: maxTokens > StreamAbove,
	}
	if m.Stream != nil {
		prep.Stream = *m.Stream
	}
	if prep.SchemaMode == "" {
		prep.SchemaMode = SchemaJSONSchema
	}
	if s != nil {
		prep.Schema = wireSchema(req.Schema)
		prep.SchemaText = string(req.Schema)
		prep.Strict = s.strict()
	}
	timeout := m.CallTimeout()
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var rep Reply
	for tries := 1; ; tries++ {
		rep, err = c.try(callCtx, p, prep)
		if err == nil {
			break
		}
		var pe *ProviderError
		if errors.As(err, &pe) && pe.Status == http.StatusBadRequest && p.Downgrade(prep, pe) {
			tries-- // the server lacks a feature; asking without it is not a retry
			continue
		}
		if callCtx.Err() != nil {
			return Response{}, c.stopped(ctx, m, timeout)
		}
		wait, retry := c.retryWait(err, tries)
		if !retry || tries > MaxRetries {
			return Response{}, failure(m, key, err, tries, "")
		}
		if dl, ok := callCtx.Deadline(); ok && time.Until(dl) < wait {
			return Response{}, failure(m, key, err, tries, fmt.Sprintf("waiting %s more would pass the call's time limit of %s", wait, timeout))
		}
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-callCtx.Done():
			t.Stop()
			return Response{}, c.stopped(ctx, m, timeout)
		}
	}

	resp := Response{
		Text:       rep.Text,
		StopReason: rep.StopReason,
		Usage:      rep.Usage,
		Model:      rep.Model,
		DurationMS: time.Since(start).Milliseconds(),
	}
	if resp.Model == "" {
		resp.Model = m.Model
	}
	if m.Price != nil {
		resp.CostUSD = m.Price.Cost(rep.Usage)
	}
	fail := func(kind ErrorKind, format string, args ...any) (Response, error) {
		return resp, &Error{Model: m.Name, Kind: kind, Msg: scrub(fmt.Sprintf(format, args...), key)}
	}
	if rep.Refused {
		return fail(ErrRefused, "declined to answer (%s)", rep.RefusalDetail)
	}
	if len(rep.Text) > MaxOutputBytes {
		resp.Text = ""
		return fail(ErrOversized, "oversized output: the answer is %d bytes, over the limit of %d", len(rep.Text), MaxOutputBytes)
	}
	if s != nil {
		j, err := parseAnswer(s, rep.Text)
		if err != nil {
			hint := ""
			if rep.StopReason == "max_tokens" {
				hint = fmt.Sprintf(" (the answer was cut off at the output cap of %d tokens)", maxTokens)
			}
			return fail(ErrInvalidOutput, "the response was not valid JSON for the schema: %v%s", err, hint)
		}
		resp.JSON = j
	}
	return resp, nil
}

// try makes one HTTP request and reads the answer.
func (c *Client) try(ctx context.Context, p Provider, prep *Prepared) (Reply, error) {
	hreq, err := p.NewRequest(ctx, prep)
	if err != nil {
		return Reply{}, &Error{Model: prep.Model.Name, Kind: ErrRequest, Msg: "could not build the request: " + err.Error()}
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(hreq)
	if err != nil {
		return Reply{}, &netError{err}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		pe := p.ParseError(res.StatusCode, body)
		pe.Status = res.StatusCode
		pe.RetryAfter = retryAfter(res.Header.Get("Retry-After"))
		return Reply{}, &pe
	}
	body := &limitedBody{r: res.Body, n: MaxResponseBytes}
	mt, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if mt == "text/event-stream" {
		rep, err := p.ParseStream(body, MaxOutputBytes)
		return rep, readError(err)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return Reply{}, readError(err)
	}
	rep, err := p.ParseResponse(data)
	return rep, readError(err)
}

// netError is a failure to reach the provider or to read its answer.
type netError struct{ err error }

func (e *netError) Error() string { return e.err.Error() }
func (e *netError) Unwrap() error { return e.err }

// readError sorts an error from reading an answer: a dropped connection is
// a netError (retried); a garbled answer, a provider error and an oversized
// answer stay as they are.
func readError(err error) error {
	var pe *ProviderError
	var me *malformedError
	switch {
	case err == nil, errors.Is(err, ErrTooLarge), errors.As(err, &pe), errors.As(err, &me):
		return err
	}
	return &netError{err}
}

// retryWait says whether err is worth another try, and how long to wait.
func (c *Client) retryWait(err error, tries int) (time.Duration, bool) {
	maxWait := c.MaxWait
	if maxWait <= 0 {
		maxWait = 30 * time.Second
	}
	backoff := c.Backoff
	if backoff <= 0 {
		backoff = time.Second
	}
	wait := min(backoff<<(tries-1), maxWait)
	var pe *ProviderError
	var ne *netError
	switch {
	case errors.As(err, &pe):
		switch pe.Status {
		case 408, 429, 500, 502, 503, 504, 529:
		default:
			return 0, false
		}
		if pe.RetryAfter > 0 {
			wait = min(pe.RetryAfter, maxWait)
		}
		return wait, true
	case errors.As(err, &ne):
		return wait, true
	}
	return 0, false
}

// stopped describes a call that ran out of time or was canceled.
func (c *Client) stopped(parent context.Context, m Model, timeout time.Duration) error {
	if err := parent.Err(); err != nil {
		return &Error{Model: m.Name, Kind: ErrCanceled, Msg: "the call was stopped before an answer came: " + err.Error()}
	}
	return &Error{Model: m.Name, Kind: ErrTimeout, Msg: fmt.Sprintf("no answer within %s", timeout)}
}

// failure turns the last try's error into an *Error in words.
func failure(m Model, key string, err error, tries int, note string) error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	after := ""
	if tries > 1 || note != "" {
		after = " after " + plural(tries, "try", "tries")
	}
	out := &Error{Model: m.Name}
	var pe *ProviderError
	var ne *netError
	switch {
	case errors.Is(err, ErrTooLarge):
		out.Kind = ErrOversized
		out.Msg = fmt.Sprintf("oversized output: the provider sent more than %d bytes", MaxResponseBytes)
	case errors.As(err, &pe):
		out.Status, out.RetryAfter = pe.Status, pe.RetryAfter
		what := fmt.Sprintf("%d %s", pe.Status, pe.Type)
		secret := "the model has no key configured"
		if m.Key != nil {
			secret = fmt.Sprintf("check the secret %s in store %s", m.Key.Key, m.Key.Store)
		}
		switch {
		case pe.Status == 401:
			out.Kind = ErrAuth
			out.Msg = fmt.Sprintf("the provider refused the key (%s): %s", what, secret)
		case pe.Status == 403:
			out.Kind = ErrAuth
			out.Msg = fmt.Sprintf("the provider refused access (%s): %s; %s", what, pe.Message, secret)
		case pe.Status == 404:
			out.Kind = ErrRejected
			out.Msg = fmt.Sprintf("the provider has no model %q, or there is no API at %s (%s): %s", m.Model, m.Endpoint, what, pe.Message)
		case pe.Status == 429:
			out.Kind = ErrRateLimited
			out.Msg = fmt.Sprintf("rate limited (429)%s", after)
			if pe.RetryAfter > 0 {
				out.Msg += "; retry-after " + pe.RetryAfter.String()
			}
			if pe.Message != "" {
				out.Msg += ": " + pe.Message
			}
		case pe.Status >= 500 || pe.Status == 408:
			out.Kind = ErrUnavailable
			out.Msg = fmt.Sprintf("the provider failed (%s)%s: %s", what, after, pe.Message)
		default:
			out.Kind = ErrRejected
			out.Msg = fmt.Sprintf("the provider rejected the request (%s): %s", what, pe.Message)
		}
	case errors.As(err, &ne):
		out.Kind = ErrUnavailable
		if errors.Is(err, ErrStreamEnded) {
			out.Msg = "the answer stream ended before it was complete" + after
		} else {
			out.Msg = fmt.Sprintf("could not reach the provider at %s%s: %v", m.Endpoint, after, ne.err)
		}
	default:
		out.Kind = ErrUnavailable
		out.Msg = "the provider's answer could not be read: " + err.Error()
	}
	if note != "" {
		out.Msg += "; " + note
	}
	out.Msg = scrub(out.Msg, key)
	return out
}

// retryAfter reads a Retry-After header: seconds, or an HTTP date.
func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil && s > 0 {
		return time.Duration(s * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d.Round(time.Second)
		}
	}
	return 0
}

// scrub removes the key from text that came from elsewhere, such as a
// provider's error message that quotes the key it refused.
func scrub(s, key string) string {
	key = strings.TrimSpace(key)
	if len(key) < 4 {
		return s
	}
	return strings.ReplaceAll(s, key, "[key]")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
