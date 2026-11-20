package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// openWith opens an engine with the given artifact options and log.
func openWith(t *testing.T, addr string, clk *clock, o ArtifactOptions, schema int, buf *syncBuffer) *Engine {
	t.Helper()
	if buf == nil {
		buf = &syncBuffer{}
	}
	e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now, Artifacts: o, schema: schema,
		Logger: slogTo(buf)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func fsStore(t *testing.T, dir string) *blobstore.Filesystem {
	t.Helper()
	s, err := blobstore.NewFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)} }

// The migration's tables are blobstore.DatabaseSchema word for word, and
// that text has shipped: changing it needs a new migration.
func TestBlobSchemaIsFrozen(t *testing.T) {
	if !strings.HasPrefix(migrations[schemaBlobs-1].sql, blobstore.DatabaseSchema) {
		t.Fatal("migration 17 is not blobstore.DatabaseSchema")
	}
	sum := sha256.Sum256([]byte(blobstore.DatabaseSchema))
	if got := hex.EncodeToString(sum[:]); got != frozenBlobSchema {
		t.Fatalf("blobstore.DatabaseSchema changed (SHA-256 %s); it is migration 17, which must never change: add a migration instead", got)
	}
}

// frozenBlobSchema is the SHA-256 of blobstore.DatabaseSchema.
const frozenBlobSchema = "7638861e0649e9dbeb8a476f0a0160111ca3f4718e61a2adbab1b9db39d1611d"

func slogTo(buf *syncBuffer) *slog.Logger { return slog.New(slog.NewTextHandler(buf, nil)) }

// The database store end to end in the engine: a file bigger than a chunk
// goes up, is fetched by a later step and from an offset, and once its
// artifact is deleted and the grace period passes, every row of the store
// is gone again.
func TestDatabaseStoreRoundTrip(t *testing.T) {
	clk := newClock()
	e := openWith(t, testdb.New(t), clk, ArtifactOptions{Choice: StoreDatabase}, 0, nil)
	if e.ArtifactOptions().Store == nil || e.ArtifactOptions().Store.Name() != "database" {
		t.Fatalf("store %v: %s", e.ArtifactOptions().Store, e.ArtifactOptions().StoreProblem)
	}
	id := create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	if sum, _ := e.Summary(id); sum.MinSchema != schemaBlobs {
		t.Fatalf("a run keeping artifacts in the database store has min_schema %d, want %d", sum.MinSchema, schemaBlobs)
	}
	j := take(t, e)
	data := big(2*ArtifactChunkBytes+777, 9)
	s := keep(t, e, j.LeaseID, "binary", []upFile{{"bin/app", data, 0o755}})
	ok(t, e, j, nil)
	var store string
	e.db.QueryRow(`SELECT store FROM artifact_blobs`).Scan(&store)
	if store != "database" {
		t.Fatalf("the blob is recorded in %q", store)
	}
	d := take(t, e)
	if got := readFetch(t, e, d.LeaseID, d.Fetch[0].ArtifactID, "bin/app"); !bytes.Equal(got, data) {
		t.Fatal("fetched other bytes")
	}
	g, err := e.OpenFetch(context.Background(), d.LeaseID, d.Fetch[0].ArtifactID, "bin/app", ArtifactChunkBytes+5)
	if err != nil {
		t.Fatal(err)
	}
	var rest bytes.Buffer
	rest.ReadFrom(g.Body)
	g.Body.Close()
	if !bytes.Equal(rest.Bytes(), data[ArtifactChunkBytes+5:]) {
		t.Fatal("reading from an offset")
	}
	ok(t, e, d, nil)
	st, err := e.StoreStatus(context.Background())
	if err != nil || st.Store != "database" || st.Usage == nil || st.Usage.ObjectBytes < int64(len(data)) || st.Kept.Files != 1 || len(st.Elsewhere) != 0 {
		t.Fatalf("status %+v %v", st, err)
	}
	if _, err := e.DeleteArtifact(id, s.ID, "ann"); err != nil {
		t.Fatal(err)
	}
	clk.Add(2 * time.Hour)
	e.pruneArtifacts()
	for _, table := range []string{"artifact_blobs", "blob_chunks", "blob_parts", "blob_uploads"} {
		var n int
		e.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
		want := 0
		if table != "artifact_blobs" {
			// The store marker stays: one object.
			want = 1
		}
		if n != want {
			t.Errorf("%d rows left in %s, want %d", n, table, want)
		}
	}
}

// A store that is full says so before anything is sent.
func TestDatabaseStoreFull(t *testing.T) {
	clk := newClock()
	e := openWith(t, testdb.New(t), clk, ArtifactOptions{Choice: StoreDatabase,
		DatabaseLimits: blobstore.DatabaseOptions{MaxBytes: 4 << 20, MaxObjectBytes: 4 << 20}}, 0, nil)
	if e.ArtifactOptions().MaxFileBytes != 4<<20 {
		t.Fatalf("the largest file is %d, not the store's largest object", e.ArtifactOptions().MaxFileBytes)
	}
	create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	keep(t, e, j.LeaseID, "binary", []upFile{{"bin/app", big(3<<20, 1), 0o755}})
	ok(t, e, j, nil)
	ok(t, e, take(t, e), nil)
	create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j = take(t, e)
	_, err := e.BeginArtifact(j.LeaseID, BeginArtifact{Name: "binary", Files: infos([]upFile{{"bin/app", big(2<<20, 2), 0o755}})})
	if !errors.Is(err, ErrQuota) || !strings.Contains(err.Error(), "store is full") || !strings.Contains(err.Error(), "max_total_gib") {
		t.Fatalf("a full store: %v", err)
	}
}

// With nothing configured: SQLite keeps the filesystem; PostgreSQL keeps
// the database, unless artifacts were kept on the filesystem before, which
// it keeps using until they are copied; after the copy it moves to the
// database by itself, and servers read the copied artifacts there.
func TestAutomaticStoreChoice(t *testing.T) {
	clk := newClock()
	addr := testdb.New(t)
	dir := t.TempDir()
	auto := ArtifactOptions{Choice: StoreAuto, Store: fsStore(t, dir)}
	if !testdb.Postgres() {
		e := openWith(t, addr, clk, auto, 0, nil)
		if e.ArtifactOptions().Store.Name() != "filesystem" || !strings.Contains(e.StoreWhy(), "SQLite") {
			t.Fatalf("SQLite: %s, %q", e.ArtifactOptions().Store.Name(), e.StoreWhy())
		}
		return
	}
	// A fresh PostgreSQL installation: the database.
	fresh := openWith(t, testdb.New(t), clk, ArtifactOptions{Choice: StoreAuto, Store: fsStore(t, t.TempDir())}, 0, nil)
	if fresh.ArtifactOptions().Store.Name() != "database" {
		t.Fatalf("a fresh installation chose %s: %s", fresh.ArtifactOptions().Store.Name(), fresh.ArtifactOptions().StoreProblem)
	}

	// An installation from before schema 17, keeping artifacts on the
	// filesystem (its blobs have no store recorded).
	old := openWith(t, addr, clk, ArtifactOptions{Store: fsStore(t, dir)}, 0, nil)
	id := create(t, old, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, old)
	data := big(ArtifactChunkBytes+99, 4)
	keep(t, old, j.LeaseID, "binary", []upFile{{"bin/app", data, 0o755}})
	ok(t, old, j, nil)
	old.db.Exec(`UPDATE artifact_blobs SET store = ''`)
	old.Close()

	var log syncBuffer
	e := openWith(t, addr, clk, ArtifactOptions{Choice: StoreAuto, Store: fsStore(t, dir)}, 0, &log)
	if e.ArtifactOptions().Store.Name() != "filesystem" || !strings.Contains(e.StoreWhy(), "launchway blobs copy --from filesystem --to database") {
		t.Fatalf("legacy: %s, %q", e.ArtifactOptions().Store.Name(), e.StoreWhy())
	}
	st, _ := e.StoreStatus(context.Background())
	if len(st.Elsewhere) != 1 || st.Elsewhere[0].Store != "" || len(st.Advice) == 0 {
		t.Fatalf("status of the legacy store %+v", st)
	}

	// Another server, whose directory is not the marked one (an emptyDir
	// after a restart, or a server that never had the artifacts), takes
	// the database, and the database becomes the marked store. It says
	// where the old files are, and so does a download of one.
	var bLog syncBuffer
	b := openWith(t, addr, clk, ArtifactOptions{Choice: StoreAuto, Store: fsStore(t, t.TempDir())}, 0, &bLog)
	if b.ArtifactOptions().Store == nil || b.ArtifactOptions().Store.Name() != "database" {
		t.Fatalf("a server without the old directory: %v %q", b.ArtifactOptions().Store, b.ArtifactOptions().StoreProblem)
	}
	if !strings.Contains(bLog.String(), "cannot read them until they are copied") {
		t.Fatalf("no warning about the files kept before:\n%s", bLog.String())
	}
	st, _ = b.StoreStatus(context.Background())
	if len(st.Elsewhere) != 1 || st.Elsewhere[0].Files != 1 || len(st.Advice) != 1 || !strings.Contains(st.Advice[0], "launchway blobs copy --from filesystem --to database") {
		t.Fatalf("status after moving to the database: %+v", st)
	}
	list, err := b.RunArtifacts(id)
	if err != nil || len(list.Artifacts) != 1 {
		t.Fatal(list, err)
	}
	if _, err := b.OpenArtifactFile(context.Background(), id, list.Artifacts[0].ID, "bin/app", 0); !errors.Is(err, ErrNotFound) ||
		!strings.Contains(err.Error(), "launchway blobs copy --from filesystem --to database") {
		t.Fatalf("downloading an old file before the copy: %v", err)
	}
	// The same content kept again is sent again, not linked to the old copy
	// this server cannot read.
	ok(t, b, take(t, b), nil) // the first run's deploy step
	create(t, b, artifactsPipeline, Trigger{Event: "manual"})
	jb := take(t, b)
	begun, err := b.BeginArtifact(jb.LeaseID, BeginArtifact{Name: "binary", Files: infos([]upFile{{"bin/app", data, 0o755}})})
	if err != nil || begun.Files[0].Done {
		t.Fatalf("the same content again: %+v %v", begun, err)
	}

	// The copy, while servers keep running.
	admin, err := OpenBlobAdmin(addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	dbs, err := admin.DatabaseStore(blobstore.DatabaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := admin.Copy(context.Background(), fsStore(t, dir), dbs, CopyOptions{})
	if err != nil || rep.Copied != 1 || rep.Failed != 0 || !rep.Marker || len(rep.Left) != 0 {
		t.Fatalf("copy %+v %v", rep, err)
	}
	// Again: nothing to do (resumable, idempotent).
	if rep, err := admin.Copy(context.Background(), fsStore(t, dir), dbs, CopyOptions{}); err != nil || rep.Copied+rep.Present != 0 {
		t.Fatalf("second copy %+v %v", rep, err)
	}
	// Now the database by itself, and the artifact reads from it.
	c := openWith(t, addr, clk, ArtifactOptions{Choice: StoreAuto, Store: fsStore(t, t.TempDir())}, 0, nil)
	if c.ArtifactOptions().Store == nil || c.ArtifactOptions().Store.Name() != "database" {
		t.Fatalf("after the copy: %v %s", c.ArtifactOptions().Store, c.ArtifactOptions().StoreProblem)
	}
	f, err := c.OpenArtifactFile(context.Background(), id, list.Artifacts[0].ID, "bin/app", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	got.ReadFrom(f.Body)
	f.Body.Close()
	if !bytes.Equal(got.Bytes(), data) {
		t.Fatal("the copied artifact reads back wrong")
	}
}

// A server whose store lacks a file the database records elsewhere says
// where it is and how to copy it, instead of a bare "not found".
func TestMissingContentSaysWhere(t *testing.T) {
	clk := newClock()
	addr := testdb.New(t)
	e := openWith(t, addr, clk, ArtifactOptions{Choice: StoreDatabase}, 0, nil)
	id := create(t, e, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, e)
	keep(t, e, j.LeaseID, "binary", []upFile{{"bin/app", []byte("hello"), 0o755}})
	ok(t, e, j, nil)
	list, _ := e.RunArtifacts(id)
	// Pretend it was kept in S3 and never copied.
	var key string
	e.db.QueryRow(`SELECT store_key FROM artifact_blobs`).Scan(&key)
	e.db.Exec(`UPDATE artifact_blobs SET store = 's3'`)
	e.ArtifactOptions().Store.Delete(context.Background(), key)
	_, err := e.OpenArtifactFile(context.Background(), id, list.Artifacts[0].ID, "bin/app", 0)
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "launchway blobs copy --from s3 --to database") {
		t.Fatalf("missing content: %v", err)
	}
	e.db.Exec(`UPDATE artifact_blobs SET store = 'database'`)
	_, err = e.OpenArtifactFile(context.Background(), id, list.Artifacts[0].ID, "bin/app", 0)
	if err == nil || !strings.Contains(err.Error(), "removed outside Launchway") {
		t.Fatalf("content missing from its own store: %v", err)
	}
}

// A server that dies mid-upload: whole parts are kept, nothing is visible,
// another server carries the upload on from where it was.
func TestDatabaseStoreUploadCarriesOnAtAnotherServer(t *testing.T) {
	clk := newClock()
	addr := testdb.New(t)
	a := openWith(t, addr, clk, ArtifactOptions{Choice: StoreDatabase}, 0, nil)
	b := openWith(t, addr, clk, ArtifactOptions{Choice: StoreDatabase}, 0, nil)
	create(t, a, artifactsPipeline, Trigger{Event: "manual"})
	j := take(t, a)
	data := big(3*ArtifactChunkBytes+1, 5)
	files := []upFile{{"bin/app", data, 0o755}}
	begun, err := a.BeginArtifact(j.LeaseID, BeginArtifact{Name: "binary", Files: infos(files)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.PutArtifactChunk(context.Background(), j.LeaseID, begun.ID, "bin/app", 0, sum(data[:ArtifactChunkBytes]), data[:ArtifactChunkBytes]); err != nil {
		t.Fatal(err)
	}
	a.Close() // the first server goes away
	var objects int
	b.db.QueryRow(`SELECT COUNT(*) FROM blob_objects WHERE key LIKE 'artifacts/%'`).Scan(&objects)
	if objects != 0 {
		t.Fatal("half an upload is visible as an object")
	}
	again, err := b.BeginArtifact(j.LeaseID, BeginArtifact{Name: "binary", Files: infos(files)})
	if err != nil || again.Files[0].Received != ArtifactChunkBytes {
		t.Fatalf("carrying on: %+v %v", again, err)
	}
	sendAll(t, b, j.LeaseID, again, files)
	if _, err := b.CommitArtifact(context.Background(), j.LeaseID, again.ID); err != nil {
		t.Fatal(err)
	}
	ok(t, b, j, nil)
	d := take(t, b)
	if got := readFetch(t, b, d.LeaseID, d.Fetch[0].ArtifactID, "bin/app"); !bytes.Equal(got, data) {
		t.Fatal("fetched other bytes")
	}
}

// Rolling upgrade or rollback: a server of schema 16 next to servers using
// the database store. It keeps no artifacts (its filesystem lacks the
// marker), leaves alone runs that keep artifacts in the database store,
// and runs everything else.
func TestOlderBuildLeavesDatabaseStoreRunsAlone(t *testing.T) {
	n := schemaBlobs
	if migrations[n-1].compat != additive {
		t.Fatalf("migration %d must be additive", n)
	}
	clk := newClock()
	addr := testdb.New(t)
	cur := openWith(t, addr, clk, ArtifactOptions{Choice: StoreDatabase}, 0, nil)
	kept := create(t, cur, artifactsPipeline, Trigger{Event: "manual"})
	plain := create(t, cur, linear, Trigger{Event: "manual"})
	var oldLog syncBuffer
	old := openWith(t, addr, clk, ArtifactOptions{Choice: StoreAuto, Store: fsStore(t, t.TempDir())}, n-1, &oldLog)
	if old.ArtifactOptions().Store != nil || !strings.Contains(old.ArtifactOptions().StoreProblem, "not the one the other servers use") {
		t.Fatalf("the older server's store: %v %q", old.ArtifactOptions().Store, old.ArtifactOptions().StoreProblem)
	}
	d, err := old.GetRun(kept)
	if err != nil || !d.NeedsNewerServer {
		t.Fatalf("older build's view of a run keeping artifacts in the database: %+v %v", d, err)
	}
	for range 3 {
		j := take(t, old)
		if j.RunID != plain {
			t.Fatalf("older build leased %s/%s", j.RunID, j.StepID)
		}
		ok(t, old, j, nil)
	}
	none(t, old)
	j := take(t, cur)
	keep(t, cur, j.LeaseID, "binary", []upFile{{"bin/app", []byte("x"), 0o755}})
	ok(t, cur, j, nil)
	ok(t, cur, take(t, cur), nil)
	want(t, statuses(t, cur, kept), "run", "succeeded")
}
