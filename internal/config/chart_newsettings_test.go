package config

import (
	"strings"
	"testing"
)

// The settings that the queues, trust, notifications and check-source work made configurable are values of
// the chart, not only keys of a file: the rendered file loads, says what was set and nothing else, and the
// default writes none of them.
func TestChartQueuesTrustAndNotificationSettings(t *testing.T) {
	def := renderChart(t, "postgresql: { enabled: true }\n")
	for _, k := range []string{"queues", "automation", "notifications", "untrusted"} {
		if strings.Contains(def["server"], k) {
			t.Errorf("the default configuration has %s:\n%s", k, def["server"])
		}
	}
	files := renderChart(t, `postgresql: { enabled: true }
config:
  queues: { maxWait: 30m, runs: 5, automationRuns: 0, supersedePullRequests: false }
  automation: { accounts: ["mybot*"] }
  pullRequests: { untrusted: allow }
  notifications:
    channels:
      - { name: ops, kind: slack, url: { store: vault, key: ci/slack, field: url } }
      - { name: hub, kind: webhook, url: { store: vault, key: ci/hub }, secret: { store: vault, key: ci/hub-key } }
    rules:
      - { "on": [approval.requested, deployment.failed], projects: ["*"], to: [ops, hub] }
ai:
  serverSecretStores:
    vault: { type: vault, address: https://vault.example:8200, role: launchway-server }
`)
	c, err := Load(writeTemp(t, files["server"]))
	if err != nil {
		t.Fatalf("server: %v\n%s", err, files["server"])
	}
	if c.Queues.MaxWait != "30m" || c.Queues.Runs == nil || *c.Queues.Runs != 5 || c.Queues.AutomationRuns == nil || *c.Queues.AutomationRuns != 0 ||
		c.Queues.SupersedePullRequests == nil || *c.Queues.SupersedePullRequests {
		t.Fatalf("queues: %+v", c.Queues)
	}
	if got := c.AutomationAccounts(); len(got) != 1 || got[0] != "mybot*" {
		t.Fatalf("automation: %v", got)
	}
	if c.PullRequests.Untrusted != "allow" {
		t.Fatalf("pull_requests: %+v", c.PullRequests)
	}
	if len(c.Notifications.Channels) != 2 || len(c.Notifications.Rules) != 1 || c.Notifications.Rules[0].On[0] != "approval.requested" {
		t.Fatalf("notifications: %+v", c.Notifications)
	}
}

// A channel's address written as a value is refused: it is a credential.
func TestChartRefusesANotificationAddressAsAValue(t *testing.T) {
	out, failed := helmOut(t, `config:
  notifications:
    channels:
      - { name: ops, kind: slack, url: "https://hooks.example.com/services/T000/B000/xyz" }
`)
	if !failed || !strings.Contains(out, "url") {
		t.Fatalf("an address given as a value must be refused:\n%s", out)
	}
}

// A check source is the deploy runner's own list of what a health check may call: it takes hosts and
// namespaces, no url; the chart renders it for the deploy runner, admits egress to exactly those
// namespaces and address ranges, and refuses one with nothing listed.
func TestChartCheckSource(t *testing.T) {
	deploy := `delivery:
  environments: [{ name: prod, target: home, projects: ["*"] }]
  deployRunner:
    enabled: true
    target: home
    applications: [{ projects: ["*"], names: [shop] }]
    repos: { gitops: { url: "https://git.example.com/ops.git" } }
  verification:
    sources:
      - { name: probe, type: check, projects: [shop], namespaces: [shop, shop-canary], hosts: ["10.20.0.0/16", "203.0.113.7"] }
`
	out, failed := helmOut(t, deploy)
	if failed {
		t.Fatalf("a check source must render:\n%s", out)
	}
	for _, want := range []string{"health source probe: Services in the namespace shop", "kubernetes.io/metadata.name: shop-canary", "cidr: 10.20.0.0/16", "cidr: 203.0.113.7/32", "namespaces:", "type: check"} {
		if !strings.Contains(out, want) {
			t.Errorf("the render lacks %q", want)
		}
	}
	if strings.Contains(out, "url: \n") {
		t.Error("a check source has no url")
	}
	if out, failed := helmOut(t, strings.Replace(deploy, `namespaces: [shop, shop-canary], hosts: ["10.20.0.0/16", "203.0.113.7"]`, `namespaces: []`, 1)); !failed || !strings.Contains(out, "needs hosts or namespaces") {
		t.Fatalf("a check source that allows nothing must be refused:\n%s", out)
	}
}

// The default-deny policies admit what the features need by default and nothing more, and an operator adds
// egress with values, not by turning the policy off.
func TestChartServerEgressForTheNewFeatures(t *testing.T) {
	out, failed := helmOut(t, "")
	if failed {
		t.Fatal(out)
	}
	server := policyOf(t, out, "t-launchway-server")
	for _, want := range []string{"cidr: 0.0.0.0/0", "10.0.0.0/8", "169.254.0.0/16", "port: 443"} {
		if !strings.Contains(server, want) {
			t.Errorf("the server policy lacks %q:\n%s", want, server)
		}
	}
	if strings.Contains(server, "::/0") {
		t.Error("the server policy admits IPv6 by default")
	}
	// Off, with one rule added by value: no public egress, the rule is there, the policy is still on.
	out, failed = helmOut(t, `networkPolicy:
  server:
    publicHTTPS: { enabled: false }
    extraEgress:
      - to: [{ ipBlock: { cidr: 10.9.0.0/24 } }]
        ports: [{ protocol: TCP, port: 8443 }]
`)
	if failed {
		t.Fatal(out)
	}
	server = policyOf(t, out, "t-launchway-server")
	if strings.Contains(server, "0.0.0.0/0") || !strings.Contains(server, "cidr: 10.9.0.0/24") || !strings.Contains(server, "policyTypes: [Ingress, Egress]") {
		t.Fatalf("the operator's rule must be added to a policy that stays on:\n%s", server)
	}
}

// policyOf returns the NetworkPolicy document with that name.
func policyOf(t *testing.T, rendered, name string) string {
	t.Helper()
	for _, doc := range strings.Split(rendered, "\n---\n") {
		if strings.Contains(doc, "kind: NetworkPolicy") && strings.Contains(doc, "\n  name: "+name+"\n") {
			return doc
		}
	}
	t.Fatalf("no NetworkPolicy %s in the render", name)
	return ""
}
