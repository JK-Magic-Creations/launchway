package hosting_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/forgetest"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/launchway"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

type minter struct{}

func (minter) Mint(repo string, write bool, ttl time.Duration) (string, error) {
	return "lwr_" + repo, nil
}

func open(t *testing.T, po launchway.Options) (*hosting.Hosting, *engine.Engine) {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	blobs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	po.Minter = minter{}
	h, err := hosting.New(context.Background(), hosting.Options{Engine: eng, Blobs: blobs, Data: t.TempDir(), Server: "t", BaseURL: "https://lw.example", Provider: po})
	if err != nil {
		t.Fatal(err)
	}
	return h, eng
}

func TestConnectionIsMadeOnceAndIsOfKindLaunchway(t *testing.T) {
	h, eng := open(t, launchway.Options{})
	if h.Connection.Kind != forge.KindLaunchway || h.Connection.Name != hosting.ConnectionName {
		t.Fatalf("%+v", h.Connection)
	}
	h2, err := hosting.New(context.Background(), hosting.Options{Engine: eng, Blobs: mustBlobs(t), Data: t.TempDir(), Server: "u"})
	// A second server with another blob store refuses to host (it would split the repositories).
	if err == nil {
		t.Fatalf("a server with a different store was let in: %+v", h2)
	}
	if conns, _ := h.Forge.Connections(context.Background()); len(conns) != 1 {
		t.Fatalf("%d connections", len(conns))
	}
}

func mustBlobs(t *testing.T) blobstore.Store {
	b, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRepositoriesLifecycle(t *testing.T) {
	h, _ := open(t, launchway.Options{})
	ctx := context.Background()
	repos := h.Provider.Repositories()
	r, err := repos.Create(ctx, forge.NewRepository{Home: "jkhunter", Name: "demo"})
	if err != nil || r.Path != "jkhunter/demo" || r.Visibility != forge.Private || r.DefaultBranch != "main" ||
		r.Clone.HTTPS != "https://lw.example/git/jkhunter/demo.git" || !strings.HasPrefix(r.ID, "rep_") {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := repos.Create(ctx, forge.NewRepository{Home: "jkhunter", Name: "demo"}); !errors.Is(err, store.ErrExists) {
		t.Errorf("duplicate: %v", err)
	}
	refs, err := h.Provider.Refs(ctx, r)
	if err != nil || len(refs) != 0 {
		t.Errorf("a new repository has refs %v %v", refs, err)
	}
	if err := repos.SetDefaultBranch(ctx, "jkhunter/demo", "trunk"); err != nil {
		t.Fatal(err)
	}
	if err := repos.SetDefaultBranch(ctx, "jkhunter/demo", "bad..name"); !errors.Is(err, forge.ErrInvalid) {
		t.Errorf("bad branch: %v", err)
	}
	if err := h.Provider.SetVisibility(ctx, "jkhunter/demo", forge.Public); err != nil {
		t.Fatal(err)
	}
	moved, err := h.Provider.Rename(ctx, "jkhunter/demo", "", "renamed")
	if err != nil || moved.ID != r.ID || moved.Path != "jkhunter/renamed" || moved.DefaultBranch != "trunk" || moved.Visibility != forge.Public ||
		!strings.HasSuffix(moved.Clone.HTTPS, "/git/jkhunter/renamed.git") {
		t.Fatalf("%+v %v", moved, err)
	}
	if _, err := repos.Get(ctx, "jkhunter/demo"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the old name still answers: %v", err)
	}
	if _, err := repos.Create(ctx, forge.NewRepository{Home: "jkhunter", Name: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Provider.Rename(ctx, "jkhunter/other", "", "renamed"); !errors.Is(err, store.ErrExists) {
		t.Errorf("rename onto a name in use: %v", err)
	}
	// Delete goes to the trash: gone from lists and lookups, restorable, name free for reuse.
	if err := repos.Delete(ctx, "jkhunter/renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.Get(ctx, "jkhunter/renamed"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a deleted repository is served: %v", err)
	}
	page, _ := repos.List(ctx, "")
	if len(page.Items) != 1 || page.Items[0].Path != "jkhunter/other" {
		t.Errorf("list: %+v", page.Items)
	}
	trash, _ := h.Provider.Trash(ctx)
	if len(trash) != 1 || trash[0].ID != r.ID {
		t.Fatalf("trash: %+v", trash)
	}
	if err := repos.Delete(ctx, ".trash/"+r.ID); err == nil {
		t.Error("the trash path can be addressed")
	}
	if err := h.Provider.Restore(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := repos.Get(ctx, "jkhunter/renamed"); err != nil || got.ID != r.ID {
		t.Errorf("restore: %+v %v", got, err)
	}
	if _, err := h.Store.Refs(ctx, r.ID); err != nil {
		t.Errorf("the repository's storage went with the row: %v", err)
	}
}

func TestHostileNamesNeverReachTheFilesystem(t *testing.T) {
	h, _ := open(t, launchway.Options{})
	ctx := context.Background()
	for _, n := range []string{"..", "../../etc", "a/b", ".git", "x.git", "/root", strings.Repeat("a", 5000), "ünï", "a\x00b", "con", "x\nname"} {
		for _, home := range []string{"owner", n} {
			name := n
			if home == n {
				name = "ok"
			}
			if _, err := h.Provider.Repositories().Create(ctx, forge.NewRepository{Home: home, Name: name}); !errors.Is(err, forge.ErrInvalid) {
				t.Errorf("home %.20q name %.20q: %v", home, name, err)
			}
		}
	}
	if page, _ := h.Provider.Repositories().List(ctx, ""); len(page.Items) != 0 {
		t.Errorf("%d repositories exist", len(page.Items))
	}
}

func TestQuotaAndRateLimit(t *testing.T) {
	now := time.Now()
	h, _ := open(t, launchway.Options{MaxPerHome: 2, CreatesPerMinute: 3, Now: func() time.Time { return now }})
	ctx := context.Background()
	mk := func(home, name string) error {
		_, err := h.Provider.Repositories().Create(ctx, forge.NewRepository{Home: home, Name: name})
		return err
	}
	if err := mk("a", "one"); err != nil {
		t.Fatal(err)
	}
	if err := mk("a", "two"); err != nil {
		t.Fatal(err)
	}
	if err := mk("a", "three"); !errors.Is(err, launchway.ErrQuota) {
		t.Errorf("quota: %v", err)
	}
	if err := mk("b", "one"); err != nil {
		t.Fatal(err)
	}
	if err := mk("b", "two"); !errors.Is(err, launchway.ErrRateLimited) {
		t.Errorf("rate: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if err := mk("b", "two"); err != nil {
		t.Errorf("the limit never lifts: %v", err)
	}
	// Deleting frees the quota.
	if err := h.Provider.Repositories().Delete(ctx, "a/one"); err != nil {
		t.Fatal(err)
	}
	if err := mk("a", "three"); err != nil {
		t.Errorf("after a delete: %v", err)
	}
}

func TestConformanceAndCredentials(t *testing.T) {
	h, _ := open(t, launchway.Options{})
	ctx := context.Background()
	r, err := h.Provider.Repositories().Create(ctx, forge.NewRepository{Name: "conformance"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != "launchway/conformance" {
		t.Fatalf("default owner: %s", r.Path)
	}
	forgetest.Run(t, forgetest.Harness{Provider: h.Provider, Repo: r})
	c, err := h.Provider.Access().Credential(ctx, r, false)
	if err != nil || c.Secret != "lwr_"+r.ID {
		t.Errorf("%+v %v", c, err)
	}
	if _, err := h.Provider.Access().Credential(ctx, r, true); err == nil {
		t.Error("a write credential was minted for a run")
	}
}

func TestMaintenanceRunsOverRepositories(t *testing.T) {
	h, _ := open(t, launchway.Options{})
	ctx := context.Background()
	if _, err := h.Provider.Repositories().Create(ctx, forge.NewRepository{Name: "m"}); err != nil {
		t.Fatal(err)
	}
	h.Maintain(ctx, true) // nothing to consolidate; must not fail or hang
}

func TestRefusesToHostWithoutAStore(t *testing.T) {
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := hosting.New(context.Background(), hosting.Options{Engine: eng, Data: t.TempDir()}); err == nil {
		t.Error("hosting started without a blob store")
	}
}
