package ai

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Defaults and hard limits for a model's settings.
const (
	DefaultMaxTokens     = 4096
	MaxMaxTokens         = 128000
	DefaultMaxInputBytes = 256 << 10
	MaxMaxInputBytes     = 8 << 20
	DefaultCallsPerRun   = 20
	MaxCallsPerRun       = 1000
	DefaultTimeout       = 2 * time.Minute
	MaxTimeout           = 15 * time.Minute

	// StreamAbove is the output cap above which a call streams unless the
	// model says otherwise: long answers over one plain request risk idle
	// timeouts in proxies and load balancers.
	StreamAbove = 16000

	// DefaultAnthropicEndpoint is where anthropic models are called when the
	// operator names no endpoint.
	DefaultAnthropicEndpoint = "https://api.anthropic.com"
)

// Model is one entry of the operator's model registry (the server
// configuration's ai.models list). Pipelines name a model; only the operator
// says where it lives, which key it uses and how much it may spend.
type Model struct {
	Name          string              `yaml:"name" json:"name"`                               // what pipelines call it: [a-z][a-z0-9-]{0,39}
	Provider      string              `yaml:"provider" json:"provider"`                       // anthropic or openai (OpenAI-compatible chat completions)
	Endpoint      string              `yaml:"endpoint" json:"endpoint,omitempty"`             // base URL; default https://api.anthropic.com for anthropic, required for openai
	Model         string              `yaml:"model" json:"model"`                             // the provider's model id, such as claude-sonnet-5-5 or qwen3:8b
	Key           *pipeline.SecretRef `yaml:"key" json:"key,omitempty"`                       // where the API key is kept; a local server may need none
	Projects      []string            `yaml:"projects" json:"projects,omitempty"`             // path.Match patterns of projects that may use it; empty: none
	Price         *Price              `yaml:"price" json:"price,omitempty"`                   // for showing cost; built in for current Claude models
	Headers       map[string]string   `yaml:"headers" json:"headers,omitempty"`               // extra headers that are not secret
	Timeout       string              `yaml:"timeout" json:"timeout,omitempty"`               // per call, retries included; default 2m, at most 15m
	Effort        string              `yaml:"effort" json:"effort,omitempty"`                 // anthropic only: low, medium, high, xhigh or max; unset leaves the model's default
	Stream        *bool               `yaml:"stream" json:"stream,omitempty"`                 // default: stream when a call may write more than 16000 tokens
	MaxTokens     int                 `yaml:"max_tokens" json:"max_tokens"`                   // output cap per call; default 4096, at most 128000
	MaxInputBytes int                 `yaml:"max_input_bytes" json:"max_input_bytes"`         // system plus prompt bytes per call; default 256 KiB
	CallsPerRun   int                 `yaml:"calls_per_run" json:"calls_per_run"`             // default 20; the caller enforces it
	TokensPerDay  int64               `yaml:"tokens_per_day" json:"tokens_per_day,omitempty"` // per project per day, input plus output; 0: no limit; the caller enforces it

	// MaxTokensField is the request field that carries the output cap, for
	// openai models: max_tokens (the default, which every local server
	// accepts) or max_completion_tokens (which newer hosted models need).
	MaxTokensField string `yaml:"max_tokens_field" json:"max_tokens_field,omitempty"`

	// SchemaMode is how an openai model is asked for JSON when a step gives
	// a schema: json_schema (the default: response_format with the schema),
	// json_object (response_format json_object, schema in the instructions)
	// or prompt (schema in the instructions only, for servers with neither).
	// A json_schema call that the server rejects because of response_format
	// is tried once more as json_object, then as prompt. The answer is
	// checked against the schema here in every mode.
	SchemaMode string `yaml:"schema_mode" json:"schema_mode,omitempty"`
}

// Price is what a model costs in US dollars per million tokens. It is only
// used to show an estimated cost next to each call.
type Price struct {
	InputPerMTok      float64 `yaml:"input_per_mtok" json:"input_per_mtok"`
	OutputPerMTok     float64 `yaml:"output_per_mtok" json:"output_per_mtok"`
	CacheReadPerMTok  float64 `yaml:"cache_read_per_mtok" json:"cache_read_per_mtok,omitempty"`   // default: a tenth of the input price
	CacheWritePerMTok float64 `yaml:"cache_write_per_mtok" json:"cache_write_per_mtok,omitempty"` // default: 1.25 times the input price

	// Above LongInputTokens input tokens in one call, the long prices apply
	// to the whole call. Zero: no separate long-prompt prices.
	LongInputTokens   int64   `yaml:"long_input_tokens" json:"long_input_tokens,omitempty"`
	LongInputPerMTok  float64 `yaml:"long_input_per_mtok" json:"long_input_per_mtok,omitempty"`
	LongOutputPerMTok float64 `yaml:"long_output_per_mtok" json:"long_output_per_mtok,omitempty"`
}

// builtinPrices are Anthropic's list prices for its current models
// (November 2026), used when an anthropic model has no price configured.
var builtinPrices = map[string]Price{
	"claude-opus-5-5":   {InputPerMTok: 4, OutputPerMTok: 20, CacheReadPerMTok: 0.20},
	"claude-sonnet-5-5": {InputPerMTok: 2, OutputPerMTok: 10, CacheReadPerMTok: 0.10},
	"claude-haiku-5-5": {InputPerMTok: 0.10, OutputPerMTok: 0.50,
		LongInputTokens: 100000, LongInputPerMTok: 0.50, LongOutputPerMTok: 2.50},
}

// Cost estimates what a call with this usage cost, in US dollars.
func (p Price) Cost(u Usage) float64 {
	in, out := p.InputPerMTok, p.OutputPerMTok
	if p.LongInputTokens > 0 && u.InputTokens+u.CacheReadTokens+u.CacheWriteTokens > p.LongInputTokens {
		in, out = p.LongInputPerMTok, p.LongOutputPerMTok
	}
	read, write := p.CacheReadPerMTok, p.CacheWritePerMTok
	if read == 0 {
		read = in / 10
	}
	if write == 0 {
		write = in * 1.25
	}
	return (float64(u.InputTokens)*in + float64(u.OutputTokens)*out +
		float64(u.CacheReadTokens)*read + float64(u.CacheWriteTokens)*write) / 1e6
}

var (
	nameRE    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	headerRE  = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
	effortSet = []string{"low", "medium", "high", "xhigh", "max"}
)

// Allows reports whether the project may use the model. A model with no
// project patterns is usable by none: the operator opts projects in.
func (m Model) Allows(project string) bool {
	for _, p := range m.Projects {
		if ok, _ := path.Match(p, project); ok {
			return true
		}
	}
	return false
}

// SupportsTemperature reports whether a step may set a temperature. The
// Anthropic Messages API (version 2026-10-21) rejects sampling settings on
// every model, so only openai models take one.
func (m Model) SupportsTemperature() bool { return m.Provider != "anthropic" }

// CallTimeout is how long one call, retries included, may take.
func (m Model) CallTimeout() time.Duration {
	if d, err := time.ParseDuration(m.Timeout); err == nil && d > 0 {
		return d
	}
	return DefaultTimeout
}

// Registry is the operator's models, checked and with defaults filled in.
type Registry struct {
	models []Model
	byName map[string]int
}

// NewRegistry checks the models and fills in their defaults. Every error
// names the model it is about.
func NewRegistry(models []Model) (*Registry, error) {
	r := &Registry{byName: map[string]int{}}
	for i, m := range models {
		label := m.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		if _, dup := r.byName[m.Name]; dup {
			return nil, fmt.Errorf("ai model %s: the name is used twice", label)
		}
		if err := m.normalize(); err != nil {
			return nil, fmt.Errorf("ai model %s: %w", label, err)
		}
		r.byName[m.Name] = len(r.models)
		r.models = append(r.models, m)
	}
	return r, nil
}

// Get returns the named model.
func (r *Registry) Get(name string) (Model, bool) {
	if r == nil {
		return Model{}, false
	}
	i, ok := r.byName[name]
	if !ok {
		return Model{}, false
	}
	return r.models[i].clone(), true
}

// List returns every model, in the order the operator wrote them.
func (r *Registry) List() []Model {
	if r == nil {
		return nil
	}
	out := make([]Model, len(r.models))
	for i, m := range r.models {
		out[i] = m.clone()
	}
	return out
}

func (m Model) clone() Model {
	if m.Key != nil {
		k := *m.Key
		m.Key = &k
	}
	if m.Price != nil {
		p := *m.Price
		m.Price = &p
	}
	if m.Stream != nil {
		s := *m.Stream
		m.Stream = &s
	}
	m.Projects = append([]string(nil), m.Projects...)
	if m.Headers != nil {
		h := make(map[string]string, len(m.Headers))
		for k, v := range m.Headers {
			h[k] = v
		}
		m.Headers = h
	}
	return m
}

// normalize checks m and fills in its defaults.
func (m *Model) normalize() error {
	if !nameRE.MatchString(m.Name) {
		return fmt.Errorf("name %q must start with a letter and use only a-z, 0-9 and -, at most 40 characters", m.Name)
	}
	p, ok := providers[m.Provider]
	if !ok {
		return fmt.Errorf("provider %q is not known; use anthropic, or openai for any OpenAI-compatible server (Ollama, vLLM, LM Studio and others)", m.Provider)
	}
	if m.Model == "" || strings.ContainsFunc(m.Model, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("model %q must be the provider's model id, without spaces", m.Model)
	}
	if m.Endpoint == "" && m.Provider == "anthropic" {
		m.Endpoint = DefaultAnthropicEndpoint
	}
	if m.Endpoint == "" {
		return fmt.Errorf("endpoint is required for provider %s, for example http://ollama.ai.svc:11434/v1", m.Provider)
	}
	u, err := url.Parse(m.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("endpoint %q must be an http or https URL without a query", m.Endpoint)
	}
	if u.User != nil {
		return fmt.Errorf("endpoint must not carry a user or password; put the key in key:")
	}
	m.Endpoint = strings.TrimRight(m.Endpoint, "/")
	if m.Key != nil && (m.Key.Store == "" || m.Key.Key == "") {
		return fmt.Errorf("key must name a store and a key, such as {store: vault, key: ai/anthropic, field: key}")
	}
	if m.Key == nil && u.Host == "api.anthropic.com" {
		return fmt.Errorf("key is required for %s: name the secret that holds the API key", u.Host)
	}
	for _, pat := range m.Projects {
		if _, err := path.Match(pat, ""); err != nil || pat == "" {
			return fmt.Errorf("projects: %q is not a valid pattern", pat)
		}
	}
	switch {
	case m.MaxTokens == 0:
		m.MaxTokens = DefaultMaxTokens
	case m.MaxTokens < 0 || m.MaxTokens > MaxMaxTokens:
		return fmt.Errorf("max_tokens %d must be between 1 and %d", m.MaxTokens, MaxMaxTokens)
	}
	switch {
	case m.MaxInputBytes == 0:
		m.MaxInputBytes = DefaultMaxInputBytes
	case m.MaxInputBytes < 0 || m.MaxInputBytes > MaxMaxInputBytes:
		return fmt.Errorf("max_input_bytes %d must be between 1 and %d", m.MaxInputBytes, MaxMaxInputBytes)
	}
	switch {
	case m.CallsPerRun == 0:
		m.CallsPerRun = DefaultCallsPerRun
	case m.CallsPerRun < 0 || m.CallsPerRun > MaxCallsPerRun:
		return fmt.Errorf("calls_per_run %d must be between 1 and %d", m.CallsPerRun, MaxCallsPerRun)
	}
	if m.TokensPerDay < 0 {
		return fmt.Errorf("tokens_per_day must not be negative (0 means no limit)")
	}
	if m.Timeout == "" {
		m.Timeout = DefaultTimeout.String()
	}
	d, err := time.ParseDuration(m.Timeout)
	if err != nil || d <= 0 || d > MaxTimeout {
		return fmt.Errorf("timeout %q must be a duration such as 90s or 5m, at most %s", m.Timeout, MaxTimeout)
	}
	m.Timeout = d.String()
	if pr := m.Price; pr != nil {
		for _, v := range []float64{pr.InputPerMTok, pr.OutputPerMTok, pr.CacheReadPerMTok, pr.CacheWritePerMTok, pr.LongInputPerMTok, pr.LongOutputPerMTok} {
			if v < 0 {
				return fmt.Errorf("price must not be negative")
			}
		}
		if pr.LongInputTokens < 0 {
			return fmt.Errorf("price long_input_tokens must not be negative")
		}
	} else if bp, ok := builtinPrices[m.Model]; ok && m.Provider == "anthropic" {
		m.Price = &bp
	}
	for name, v := range m.Headers {
		if !headerRE.MatchString(name) {
			return fmt.Errorf("header %q is not a valid header name", name)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return fmt.Errorf("header %s: the value must be one line", name)
		}
		if secretHeader(name) {
			return fmt.Errorf("header %s looks like a credential or is set by Launchway; headers are not secret, so put the key in key: instead", name)
		}
	}
	return p.Check(*m)
}

// secretHeader reports whether a header name carries credentials, or is
// one the providers set themselves.
func secretHeader(name string) bool {
	n := strings.ToLower(name)
	switch http.CanonicalHeaderKey(name) {
	case "Host", "Content-Type", "Content-Length", "Accept", "Anthropic-Version", "Transfer-Encoding", "Connection":
		return true
	}
	for _, w := range []string{"auth", "key", "token", "secret", "cookie", "password", "session"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}
