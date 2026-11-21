package mirror_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// offsite is an empty bare repository somewhere else.
func offsite(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "off.git")
	runGit(t, filepath.Dir(d), "init", "-q", "--bare", d)
	return d
}

// update makes the hosted repository equal to src through the storage's own
// write path (what a push or a mirror's sync does) and tells the service,
// as the server does after an accepted push.
func (w *world) update(r forge.Repository, src *source) {
	w.t.Helper()
	_, err := w.h.Store.Converge(context.Background(), r.ID, storage.ConvergeOptions{Prefixes: mirror.Managed, Prune: true,
		Fetch: func(ctx context.Context, scratch string) error {
			runGit(w.t, scratch, "fetch", "-q", "--no-tags", src.bare, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*", "+refs/notes/*:refs/notes/*")
			return nil
		}})
	if err != nil {
		w.t.Fatal(err)
	}
	w.h.Mirrors.Pushed(context.Background(), r.ID)
}

func TestPushMirrorCopiesEveryRefToEveryRemoteAndFollowsChanges(t *testing.T) {
	w, s, ctx := newWorld(t, worldOpts{mo: mirror.Options{Tick: 30 * time.Millisecond}}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	w.update(repo, s)
	a, b := offsite(t), offsite(t)
	ma, err := w.h.Mirrors.SetPush(ctx, repo.Path, mirror.Spec{URL: "file://" + a}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	mb, err := w.h.Mirrors.SetPush(ctx, repo.Path, mirror.Spec{URL: "file://" + b}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	// Both are due at once; run them on demand first, then let the scheduler follow changes.
	w.mustSync(ma.ID)
	w.mustSync(mb.ID)
	want := w.hostedRefs(repo)
	for _, dir := range []string{a, b} {
		if got := refsOf(t, dir); !equal(got, want) {
			t.Fatalf("%s\n got: %s\nwant: %s", dir, describe(got), describe(want))
		}
	}
	got, _ := w.h.Mirrors.Get(ctx, ma.ID)
	if got.State != mirror.StateOK || got.Refs != len(want) || !got.DirtySince.IsZero() {
		t.Fatalf("status: %+v", got)
	}

	// Every accepted update goes to every remote on its own, asynchronously: a new
	// commit, a force-push, a deleted branch and tag.
	done := w.h.Mirrors.Start(ctx)
	cctx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); _ = done }()
	_ = cctx
	s.commit("new work")
	s.git("tag", "v9")
	s.git("branch", "-D", "dev")
	s.git("tag", "-d", "v1")
	s.git("push", "-q", "origin", ":refs/heads/dev")
	s.git("push", "-q", "origin", ":refs/tags/v1")
	s.pushAll()
	w.update(repo, s)
	want = w.hostedRefs(repo)
	for _, dir := range []string{a, b} {
		eventually(t, "the push mirror at "+dir, 20*time.Second, func() bool { return equal(refsOf(t, dir), want) })
	}
	if _, ok := refsOf(t, a)["refs/heads/dev"]; ok {
		t.Error("a deleted branch was kept on the remote")
	}
	runGit(t, a, "fsck", "--strict", "--no-dangling")
	// Per-remote status.
	vs, _ := w.h.Mirrors.Views(ctx, repo.Path)
	if len(vs) != 2 || vs[0].State != "ok" || vs[1].State != "ok" {
		t.Errorf("%+v", vs)
	}
}

func TestPushMirrorNeverEmptiesARemoteFromAnEmptyRepository(t *testing.T) {
	w, s, ctx := newWorld(t, worldOpts{}), newSource(t), context.Background()
	populate(s)
	off := offsite(t)
	runGit(t, s.work, "push", "-q", "file://"+off, "--all")
	before := refsOf(t, off)
	repo := w.newRepo("acme/empty")
	m, _ := w.h.Mirrors.SetPush(ctx, repo.Path, mirror.Spec{URL: "file://" + off}, "ada")
	w.mustSync(m.ID)
	if !equal(refsOf(t, off), before) {
		t.Fatal("an empty repository emptied the remote")
	}
}

func TestPushMirrorRetriesWithBackoffReportsLagAndAlertsThenRecovers(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := &now
	w, s, ctx := newWorld(t, worldOpts{mo: mirror.Options{Now: func() time.Time { return *clock }, BehindAfter: 10 * time.Minute, Tick: 20 * time.Millisecond}}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	w.update(repo, s)
	gone := filepath.Join(t.TempDir(), "not-there.git")
	m, _ := w.h.Mirrors.SetPush(ctx, repo.Path, mirror.Spec{URL: "file://" + gone}, "ada")
	got, err := w.h.Mirrors.SyncNow(ctx, m.ID)
	if err == nil || got.Failures != 1 || got.State != mirror.StateError || got.LastError == "" {
		t.Fatalf("%v %+v", err, got)
	}
	if d := got.NextAt.Sub(*clock); d != time.Minute {
		t.Errorf("retry in %v, want 1m", d)
	}
	got, _ = w.h.Mirrors.SyncNow(ctx, m.ID)
	if d := got.NextAt.Sub(*clock); d != 2*time.Minute || got.Failures != 2 {
		t.Errorf("retry in %v after %d failures", d, got.Failures)
	}
	// A push while it is failing does not make it retry at once.
	w.h.Mirrors.Pushed(ctx, repo.ID)
	if again, _ := w.h.Mirrors.Get(ctx, m.ID); !again.NextAt.Equal(got.NextAt) {
		t.Errorf("a change cut the backoff short: %v -> %v", got.NextAt, again.NextAt)
	}
	// Lag grows while it is behind, and passes the threshold.
	*clock = now.Add(30 * time.Minute)
	v := w.h.Mirrors.View(ctx, got)
	if !v.Behind || v.LagSeconds < 30*60 || !strings.Contains(v.LastError, "") {
		t.Fatalf("%+v", v)
	}
	// The alert is an event in the connection's log, raised once.
	cctx, cancel := context.WithCancel(ctx)
	done := w.h.Mirrors.Start(cctx)
	eventually(t, "the behind alert", 10*time.Second, func() bool { return hasEvent(t, w, repo.ID, "mirror.behind") })
	cancel()
	<-done
	// The remote comes back: the next run (due now) catches it up.
	runGit(t, filepath.Dir(gone), "init", "-q", "--bare", gone)
	*clock = clock.Add(2 * time.Hour)
	got, err = w.h.Mirrors.SyncNow(ctx, m.ID)
	if err != nil || got.State != mirror.StateOK || got.Failures != 0 || !got.DirtySince.IsZero() {
		t.Fatalf("%v %+v", err, got)
	}
	if !equal(refsOf(t, gone), w.hostedRefs(repo)) {
		t.Fatal("not caught up")
	}
	cctx2, cancel2 := context.WithCancel(ctx)
	done2 := w.h.Mirrors.Start(cctx2)
	eventually(t, "the recovered event", 10*time.Second, func() bool { return hasEvent(t, w, repo.ID, "mirror.recovered") })
	cancel2()
	<-done2
	if v := w.h.Mirrors.View(ctx, got); v.Behind || v.LagSeconds != 0 {
		t.Errorf("%+v", v)
	}
}

func hasEvent(t *testing.T, w *world, repoID, kind string) bool {
	t.Helper()
	evs, err := w.h.Forge.Events(context.Background(), w.h.Connection.ID, repoID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if string(e.Kind) == kind {
			return true
		}
	}
	return false
}

func TestPushMirrorUsesACredentialReferenceAndNeverShowsIt(t *testing.T) {
	const token = "push-token-8812-secret"
	root := t.TempDir()
	bare := filepath.Join(root, "team", "app.git")
	runGit(t, root, "init", "-q", "--bare", bare)
	runGit(t, bare, "config", "http.receivepack", "true")
	gp, _ := exec.Command("git", "--exec-path").Output()
	h := &cgi.Handler{Path: filepath.Join(strings.TrimSpace(string(gp)), "git-http-backend"),
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, p, ok := r.BasicAuth(); !ok || p != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	allow, _ := egress.New([]string{"127.0.0.1"})
	allow.AllowFiles = true
	w, s, ctx := newWorld(t, worldOpts{guard: allow}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	w.update(repo, s)
	secret := filepath.Join(t.TempDir(), "tok")
	os.WriteFile(secret, []byte("wrong"), 0o600)
	m, _ := w.h.Mirrors.SetPush(ctx, repo.Path, mirror.Spec{URL: srv.URL + "/team/app.git", Username: "bot", Credential: forge.SecretRef{File: secret}}, "ada")
	got, err := w.h.Mirrors.SyncNow(ctx, m.ID)
	if err == nil || !strings.Contains(got.LastError, "401") || strings.Contains(got.LastError+err.Error(), token) {
		t.Fatalf("%v %+v", err, got)
	}
	os.WriteFile(secret, []byte(token), 0o600)
	got, err = w.h.Mirrors.SyncNow(ctx, m.ID)
	if err != nil || got.State != mirror.StateOK {
		t.Fatalf("%v %+v", err, got)
	}
	if !equal(refsOf(t, bare), w.hostedRefs(repo)) {
		t.Fatal("not pushed with the credential")
	}
	if v := w.h.Mirrors.View(ctx, got); v.Credential != "file:"+secret || strings.Contains(v.URL+v.LastError, token) {
		t.Errorf("%+v", v)
	}
}

func TestPushMirrorRefusesInternalAddressesAndBadSetUp(t *testing.T) {
	strict, _ := egress.New(nil)
	w, ctx := newWorld(t, worldOpts{guard: strict}), context.Background()
	repo := w.newRepo("acme/app")
	if _, err := w.h.Mirrors.SetPush(ctx, repo.Path, mirror.Spec{URL: "http://10.0.0.5/x.git"}, "ada"); !errors.Is(err, forge.ErrInvalid) || !strings.Contains(err.Error(), "forge.mirror_allow") {
		t.Fatalf("%v", err)
	}
	if _, err := w.h.Mirrors.SetPush(ctx, repo.Path, mirror.Spec{URL: "https://x:y@git.example.com/x.git"}, "ada"); !errors.Is(err, forge.ErrInvalid) {
		t.Fatalf("%v", err)
	}
	// A push mirror may be removed (a pull mirror may not: it is ended by stopping mirroring).
	allow := newWorld(t, worldOpts{})
	r2 := allow.newRepo("acme/app")
	m, err := allow.h.Mirrors.SetPush(ctx, r2.Path, mirror.Spec{URL: "file://" + offsite(t)}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := allow.h.Mirrors.SetPush(ctx, r2.Path, mirror.Spec{URL: m.URL}, "ada"); !errors.Is(err, forge.ErrExists) {
		t.Errorf("a duplicate: %v", err)
	}
	if _, err := allow.h.Mirrors.Remove(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAPullMirrorsUpdatesGoOnToItsPushMirrors(t *testing.T) {
	w, s, ctx := newWorld(t, worldOpts{mo: mirror.Options{Tick: 30 * time.Millisecond}}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	pm, _ := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: s.url()}, "ada")
	off := offsite(t)
	if _, err := w.h.Mirrors.SetPush(ctx, repo.Path, mirror.Spec{URL: "file://" + off}, "ada"); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	done := w.h.Mirrors.Start(cctx)
	defer func() { cancel(); <-done }()
	w.mustSync(pm.ID)
	eventually(t, "the off-site copy of a mirror", 20*time.Second, func() bool { return equal(refsOf(t, off), refsOf(t, s.bare)) })
}
