package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/ai/aitest"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// testKey is built at run time so no key-shaped string is in the source.
var testKey = strings.Join([]string{"fake", "test", strings.Repeat("k", 24)}, "-")

const riskSchema = `{"type":"object","properties":{"risk":{"type":"string","enum":["low","high"]},"why":{"type":"string","maxLength":200}},"required":["risk","why"]}`

func fastClient() *ai.Client {
	return &ai.Client{Backoff: time.Millisecond, MaxWait: 20 * time.Millisecond}
}

func registry(t *testing.T, models ...ai.Model) *ai.Registry {
	t.Helper()
	r, err := ai.NewRegistry(models)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func claude(t *testing.T, srv *aitest.Server, edit ...func(*ai.Model)) ai.Model {
	t.Helper()
	m := ai.Model{Name: "claude", Provider: "anthropic", Endpoint: srv.AnthropicEndpoint(), Model: "claude-sonnet-5-5",
		Key: &pipeline.SecretRef{Store: "vault", Key: "ai/anthropic", Field: "key"}, Projects: []string{"*"}}
	for _, e := range edit {
		e(&m)
	}
	got, _ := registry(t, m).Get(m.Name)
	return got
}

func local(t *testing.T, srv *aitest.Server, edit ...func(*ai.Model)) ai.Model {
	t.Helper()
	m := ai.Model{Name: "local", Provider: "openai", Endpoint: srv.OpenAIEndpoint(), Model: "qwen3:8b", Projects: []string{"team-*"}}
	for _, e := range edit {
		e(&m)
	}
	got, _ := registry(t, m).Get(m.Name)
	return got
}

func wantKind(t *testing.T, err error, kind ai.ErrorKind, contains ...string) *ai.Error {
	t.Helper()
	var e *ai.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v (%T) is not an *ai.Error", err, err)
	}
	if e.Kind != kind {
		t.Fatalf("kind %s, want %s: %v", e.Kind, kind, err)
	}
	for _, c := range contains {
		if !strings.Contains(err.Error(), c) {
			t.Errorf("error %q does not contain %q", err, c)
		}
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("error contains the key: %v", err)
	}
	return e
}

func TestAnthropicCall(t *testing.T) {
	srv := aitest.New(t)
	srv.Enqueue(aitest.Reply{JSON: map[string]any{"risk": "low", "why": "docs only"},
		InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 500, Model: "claude-sonnet-5-5"})
	m := claude(t, srv, func(m *ai.Model) { m.Effort = "low" })
	resp, err := fastClient().Call(context.Background(), m, testKey, ai.Request{
		System: "You review changes.", Prompt: "diff here", Schema: json.RawMessage(riskSchema)})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.JSON) != `{"risk":"low","why":"docs only"}` || resp.StopReason != "end_turn" || resp.Model != "claude-sonnet-5-5" {
		t.Errorf("response %+v", resp)
	}
	if resp.Usage != (ai.Usage{InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 500}) {
		t.Errorf("usage %+v", resp.Usage)
	}
	// Built-in Sonnet price: $2 in, $10 out, $0.10 cache read per million.
	if want := (1000*2.0 + 200*10.0 + 500*0.10) / 1e6; abs(resp.CostUSD-want) > 1e-12 {
		t.Errorf("cost %v, want %v", resp.CostUSD, want)
	}
	req := srv.Last(t)
	if req.Header.Get("X-Api-Key") != testKey || req.Header.Get("Anthropic-Version") != "2026-10-21" || req.Header.Get("Authorization") != "" {
		t.Errorf("headers %v", req.Header)
	}
	for _, f := range []string{"temperature", "top_p", "top_k", "thinking", "stream", "output_format"} {
		if req.Has(f) {
			t.Errorf("request has %s: %s", f, req.Body)
		}
	}
	if req.JSON["max_tokens"] != float64(4096) || req.JSON["system"] != "You review changes." {
		t.Errorf("body %s", req.Body)
	}
	oc := req.JSON["output_config"].(map[string]any)
	if oc["effort"] != "low" {
		t.Errorf("effort %v", oc["effort"])
	}
	format := oc["format"].(map[string]any)
	schema := format["schema"].(map[string]any)
	if format["type"] != "json_schema" || schema["additionalProperties"] != false {
		t.Errorf("format %v", format)
	}
	why := schema["properties"].(map[string]any)["why"].(map[string]any)
	if _, has := why["maxLength"]; has || !strings.Contains(why["description"].(string), "maxLength 200") {
		t.Errorf("constraint not moved to the description: %v", why)
	}
	if strings.Contains(req.Text(), testKey) {
		t.Error("the key is in the prompt")
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestAnthropicStream(t *testing.T) {
	srv := aitest.New(t)
	srv.Enqueue(aitest.Reply{Text: "a longer streamed answer, in pieces", ChunkSize: 5, InputTokens: 30, OutputTokens: 12, CacheWriteTokens: 7})
	m := claude(t, srv, func(m *ai.Model) { m.MaxTokens = 32000 })
	resp, err := fastClient().Call(context.Background(), m, testKey, ai.Request{Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "a longer streamed answer, in pieces" || resp.StopReason != "end_turn" {
		t.Errorf("response %+v", resp)
	}
	if resp.Usage != (ai.Usage{InputTokens: 30, OutputTokens: 12, CacheWriteTokens: 7}) {
		t.Errorf("usage %+v", resp.Usage)
	}
	req := srv.Last(t)
	if !req.Stream() || req.JSON["max_tokens"] != float64(32000) {
		t.Errorf("body %s", req.Body)
	}

	// A model may turn streaming off, or on for small calls.
	off := false
	m = claude(t, srv, func(m *ai.Model) { m.MaxTokens = 32000; m.Stream = &off })
	if _, err := fastClient().Call(context.Background(), m, testKey, ai.Request{Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	if srv.Last(t).Stream() {
		t.Error("streamed although stream: false")
	}
}

func TestOpenAICall(t *testing.T) {
	for _, stream := range []bool{false, true} {
		srv := aitest.New(t)
		srv.Enqueue(aitest.Reply{JSON: map[string]any{"risk": "high", "why": "touches auth"}, InputTokens: 50, CacheReadTokens: 10, OutputTokens: 9})
		m := local(t, srv, func(m *ai.Model) { m.Stream = &stream; m.Price = &ai.Price{InputPerMTok: 1, OutputPerMTok: 2} })
		temp := 0.2
		resp, err := fastClient().Call(context.Background(), m, "", ai.Request{
			System: "sys", Prompt: "p", Schema: json.RawMessage(riskSchema), Temperature: &temp, MaxTokens: 100})
		if err != nil {
			t.Fatal(stream, err)
		}
		if string(resp.JSON) != `{"risk":"high","why":"touches auth"}` || resp.StopReason != "end_turn" || resp.Model != "qwen3:8b" {
			t.Errorf("stream %v: response %+v", stream, resp)
		}
		if resp.Usage != (ai.Usage{InputTokens: 50, OutputTokens: 9, CacheReadTokens: 10}) {
			t.Errorf("stream %v: usage %+v", stream, resp.Usage)
		}
		req := srv.Last(t)
		if req.Header.Get("Authorization") != "" {
			t.Error("sent an Authorization header without a key")
		}
		if req.JSON["temperature"] != 0.2 || req.JSON["max_tokens"] != float64(100) || req.Has("max_completion_tokens") {
			t.Errorf("body %s", req.Body)
		}
		rf := req.JSON["response_format"].(map[string]any)
		js := rf["json_schema"].(map[string]any)
		if rf["type"] != "json_schema" || js["name"] != "output" || js["strict"] != true {
			t.Errorf("response_format %v", rf)
		}
		if stream {
			if opts := req.JSON["stream_options"].(map[string]any); opts["include_usage"] != true {
				t.Errorf("stream_options %v", opts)
			}
		}
		msgs := req.JSON["messages"].([]any)
		if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "p" {
			t.Errorf("messages %v", msgs)
		}
	}
}

func TestOpenAIKeyAndTokenField(t *testing.T) {
	srv := aitest.New(t)
	m := local(t, srv, func(m *ai.Model) {
		m.MaxTokensField = "max_completion_tokens"
		m.Key = &pipeline.SecretRef{Store: "env", Key: "OPENAI_KEY"}
		m.Headers = map[string]string{"X-Team": "build"}
	})
	if _, err := fastClient().Call(context.Background(), m, testKey, ai.Request{Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	req := srv.Last(t)
	if req.Header.Get("Authorization") != "Bearer "+testKey || req.Header.Get("X-Team") != "build" {
		t.Errorf("headers %v", req.Header)
	}
	if req.Has("max_tokens") || req.JSON["max_completion_tokens"] != float64(4096) || req.Has("response_format") {
		t.Errorf("body %s", req.Body)
	}
}

func TestSchemaModeFallback(t *testing.T) {
	srv := aitest.New(t)
	srv.Enqueue(aitest.Reply{Status: 400, ErrorMessage: "response_format type json_schema is not supported"},
		aitest.Reply{Text: "```json\n{\"risk\":\"low\",\"why\":\"x\"}\n```"})
	m := local(t, srv)
	resp, err := fastClient().Call(context.Background(), m, "", ai.Request{Prompt: "p", Schema: json.RawMessage(riskSchema)})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.JSON) != `{"risk":"low","why":"x"}` {
		t.Errorf("json %s", resp.JSON)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	rf := reqs[1].JSON["response_format"].(map[string]any)
	if rf["type"] != "json_object" || !strings.Contains(reqs[1].Text(), `"enum":["low","high"]`) {
		t.Errorf("fallback request %s", reqs[1].Body)
	}

	// Prompt mode sends no response_format at all.
	m = local(t, srv, func(m *ai.Model) { m.SchemaMode = "prompt" })
	srv.Enqueue(aitest.Reply{Text: `<think>hmm</think>{"risk":"high","why":"y"}`})
	if _, err := fastClient().Call(context.Background(), m, "", ai.Request{Prompt: "p", Schema: json.RawMessage(riskSchema)}); err != nil {
		t.Fatal(err)
	}
	if last := srv.Last(t); last.Has("response_format") || !strings.Contains(last.Text(), "JSON schema") {
		t.Errorf("prompt mode request %s", last.Body)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		kind     ai.ErrorKind
		requests int
		want     string
	}{
		{400, ai.ErrRejected, 1, "the provider rejected the request (400 invalid_request_error)"},
		{401, ai.ErrAuth, 1, "the provider refused the key (401 authentication_error): check the secret ai/anthropic in store vault"},
		{403, ai.ErrAuth, 1, "the provider refused access (403 permission_error)"},
		{404, ai.ErrRejected, 1, `the provider has no model "claude-sonnet-5-5"`},
		{500, ai.ErrUnavailable, 3, "the provider failed (500 api_error) after 3 tries"},
		{529, ai.ErrUnavailable, 3, "the provider failed (529 overloaded_error) after 3 tries"},
	}
	for _, api := range []string{"anthropic", "openai"} {
		for _, c := range cases {
			srv := aitest.New(t)
			// The provider's message quotes the key, as some do for a bad key.
			srv.SetDefault(aitest.Reply{Status: c.status, ErrorMessage: "bad key " + testKey})
			m := claude(t, srv)
			if api == "openai" {
				m = local(t, srv, func(m *ai.Model) {
					m.Key = &pipeline.SecretRef{Store: "vault", Key: "ai/anthropic"}
					m.Model = "claude-sonnet-5-5"
				})
			}
			resp, err := fastClient().Call(context.Background(), m, testKey, ai.Request{Prompt: "p"})
			e := wantKind(t, err, c.kind, "model "+m.Name+": ", c.want)
			if e.Status != c.status || resp.Text != "" {
				t.Errorf("%s %d: status %d, response %+v", api, c.status, e.Status, resp)
			}
			if n := len(srv.Requests()); n != c.requests {
				t.Errorf("%s %d: %d requests, want %d", api, c.status, n, c.requests)
			}
		}
	}
}

func TestRateLimitRetryAfter(t *testing.T) {
	srv := aitest.New(t)
	srv.Enqueue(aitest.Reply{Status: 429, RetryAfter: "1"}, aitest.Reply{Text: "done"})
	m := claude(t, srv)
	start := time.Now()
	resp, err := (&ai.Client{}).Call(context.Background(), m, testKey, ai.Request{Prompt: "p"})
	if err != nil || resp.Text != "done" {
		t.Fatal(resp, err)
	}
	if d := time.Since(start); d < time.Second {
		t.Errorf("retried after %s, before the provider's retry-after of 1s", d)
	}
	if n := len(srv.Requests()); n != 2 {
		t.Errorf("%d requests", n)
	}
}

func TestRateLimitExhausted(t *testing.T) {
	srv := aitest.New(t)
	srv.SetDefault(aitest.Reply{Status: 429, RetryAfter: "20"})
	m := claude(t, srv)
	_, err := fastClient().Call(context.Background(), m, testKey, ai.Request{Prompt: "p"})
	e := wantKind(t, err, ai.ErrRateLimited, "model claude: rate limited (429) after 3 tries; retry-after 20s")
	if e.RetryAfter != 20*time.Second || len(srv.Requests()) != 3 {
		t.Errorf("retry-after %s, %d requests", e.RetryAfter, len(srv.Requests()))
	}

	// A wait past the call's time limit is not started.
	srv2 := aitest.New(t)
	srv2.SetDefault(aitest.Reply{Status: 429, RetryAfter: "20"})
	m = claude(t, srv2, func(m *ai.Model) { m.Timeout = "5s" })
	start := time.Now()
	_, err = (&ai.Client{}).Call(context.Background(), m, testKey, ai.Request{Prompt: "p"})
	wantKind(t, err, ai.ErrRateLimited, "would pass the call's time limit of 5s")
	if time.Since(start) > testlib.Slow(2*time.Second) || len(srv2.Requests()) != 1 {
		t.Errorf("waited %s, %d requests", time.Since(start), len(srv2.Requests()))
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	srv := aitest.New(t)
	srv.SetDefault(aitest.Reply{Delay: 10 * time.Second})
	m := local(t, srv, func(m *ai.Model) { m.Timeout = "300ms" })
	start := time.Now()
	_, err := fastClient().Call(context.Background(), m, "", ai.Request{Prompt: "p"})
	wantKind(t, err, ai.ErrTimeout, "model local: no answer within 300ms")
	if d := time.Since(start); d > testlib.Slow(3*time.Second) {
		t.Errorf("took %s", d)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err = fastClient().Call(ctx, local(t, srv), "", ai.Request{Prompt: "p"})
	wantKind(t, err, ai.ErrCanceled, "stopped before an answer came")
}

func TestOversized(t *testing.T) {
	big := strings.Repeat("x", ai.MaxOutputBytes+1)
	for _, stream := range []bool{false, true} {
		srv := aitest.New(t)
		srv.SetDefault(aitest.Reply{Text: big, ChunkSize: 64 << 10})
		for _, m := range []ai.Model{claude(t, srv, func(m *ai.Model) { m.Stream = &stream }), local(t, srv, func(m *ai.Model) { m.Stream = &stream })} {
			resp, err := fastClient().Call(context.Background(), m, testKey, ai.Request{Prompt: "p"})
			wantKind(t, err, ai.ErrOversized, "oversized output")
			if len(resp.Text) > ai.MaxOutputBytes {
				t.Errorf("stream %v: returned %d bytes of text", stream, len(resp.Text))
			}
		}
	}
	srv := aitest.New(t)
	srv.SetDefault(aitest.Reply{RawBody: `{"pad":"` + strings.Repeat("y", ai.MaxResponseBytes) + `"}`})
	_, err := fastClient().Call(context.Background(), local(t, srv), "", ai.Request{Prompt: "p"})
	wantKind(t, err, ai.ErrOversized, "more than 4194304 bytes")
	if len(srv.Requests()) != 1 {
		t.Errorf("oversized answers were retried")
	}
}

func TestInvalidStructuredOutput(t *testing.T) {
	cases := map[string]string{
		`{"risk":"medium","why":"x"}`:        "$.risk: must be one of low, high",
		`{"risk":"low"}`:                     `$: missing required property "why"`,
		`{"risk":"low","why":"x","extra":1}`: `$: unexpected property "extra"`,
		`not json at all`:                    "the answer is not JSON",
	}
	for text, want := range cases {
		for _, api := range []string{"anthropic", "openai"} {
			srv := aitest.New(t)
			srv.SetDefault(aitest.Reply{Text: text, OutputTokens: 5})
			m := claude(t, srv)
			if api == "openai" {
				m = local(t, srv)
			}
			resp, err := fastClient().Call(context.Background(), m, testKey, ai.Request{Prompt: "p", Schema: json.RawMessage(riskSchema)})
			wantKind(t, err, ai.ErrInvalidOutput, "the response was not valid JSON for the schema: "+want)
			if resp.Usage.OutputTokens != 5 || resp.JSON != nil {
				t.Errorf("response with the error: %+v", resp)
			}
		}
	}
	srv := aitest.New(t)
	srv.SetDefault(aitest.Reply{Text: `{"risk":"lo`, StopReason: "max_tokens"})
	_, err := fastClient().Call(context.Background(), claude(t, srv), testKey, ai.Request{Prompt: "p", Schema: json.RawMessage(riskSchema)})
	wantKind(t, err, ai.ErrInvalidOutput, "cut off at the output cap of 4096 tokens")
}

func TestRefusal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		srv := aitest.New(t)
		srv.SetDefault(aitest.Reply{Refuse: true, RefusalCategory: "cyber", OutputTokens: 3})
		resp, err := fastClient().Call(context.Background(), claude(t, srv, func(m *ai.Model) { m.Stream = &stream }), testKey, ai.Request{Prompt: "p"})
		wantKind(t, err, ai.ErrRefused, "model claude: declined to answer (refusal, category cyber)")
		if resp.StopReason != "refusal" || resp.Usage.OutputTokens != 3 {
			t.Errorf("response %+v", resp)
		}
		_, err = fastClient().Call(context.Background(), local(t, srv, func(m *ai.Model) { m.Stream = &stream }), "", ai.Request{Prompt: "p"})
		wantKind(t, err, ai.ErrRefused, "model local: declined to answer (refusal: I can't help with that.)")

		srv.SetDefault(aitest.Reply{StopReason: "refusal"}) // openai content_filter
		_, err = fastClient().Call(context.Background(), local(t, srv, func(m *ai.Model) { m.Stream = &stream }), "", ai.Request{Prompt: "p"})
		wantKind(t, err, ai.ErrRefused, "declined to answer (content_filter)")
	}
}

func TestRequestChecks(t *testing.T) {
	srv := aitest.New(t)
	small := claude(t, srv, func(m *ai.Model) { m.MaxInputBytes = 10; m.MaxTokens = 100 })
	temp := 0.5
	cases := []struct {
		m    ai.Model
		req  ai.Request
		want string
	}{
		{small, ai.Request{System: "123456", Prompt: "12345"}, "the prompt and instructions are 11 bytes, over the model's limit of 10 (max_input_bytes)"},
		{small, ai.Request{Prompt: "p", MaxTokens: 101}, "max_tokens 101 is over the model's cap of 100"},
		{small, ai.Request{Prompt: " "}, "the prompt is empty"},
		{small, ai.Request{Prompt: "p", Temperature: &temp}, "temperature cannot be set for anthropic models"},
		{small, ai.Request{Prompt: "p", Schema: json.RawMessage(`{"type":"string"}`)}, "must describe an object"},
	}
	for _, c := range cases {
		_, err := fastClient().Call(context.Background(), c.m, testKey, c.req)
		wantKind(t, err, ai.ErrRequest, c.want)
		if c.m.CheckRequest(c.req) == nil {
			t.Errorf("CheckRequest accepted %+v", c.req)
		}
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("%d requests sent for invalid calls", n)
	}
	if !local(t, srv).SupportsTemperature() || small.SupportsTemperature() {
		t.Error("SupportsTemperature")
	}
}

func TestStreamFailures(t *testing.T) {
	cases := []struct {
		reply    aitest.Reply
		kind     ai.ErrorKind
		requests int
		want     string
	}{
		{aitest.Reply{Text: "abcdefghijklmnop", MalformedEvent: true}, ai.ErrUnavailable, 1, "could not be read"},
		{aitest.Reply{Text: "abcdefghijklmnop", CutStream: true}, ai.ErrUnavailable, 3, "ended before it was complete after 3 tries"},
		{aitest.Reply{Text: "abcdefghijklmnop", StreamError: true}, ai.ErrUnavailable, 3, "after 3 tries"},
	}
	on := true
	for _, c := range cases {
		for _, api := range []string{"anthropic", "openai"} {
			srv := aitest.New(t)
			srv.SetDefault(c.reply)
			m := claude(t, srv, func(m *ai.Model) { m.Stream = &on })
			if api == "openai" {
				m = local(t, srv, func(m *ai.Model) { m.Stream = &on })
			}
			_, err := fastClient().Call(context.Background(), m, testKey, ai.Request{Prompt: "p"})
			wantKind(t, err, c.kind, c.want)
			if n := len(srv.Requests()); n != c.requests {
				t.Errorf("%s %+v: %d requests, want %d", api, c.reply, n, c.requests)
			}
		}
	}
	// A stream error followed by success on the retry.
	srv := aitest.New(t)
	srv.Enqueue(aitest.Reply{Text: "abcdefghijklmnop", StreamError: true}, aitest.Reply{Text: "fine"})
	resp, err := fastClient().Call(context.Background(), claude(t, srv, func(m *ai.Model) { m.Stream = &on }), testKey, ai.Request{Prompt: "p"})
	if err != nil || resp.Text != "fine" {
		t.Fatal(resp, err)
	}
}

func TestUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	m, _ := registry(t, ai.Model{Name: "gone", Provider: "openai", Endpoint: url + "/v1", Model: "m"}).Get("gone")
	_, err := fastClient().Call(context.Background(), m, testKey, ai.Request{Prompt: "p"})
	wantKind(t, err, ai.ErrUnavailable, "could not reach the provider at "+url+"/v1 after 3 tries")
}

func TestNonJSONErrorBody(t *testing.T) {
	srv := aitest.New(t)
	srv.SetDefault(aitest.Reply{Status: 502, RawBody: "<html>Bad Gateway</html>", ContentType: "text/html"})
	_, err := fastClient().Call(context.Background(), claude(t, srv), testKey, ai.Request{Prompt: "p"})
	wantKind(t, err, ai.ErrUnavailable, "the provider failed (502 http_error) after 3 tries: Bad Gateway")
}
