// Package notify is Launchway's built-in notifications: who is told when
// something needs a person or has gone wrong, where, and in what words.
//
// Rules say which events (an approval waits, a run failed, a deployment
// failed or was rolled back, a step has waited too long) go to which
// channels, narrowed by project, environment and branch. A channel is a place
// to send to: a generic signed webhook, which an alert hub can receive, or
// the incoming-webhook address of a chat service (Slack, Discord, Microsoft
// Teams). A channel's address and signing key are only references to a
// secret store; their values are read when a message is sent and are never
// shown, logged or put in a message.
//
// This package is pure: it holds the configuration, the checks, the matching
// and the wording. The engine's notification service (internal/engine) reads
// events, applies the rules and delivers, with a log and retries.
package notify

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Events a rule may name.
const (
	ApprovalRequested  = "approval.requested"
	RunFailed          = "run.failed"
	DeploymentSucceed  = "deployment.succeeded"
	DeploymentFailed   = "deployment.failed"
	DeploymentRolledBk = "deployment.rolled_back"
	RollbackNeedsView  = "rollback.needs_review"
	StepWaitingLong    = "step.waiting_long"
)

// Events lists every event a rule may name.
var Events = []string{ApprovalRequested, RunFailed, DeploymentSucceed, DeploymentFailed, DeploymentRolledBk, RollbackNeedsView, StepWaitingLong}

// Channel kinds.
const (
	Webhook = "webhook" // a JSON body, signed with HMAC-SHA256 when the channel has a secret
	Slack   = "slack"   // a Slack incoming webhook
	Discord = "discord" // a Discord webhook
	Teams   = "teams"   // a Microsoft Teams incoming webhook
)

// Kinds lists every kind of channel.
var Kinds = []string{Webhook, Slack, Discord, Teams}

// Config is the notifications section of the server's configuration.
type Config struct {
	Channels []Channel `yaml:"channels" json:"channels,omitempty"`
	Rules    []Rule    `yaml:"rules" json:"rules,omitempty"`
}

// Channel is a place to send messages to.
type Channel struct {
	Name string `yaml:"name" json:"name"`
	Kind string `yaml:"kind" json:"kind"`
	// URL is where it is sent: a reference to a secret store, because the
	// address of a chat webhook is a credential.
	URL pipeline.SecretRef `yaml:"url" json:"url"`
	// Secret, for a webhook, is the key the body is signed with
	// (X-Launchway-Signature-256, as repository webhooks are); optional.
	Secret *pipeline.SecretRef `yaml:"secret,omitempty" json:"secret,omitempty"`
}

// Rule sends events to channels.
type Rule struct {
	On []string `yaml:"on" json:"on"`
	// Projects, Environments and Branches narrow the rule (patterns such as
	// mcp-*); empty means all. A branch is the branch a run was made from.
	Projects     []string `yaml:"projects,omitempty" json:"projects,omitempty"`
	Environments []string `yaml:"environments,omitempty" json:"environments,omitempty"`
	Branches     []string `yaml:"branches,omitempty" json:"branches,omitempty"`
	To           []string `yaml:"to" json:"to"`
	// After, for step.waiting_long, is how long a step waits before it is
	// reported; default 1h.
	After string `yaml:"after,omitempty" json:"after,omitempty"`
}

// DefaultAfter is how long a step waits before step.waiting_long.
const DefaultAfter = time.Hour

// Wait is the rule's After as a duration.
func (r Rule) Wait() time.Duration {
	if d, err := time.ParseDuration(r.After); err == nil && d > 0 {
		return d
	}
	return DefaultAfter
}

// Check checks the configuration and says, in words, what is wrong.
func (c Config) Check() error {
	names := map[string]bool{}
	for i, ch := range c.Channels {
		at := fmt.Sprintf("notifications.channels[%d] (%s)", i, ch.Name)
		switch {
		case ch.Name == "" || strings.ContainsAny(ch.Name, " /\\") || len(ch.Name) > 64:
			return fmt.Errorf("%s: give the channel a short name without spaces", at)
		case names[ch.Name]:
			return fmt.Errorf("%s: the name is used twice", at)
		case !slices.Contains(Kinds, ch.Kind):
			return fmt.Errorf("%s: kind %q is not one of %s", at, ch.Kind, strings.Join(Kinds, ", "))
		case !pipeline.ValidStoreName(ch.URL.Store) || ch.URL.Key == "":
			return fmt.Errorf("%s: url is a reference to a secret store, such as { store: vault, key: apps/launchway/notify, field: url }: the address of a chat webhook is a credential and is never written in the file", at)
		case ch.Secret != nil && ch.Kind != Webhook:
			return fmt.Errorf("%s: secret signs the body of a webhook; %s channels are not signed", at, ch.Kind)
		case ch.Secret != nil && (!pipeline.ValidStoreName(ch.Secret.Store) || ch.Secret.Key == ""):
			return fmt.Errorf("%s: secret is a reference to a secret store, such as { store: vault, key: apps/launchway/notify, field: signing }", at)
		}
		names[ch.Name] = true
	}
	for i, r := range c.Rules {
		at := fmt.Sprintf("notifications.rules[%d]", i)
		if len(r.On) == 0 || len(r.To) == 0 {
			return fmt.Errorf("%s: say which events (on) go to which channels (to)", at)
		}
		for _, ev := range r.On {
			if !slices.Contains(Events, ev) {
				return fmt.Errorf("%s: %q is not an event; use %s", at, ev, strings.Join(Events, ", "))
			}
		}
		for _, to := range r.To {
			if !names[to] {
				return fmt.Errorf("%s: there is no channel %q (it has: %s)", at, to, strings.Join(slices.Sorted(mapKeys(names)), ", "))
			}
		}
		for _, p := range slices.Concat(r.Projects, r.Environments, r.Branches) {
			if _, err := path.Match(p, ""); err != nil {
				return fmt.Errorf("%s: %q is not a valid pattern", at, p)
			}
		}
		if r.After != "" {
			if d, err := time.ParseDuration(r.After); err != nil || d < time.Minute {
				return fmt.Errorf("%s: after is a duration of at least 1m, such as 2h", at)
			}
		}
	}
	return nil
}

func mapKeys(m map[string]bool) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// Event is something that happened, with what a message about it says.
type Event struct {
	Kind        string `json:"event"`
	Project     string `json:"project"`
	Environment string `json:"environment,omitempty"`
	Branch      string `json:"branch,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	RunNumber   int    `json:"run_number,omitempty"`
	Step        string `json:"step,omitempty"`
	// Key makes one message per event: the same event is never sent twice
	// to the same channel, whichever server notices it.
	Key string `json:"-"`
	// Title is one plain sentence; Detail adds what to do; Link opens the
	// page where it can be done.
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Link   string `json:"link,omitempty"`
	At     int64  `json:"at"`
}

// Matches reports whether a rule sends the event.
func (r Rule) Matches(e Event) bool {
	if !slices.Contains(r.On, e.Kind) {
		return false
	}
	return matchAny(r.Projects, e.Project) && matchAny(r.Environments, e.Environment) && matchAny(r.Branches, e.Branch)
}

func matchAny(patterns []string, v string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, v); ok {
			return true
		}
	}
	return false
}

// Targets returns the channels the configuration sends the event to, once each.
func (c Config) Targets(e Event) []Channel {
	var out []Channel
	seen := map[string]bool{}
	for _, r := range c.Rules {
		if !r.Matches(e) {
			continue
		}
		for _, to := range r.To {
			if seen[to] {
				continue
			}
			seen[to] = true
			for _, ch := range c.Channels {
				if ch.Name == to {
					out = append(out, ch)
				}
			}
		}
	}
	return out
}

// Text is the message in plain words: the title, what to do, and the link.
func (e Event) Text() string {
	var b strings.Builder
	b.WriteString(e.Title)
	if e.Detail != "" {
		b.WriteString("\n" + e.Detail)
	}
	if e.Link != "" {
		b.WriteString("\n" + e.Link)
	}
	return b.String()
}

// Body is what is sent to a channel of the kind.
func (e Event) Body(kind string) ([]byte, error) {
	switch kind {
	case Slack:
		return json.Marshal(map[string]string{"text": e.Text()})
	case Teams:
		return json.Marshal(map[string]string{"text": strings.ReplaceAll(e.Text(), "\n", "\n\n")})
	case Discord:
		return json.Marshal(map[string]string{"content": clip(e.Text(), 2000)})
	}
	return json.Marshal(struct {
		Event
		Text string `json:"text"`
	}{e, e.Text()})
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// RetryAfter is how long to wait after the given number of failed attempts
// before the next, and whether there is one: five attempts over about three
// hours, then the failure stays in the log.
func RetryAfter(attempts int) (time.Duration, bool) {
	steps := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	if attempts < 1 || attempts > len(steps) {
		return 0, false
	}
	return steps[attempts-1], true
}
