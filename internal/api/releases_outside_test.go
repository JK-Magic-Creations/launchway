package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
	"github.com/jk-magic-creations/launchway/internal/source"
)

const outsideToken = "not-a-real-credential-2"

// outsideHost is a local stand-in for an outside Git host's API: the only server these tests talk to.
type outsideHost struct {
	mu       sync.Mutex
	srv      *httptest.Server
	tags     map[string]bool
	releases map[string]map[string]any
	assets   map[string][]byte
	calls    int
	badToken bool
}

func newOutsideHost(t *testing.T) *outsideHost {
	f := &outsideHost{tags: map[string]bool{}, releases: map[string]map[string]any{}, assets: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *outsideHost) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func (f *outsideHost) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.URL.Path, "/repos/") && !strings.HasPrefix(r.URL.Path, "/uploads/") {
		w.WriteHeader(404) // the mirror's own git push, which is not what these tests are about
		return
	}
	f.calls++
	w.Header().Set("Content-Type", "application/json")
	if f.badToken || r.Header.Get("Authorization") != "Bearer "+outsideToken {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"message":"bad credentials %s"}`, outsideToken) // a host that echoes what it was sent
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/repos/acme/app")
	switch {
	case r.Method == "GET" && strings.HasPrefix(p, "/git/ref/tags/"):
		if !f.tags[strings.TrimPrefix(p, "/git/ref/tags/")] {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `{}`)
	case r.Method == "GET" && strings.HasPrefix(p, "/releases/tags/"):
		rel, ok := f.releases[strings.TrimPrefix(p, "/releases/tags/")]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(rel)
	case r.Method == "POST" && p == "/releases":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		tag := in["tag_name"].(string)
		in["id"] = 7
		in["html_url"] = f.srv.URL + "/acme/app/releases/tag/" + tag
		in["upload_url"] = f.srv.URL + "/uploads/acme/app/7/assets{?name,label}"
		in["assets"] = []any{}
		f.releases[tag] = in
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(in)
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/uploads/"):
		b, _ := io.ReadAll(r.Body)
		name := r.URL.Query().Get("name")
		f.assets[name] = b
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"name":%q,"size":%d}`, name, len(b))
	default:
		w.WriteHeader(404)
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

type outsideEnv struct {
	*authEnv
	host *outsideHost
	log  *lockedBuf
	ada  *browser
	vic  *browser
	repo forge.Repository
	seen []string // every answer the API gave, to look for the credential in
}

func (e *outsideEnv) call(t *testing.T, b *browser, method, path, body string) response {
	t.Helper()
	r := b.do(method, e.srv.URL+path, body)
	e.seen = append(e.seen, string(r.body))
	return r
}

func setupOutside(t *testing.T) *outsideEnv {
	t.Helper()
	t.Setenv("OUTSIDE_TEST_TOKEN", outsideToken)
	old := outsideWait
	outsideWait = func(int) time.Duration { return 0 }
	t.Cleanup(func() { outsideWait = old })
	guard, _ := egress.New([]string{"127.0.0.1"})
	guard.AllowFiles = true
	blobs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := hostedEnvWith(t, envOpts{artifacts: &engine.ArtifactOptions{Store: blobs}}, hosting.Options{Guard: guard}, githttp.Options{})
	e := &outsideEnv{authEnv: a, host: newOutsideHost(t), log: &lockedBuf{}}
	a.api.Log = slog.New(slog.NewTextHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.ada, e.vic = a.signIn(t, "ada"), a.signIn(t, "vic")
	r := e.call(t, e.ada, "POST", "/api/v1/repositories", `{"owner":"acme","name":"app"}`)
	r.json(t, &e.repo)
	if r.status != 201 {
		t.Fatalf("create the repository: %d %s", r.status, r.body)
	}
	// The Flight reads the hosted repository; it is configured and never started (nothing polls).
	if err := a.api.Projects.Reconfigure([]source.Project{{Name: "app-ship", URL: e.repo.Clone.HTTPS, Poll: time.Hour}}, nil); err != nil {
		t.Fatal(err)
	}
	return e
}

// addMirror sets up a push mirror whose address and API are the local fake.
func (e *outsideEnv) addMirror(t *testing.T, extra string) mirror.View {
	t.Helper()
	body := `{"direction":"push","url":"` + e.host.srv.URL + `/acme/app.git","credential":{"env":"OUTSIDE_TEST_TOKEN"},"username":"ci"` + extra + `}`
	r := e.call(t, e.ada, "POST", "/api/v1/repositories/acme/app/mirrors", body)
	var v mirror.View
	r.json(t, &v)
	if r.status != 201 {
		t.Fatalf("add a push mirror: %d %s", r.status, r.body)
	}
	return v
}

// publish makes a published release with files, as publishing does (the tag exists), and queues it.
func (e *outsideEnv) publish(t *testing.T, version string, files map[string]string) *engine.Release {
	t.Helper()
	sha := strings.Repeat("ab", 20)
	rel, err := e.eng.CreateRelease(engine.NewRelease{Project: "app-ship", Version: version, Title: "Acme " + version, Notes: "## Added\n- the thing (#4)\n", SHA: sha, Actor: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if _, err := e.eng.AddReleaseFile(context.Background(), rel.ID, name, "", strings.NewReader(body), engine.Provenance{}, "ada"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.eng.PublishRelease(rel.ID, engine.PublishInput{Tag: "v" + version, TagObject: strings.Repeat("cd", 20), TagCreated: true, Actor: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	e.api.queueOutside(context.Background(), got)
	return got
}

func (e *outsideEnv) outsideOf(t *testing.T, rel *engine.Release) []outsideView {
	t.Helper()
	r := e.call(t, e.ada, "GET", "/api/v1/projects/app-ship/releases/"+rel.Version, "")
	var v struct {
		Outside []outsideView `json:"outside"`
	}
	r.json(t, &v)
	return v.Outside
}

func TestAPublishedReleaseIsMadeOnTheOutsideHostAfterItWaitsForTheTag(t *testing.T) {
	e := setupOutside(t)
	m := e.addMirror(t, `,"publish_releases":true,"outside_api":"`+e.host.srv.URL+`"`)
	if !m.PublishReleases || m.OutsideAPI != e.host.srv.URL {
		t.Fatalf("the mirror %+v", m)
	}
	rel := e.publish(t, "1.2.0", map[string]string{"app-linux.tar.gz": "bytes of the archive", "SHA256SUMS": "abc  app-linux.tar.gz\n"})

	// The mirror has not copied the tag: the outside host says so, and the publish waits (pending), creating nothing.
	e.api.advanceOutside(context.Background())
	out := e.outsideOf(t, rel)
	if len(out) != 1 || out[0].State != "pending" || !strings.Contains(out[0].Reason, "waiting for the mirror") || out[0].Attempts != 1 {
		t.Fatalf("after the first try: %+v", out)
	}
	if len(e.host.releases) != 0 {
		t.Fatalf("a release was made for a tag that is not there")
	}

	// The tag arrives; the next try makes the release and uploads the files.
	e.host.mu.Lock()
	e.host.tags["v1.2.0"] = true
	e.host.mu.Unlock()
	e.api.advanceOutside(context.Background())
	out = e.outsideOf(t, rel)
	if len(out) != 1 || out[0].State != "published" || out[0].URL != e.host.srv.URL+"/acme/app/releases/tag/v1.2.0" || out[0].Reason != "" || out[0].Attempts != 2 {
		t.Fatalf("after the second try: %+v", out)
	}
	got := e.host.releases["v1.2.0"]
	if got["tag_name"] != "v1.2.0" || got["name"] != "Acme 1.2.0" || got["body"] != "## Added\n- the thing (#4)\n" || got["prerelease"] != false {
		t.Errorf("the release there: %v", got)
	}
	if string(e.host.assets["app-linux.tar.gz"]) != "bytes of the archive" || string(e.host.assets["SHA256SUMS"]) != "abc  app-linux.tar.gz\n" || len(e.host.assets) != 2 {
		t.Errorf("the files there: %v", e.host.assets)
	}
	// Nothing more is sent for a release that is done.
	n := e.host.count()
	e.api.advanceOutside(context.Background())
	if e.host.count() != n {
		t.Errorf("a published release was sent again")
	}

	// The mirror's status shows it, and every attempt is in the Logbook.
	var page forge.Page[mirror.View]
	r := e.call(t, e.ada, "GET", "/api/v1/mirrors", "")
	r.json(t, &page)
	if len(page.Items) != 1 || len(page.Items[0].Outside) != 1 || page.Items[0].Outside[0].State != "published" || page.Items[0].Outside[0].Version != "app-ship 1.2.0" {
		t.Errorf("the mirror list: %s", r.body)
	}
	var outcomes []string
	for _, a := range e.auditEntries(t) {
		if a.Action == "release.outside_publish" {
			outcomes = append(outcomes, a.Outcome)
			if a.Project != "app-ship" || !strings.Contains(a.Detail, m.ID) {
				t.Errorf("the audit entry %+v", a)
			}
		}
	}
	if strings.Join(outcomes, ",") != "waiting,ok" {
		t.Errorf("audit outcomes %v", outcomes)
	}
}

func TestAFailedOutsidePublishLeavesTheReleaseAndCanBeRetried(t *testing.T) {
	e := setupOutside(t)
	e.addMirror(t, `,"publish_releases":true,"outside_api":"`+e.host.srv.URL+`"`)
	e.host.tags["v2.0.0"] = true
	e.host.badToken = true // the host refuses the credential, and echoes it in its words
	rel := e.publish(t, "2.0.0", map[string]string{"a.bin": "aaa"})

	e.api.advanceOutside(context.Background())
	out := e.outsideOf(t, rel)
	if len(out) != 1 || out[0].State != "failed" || !strings.Contains(out[0].Reason, "may not be allowed to make releases") || strings.Contains(out[0].Reason, outsideToken) {
		t.Fatalf("a failed publish: %+v", out)
	}
	// The Launchway release stands.
	if got, err := e.eng.GetRelease(rel.ID); err != nil || got.State != engine.ReleasePublished {
		t.Fatalf("the release: %+v %v", got, err)
	}
	// A viewer may not retry; nothing is retried while it is not failed; the operator's retry makes it pending.
	path := "/api/v1/projects/app-ship/releases/2.0.0/outside/" + out[0].MirrorID + "/retry"
	if r := e.call(t, e.vic, "POST", path, `{}`); r.status != 403 && r.status != 404 {
		t.Errorf("a viewer retried: %d %s", r.status, r.body)
	}
	e.host.mu.Lock()
	e.host.badToken = false
	e.host.mu.Unlock()
	if r := e.call(t, e.ada, "POST", path, `{}`); r.status != 200 {
		t.Fatalf("retry: %d %s", r.status, r.body)
	}
	if out = e.outsideOf(t, rel); out[0].State != "pending" || out[0].Reason != "" {
		t.Fatalf("after the retry: %+v", out)
	}
	if r := e.call(t, e.ada, "POST", path, `{}`); r.status != 409 {
		t.Errorf("a retry of a pending publish: %d %s", r.status, r.body)
	}
	e.api.advanceOutside(context.Background())
	if out = e.outsideOf(t, rel); out[0].State != "published" || string(e.host.assets["a.bin"]) != "aaa" {
		t.Fatalf("after the second try: %+v %v", out, e.host.assets)
	}
	if r := e.call(t, e.ada, "POST", path, `{}`); r.status != 409 {
		t.Errorf("a retry of a published release: %d %s", r.status, r.body)
	}
	var seen []string
	for _, a := range e.auditEntries(t) {
		if strings.HasPrefix(a.Action, "release.outside_") && (a.Action == "release.outside_publish" || a.Outcome == "ok") {
			seen = append(seen, a.Action+":"+a.Outcome)
		}
	}
	if strings.Join(seen, ",") != "release.outside_publish:failed,release.outside_retry:ok,release.outside_publish:ok" {
		t.Errorf("audit %v", seen)
	}
}

func TestNothingIsSentWhenTheMirrorDoesNotPublishReleases(t *testing.T) {
	e := setupOutside(t)
	off := e.addMirror(t, `,"outside_api":"`+e.host.srv.URL+`"`) // publish_releases is off by default
	if off.PublishReleases {
		t.Fatal("publish_releases is on by default")
	}
	e.host.tags["v1.0.0"] = true
	rel := e.publish(t, "1.0.0", map[string]string{"a.bin": "aaa"})
	e.api.advanceOutside(context.Background())
	if n := e.host.count(); n != 0 || len(e.outsideOf(t, rel)) != 0 {
		t.Fatalf("the host was called %d times for a mirror that does not publish", n)
	}
	// It may not be asked for either, until the setting is on.
	path := "/api/v1/projects/app-ship/releases/1.0.0/outside/" + off.ID + "/retry"
	if r := e.call(t, e.ada, "POST", path, `{}`); r.status != 409 {
		t.Errorf("a retry on a mirror that does not publish: %d %s", r.status, r.body)
	}
	// Turned on afterwards, the same call publishes the release that was made before.
	if r := e.call(t, e.ada, "PATCH", "/api/v1/repositories/acme/app/mirrors/"+off.ID, `{"publish_releases":true}`); r.status != 200 {
		t.Fatalf("turn it on: %d %s", r.status, r.body)
	}
	if r := e.call(t, e.ada, "POST", path, `{}`); r.status != 200 {
		t.Fatalf("queue: %d %s", r.status, r.body)
	}
	// Turned off again before the worker came: it is refused, and nothing is sent.
	if r := e.call(t, e.ada, "PATCH", "/api/v1/repositories/acme/app/mirrors/"+off.ID, `{"publish_releases":false}`); r.status != 200 {
		t.Fatalf("turn it off: %d %s", r.status, r.body)
	}
	e.api.advanceOutside(context.Background())
	out := e.outsideOf(t, rel)
	if len(out) != 1 || out[0].State != "failed" || !strings.Contains(out[0].Reason, "is off") || e.host.count() != 0 {
		t.Fatalf("%+v, calls %d", out, e.host.count())
	}
	// The setting belongs to push mirrors, and an outside address is checked.
	if r := e.call(t, e.ada, "PATCH", "/api/v1/repositories/acme/app/mirrors/"+off.ID, `{"outside_api":"ftp://x"}`); r.status != 400 {
		t.Errorf("a bad outside address: %d %s", r.status, r.body)
	}
	if r := e.call(t, e.ada, "POST", "/api/v1/repositories/acme/app/mirrors", `{"url":"`+e.host.srv.URL+`/x.git","publish_releases":true}`); r.status != 400 {
		t.Errorf("a pull mirror that publishes releases: %d %s", r.status, r.body)
	}
}

func TestTheCredentialIsNowhereButTheServersEnvironment(t *testing.T) {
	e := setupOutside(t)
	e.addMirror(t, `,"publish_releases":true,"outside_api":"`+e.host.srv.URL+`"`)
	e.host.tags["v3.0.0"] = true
	e.host.badToken = true
	rel := e.publish(t, "3.0.0", map[string]string{"a.bin": "aaa"})
	e.api.advanceOutside(context.Background()) // fails, and the host echoes the token
	e.host.mu.Lock()
	e.host.badToken = false
	e.host.mu.Unlock()
	e.outsideOf(t, rel)
	if r := e.call(t, e.ada, "POST", "/api/v1/projects/app-ship/releases/3.0.0/outside/"+e.outsideOf(t, rel)[0].MirrorID+"/retry", `{}`); r.status != 200 {
		t.Fatal(string(r.body))
	}
	e.api.advanceOutside(context.Background())
	e.call(t, e.ada, "GET", "/api/v1/mirrors", "")
	e.call(t, e.ada, "GET", "/api/v1/repositories/acme/app/mirrors", "")
	e.outsideOf(t, rel)

	for _, s := range e.seen {
		if strings.Contains(s, outsideToken) {
			t.Errorf("the credential is in an API answer: %.200s", s)
		}
	}
	if strings.Contains(e.log.String(), outsideToken) {
		t.Errorf("the credential is in the log:\n%s", e.log.String())
	}
	for _, a := range e.auditEntries(t) {
		if strings.Contains(fmt.Sprintf("%+v", a), outsideToken) {
			t.Errorf("the credential is in the audit trail: %+v", a)
		}
	}
	rows, _ := e.eng.ReleaseOutsideOf(rel.ID)
	ms, _ := e.api.Forge.Mirrors.All(context.Background())
	if strings.Contains(fmt.Sprintf("%+v %+v", rows, ms), outsideToken) {
		t.Errorf("the credential is in a stored row: %+v %+v", rows, ms)
	}
	// What is kept of the mirror's credential is where it is read from.
	if v := e.api.Forge.Mirrors.View(context.Background(), ms[0]); v.Credential != "env:OUTSIDE_TEST_TOKEN" && !strings.Contains(v.Credential, "OUTSIDE_TEST_TOKEN") {
		t.Errorf("the credential reference %q", v.Credential)
	}
}
