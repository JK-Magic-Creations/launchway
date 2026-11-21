package api

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // the fake reads the older header, as an older receiver does
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge/webhooks"
)

// fakeGitOps reads a push as a GitOps controller's webhook receiver does when it is given GitHub's
// format: the event is named in X-GitHub-Event; with a secret configured the body must carry an HMAC of it
// in X-Hub-Signature-256 (current libraries) or X-Hub-Signature, sha1 (older ones); the repository is found by
// comparing repository.html_url with each application's repoURL after dropping the scheme, user, ".git"
// and a trailing slash; the branch is the ref's, and it is the default branch when the application follows HEAD.
// It answers what such a receiver answers: 200 with nothing to do, 400 for a bad signature or an unreadable body.
type fakeGitOps struct {
	srv       *httptest.Server
	secret    string
	reads     string // "sha256" or "sha1": which signature header this receiver version reads
	apps      map[string]string
	mu        sync.Mutex
	refreshed []string // application names refreshed
	answers   []int
}

func normURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return strings.ToLower(u.Host) + strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
}

func newFakeGitOps(t *testing.T, reads, secret string, apps map[string]string) *fakeGitOps {
	t.Helper()
	g := &fakeGitOps{secret: secret, reads: reads, apps: apps}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := g.handle(r)
		g.mu.Lock()
		g.answers = append(g.answers, code)
		g.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitOps) handle(r *http.Request) int {
	body, _ := io.ReadAll(r.Body)
	if r.Header.Get("X-GitHub-Event") != "push" {
		return http.StatusOK // an event it does not act on
	}
	if g.secret != "" {
		var want, got string
		switch g.reads {
		case "sha256":
			m := hmac.New(sha256.New, []byte(g.secret))
			m.Write(body)
			want, got = "sha256="+hex.EncodeToString(m.Sum(nil)), r.Header.Get("X-Hub-Signature-256")
		default:
			m := hmac.New(sha1.New, []byte(g.secret))
			m.Write(body)
			want, got = "sha1="+hex.EncodeToString(m.Sum(nil)), r.Header.Get("X-Hub-Signature")
		}
		if got == "" || !hmac.Equal([]byte(got), []byte(want)) {
			return http.StatusBadRequest
		}
	}
	var p struct {
		Ref        string `json:"ref"`
		Before     string `json:"before"`
		After      string `json:"after"`
		Repository struct {
			HTMLURL       string `json:"html_url"`
			CloneURL      string `json:"clone_url"`
			SSHURL        string `json:"ssh_url"`
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil || p.Repository.HTMLURL == "" || !strings.HasPrefix(p.Ref, "refs/heads/") {
		return http.StatusBadRequest
	}
	if strings.TrimPrefix(p.Ref, "refs/heads/") != p.Repository.DefaultBranch {
		return http.StatusOK // an application following HEAD is not touched by another branch
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for name, repoURL := range g.apps {
		if normURL(repoURL) == normURL(p.Repository.HTMLURL) {
			g.refreshed = append(g.refreshed, name)
		}
	}
	return http.StatusOK
}

func (g *fakeGitOps) did() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.refreshed...)
}

// A push to a hosted repository, delivered to a GitOps controller's receiver, refreshes the applications that read
// that repository, whichever signature header the receiver's version reads; a wrong secret is refused; and the
// address the payload carries is the one the applications must use.
func TestPushWebhookRefreshesApplicationsOfAGitOpsReceiver(t *testing.T) {
	e, h := whEnv(t, webhooks.Options{})
	hostedRepo(t, e, "acme/gitops")
	ada := e.signIn(t, "ada")
	apps := map[string]string{
		"shop":      e.srv.URL + "/git/acme/gitops.git", // the address Clone.HTTPS gives: read by the payload's html_url
		"shop-copy": e.srv.URL + "/git/acme/gitops",     // without .git: the receiver normalizes
		"elsewhere": e.srv.URL + "/git/acme/other.git",  // another repository: untouched
	}
	current := newFakeGitOps(t, "sha256", "argo-secret", apps)
	older := newFakeGitOps(t, "sha1", "argo-secret", apps)
	wrong := newFakeGitOps(t, "sha256", "a-different-secret", apps)
	base := e.srv.URL + "/api/v1/repositories/acme/gitops/hooks"
	for _, g := range []*fakeGitOps{current, older, wrong} {
		whAdd(t, ada, base, `{"name":"argo","url":"`+g.srv.URL+`/api/webhook","events":["push"],"secret":"argo-secret"}`)
	}

	tok := e.tokenFor(t, "ada", "t")
	src := work(t)
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/acme/gitops.git", tok), "HEAD:refs/heads/main")
	whUntil(t, h, "the three receivers to be told", func() bool {
		current.mu.Lock()
		defer current.mu.Unlock()
		older.mu.Lock()
		defer older.mu.Unlock()
		wrong.mu.Lock()
		defer wrong.mu.Unlock()
		return len(current.answers) > 0 && len(older.answers) > 0 && len(wrong.answers) > 0
	})
	for name, g := range map[string]*fakeGitOps{"a receiver that reads X-Hub-Signature-256": current, "a receiver that reads X-Hub-Signature (sha1)": older} {
		got := strings.Join(sorted(g.did()), ",")
		if got != "shop,shop-copy" {
			t.Errorf("%s refreshed %q, want shop and shop-copy only (answers %v)", name, got, g.answers)
		}
	}
	if len(wrong.did()) != 0 || wrong.answers[0] != http.StatusBadRequest {
		t.Errorf("a receiver with another secret acted: %v %v", wrong.did(), wrong.answers)
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
