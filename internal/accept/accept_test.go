package accept

import (
	"strings"
	"testing"
	"time"
)

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		p, s string
		want bool
	}{
		{"*", "", true}, {"web-*", "web-shop", true}, {"web-*", "api", false},
		{"https://git.example.com/shop/*", "https://git.example.com/shop/a/b.git", true},
		{"registry.example.com/*", "registry.example.com.evil.io/x", false},
		{"go?ang", "golang", true}, {"a*b*c", "aXbYc", true}, {"a*b*c", "aXbY", false},
	} {
		if got := Match(c.p, c.s); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.p, c.s, got)
		}
	}
}

func TestRefusals(t *testing.T) {
	no := false
	c := &Config{Projects: []string{"shop"}, Repositories: []string{"https://git.example.com/*"},
		Images: []string{"docker.io/library/golang@sha256:*", "registry.example.com/*"}, Executors: []string{"podman"},
		Environments: []string{"staging"}, PullRequests: &no, Drafts: &no, MaxTimeout: "1h", EnvDeny: []string{"LD_PRELOAD", "GIT_*"}}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	checks := []struct{ why, want string }{
		{c.Project("other"), "accept.projects"},
		{c.Event("pull_request"), "accept.pull_requests"},
		{c.Event("draft"), "accept.drafts"},
		{c.Event(""), "did not say"},
		{c.Repository("https://evil.example.org/x.git"), "accept.repositories"},
		{c.Image("golang:1.22"), "accept.images"},
		{c.Image("alpine"), "accept.images"},
		{c.Executor("host"), "accept.executors"},
		{c.Environment("prod"), "accept.environments"},
		{c.StepTimeout(2 * time.Hour), "accept.max_timeout"},
		{c.EnvName("GIT_SSH_COMMAND"), "accept.env_deny"},
	}
	for i, ch := range checks {
		if !strings.Contains(ch.why, ch.want) {
			t.Errorf("%d: %q does not mention %s", i, ch.why, ch.want)
		}
	}
	allowed := []string{c.Project("shop"), c.Event("push"), c.Repository("https://git.example.com/shop.git"),
		c.Image("golang@sha256:abc"), c.Image("registry.example.com/team/app:1"), c.Executor("podman"),
		c.Environment("staging"), c.StepTimeout(time.Hour), c.EnvName("PATH")}
	for i, why := range allowed {
		if why != "" {
			t.Errorf("allowed %d refused: %s", i, why)
		}
	}
	d := &Config{ImagesByDigest: true}
	if d.Image("golang:1.22") == "" || d.Image("golang@sha256:abc") != "" {
		t.Error("images_by_digest")
	}
	var none *Config
	if none.Project("x") != "" || none.Image("x") != "" || !none.Empty() {
		t.Error("a nil section refuses")
	}
	if (&Config{MaxTimeout: "soon"}).Check() == nil {
		t.Error("a bad duration is accepted")
	}
}

func TestFullImage(t *testing.T) {
	for in, want := range map[string]string{
		"golang:1.22": "docker.io/library/golang:1.22", "team/app": "docker.io/team/app",
		"registry.example.com/a": "registry.example.com/a", "localhost:5000/a": "localhost:5000/a",
	} {
		if got := FullImage(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}
