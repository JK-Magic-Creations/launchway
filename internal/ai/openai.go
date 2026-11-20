package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openAIProvider speaks OpenAI-compatible chat completions: POST
// {endpoint}/chat/completions. Ollama, vLLM, LM Studio and most other local
// servers offer the same API, so it is the way to reach a local model.
type openAIProvider struct{}

// Schema modes of openai models, from most to least help by the server.
const (
	SchemaJSONSchema = "json_schema"
	SchemaJSONObject = "json_object"
	SchemaPrompt     = "prompt"
)

func (openAIProvider) Check(m Model) error {
	if m.Effort != "" {
		return fmt.Errorf("effort is only for anthropic models")
	}
	switch m.MaxTokensField {
	case "", "max_tokens", "max_completion_tokens":
	default:
		return fmt.Errorf("max_tokens_field %q must be max_tokens or max_completion_tokens", m.MaxTokensField)
	}
	switch m.SchemaMode {
	case "", SchemaJSONSchema, SchemaJSONObject, SchemaPrompt:
	default:
		return fmt.Errorf("schema_mode %q must be json_schema, json_object or prompt", m.SchemaMode)
	}
	return nil
}

type openAIRequest struct {
	Model               string            `json:"model"`
	Messages            []openAIMsg       `json:"messages"`
	MaxTokens           int               `json:"max_tokens,omitempty"`
	MaxCompletionTokens int               `json:"max_completion_tokens,omitempty"`
	Temperature         *float64          `json:"temperature,omitempty"`
	ResponseFormat      *openAIFormat     `json:"response_format,omitempty"`
	Stream              bool              `json:"stream,omitempty"`
	StreamOptions       *openAIStreamOpts `json:"stream_options,omitempty"`
}

type openAIMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIFormat struct {
	Type       string            `json:"type"`
	JSONSchema *openAIJSONSchema `json:"json_schema,omitempty"`
}

type openAIJSONSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type openAIStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

func (openAIProvider) NewRequest(ctx context.Context, p *Prepared) (*http.Request, error) {
	system := p.System
	body := openAIRequest{Model: p.Model.Model, Temperature: p.Temperature, Stream: p.Stream}
	if p.Model.MaxTokensField == "max_completion_tokens" {
		body.MaxCompletionTokens = p.MaxTokens
	} else {
		body.MaxTokens = p.MaxTokens
	}
	if p.Stream {
		body.StreamOptions = &openAIStreamOpts{IncludeUsage: true}
	}
	if p.Schema != nil {
		switch p.SchemaMode {
		case SchemaJSONSchema:
			body.ResponseFormat = &openAIFormat{Type: "json_schema",
				JSONSchema: &openAIJSONSchema{Name: "output", Schema: p.Schema, Strict: p.Strict}}
		case SchemaJSONObject:
			body.ResponseFormat = &openAIFormat{Type: "json_object"}
			system = p.schemaInstruction()
		default:
			system = p.schemaInstruction()
		}
	}
	if system != "" {
		body.Messages = append(body.Messages, openAIMsg{Role: "system", Content: system})
	}
	body.Messages = append(body.Messages, openAIMsg{Role: "user", Content: p.Prompt})
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Model.Endpoint+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	for k, v := range p.Model.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if p.Key != "" {
		req.Header.Set("Authorization", "Bearer "+p.Key)
	}
	return req, nil
}

type openAIUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	PromptDetails    *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// usage counts cached prompt tokens apart, as the Anthropic API does, so
// InputTokens is always the input that was neither read from nor written to
// a cache.
func (u *openAIUsage) usage() Usage {
	out := Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
	if u.PromptDetails != nil && u.PromptDetails.CachedTokens <= u.PromptTokens {
		out.CacheReadTokens = u.PromptDetails.CachedTokens
		out.InputTokens -= u.PromptDetails.CachedTokens
	}
	return out
}

type openAIMessage struct {
	Content *string `json:"content"`
	Refusal *string `json:"refusal"`
}

type openAIChoice struct {
	Message      openAIMessage `json:"message"`
	Delta        openAIMessage `json:"delta"`
	FinishReason *string       `json:"finish_reason"`
}

type openAIResponse struct {
	Model   string          `json:"model"`
	Choices []openAIChoice  `json:"choices"`
	Usage   *openAIUsage    `json:"usage"`
	Error   json.RawMessage `json:"error"`
}

func (openAIProvider) ParseResponse(body []byte) (Reply, error) {
	var resp openAIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Reply{}, Malformed("the answer is not a chat completions response: %v", err)
	}
	if len(resp.Choices) == 0 {
		return Reply{}, Malformed("the answer has no choices")
	}
	c := resp.Choices[0]
	r := Reply{Model: resp.Model}
	if c.Message.Content != nil {
		r.Text = *c.Message.Content
	}
	if resp.Usage != nil {
		r.Usage = resp.Usage.usage()
	}
	finish := ""
	if c.FinishReason != nil {
		finish = *c.FinishReason
	}
	refusal := ""
	if c.Message.Refusal != nil {
		refusal = *c.Message.Refusal
	}
	openAIStop(&r, finish, refusal)
	return r, nil
}

// openAIStop records the finish reason in the Anthropic API's words, so
// callers see one vocabulary: end_turn, max_tokens, tool_use, refusal.
func openAIStop(r *Reply, finish, refusal string) {
	switch finish {
	case "stop":
		r.StopReason = "end_turn"
	case "length":
		r.StopReason = "max_tokens"
	case "tool_calls", "function_call":
		r.StopReason = "tool_use"
	case "content_filter":
		r.StopReason = "refusal"
		r.Refused, r.RefusalDetail = true, "content_filter"
	default:
		r.StopReason = finish
	}
	if refusal != "" {
		r.StopReason = "refusal"
		r.Refused, r.RefusalDetail = true, "refusal: "+oneLine(refusal)
	}
}

func (openAIProvider) ParseStream(rd io.Reader, maxText int) (Reply, error) {
	var r Reply
	text := textBuilder{max: maxText}
	finish, refusal := "", ""
	err := readSSE(rd, func(_, data string) (bool, error) {
		if strings.TrimSpace(data) == "[DONE]" {
			return true, nil
		}
		var chunk openAIResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return false, Malformed("a stream chunk is not JSON: %v", err)
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			e := openAIParseError(500, chunk.Error)
			e.Status = 500
			return false, &e
		}
		if chunk.Model != "" {
			r.Model = chunk.Model
		}
		if chunk.Usage != nil {
			r.Usage = chunk.Usage.usage()
		}
		if len(chunk.Choices) > 0 {
			c := chunk.Choices[0]
			if c.FinishReason != nil && *c.FinishReason != "" {
				finish = *c.FinishReason
			}
			if c.Delta.Refusal != nil {
				refusal += *c.Delta.Refusal
			}
			if c.Delta.Content != nil {
				return false, text.add(*c.Delta.Content)
			}
		}
		return false, nil
	})
	if err == ErrStreamEnded && finish != "" {
		err = nil // a few servers end the stream without [DONE]
	}
	r.Text = text.b.String()
	openAIStop(&r, finish, refusal)
	return r, err
}

func (openAIProvider) ParseError(status int, body []byte) ProviderError {
	var e struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &e) != nil {
		return unknownError(status, body)
	}
	switch {
	case len(e.Error) > 0 && string(e.Error) != "null":
		return openAIParseError(status, e.Error)
	case e.Message != "":
		return ProviderError{Type: http.StatusText(status), Message: oneLine(e.Message)}
	case len(e.Detail) > 0:
		return ProviderError{Type: http.StatusText(status), Message: oneLine(string(e.Detail))}
	}
	return unknownError(status, body)
}

// openAIParseError reads {"message", "type", "code"}, or a bare message
// string, which some local servers send instead.
func openAIParseError(status int, raw json.RawMessage) ProviderError {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return ProviderError{Type: http.StatusText(status), Message: oneLine(s)}
	}
	var e struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
	}
	_ = json.Unmarshal(raw, &e)
	typ := e.Type
	if typ == "" {
		var code string
		if json.Unmarshal(e.Code, &code) == nil && code != "" {
			typ = code
		} else {
			typ = http.StatusText(status)
		}
	}
	return ProviderError{Type: typ, Message: oneLine(e.Message)}
}

// Downgrade drops to the next schema mode when the server refuses the
// structured-output request, so a server without json_schema support still
// works; the answer is checked here either way.
func (openAIProvider) Downgrade(p *Prepared, e *ProviderError) bool {
	if p.Schema == nil || e.Status != http.StatusBadRequest {
		return false
	}
	msg := strings.ToLower(e.Message + " " + e.Type)
	if !strings.Contains(msg, "response_format") && !strings.Contains(msg, "json_schema") &&
		!strings.Contains(msg, "json schema") && !strings.Contains(msg, "json_object") {
		return false
	}
	switch p.SchemaMode {
	case SchemaJSONSchema:
		p.SchemaMode = SchemaJSONObject
	case SchemaJSONObject:
		p.SchemaMode = SchemaPrompt
	default:
		return false
	}
	return true
}
