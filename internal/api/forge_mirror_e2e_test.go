package api

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

// bareOf makes a bare repository that holds what a working copy has, as an
// external Git server would.
func bareOf(t *testing.T, work string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "up.git")
	mustGit(t, work, "clone", "-q", "--mirror", work, bare)
	return bare
}

func mirrorEnv(t *testing.T) *authEnv {
	t.Helper()
	e, _ := hostedEnvWith(t, envOpts{}, hosting.Options{Guard: &egress.Guard{AllowFiles: true}}, githttp.Options{})
	return e
}

func TestPullMirrorThroughTheAPI(t *testing.T) {
	e := mirrorEnv(t)
	ada, vic, otto := e.signIn(t, "ada"), e.signIn(t, "vic"), e.signIn(t, "otto")
	repos := e.srv.URL + "/api/v1/repositories"
	ada.do("POST", repos, `{"owner":"acme","name":"app"}`)
	src := work(t)
	up := bareOf(t, src)
	base := repos + "/acme/app/mirrors"

	// Setting up needs the repository's admin role.
	body := `{"url":"file://` + up + `","interval_seconds":300}`
	for name, b := range map[string]*browser{"viewer": vic, "operator": otto} {
		if r := b.do("POST", base, body); r.status != 403 {
			t.Errorf("a %s set up a mirror: %d %s", name, r.status, r.body)
		}
	}
	// A credential is a reference: a value in its place is refused, and so is a secret in the address.
	for _, bad := range []string{`{"url":"file://` + up + `","credential":{"env":"hunter2-secret"}}`, `{"url":"https://u:pw@git.example.com/x.git"}`, `{"direction":"sideways","url":"https://git.example.com/x.git"}`} {
		if r := ada.do("POST", base, bad); r.status != 400 || strings.Contains(string(r.body), "hunter2") || strings.Contains(string(r.body), ":pw@") {
			t.Errorf("%.50s: %d %s", bad, r.status, r.body)
		}
	}
	r := ada.do("POST", base, body)
	var v mirror.View
	r.json(t, &v)
	if r.status != 201 || v.Direction != "pull" || v.Repository != "acme/app" || v.State != "pending" || !strings.HasPrefix(v.ID, "mir_") {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	// An operator may run it now; the status then says when and what.
	r = otto.do("POST", base+"/"+v.ID+"/sync", "")
	r.json(t, &v)
	if r.status != 200 || v.State != "ok" || v.Refs < 1 || v.LastSuccess.IsZero() || v.Behind {
		t.Fatalf("sync: %d %s", r.status, r.body)
	}
	if r := vic.do("POST", base+"/"+v.ID+"/sync", ""); r.status != 403 {
		t.Errorf("a viewer synced: %d", r.status)
	}
	var page forge.Page[mirror.View]
	vic.get(base).json(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != v.ID || page.Items[0].State != "ok" {
		t.Errorf("a viewer lists %+v", page.Items)
	}
	var all forge.Page[mirror.View]
	vic.get(e.srv.URL+"/api/v1/mirrors").json(t, &all)
	if len(all.Items) != 1 {
		t.Errorf("all mirrors: %+v", all.Items)
	}

	// The repository takes no pushes while it is a mirror.
	tok := e.tokenFor(t, "ada", "git")
	out, err := git(t, src, "push", withToken(e.srv.URL, "/acme/app.git", tok), "HEAD:refs/heads/other")
	if err == nil || !strings.Contains(out, "403") && !strings.Contains(out, "mirror") {
		t.Fatalf("a push to a mirror: %v\n%s", err, out)
	}
	// It is cloned like any repository, and has what the source has.
	c := t.TempDir()
	mustGit(t, c, "clone", "-q", withToken(e.srv.URL, "/acme/app.git", tok), "c")
	if got := strings.TrimSpace(mustGit(t, filepath.Join(c, "c"), "log", "-1", "--format=%s")); got != "first" {
		t.Errorf("cloned %q", got)
	}

	// Another repository's admin cannot reach this mirror by its ID.
	ada.do("POST", repos, `{"owner":"acme","name":"other"}`)
	if r := ada.do("PATCH", repos+"/acme/other/mirrors/"+v.ID, `{"enabled":false}`); r.status != 404 {
		t.Errorf("a mirror reached through another repository: %d %s", r.status, r.body)
	}
	if r := ada.do("PATCH", base+"/"+v.ID, `{"enabled":false}`); r.status != 200 {
		t.Errorf("pause: %d %s", r.status, r.body)
	}
	if r := ada.do("DELETE", base+"/"+v.ID, ""); r.status != 400 || !strings.Contains(string(r.body), "stop mirroring") {
		t.Errorf("deleting a pull mirror: %d %s", r.status, r.body)
	}

	// Stopping mirroring is explicit, needs the admin role, and is audited.
	stop := repos + "/acme/app/stop-mirroring"
	if r := otto.do("POST", stop, `{}`); r.status != 403 {
		t.Errorf("an operator stopped mirroring: %d", r.status)
	}
	if r := ada.do("POST", stop, `{"final_sync":true}`); r.status != 200 {
		t.Fatalf("stop: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", stop, `{}`); r.status != 409 {
		t.Errorf("stopping twice: %d %s", r.status, r.body)
	}
	commit(t, src, "b.txt", "two\n", "second")
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/acme/app.git", tok), "HEAD:refs/heads/main")

	seen := map[string]string{}
	for _, a := range e.auditEntries(t) {
		if strings.HasPrefix(a.Action, "mirror.") {
			seen[a.Action] += a.Detail + "|" + a.Outcome + "|" + a.User + ";"
		}
	}
	for _, want := range []string{"mirror.create", "mirror.sync", "mirror.update", "mirror.promote"} {
		if seen[want] == "" {
			t.Errorf("no audit entry %s (have %v)", want, seen)
		}
	}
	if !strings.Contains(seen["mirror.promote"], "final sync") || !strings.Contains(seen["mirror.promote"], "ada") {
		t.Errorf("the promote entry: %s", seen["mirror.promote"])
	}
	for _, a := range e.auditEntries(t) {
		if strings.Contains(a.Detail, "hunter2") {
			t.Errorf("a secret in the audit trail: %+v", a)
		}
	}
}

// A push to a hosted repository is copied to its push mirror, asynchronously.
func TestPushMirrorThroughTheAPIFollowsAcceptedPushes(t *testing.T) {
	e := mirrorEnv(t)
	e.api.Forge.Mirrors.Start(t.Context())
	ada := e.signIn(t, "ada")
	repos := e.srv.URL + "/api/v1/repositories"
	ada.do("POST", repos, `{"owner":"acme","name":"app"}`)
	off := filepath.Join(t.TempDir(), "off.git")
	mustGit(t, filepath.Dir(off), "init", "-q", "--bare", off)
	r := ada.do("POST", repos+"/acme/app/mirrors", `{"direction":"push","url":"file://`+off+`"}`)
	var v mirror.View
	r.json(t, &v)
	if r.status != 201 || v.Direction != "push" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	tok := e.tokenFor(t, "ada", "git")
	src := work(t)
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/acme/app.git", tok), "HEAD:refs/heads/main")
	want := strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD"))
	waitForMirror(t, "the push to reach the off-site copy", func() bool {
		out, err := git(t, off, "rev-parse", "refs/heads/main")
		return err == nil && strings.TrimSpace(out) == want
	})
	commit(t, src, "b.txt", "two\n", "second")
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/acme/app.git", tok), "HEAD:refs/heads/main")
	want = strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD"))
	waitForMirror(t, "the second push", func() bool {
		out, err := git(t, off, "rev-parse", "refs/heads/main")
		return err == nil && strings.TrimSpace(out) == want
	})
	var page forge.Page[mirror.View]
	ada.get(repos+"/acme/app/mirrors").json(t, &page)
	if len(page.Items) != 1 || page.Items[0].State != "ok" || page.Items[0].Behind {
		t.Errorf("%+v", page.Items)
	}
	if r := ada.do("DELETE", repos+"/acme/app/mirrors/"+v.ID, ""); r.status != 204 {
		t.Errorf("remove: %d %s", r.status, r.body)
	}
}

func waitForMirror(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}
