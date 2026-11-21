package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func cfg() Config {
	ref := pipeline.SecretRef{Store: "vault", Key: "k", Field: "url"}
	return Config{
		Channels: []Channel{{Name: "hub", Kind: Webhook, URL: ref, Secret: &ref}, {Name: "chat", Kind: Slack, URL: ref}},
		Rules: []Rule{
			{On: []string{ApprovalRequested, RunFailed}, To: []string{"hub"}},
			{On: []string{RunFailed}, Projects: []string{"mcp-*"}, Branches: []string{"main"}, To: []string{"chat", "hub"}},
		},
	}
}

func TestRulesChooseChannels(t *testing.T) {
	c := cfg()
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	names := func(e Event) string {
		var n []string
		for _, ch := range c.Targets(e) {
			n = append(n, ch.Name)
		}
		return strings.Join(n, ",")
	}
	for e, want := range map[Event]string{
		{Kind: ApprovalRequested, Project: "shop"}:                "hub",
		{Kind: RunFailed, Project: "mcp-weather", Branch: "main"}: "hub,chat",
		{Kind: RunFailed, Project: "mcp-weather", Branch: "dev"}:  "hub",
		{Kind: DeploymentFailed, Project: "shop"}:                 "",
	} {
		if got := names(e); got != want {
			t.Errorf("%+v: %q, want %q", e, got, want)
		}
	}
}

func TestConfigMistakesAreSaidPlainly(t *testing.T) {
	ref := pipeline.SecretRef{Store: "vault", Key: "k"}
	for want, c := range map[string]Config{
		"a reference to a secret store":   {Channels: []Channel{{Name: "a", Kind: Webhook}}},
		"is not one of webhook":           {Channels: []Channel{{Name: "a", Kind: "pager", URL: ref}}},
		"used twice":                      {Channels: []Channel{{Name: "a", Kind: Webhook, URL: ref}, {Name: "a", Kind: Slack, URL: ref}}},
		"slack channels are not signed":   {Channels: []Channel{{Name: "a", Kind: Slack, URL: ref, Secret: &ref}}},
		"there is no channel \"nope\"":    {Channels: []Channel{{Name: "a", Kind: Webhook, URL: ref}}, Rules: []Rule{{On: []string{RunFailed}, To: []string{"nope"}}}},
		"is not an event":                 {Channels: []Channel{{Name: "a", Kind: Webhook, URL: ref}}, Rules: []Rule{{On: []string{"run.done"}, To: []string{"a"}}}},
		"after is a duration of at least": {Channels: []Channel{{Name: "a", Kind: Webhook, URL: ref}}, Rules: []Rule{{On: []string{StepWaitingLong}, After: "5s", To: []string{"a"}}}},
	} {
		if err := c.Check(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}

func TestBodiesAndRetries(t *testing.T) {
	e := Event{Kind: RunFailed, Project: "shop", Title: "Run #4 of shop failed", Detail: "Open the run.", Link: "https://ci/runs/1"}
	for kind, want := range map[string]string{
		Slack:   `{"text":"Run #4 of shop failed\nOpen the run.\nhttps://ci/runs/1"}`,
		Discord: `{"content":"Run #4 of shop failed\nOpen the run.\nhttps://ci/runs/1"}`,
	} {
		b, _ := e.Body(kind)
		if string(b) != want {
			t.Errorf("%s: %s", kind, b)
		}
	}
	b, _ := e.Body(Webhook)
	if !strings.Contains(string(b), `"event":"run.failed"`) || !strings.Contains(string(b), `"link":"https://ci/runs/1"`) || !strings.Contains(string(b), `"text":"Run #4`) {
		t.Errorf("webhook: %s", b)
	}
	var last time.Duration
	for i := 1; i <= 4; i++ {
		d, ok := RetryAfter(i)
		if !ok || d <= last {
			t.Fatalf("attempt %d: %v %v", i, d, ok)
		}
		last = d
	}
	if _, ok := RetryAfter(5); ok {
		t.Fatal("a fifth retry")
	}
}
