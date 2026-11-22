package config

import (
	"strings"
	"testing"
)

// The mail settings are values of the chart: the rendered file loads, says what was set and nothing else, the default
// writes none, and a credential written as a value is refused.
func TestChartMailSettings(t *testing.T) {
	def := renderChart(t, "postgresql: { enabled: true }\n")
	if strings.Contains(def["server"], "mail:") {
		t.Errorf("the default configuration has a mail section:\n%s", def["server"])
	}
	files := renderChart(t, `postgresql: { enabled: true }
config:
  mail:
    host: smtp.example.com
    from: "Launchway <notify@example.com>"
    publicUrl: https://launchway.example.com
    ratePerMinute: 10
    username: { store: vault, key: mail/launchway, field: user }
    password: { store: vault, key: mail/launchway, field: password }
ai:
  serverSecretStores:
    vault: { type: vault, address: https://vault.example:8200, role: launchway-server }
`)
	c, err := Load(writeTemp(t, files["server"]))
	if err != nil {
		t.Fatalf("server: %v\n%s", err, files["server"])
	}
	m := c.Mail
	if m.Host != "smtp.example.com" || m.PublicURL != "https://launchway.example.com" || m.RatePerMinute != 10 || m.Username == nil || m.Password == nil || m.Password.Field != "password" || m.IncludePrivateDetails {
		t.Fatalf("mail: %+v", m)
	}
	if !m.Enabled() || m.Sender().Check() != nil {
		t.Fatalf("the settings do not make a sender: %v", m.Sender().Check())
	}
	out, failed := helmOut(t, `config:
  mail:
    host: smtp.example.com
    from: "x <x@example.com>"
    password: "hunter2"
`)
	if !failed || strings.Contains(out, "hunter2") && !strings.Contains(out, "password") {
		t.Fatalf("a password written as a value must be refused:\n%s", out)
	}
}

func TestMailSettingsAreChecked(t *testing.T) {
	for name, m := range map[string]MailConfig{
		"no host":         {From: "a <a@example.com>"},
		"no from":         {Host: "smtp.example.com"},
		"bad security":    {Host: "smtp.example.com", From: "a <a@example.com>", Security: "ssl"},
		"plain unvouched": {Host: "smtp.example.com", From: "a <a@example.com>", Security: "none"},
		"a url as host":   {Host: "https://smtp.example.com", From: "a <a@example.com>"},
		"bad public url":  {Host: "smtp.example.com", From: "a <a@example.com>", PublicURL: "launchway"},
	} {
		if err := m.Check(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := (MailConfig{Host: "smtp.example.com", From: "a <a@example.com>"}).Check(); err != nil {
		t.Errorf("the plainest settings are refused: %v", err)
	}
	if err := (MailConfig{}).Check(); err != nil {
		t.Errorf("no mail section is refused: %v", err)
	}
}
