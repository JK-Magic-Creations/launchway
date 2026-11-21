package mirror_test

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

// An outside code host that does not challenge: a request without the right
// credentials is answered "not found", as such hosts answer for a private
// repository, so a client that waits for a 401 before sending credentials never
// gets in. It wants a fixed user name and the token as the password.
type quietHost struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
	bare     int // requests that carried no credentials at all
	wrong    int
}

func newQuietHost(t *testing.T, root, user, token string) *quietHost {
	t.Helper()
	gp, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{Path: filepath.Join(strings.TrimSpace(string(gp)), "git-http-backend"),
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}}
	h := &quietHost{}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		h.mu.Lock()
		h.requests++
		switch {
		case !ok:
			h.bare++
		case u != user || p != token:
			h.wrong++
		}
		h.mu.Unlock()
		if !ok || u != user || p != token {
			http.NotFound(w, r) // no WWW-Authenticate: nothing invites the client to try again
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(h.Close)
	return h
}

// A push mirror to a host like that sends its credentials with its first
// request, with the user name it was given, and keeps the token out of every
// answer, status and error.
func TestPushMirrorAuthenticatesWithoutWaitingForAChallenge(t *testing.T) {
	const token = "ghp_pushMirrorTokenForTheQuietHost0001"
	root := t.TempDir()
	bare := filepath.Join(root, "team", "app.git")
	runGit(t, root, "init", "-q", "--bare", bare)
	runGit(t, bare, "config", "http.receivepack", "true")
	host := newQuietHost(t, root, "x-access-token", token)
	allow, _ := egress.New([]string{"127.0.0.1"})
	w, s, ctx := newWorld(t, worldOpts{guard: allow}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	w.update(repo, s)
	secret := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(secret, []byte(token+"\n"), 0o600); err != nil { // a trailing newline, as files have
		t.Fatal(err)
	}

	// No user name given: the default user is refused by a host that wants its own, and says what to check.
	m, err := w.h.Mirrors.SetPush(ctx, repo.Path, mirror.Spec{URL: host.URL + "/team/app.git", Credential: forge.SecretRef{File: secret}}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	got, err := w.h.Mirrors.SyncNow(ctx, m.ID)
	if err == nil || !strings.Contains(got.LastError, "404") || strings.Contains(got.LastError+err.Error(), token) {
		t.Fatalf("with the wrong user name: %v %+v", err, got)
	}
	if host.bare != 0 {
		t.Errorf("%d of %d requests carried no credentials: the mirror waited for a challenge", host.bare, host.requests)
	}

	// With the user name the host wants, the push lands.
	if _, err := w.h.Mirrors.Remove(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	m2, err := w.h.Mirrors.SetPush(ctx, repo.Path, mirror.Spec{URL: host.URL + "/team/app.git", Username: "x-access-token", Credential: forge.SecretRef{File: secret}}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	got, err = w.h.Mirrors.SyncNow(ctx, m2.ID)
	if err != nil || got.State != mirror.StateOK {
		t.Fatalf("%v %+v", err, got)
	}
	if !equal(refsOf(t, bare), w.hostedRefs(repo)) {
		t.Fatal("not pushed")
	}
	if host.bare != 0 {
		t.Errorf("%d of %d requests carried no credentials", host.bare, host.requests)
	}
	if v := w.h.Mirrors.View(ctx, got); strings.Contains(v.URL+v.LastError+v.Credential, token) {
		t.Errorf("the token is in the mirror's view: %+v", v)
	}
}
