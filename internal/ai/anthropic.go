package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
)

// AnthropicVersion is the Messages API version Launchway speaks. On this
// version temperature, top_p and top_k are refused on every model, so they
// are never sent.
const AnthropicVersion = "2026-10-21"

// anthropicProvider speaks the Anthropic Messages API: POST /v1/messages.
type anthropicProvider struct{}

func (anthropicProvider) Check(m Model) error {
	if m.Effort != "" && !slices.Contains(effortSet, m.Effort) {
		return fmt.Errorf("effort %q must be one of low, medium, high, xhigh, max (or unset for the model's default)", m.Effort)
	}
	if m.MaxTokensField != "" {
		return fmt.Errorf("max_tokens_field is only for openai models")
	}
	if m.SchemaMode != "" {
		return fmt.Errorf("schema_mode is only for openai models; anthropic models always use structured outputs")
	}
	return nil
}

type anthropicRequest struct {
	Model        string           `json:"model"`
	MaxTokens    int              `json:"max_tokens"`
	System       string           `json:"system,omitempty"`
	Messages     []anthropicMsg   `json:"messages"`
	Stream       bool             `json:"stream,omitempty"`
	OutputConfig *anthropicOutCfg `json:"output_config,omitempty"`
}

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicOutCfg struct {
	Effort string           `json:"effort,omitempty"`
	Format *anthropicFormat `json:"format,omitempty"`
}

type anthropicFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

func (anthropicProvider) NewRequest(ctx context.Context, p *Prepared) (*http.Request, error) {
	body := anthropicRequest{
		Model:     p.Model.Model,
		MaxTokens: p.MaxTokens,
		System:    p.System,
		Messages:  []anthropicMsg{{Role: "user", Content: p.Prompt}},
		Stream:    p.Stream,
	}
	if p.Model.Effort != "" || p.Schema != nil {
		body.OutputConfig = &anthropicOutCfg{Effort: p.Model.Effort}
		if p.Schema != nil {
			body.OutputConfig.Format = &anthropicFormat{Type: "json_schema", Schema: p.Schema}
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Model.Endpoint+"/v1/messages", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	for k, v := range p.Model.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", AnthropicVersion)
	if p.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if p.Key != "" {
		req.Header.Set("X-Api-Key", p.Key)
	}
	return req, nil
}

type anthropicUsage struct {
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	CacheWriteTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadTokens  *int64 `json:"cache_read_input_tokens"`
}

// merge takes the counts present in u; stream events repeat running totals.
func (u anthropicUsage) merge(into *Usage) {
	for _, f := range []struct {
		from *int64
		to   *int64
	}{{u.InputTokens, &into.InputTokens}, {u.OutputTokens, &into.OutputTokens},
		{u.CacheWriteTokens, &into.CacheWriteTokens}, {u.CacheReadTokens, &into.CacheReadTokens}} {
		if f.from != nil {
			*f.to = *f.from
		}
	}
}

type anthropicStopDetails struct {
	Category    string `json:"category"`
	Explanation string `json:"explanation"`
}

type anthropicMessage struct {
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason  string                `json:"stop_reason"`
	StopDetails *anthropicStopDetails `json:"stop_details"`
	Usage       anthropicUsage        `json:"usage"`
}

func (anthropicProvider) ParseResponse(body []byte) (Reply, error) {
	var msg anthropicMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return Reply{}, Malformed("the answer is not a Messages API response: %v", err)
	}
	var r Reply
	for _, c := range msg.Content {
		if c.Type == "text" { // thinking and other blocks are not the answer
			r.Text += c.Text
		}
	}
	r.Model = msg.Model
	msg.Usage.merge(&r.Usage)
	anthropicStop(&r, msg.StopReason, msg.StopDetails)
	return r, nil
}

func anthropicStop(r *Reply, reason string, d *anthropicStopDetails) {
	r.StopReason = reason
	if reason == "refusal" {
		r.Refused = true
		r.RefusalDetail = "refusal"
		if d != nil && d.Category != "" {
			r.RefusalDetail += ", category " + d.Category
		}
	}
}

func (anthropicProvider) ParseStream(rd io.Reader, maxText int) (Reply, error) {
	var r Reply
	text := textBuilder{max: maxText}
	textBlocks := map[int]bool{}
	err := readSSE(rd, func(event, data string) (bool, error) {
		var ev struct {
			Type    string           `json:"type"`
			Index   int              `json:"index"`
			Message anthropicMessage `json:"message"`
			Block   struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content_block"`
			Delta struct {
				Type        string                `json:"type"`
				Text        string                `json:"text"`
				StopReason  string                `json:"stop_reason"`
				StopDetails *anthropicStopDetails `json:"stop_details"`
			} `json:"delta"`
			Usage anthropicUsage `json:"usage"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return false, Malformed("a stream event (%s) is not JSON: %v", event, err)
		}
		switch ev.Type {
		case "message_start":
			r.Model = ev.Message.Model
			ev.Message.Usage.merge(&r.Usage)
		case "content_block_start":
			if ev.Block.Type == "text" {
				textBlocks[ev.Index] = true
				return false, text.add(ev.Block.Text)
			}
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && textBlocks[ev.Index] {
				return false, text.add(ev.Delta.Text)
			}
		case "message_delta":
			ev.Usage.merge(&r.Usage)
			if ev.Delta.StopReason != "" {
				anthropicStop(&r, ev.Delta.StopReason, ev.Delta.StopDetails)
			}
		case "message_stop":
			return true, nil
		case "error":
			e := &ProviderError{Status: anthropicStatus(ev.Error.Type), Type: ev.Error.Type, Message: ev.Error.Message}
			return false, e
		}
		return false, nil // ping, content_block_stop and events added later
	})
	r.Text = text.b.String()
	return r, err
}

// anthropicStatus is the HTTP status an error type reported in a stream
// stands for, so that it is retried like the same error before the stream.
func anthropicStatus(typ string) int {
	switch typ {
	case "overloaded_error":
		return 529
	case "rate_limit_error":
		return 429
	case "api_error":
		return 500
	case "invalid_request_error":
		return 400
	}
	return 500
}

func (anthropicProvider) ParseError(status int, body []byte) ProviderError {
	var e struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error.Type == "" {
		return unknownError(status, body)
	}
	return ProviderError{Type: e.Error.Type, Message: oneLine(e.Error.Message)}
}

func (anthropicProvider) Downgrade(*Prepared, *ProviderError) bool { return false }
