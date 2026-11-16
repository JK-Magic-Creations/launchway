package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadText(t *testing.T, text string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "launchway.yaml")
	os.WriteFile(path, []byte(text), 0o644)
	return Load(path)
}

func TestTriggerSettings(t *testing.T) {
	c, err := loadText(t, `
loops: { max_members: 100 }
schedules: { enabled: false }
auth: { tokens: { max_days: 30 } }
projects:
  - name: a
    repo: /tmp/a
    schedules: { enabled: true }
    git_webhook: { kind: gitlab, secret_file: hook-secret }
    webhooks:
      deploy: { hmac_secret_env: DEPLOY_HMAC }
      ping: { token_file: secrets/ping }
  - name: b
    repo: /tmp/b
`)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := c.LoopLimit(); n != 100 {
		t.Fatalf("loop limit %d", n)
	}
	if n, _ := c.TokenMaxDays(); n != 30 {
		t.Fatalf("token days %d", n)
	}
	ps, err := c.SourceProjects()
	if err != nil {
		t.Fatal(err)
	}
	a, b := ps[0], ps[1]
	if a.SchedulesOff || !b.SchedulesOff {
		t.Fatalf("schedules: a off %v, b off %v", a.SchedulesOff, b.SchedulesOff)
	}
	if a.Hook == nil || a.Hook.Kind != "gitlab" || !filepath.IsAbs(a.Hook.Secret.File) {
		t.Fatalf("hook: %+v", a.Hook)
	}
	if a.Triggers["deploy"].HMAC.Env != "DEPLOY_HMAC" || !a.Triggers["deploy"].UsesHMAC() || !filepath.IsAbs(a.Triggers["ping"].Token.File) || a.Triggers["ping"].UsesHMAC() {
		t.Fatalf("triggers: %+v %+v", a.Triggers["deploy"], a.Triggers["ping"])
	}
	// Without the settings: the defaults, and no hooks.
	c, err = loadText(t, "projects: [ { name: a, repo: /tmp/a } ]\n")
	if err != nil {
		t.Fatal(err)
	}
	ps, _ = c.SourceProjects()
	if n, _ := c.LoopLimit(); n != 0 || ps[0].SchedulesOff || ps[0].Hook != nil || ps[0].Triggers != nil {
		t.Fatalf("defaults: %d %+v", n, ps[0])
	}
}

func TestTriggerSettingsRefused(t *testing.T) {
	for want, text := range map[string]string{
		"loops.max_members":      "loops: { max_members: 501 }\n",
		"auth.tokens.max_days":   "auth: { tokens: { max_days: 4000 } }\n",
		"git_webhook.kind":       "projects: [ { name: a, repo: /x, git_webhook: { kind: bitbucket, secret_env: X } } ]\n",
		"exactly one of secret":  "projects: [ { name: a, repo: /x, git_webhook: { kind: github } } ]\n",
		"exactly one of token":   "projects: [ { name: a, repo: /x, webhooks: { d: { token_env: A, hmac_secret_env: B } } } ]\n",
		"not a webhook name":     "projects: [ { name: a, repo: /x, webhooks: { Bad: { token_env: A } } } ]\n",
		"field secret not found": "projects: [ { name: a, repo: /x, git_webhook: { kind: github, secret: inline } } ]\n",
	} {
		_, err := loadText(t, text)
		if err == nil {
			t.Errorf("%s: accepted", want)
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(want, "field "), " not found")
		if !strings.Contains(err.Error(), key) && !strings.Contains(err.Error(), strings.Fields(want)[0]) {
			t.Errorf("%s: %v", want, err)
		}
	}
}

// With the chart's default values, the configuration has none of the
// settings of schema 13 (so the release before can read it); with them set,
// the server's file loads and says what was set.
func TestChartTriggerSettings(t *testing.T) {
	def := renderChart(t, "postgresql: { enabled: true }\n")
	for _, key := range []string{"loops:", "schedules:", "tokens:", "max_members", "max_days"} {
		if strings.Contains(def["server"], key) {
			t.Errorf("the default server configuration has %s:\n%s", key, def["server"])
		}
	}
	files := renderChart(t, `postgresql: { enabled: true }
config:
  loops: { maxMembers: 100 }
  schedules: { enabled: false }
  tokens: { maxDays: 30 }
  extraConfig: { auth: { mode: none, allow_anonymous: true } }
`)
	c, err := Load(writeTemp(t, files["server"]))
	if err != nil {
		t.Fatalf("server: %v\n%s", err, files["server"])
	}
	if n, _ := c.LoopLimit(); n != 100 || c.Schedules.Enabled == nil || *c.Schedules.Enabled || c.Auth.Tokens.MaxDays != 30 || c.Auth.Mode != "none" {
		t.Fatalf("rendered: %+v\n%s", c, files["server"])
	}
}
