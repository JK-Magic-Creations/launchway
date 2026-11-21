package store_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func open(t *testing.T) (*store.Store, *engine.Engine, string) {
	t.Helper()
	addr := testdb.New(t)
	eng, err := engine.Open(engine.Options{Database: addr, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	return store.New(eng.Database()), eng, addr
}

func TestConnectionsRoundTrip(t *testing.T) {
	st, _, _ := open(t)
	ctx := context.Background()
	c, err := st.CreateConnection(ctx, forge.Connection{Name: "homelab", Kind: forge.KindGitea, BaseURL: "https://git.example.com",
		Credential: forge.SecretRef{Env: "GITEA_TOKEN"}, Events: forge.EventsPolling, Enabled: true}, "dana")
	if err != nil || !strings.HasPrefix(c.ID, "con_") {
		t.Fatalf("%+v %v", c, err)
	}
	got, err := st.Connection(ctx, c.ID)
	if err != nil || got.Credential.Env != "GITEA_TOKEN" || got.Kind != forge.KindGitea || !got.Enabled || got.CreatedAt.IsZero() {
		t.Fatalf("%+v %v", got, err)
	}
	if byName, err := st.ConnectionByName(ctx, "homelab"); err != nil || byName.ID != c.ID {
		t.Fatalf("by name: %v", err)
	}
	if _, err := st.CreateConnection(ctx, forge.Connection{Name: "homelab", Kind: forge.KindGitHub}, "dana"); !errors.Is(err, store.ErrExists) {
		t.Errorf("duplicate name: %v", err)
	}
	// A literal credential is refused before it reaches the database.
	if _, err := st.CreateConnection(ctx, forge.Connection{Name: "bad", Kind: forge.KindGitHub, BaseURL: "https://u:p4ssw0rd@github.example"}, "dana"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("literal credential: %v", err)
	}
	if err := st.SetEnabled(ctx, c.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Connection(ctx, c.ID); got.Enabled {
		t.Error("still enabled")
	}
	if err := st.SetEnabled(ctx, "con_missing", true); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("%v", err)
	}
	if _, err := st.Connection(ctx, "con_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("%v", err)
	}
	if _, err := st.ConnectionByName(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("%v", err)
	}
	list, err := st.Connections(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	// Repositories keep a connection from being deleted.
	r, err := st.PutRepository(ctx, forge.Repository{Connection: c.ID, Path: "home/app", DefaultBranch: "main", Clone: forge.CloneAddresses{HTTPS: "https://git.example.com/home/app.git"}}, "")
	if err != nil || !strings.HasPrefix(r.ID, "rep_") || r.Name != "app" || r.Visibility != forge.Private {
		t.Fatalf("%+v %v", r, err)
	}
	if err := st.DeleteConnection(ctx, c.ID); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("delete with repositories: %v", err)
	}
	other, _ := st.CreateConnection(ctx, forge.Connection{Name: "scratch", Kind: forge.KindPlain}, "")
	if err := st.DeleteConnection(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteConnection(ctx, other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("%v", err)
	}
}

func TestRepositories(t *testing.T) {
	st, _, _ := open(t)
	ctx := context.Background()
	c, _ := st.CreateConnection(ctx, forge.Connection{Name: "c", Kind: forge.KindPlain}, "")
	r1, err := st.PutRepository(ctx, forge.Repository{Connection: c.ID, Path: "a", DefaultBranch: "main"}, "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	// Putting again updates in place and keeps the ID.
	r2, err := st.PutRepository(ctx, forge.Repository{Connection: c.ID, Path: "a", DefaultBranch: "trunk", Archived: true}, "proj-a")
	if err != nil || r2.ID != r1.ID || r2.DefaultBranch != "trunk" || !r2.Archived {
		t.Fatalf("%+v %v", r2, err)
	}
	st.PutRepository(ctx, forge.Repository{Connection: c.ID, Path: "b"}, "")
	all, _ := st.Repositories(ctx, "")
	mine, _ := st.Repositories(ctx, c.ID)
	none, _ := st.Repositories(ctx, "con_zzz")
	if len(all) != 2 || len(mine) != 2 || len(none) != 0 || all[0].Path != "a" {
		t.Fatalf("%d %d %d", len(all), len(mine), len(none))
	}
	if got, err := st.RepositoryOfProject(ctx, "proj-a"); err != nil || got.ID != r1.ID {
		t.Fatalf("%v", err)
	}
	if _, err := st.RepositoryOfProject(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Error(err)
	}
	if got, err := st.RepositoryByPath(ctx, c.ID, "b"); err != nil || got.Path != "b" {
		t.Error(err)
	}
	if _, err := st.RepositoryByPath(ctx, c.ID, "zz"); !errors.Is(err, store.ErrNotFound) {
		t.Error(err)
	}
	if _, err := st.Repository(ctx, "rep_x"); !errors.Is(err, store.ErrNotFound) {
		t.Error(err)
	}
	if _, err := st.PutRepository(ctx, forge.Repository{Connection: c.ID, Path: "../x"}, ""); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("bad path: %v", err)
	}
}

// Two servers creating the same repository at once end with one row.
func TestConcurrentPutRepositoryMakesOneRow(t *testing.T) {
	st, _, _ := open(t)
	ctx := context.Background()
	c, _ := st.CreateConnection(ctx, forge.Connection{Name: "c", Kind: forge.KindPlain}, "")
	var wg sync.WaitGroup
	var ids sync.Map
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := st.PutRepository(ctx, forge.Repository{Connection: c.ID, Path: "same"}, "p")
			if err != nil {
				t.Error(err)
				return
			}
			ids.Store(r.ID, true)
		}()
	}
	wg.Wait()
	n := 0
	ids.Range(func(_, _ any) bool { n++; return true })
	list, _ := st.Repositories(ctx, c.ID)
	if len(list) != 1 || n != 1 {
		t.Fatalf("%d rows, %d distinct ids", len(list), n)
	}
}

func TestEventsAreKeptAndPruned(t *testing.T) {
	st, _, _ := open(t)
	ctx := context.Background()
	now := time.Now()
	st.SetClock(func() time.Time { return now })
	for i := 0; i < 10; i++ {
		err := st.AppendEvent(ctx, store.Event{Connection: "con_a", Repository: "rep_1", Kind: forge.EventRefUpdated, Ref: "refs/heads/main",
			Outcome: "ignored", Detail: strings.Repeat("x", 6000), At: now.Add(time.Duration(i) * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
	}
	st.AppendEvent(ctx, store.Event{Connection: "con_b", Kind: forge.EventRefUpdated})
	ev, err := st.Events(ctx, "con_a", "", 0, 3)
	if err != nil || len(ev) != 3 || ev[0].Seq < ev[1].Seq || len(ev[0].Detail) > 4000 {
		t.Fatalf("%v %v", ev, err)
	}
	older, _ := st.Events(ctx, "con_a", "", ev[2].Seq, 100)
	if len(older) != 7 {
		t.Errorf("paging: %d", len(older))
	}
	if other, _ := st.Events(ctx, "con_a", "rep_zzz", 0, 100); len(other) != 0 {
		t.Error("repository filter")
	}
	removed, err := st.PruneEvents(ctx, "con_a", 4, 0)
	if err != nil || removed != 6 {
		t.Fatalf("pruned %d %v", removed, err)
	}
	left, _ := st.Events(ctx, "con_a", "", 0, 100)
	if len(left) != 4 || left[0].Seq != ev[0].Seq {
		t.Fatalf("kept %d", len(left))
	}
	if b, _ := st.Events(ctx, "con_b", "", 0, 100); len(b) != 1 {
		t.Error("another connection's events were pruned")
	}
	// By age: the clock moves on.
	now = now.Add(48 * time.Hour)
	removed, _ = st.PruneEvents(ctx, "con_a", 0, time.Hour)
	if removed != 4 {
		t.Errorf("by age: %d", removed)
	}
}

func TestDeliveriesAreRememberedPerConnection(t *testing.T) {
	st, _, _ := open(t)
	ctx := context.Background()
	fresh, err := st.SeeDelivery(ctx, "con_a", "d-1")
	if err != nil || !fresh {
		t.Fatal(fresh, err)
	}
	if fresh, _ := st.SeeDelivery(ctx, "con_a", "d-1"); fresh {
		t.Error("a replay is new")
	}
	if fresh, _ := st.SeeDelivery(ctx, "con_b", "d-1"); !fresh {
		t.Error("the same ID on another connection must be new")
	}
	if _, err := st.SeeDelivery(ctx, "", "x"); err == nil {
		t.Error("no connection")
	}
	// Exactly one of many concurrent servers acts.
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := st.SeeDelivery(ctx, "con_a", "race"); err == nil && ok {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Errorf("%d winners", winners.Load())
	}
	st.SetClock(func() time.Time { return time.Now().Add(48 * time.Hour) })
	if n, err := st.PruneDeliveries(ctx, time.Hour); err != nil || n < 3 {
		t.Errorf("pruned %d %v", n, err)
	}
	if fresh, _ := st.SeeDelivery(ctx, "con_a", "d-1"); !fresh {
		t.Error("a pruned delivery should be new again")
	}
}

func TestSyncPlain(t *testing.T) {
	st, _, _ := open(t)
	ctx := context.Background()
	projects := []store.Project{
		{Name: "web", URL: "https://dana:s3cretpw@git.example.com/dana/web.git", DefaultBranch: "main"},
		{Name: "tools", URL: "git@git.example.com:dana/tools.git", DefaultBranch: "main"},
		{Name: "local", URL: "/srv/git/local.git", DefaultBranch: "trunk"},
		{Name: "viassh", URL: "ssh://git@git.example.com:2222/x.git", DefaultBranch: "main"},
	}
	conn, err := st.SyncPlain(ctx, projects)
	if err != nil || conn.Kind != forge.KindPlain || conn.Name != store.PlainConnectionName {
		t.Fatalf("%+v %v", conn, err)
	}
	repos, _ := st.Repositories(ctx, conn.ID)
	if len(repos) != 4 {
		t.Fatalf("%d repositories", len(repos))
	}
	for _, r := range repos {
		if strings.Contains(r.Clone.HTTPS+r.Clone.SSH, "s3cretpw") || strings.Contains(r.Clone.HTTPS+r.Clone.SSH, "dana:") {
			t.Errorf("credentials in %+v", r)
		}
	}
	web, _ := st.RepositoryOfProject(ctx, "web")
	if web.Clone.HTTPS != "https://git.example.com/dana/web.git" || web.DefaultBranch != "main" {
		t.Errorf("%+v", web)
	}
	if tools, _ := st.RepositoryOfProject(ctx, "tools"); tools.Clone.SSH != "git.example.com:dana/tools.git" {
		t.Errorf("%+v", tools)
	}
	// Running again changes nothing but what changed; a removed project keeps its row.
	projects[0].DefaultBranch = "develop"
	conn2, err := st.SyncPlain(ctx, projects[:2])
	if err != nil || conn2.ID != conn.ID {
		t.Fatalf("second sync: %v", err)
	}
	if again, _ := st.Repositories(ctx, conn.ID); len(again) != 4 {
		t.Errorf("a removed project lost its repository: %d", len(again))
	}
	if web2, _ := st.RepositoryOfProject(ctx, "web"); web2.ID != web.ID || web2.DefaultBranch != "develop" {
		t.Errorf("%+v", web2)
	}
	// Two servers starting at once make one connection.
	st2, _, _ := open(t)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st2.SyncPlain(ctx, projects); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if cs, _ := st2.Connections(ctx); len(cs) != 1 {
		t.Errorf("%d connections", len(cs))
	}
	if rs, _ := st2.Repositories(ctx, ""); len(rs) != 4 {
		t.Errorf("%d repositories", len(rs))
	}
}

func TestCleanAddress(t *testing.T) {
	for in, want := range map[string][2]string{
		"https://u:p@h.example/a.git?token=x#f": {"https://h.example/a.git", ""},
		"ssh://git@h.example/a.git":             {"", "ssh://h.example/a.git"},
		"git@h.example:a/b.git":                 {"", "h.example:a/b.git"},
		"/srv/git/x.git":                        {"", ""},
		"file:///srv/x.git":                     {"", ""},
		"":                                      {"", ""},
	} {
		h, s := store.CleanAddress(in)
		if h != want[0] || s != want[1] {
			t.Errorf("%q: %q %q, want %q", in, h, s, want)
		}
	}
}
