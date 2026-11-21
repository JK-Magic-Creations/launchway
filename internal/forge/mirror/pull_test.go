package mirror_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

// populate gives a source branches, tags (lightweight and annotated) and notes.
func populate(s *source) {
	s.commit("first")
	s.git("tag", "v1")
	s.commit("second")
	s.git("checkout", "-q", "-b", "dev")
	s.commit("dev work")
	s.git("tag", "-a", "-m", "release two", "v2")
	s.git("notes", "add", "-m", "reviewed", "HEAD")
	s.git("checkout", "-q", "main")
	s.pushAll()
}

func TestPullMirrorCopiesEveryBranchTagAndNoteExactly(t *testing.T) {
	w, s, ctx := newWorld(t, worldOpts{}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	m, err := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: s.url()}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := w.h.Forge.Repository(ctx, repo.ID); !got.Mirror {
		t.Fatal("the repository is not marked as a mirror")
	}
	m = w.mustSync(m.ID)
	want := refsOf(t, s.bare)
	if got := w.hostedRefs(repo); !equal(got, want) {
		t.Fatalf("hosted refs differ from the source\n got: %s\nwant: %s", describe(got), describe(want))
	}
	for _, n := range []string{"refs/heads/main", "refs/heads/dev", "refs/tags/v1", "refs/tags/v2"} {
		if _, ok := want[n]; !ok {
			t.Fatalf("the test source lacks %s", n)
		}
	}
	hasNotes := false
	for n := range want {
		hasNotes = hasNotes || strings.HasPrefix(n, "refs/notes/")
	}
	if !hasNotes {
		t.Fatal("the test source has no notes")
	}
	w.fsck(repo)
	if m.State != mirror.StateOK || m.Failures != 0 || m.LastOK.IsZero() || m.Refs != len(want) || m.LastError != "" {
		t.Fatalf("status after a sync: %+v", m)
	}
	// The default branch follows the source's.
	if got, _ := w.h.Forge.Repository(ctx, repo.ID); got.DefaultBranch != "main" {
		t.Errorf("default branch %q", got.DefaultBranch)
	}
	// A second sync with nothing new changes nothing (the generation stays).
	g1, _ := w.h.Store.Generation(ctx, repo.ID)
	w.mustSync(m.ID)
	if g2, _ := w.h.Store.Generation(ctx, repo.ID); g2 != g1 {
		t.Errorf("an idle sync moved the generation %d -> %d", g1, g2)
	}
}

func TestPullMirrorFollowsNewCommitsForcePushesAndDeletions(t *testing.T) {
	w, s, ctx := newWorld(t, worldOpts{}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	m, _ := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: s.url()}, "ada")
	w.mustSync(m.ID)

	s.commit("third")            // main moves
	s.git("tag", "v3")           // a new tag
	s.git("branch", "-D", "dev") // a branch is deleted ...
	s.git("push", "-q", "origin", ":refs/heads/dev")
	s.git("tag", "-d", "v1") // ... and a tag
	s.git("push", "-q", "origin", ":refs/tags/v1")
	s.git("reset", "-q", "--hard", "HEAD~1") // a force-push rewinds main
	s.commit("rewritten")
	s.pushAll()
	w.mustSync(m.ID)

	want := refsOf(t, s.bare)
	if got := w.hostedRefs(repo); !equal(got, want) {
		t.Fatalf("after changes\n got: %s\nwant: %s", describe(got), describe(want))
	}
	if _, ok := w.hostedRefs(repo)["refs/heads/dev"]; ok {
		t.Error("a deleted branch was kept")
	}
	w.fsck(repo)
}

func TestPullMirrorWithoutPruneKeepsWhatTheSourceLost(t *testing.T) {
	w, s, ctx := newWorld(t, worldOpts{}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	no := false
	m, _ := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: s.url(), Prune: &no}, "ada")
	w.mustSync(m.ID)
	s.git("push", "-q", "origin", ":refs/heads/dev")
	w.mustSync(m.ID)
	if _, ok := w.hostedRefs(repo)["refs/heads/dev"]; !ok {
		t.Fatal("prune is off, but the branch was deleted")
	}
}

func TestPullMirrorRefusesASourceThatSuddenlyHasNothing(t *testing.T) {
	w, s, ctx := newWorld(t, worldOpts{}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	m, _ := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: s.url()}, "ada")
	w.mustSync(m.ID)
	before := w.hostedRefs(repo)
	gen, _ := w.h.Store.Generation(ctx, repo.ID)

	// The upstream is emptied: every branch, tag and note is gone.
	for n := range refsOf(t, s.bare) {
		runGit(t, s.bare, "update-ref", "-d", n)
	}
	got, err := w.h.Mirrors.SyncNow(ctx, m.ID)
	if err == nil || !strings.Contains(err.Error(), "refusing to delete them all") {
		t.Fatalf("an emptied source was followed: %v", err)
	}
	if !equal(w.hostedRefs(repo), before) {
		t.Fatal("refs were deleted though the source was empty")
	}
	if g, _ := w.h.Store.Generation(ctx, repo.ID); g != gen {
		t.Error("the repository changed")
	}
	if got.State != mirror.StateError || got.Failures != 1 || !strings.Contains(got.LastError, "refusing") || !strings.Contains(got.LastError, "primary") {
		t.Errorf("status: %+v", got)
	}
	// A source that has refs again (not empty) is followed, and the error clears.
	s.git("push", "-q", "--force", "origin", "main")
	m = w.mustSync(m.ID)
	if m.State != mirror.StateOK || m.Failures != 0 || m.LastError != "" {
		t.Errorf("after recovery: %+v", m)
	}
	if len(w.hostedRefs(repo)) != 1 {
		t.Errorf("refs: %s", describe(w.hostedRefs(repo)))
	}
}

func TestAMirrorTakesNoPushesAndOnlyAnEmptyRepositoryBecomesOne(t *testing.T) {
	w, s, ctx := newWorld(t, worldOpts{}), newSource(t), context.Background()
	populate(s)
	// A repository that already has content of its own is never made a mirror:
	// whoever controls the address could replace its content.
	own := w.newRepo("acme/own")
	w.mirrorOnceFrom(own, s) // gives it refs of its own through a sync, then it is promoted
	if _, err := w.h.Mirrors.Promote(ctx, own.Path, false, "ada"); err != nil {
		t.Fatal(err)
	}
	before := w.hostedRefs(own)
	hostile := newSource(t)
	hostile.commit("evil")
	hostile.pushAll()
	if _, err := w.h.Mirrors.SetPull(ctx, own.Path, mirror.Spec{URL: hostile.url()}, "mallory"); !errors.Is(err, forge.ErrInvalid) {
		t.Fatalf("a repository with content was made a mirror: %v", err)
	}
	if !equal(w.hostedRefs(own), before) {
		t.Fatal("the repository's refs changed")
	}
	// A mirror row whose repository is not a mirror (promoted under a running
	// sync, or edited in the database) never writes.
	repo := w.newRepo("acme/app")
	m, _ := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: s.url()}, "ada")
	if err := w.h.Forge.SetMirror(ctx, repo.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.h.Mirrors.SyncNow(ctx, m.ID); err == nil || !strings.Contains(err.Error(), "not a mirror") {
		t.Fatalf("a sync wrote to a repository that is not a mirror: %v", err)
	}
	if len(w.hostedRefs(repo)) != 0 {
		t.Fatal("refs were written")
	}
}

// mirrorOnceFrom makes the repository a mirror of s, syncs it once.
func (w *world) mirrorOnceFrom(r forge.Repository, s *source) {
	w.t.Helper()
	m, err := w.h.Mirrors.SetPull(context.Background(), r.Path, mirror.Spec{URL: s.url()}, "ada")
	if err != nil {
		w.t.Fatal(err)
	}
	w.mustSync(m.ID)
}

func TestPromoteStopsMirroringAndMakesTheRepositoryThePrimary(t *testing.T) {
	w, s, ctx := newWorld(t, worldOpts{}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	m, _ := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: s.url()}, "ada")
	w.mustSync(m.ID)
	// A final sync first picks up what the old primary gained.
	s.commit("last before the cut-over")
	s.pushAll()
	if _, err := w.h.Mirrors.Promote(ctx, repo.Path, true, "ada"); err != nil {
		t.Fatal(err)
	}
	if !equal(w.hostedRefs(repo), refsOf(t, s.bare)) {
		t.Fatal("the final sync did not run")
	}
	got, _ := w.h.Forge.Repository(ctx, repo.ID)
	if got.Mirror {
		t.Fatal("still a mirror")
	}
	if _, err := w.h.Mirrors.Get(ctx, m.ID); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("the mirror record was kept: %v", err)
	}
	if _, err := w.h.Mirrors.Promote(ctx, repo.Path, false, "ada"); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("promoting twice: %v", err)
	}
	// A failed final sync leaves the repository a mirror.
	r2 := w.newRepo("acme/two")
	m2, _ := w.h.Mirrors.SetPull(ctx, r2.Path, mirror.Spec{URL: s.url()}, "ada")
	w.mustSync(m2.ID)
	os.RemoveAll(s.bare)
	if _, err := w.h.Mirrors.Promote(ctx, r2.Path, true, "ada"); err == nil {
		t.Fatal("promoted though the final sync failed")
	}
	if got, _ := w.h.Forge.Repository(ctx, r2.ID); !got.Mirror {
		t.Fatal("no longer a mirror after a failed final sync")
	}
}

func TestSetUpRefusesCredentialsInTheAddressAndLiteralSecrets(t *testing.T) {
	w, ctx := newWorld(t, worldOpts{}), context.Background()
	repo := w.newRepo("acme/app")
	for _, tc := range []struct {
		name string
		spec mirror.Spec
	}{
		{"user and password in the address", mirror.Spec{URL: "https://bob:hunter2@git.example.com/x.git"}},
		{"a token as the credential name", mirror.Spec{URL: "https://git.example.com/x.git", Credential: forge.SecretRef{Env: "ghp_abcdef0123456789"}}},
		{"both env and file", mirror.Spec{URL: "https://git.example.com/x.git", Credential: forge.SecretRef{Env: "A", File: "/x"}}},
		{"ssh", mirror.Spec{URL: "ssh://git@git.example.com/x.git"}},
		{"too frequent", mirror.Spec{URL: "https://git.example.com/x.git", IntervalSeconds: 5}},
	} {
		if _, err := w.h.Mirrors.SetPull(ctx, repo.Path, tc.spec, "ada"); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("%s: %v", tc.name, err)
		} else if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "ghp_") {
			t.Errorf("%s: the error repeats the secret: %v", tc.name, err)
		}
	}
}

func TestEgressGuardRefusesInternalAddressesUnlessAllowed(t *testing.T) {
	root := t.TempDir()
	s := newSource(t)
	populate(s)
	runGit(t, root, "clone", "-q", "--mirror", s.bare, filepath.Join(root, "up.git"))
	srv := gitServer(t, root, "", "") // 127.0.0.1: an internal address
	ctx := context.Background()

	strict, _ := egress.New(nil)
	w := newWorld(t, worldOpts{guard: strict})
	repo := w.newRepo("acme/app")
	_, err := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: srv.URL + "/up.git"}, "ada")
	if !errors.Is(err, forge.ErrInvalid) || !strings.Contains(err.Error(), "internal address") || !strings.Contains(err.Error(), "forge.mirror_allow") {
		t.Fatalf("an internal address was not refused plainly: %v", err)
	}
	if got, _ := w.h.Forge.Repository(ctx, repo.ID); got.Mirror {
		t.Error("the repository became a mirror of a refused address")
	}
	for _, bad := range []string{"http://169.254.169.254/latest/meta-data/", "http://[::1]/x.git", "http://10.0.0.5/x.git", "http://192.168.1.11/x.git", "http://100.64.0.1/x.git", "http://0.0.0.0/x.git"} {
		if err := strict.Check(ctx, bad); !errors.Is(err, egress.ErrRefused) {
			t.Errorf("%s was not refused: %v", bad, err)
		}
	}
	if err := strict.Check(ctx, "file:///etc"); err == nil {
		t.Error("a file address passed")
	}

	// The same address, allowed by network, works, and is mirrored.
	allow, _ := egress.New([]string{"127.0.0.0/8"})
	w2 := newWorld(t, worldOpts{guard: allow})
	r2 := w2.newRepo("acme/app")
	m, err := w2.h.Mirrors.SetPull(ctx, r2.Path, mirror.Spec{URL: srv.URL + "/up.git"}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	w2.mustSync(m.ID)
	if !equal(w2.hostedRefs(r2), refsOf(t, s.bare)) {
		t.Fatal("not mirrored through the allow-list")
	}
	// A host name can be allowed by name.
	byName, _ := egress.New([]string{"localhost"})
	if err := byName.Check(ctx, "http://localhost:3000/x.git"); err != nil {
		t.Errorf("an allowed host name: %v", err)
	}
	// A redirect to an internal address is not followed by git (it would skip the guard).
	if err := strict.Check(ctx, "https://user:pw@example.com/x"); err == nil {
		t.Error("credentials in the address passed")
	}
}

func TestCredentialIsAReferenceUsedNotStoredNorShown(t *testing.T) {
	root := t.TempDir()
	s := newSource(t)
	populate(s)
	runGit(t, root, "clone", "-q", "--mirror", s.bare, filepath.Join(root, "up.git"))
	const token = "s3cr3t-token-value-0451"
	srv := gitServer(t, root, "bot", token)
	allow, _ := egress.New([]string{"127.0.0.1"})
	w, ctx := newWorld(t, worldOpts{guard: allow}), context.Background()
	repo := w.newRepo("acme/app")

	// No credential: refused by the server, and the status says so.
	m, _ := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: srv.URL + "/up.git"}, "ada")
	got, err := w.h.Mirrors.SyncNow(ctx, m.ID)
	if err == nil || got.State != mirror.StateError || !strings.Contains(got.LastError, "401") {
		t.Fatalf("an unauthenticated mirror: %v %+v", err, got)
	}

	// With a credential reference to a file the secret is read from.
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: srv.URL + "/up.git", Username: "bot", Credential: forge.SecretRef{File: secret}}, "ada"); err != nil {
		t.Fatal(err)
	}
	m = w.mustSync(m.ID)
	if !equal(w.hostedRefs(repo), refsOf(t, s.bare)) {
		t.Fatal("not mirrored with the credential")
	}
	// A wrong token: the error never contains it.
	if err := os.WriteFile(secret, []byte("wrong-"+token), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = w.h.Mirrors.SyncNow(ctx, m.ID)
	if err == nil {
		t.Fatal("a wrong token worked")
	}
	for _, text := range []string{got.LastError, err.Error()} {
		if strings.Contains(text, token) || strings.Contains(strings.ToLower(text), "authorization") {
			t.Errorf("a secret leaked into: %s", text)
		}
	}
	// Nothing in the database holds the secret: dump every table that exists.
	rows, err := w.eng.Database().Query(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err == nil {
		var names []string
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			names = append(names, n)
		}
		rows.Close()
		for _, n := range names {
			r, err := w.eng.Database().Query(ctx, `SELECT * FROM `+n)
			if err != nil {
				continue
			}
			cols, _ := r.Columns()
			for r.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				_ = r.Scan(ptrs...)
				for _, v := range vals {
					var s string
					switch x := v.(type) {
					case string:
						s = x
					case []byte:
						s = string(x)
					}
					if strings.Contains(s, token) {
						t.Errorf("the secret is stored in table %s", n)
					}
				}
			}
			r.Close()
		}
	}
	// The view shows where it is read from, not what it is.
	v := w.h.Mirrors.View(ctx, m)
	if v.Credential != "file:"+secret || strings.Contains(v.LastError+v.URL+v.Username, token) {
		t.Errorf("view: %+v", v)
	}
}

func TestSyncFailureIsRecordedWithBackoffAndRecovers(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := &now
	w, s, ctx := newWorld(t, worldOpts{mo: mirror.Options{Now: func() time.Time { return *clock }}}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	m, _ := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: "file://" + filepath.Join(t.TempDir(), "gone.git"), IntervalSeconds: 600}, "ada")
	got, err := w.h.Mirrors.SyncNow(ctx, m.ID)
	if err == nil || got.Failures != 1 || got.State != mirror.StateError || got.LastError == "" {
		t.Fatalf("%v %+v", err, got)
	}
	if d := got.NextAt.Sub(now); d != time.Minute {
		t.Errorf("first retry in %v, want 1m", d)
	}
	got, _ = w.h.Mirrors.SyncNow(ctx, m.ID)
	if d := got.NextAt.Sub(now); got.Failures != 2 || d != 2*time.Minute {
		t.Errorf("second retry in %v (failures %d), want 2m", d, got.Failures)
	}
	// Lag and behind: no success for long enough marks it behind, and the view says so.
	*clock = now.Add(3 * time.Hour)
	if v := w.h.Mirrors.View(ctx, got); !v.Behind || v.LagSeconds < 3*3600 {
		t.Errorf("view of a mirror that never worked: %+v", v)
	}
	// Fixing the address recovers it.
	if _, err := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: s.url(), IntervalSeconds: 600}, "ada"); err != nil {
		t.Fatal(err)
	}
	got = w.mustSync(m.ID)
	if got.State != mirror.StateOK || got.Failures != 0 || got.LastError != "" {
		t.Errorf("%+v", got)
	}
	if v := w.h.Mirrors.View(ctx, got); v.Behind || v.LagSeconds != 0 {
		t.Errorf("view after recovery: %+v", v)
	}
}

func TestSchedulerRunsDueMirrorsAndTwoServersDoNotRunOneTwice(t *testing.T) {
	w, s, ctx := newWorld(t, worldOpts{mo: mirror.Options{Tick: 50 * time.Millisecond}}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	m, _ := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: s.url()}, "ada")
	// A lease held (another server is running it) is respected.
	if _, err := w.h.Mirrors.SyncNow(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := w.h.Mirrors.Start(cctx)
	defer func() { cancel(); <-done }()
	s.commit("while the scheduler runs")
	s.pushAll()
	if _, err := w.h.Mirrors.Update(ctx, m.ID, nil, nil, nil); err != nil { // makes it due now
		t.Fatal(err)
	}
	eventually(t, "the scheduled sync", 20*time.Second, func() bool { return equal(w.hostedRefs(repo), refsOf(t, s.bare)) })
}

func TestPullMirrorHonoursTheRepositorySizeLimit(t *testing.T) {
	w, s, ctx := newWorld(t, worldOpts{repo: 100}), newSource(t), context.Background()
	populate(s)
	repo := w.newRepo("acme/app")
	m, _ := w.h.Mirrors.SetPull(ctx, repo.Path, mirror.Spec{URL: s.url()}, "ada")
	got, err := w.h.Mirrors.SyncNow(ctx, m.ID)
	if err == nil || !strings.Contains(got.LastError, "more than the") {
		t.Fatalf("a repository over its limit was mirrored: %v %+v", err, got)
	}
	if len(w.hostedRefs(repo)) != 0 {
		t.Error("refs were written for a refused sync")
	}
}
