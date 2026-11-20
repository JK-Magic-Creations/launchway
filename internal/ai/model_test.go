package ai

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func TestRegistryDefaults(t *testing.T) {
	r, err := NewRegistry([]Model{
		{Name: "claude", Provider: "anthropic", Model: "claude-haiku-5-5", Key: &pipeline.SecretRef{Store: "vault", Key: "ai/anthropic", Field: "key"}, Projects: []string{"web", "team-*"}},
		{Name: "local", Provider: "openai", Endpoint: "http://ollama.ai.svc:11434/v1/", Model: "qwen3:8b", Timeout: "90s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := r.Get("claude")
	if !ok || c.Endpoint != DefaultAnthropicEndpoint || c.MaxTokens != 4096 || c.MaxInputBytes != 256<<10 ||
		c.CallsPerRun != 20 || c.Timeout != "2m0s" || c.Price == nil || c.Price.InputPerMTok != 0.10 {
		t.Errorf("claude %+v", c)
	}
	if !c.Allows("web") || !c.Allows("team-a") || c.Allows("other") {
		t.Error("Allows")
	}
	l, _ := r.Get("local")
	if l.Endpoint != "http://ollama.ai.svc:11434/v1" || l.Price != nil || l.Allows("web") || l.CallTimeout().String() != "1m30s" {
		t.Errorf("local %+v", l)
	}
	if len(r.List()) != 2 || r.List()[0].Name != "claude" {
		t.Error("List")
	}
	// Get returns copies.
	c.Projects[0] = "changed"
	if again, _ := r.Get("claude"); again.Projects[0] != "web" {
		t.Error("Get shares the registry's slices")
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("Get found a model that does not exist")
	}
}

func TestHaikuLongPrice(t *testing.T) {
	p := builtinPrices["claude-haiku-5-5"]
	short := p.Cost(Usage{InputTokens: 100000, OutputTokens: 1000000})
	long := p.Cost(Usage{InputTokens: 100001, OutputTokens: 1000000})
	if abs(short-(0.01+0.50)) > 1e-9 || abs(long-(0.0500005+2.50)) > 1e-9 {
		t.Errorf("short %v long %v", short, long)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestRegistryErrors(t *testing.T) {
	key := &pipeline.SecretRef{Store: "vault", Key: "ai/x"}
	base := func(edit func(*Model)) Model {
		m := Model{Name: "m", Provider: "openai", Endpoint: "http://h:1/v1", Model: "x"}
		edit(&m)
		return m
	}
	cases := []struct {
		m    Model
		want string
	}{
		{base(func(m *Model) { m.Name = "Bad_Name" }), `ai model Bad_Name: name "Bad_Name" must start with a letter`},
		{base(func(m *Model) { m.Provider = "gemini" }), `ai model m: provider "gemini" is not known`},
		{base(func(m *Model) { m.Endpoint = "" }), "endpoint is required for provider openai"},
		{base(func(m *Model) { m.Endpoint = "ftp://h" }), "must be an http or https URL"},
		{base(func(m *Model) { m.Endpoint = "http://u:p@h/v1" }), "must not carry a user or password"},
		{base(func(m *Model) { m.Model = "" }), "must be the provider's model id"},
		{base(func(m *Model) { m.Provider = "anthropic"; m.Endpoint = "" }), "key is required for api.anthropic.com"},
		{base(func(m *Model) { m.Key = &pipeline.SecretRef{Store: "vault"} }), "key must name a store and a key"},
		{base(func(m *Model) { m.Projects = []string{"[a"} }), `projects: "[a" is not a valid pattern`},
		{base(func(m *Model) { m.MaxTokens = 200000 }), "max_tokens 200000 must be between 1 and 128000"},
		{base(func(m *Model) { m.MaxInputBytes = -1 }), "max_input_bytes -1"},
		{base(func(m *Model) { m.CallsPerRun = -2 }), "calls_per_run -2"},
		{base(func(m *Model) { m.TokensPerDay = -1 }), "tokens_per_day must not be negative"},
		{base(func(m *Model) { m.Timeout = "1h" }), `timeout "1h" must be a duration`},
		{base(func(m *Model) { m.Timeout = "soon" }), `timeout "soon"`},
		{base(func(m *Model) { m.Price = &Price{InputPerMTok: -1} }), "price must not be negative"},
		{base(func(m *Model) { m.Headers = map[string]string{"Authorization": "Bearer x"} }), "looks like a credential"},
		{base(func(m *Model) { m.Headers = map[string]string{"X-Api-Key": "x"} }), "looks like a credential"},
		{base(func(m *Model) { m.Headers = map[string]string{"X-Note": "a\nb"} }), "must be one line"},
		{base(func(m *Model) { m.Headers = map[string]string{"bad header": "x"} }), "not a valid header name"},
		{base(func(m *Model) { m.Effort = "high" }), "effort is only for anthropic models"},
		{base(func(m *Model) { m.MaxTokensField = "tokens" }), `max_tokens_field "tokens"`},
		{base(func(m *Model) { m.SchemaMode = "grammar" }), `schema_mode "grammar"`},
		{base(func(m *Model) { m.Provider = "anthropic"; m.Key = key; m.Effort = "extreme" }), `effort "extreme" must be one of`},
		{base(func(m *Model) { m.Provider = "anthropic"; m.Key = key; m.MaxTokensField = "max_tokens" }), "max_tokens_field is only for openai models"},
	}
	for _, c := range cases {
		_, err := NewRegistry([]Model{c.m})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: error %v, want %q", c.m, err, c.want)
		}
	}
	m := base(func(*Model) {})
	if _, err := NewRegistry([]Model{m, m}); err == nil || !strings.Contains(err.Error(), "ai model m: the name is used twice") {
		t.Errorf("duplicate: %v", err)
	}
}
