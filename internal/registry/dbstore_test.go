package registry_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/registry"
	"github.com/jk-magic-creations/launchway/internal/registry/enginedb"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The registry on the database blob store, which is what an installation on PostgreSQL has by default: pushes,
// pulls, collection, and the sweep of objects no row names (a store that can list its keys).
func TestRegistryOnTheDatabaseBlobStore(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	db := eng.Database()
	store, err := blobstore.NewDatabase(db.SQL(), db.Postgres(), blobstore.DatabaseOptions{WriteLock: db.WriteLock})
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Now()}
	svc := registry.New(registry.Options{DB: enginedb.New(db), Blobs: store, Now: clk.Now, Log: quiet})
	e := &env{t: t, eng: eng, svc: svc, clock: clk, signer: registry.Signer{Key: []byte("0123456789abcdef0123456789abcdef"), Now: clk.Now}}
	e.auth = &fakeAuth{secrets: map[string]who{}, svc: svc, signer: e.signer}
	mux := http.NewServeMux()
	mux.Handle("/v2/", &registry.Handler{Svc: svc, Auth: e.auth, Signer: e.signer, Base: func(r *http.Request) string { return "http://" + r.Host }})
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	e.grant("tok", who{subject: "ann", can: rw("cart")})

	big := make([]byte, 9<<20)
	for i := range big {
		big[i] = byte(i % 251)
	}
	keep := e.pushImage(t, "tok", "cart/a", "1", big)
	res, b := e.raw(t, "tok", "GET", "/v2/cart/a/manifests/1", nil, nil)
	e.expect(t, res, b, 200)
	if res.Header.Get("Docker-Content-Digest") != keep {
		t.Fatal("digest")
	}
	gone := e.pushImage(t, "tok", "cart/a", "2", []byte("short lived"))
	res, b = e.raw(t, "tok", "DELETE", "/v2/cart/a/manifests/"+gone, nil, nil)
	e.expect(t, res, b, 202)
	// An object the registry wrote and recorded nowhere: a server stopped between the two.
	if err := svc.PutStrayForTest(t.Context(), "registry/default/blobs/zz/stray-1234abcd", []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	clk.Add(3 * time.Hour)
	// The database store dates objects with its own clock: make the stray old.
	if _, err := db.Exec(t.Context(), `UPDATE blob_objects SET created_at = 1 WHERE key = ?`, "registry/default/blobs/zz/stray-1234abcd"); err != nil {
		t.Fatal(err)
	}
	rep, err := svc.GC(t.Context(), registry.GCOptions{})
	if err != nil || rep.Blobs < 2 || rep.Strays != 1 {
		t.Fatalf("collection %+v %v", rep, err)
	}
	if _, err := store.Stat(t.Context(), "registry/default/blobs/zz/stray-1234abcd"); err == nil {
		t.Fatal("the stray object survived")
	}
	res, b = e.raw(t, "tok", "GET", "/v2/cart/a/manifests/1", nil, nil)
	e.expect(t, res, b, 200)
	// What is referenced is intact.
	_, body, err := svc.ResolveManifest(t.Context(), "cart/a", keep, true)
	if err != nil || len(body) == 0 {
		t.Fatal(err)
	}
}
