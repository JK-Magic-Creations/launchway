package blobstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"

	_ "modernc.org/sqlite"
)

// openTestDB opens a database of its own for one test, with the store's
// tables: SQLite, or a schema of its own in PostgreSQL when
// LAUNCHWAY_TEST_POSTGRES is set. It returns a function that opens another
// connection pool to the same database, as another server would.
func openTestDB(t *testing.T) (db *sql.DB, pg bool, another func() *sql.DB) {
	t.Helper()
	addr := testdb.New(t)
	pg = testdb.Postgres()
	open := func() *sql.DB {
		var d *sql.DB
		var err error
		if pg {
			d, err = sql.Open("pgx", addr)
		} else {
			q := url.Values{}
			for _, p := range []string{"busy_timeout(10000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"} {
				q.Add("_pragma", p)
			}
			q.Set("_txlock", "immediate")
			d, err = sql.Open("sqlite", "file:"+addr+"?"+q.Encode())
		}
		must(t, err)
		t.Cleanup(func() { d.Close() })
		return d
	}
	db = open()
	for _, stmt := range strings.Split(RenderSchema(pg, DatabaseSchema), ";\n") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%v\n%s", err, stmt)
		}
	}
	return db, pg, open
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newDB(t *testing.T, o DatabaseOptions) (*Database, func(DatabaseOptions) *Database, *sql.DB) {
	t.Helper()
	db, pg, another := openTestDB(t)
	s, err := NewDatabase(db, pg, o)
	must(t, err)
	return s, func(o DatabaseOptions) *Database {
		s, err := NewDatabase(another(), pg, o)
		must(t, err)
		return s
	}, db
}

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	must(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&n))
	return n
}

// The database store passes the suite every store passes, and leaves
// nothing behind.
func TestDatabaseConformance(t *testing.T) {
	s, _, db := newDB(t, DatabaseOptions{})
	testStore(t, s)
	for _, table := range []string{"blob_objects", "blob_uploads", "blob_parts", "blob_chunks"} {
		if n := count(t, db, table); n != 0 {
			t.Errorf("%d rows left in %s", n, table)
		}
	}
}

// Parts are cut into chunks; ranged reads start and end anywhere,
// including on chunk and part boundaries.
func TestDatabaseChunksAndRanges(t *testing.T) {
	s, _, db := newDB(t, DatabaseOptions{})
	// Parts of unequal size, the first not a multiple of the chunk size.
	parts := [][]byte{randomBytes(2*DatabaseChunkSize + 17), randomBytes(DatabaseChunkSize), randomBytes(5)}
	whole := bytes.Join(parts, nil)
	key := "chunks/x.bin"
	id, err := s.CreateUpload(ctx, key)
	must(t, err)
	var list []Part
	for i, p := range parts {
		etag, err := s.UploadPart(ctx, key, id, i+1, p)
		must(t, err)
		list = append(list, Part{Number: i + 1, ETag: etag})
	}
	must(t, s.CompleteUpload(ctx, key, id, list))
	if n := count(t, db, "blob_chunks"); n != 3+1+1 {
		t.Errorf("%d chunks, want 5", n)
	}
	n := int64(len(whole))
	for _, off := range []int64{0, 1, DatabaseChunkSize - 1, DatabaseChunkSize, 2*DatabaseChunkSize + 16, 2*DatabaseChunkSize + 17, n - 3} {
		for _, length := range []int64{-1, 1, 2, DatabaseChunkSize + 3} {
			got := readAll(t, s, key, off, length)
			to := n
			if length >= 0 {
				to = min(n, off+length)
			}
			if !bytes.Equal(got, whole[off:to]) {
				t.Fatalf("Open(%d, %d): %d bytes differ from bytes %d..%d", off, length, len(got), off, to)
			}
		}
	}
	// Small reads through the reader cross chunks too.
	r, err := s.Open(ctx, key, 0, -1)
	must(t, err)
	var buf bytes.Buffer
	small := make([]byte, 4093)
	for {
		k, err := r.Read(small)
		buf.Write(small[:k])
		if err == io.EOF {
			break
		}
		must(t, err)
	}
	r.Close()
	if !bytes.Equal(buf.Bytes(), whole) {
		t.Fatal("reading in small pieces gave other bytes")
	}
}

// A chunk changed in the database is caught when it is read, with an error
// that says so, and not handed out.
func TestDatabaseDetectsDamage(t *testing.T) {
	s, _, db := newDB(t, DatabaseOptions{})
	data := randomBytes(3 * DatabaseChunkSize)
	putObject(t, s, "damage/x", data)
	bad := bytes.Clone(data[DatabaseChunkSize : 2*DatabaseChunkSize])
	bad[100] ^= 0xff
	if _, err := db.Exec(s.q(`UPDATE blob_chunks SET data = ? WHERE seq = 1`), bad); err != nil {
		t.Fatal(err)
	}
	// The first chunk still reads; the second fails.
	if got := readAll(t, s, "damage/x", 0, DatabaseChunkSize); !bytes.Equal(got, data[:DatabaseChunkSize]) {
		t.Fatal("the undamaged chunk read back wrong")
	}
	r, err := s.Open(ctx, "damage/x", 0, -1)
	must(t, err)
	defer r.Close()
	_, err = io.ReadAll(r)
	if err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("reading a damaged object: %v", err)
	}
}

// Two servers store the same part of one upload at the same moment, and
// two servers complete it at once: the object is one whole copy, and no
// chunk of the losing copy is left behind.
func TestDatabaseConcurrentServers(t *testing.T) {
	a, another, db := newDB(t, DatabaseOptions{})
	b := another(DatabaseOptions{})
	key := "race/x.bin"
	id, err := a.CreateUpload(ctx, key)
	must(t, err)
	first, second := randomBytes(3*DatabaseChunkSize+1), randomBytes(2*DatabaseChunkSize)
	var wg sync.WaitGroup
	etags := make([]string, 2)
	for i, s := range []*Database{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				data := first
				if i == 1 {
					data = second
				}
				e, err := s.UploadPart(ctx, key, id, 1, data)
				if err != nil {
					t.Error(err)
					return
				}
				etags[i] = e
			}
		}()
	}
	wg.Wait()
	// Whichever copy is stored, its chunks are all there and only its.
	var stored string
	must(t, db.QueryRow(a.q(`SELECT sha256 FROM blob_parts WHERE upload_id = ? AND n = 1`), id).Scan(&stored))
	want := first
	if stored == etags[1] {
		want = second
	}
	chunks := count(t, db, "blob_chunks")
	if wantChunks := (len(want) + DatabaseChunkSize - 1) / DatabaseChunkSize; chunks != wantChunks {
		t.Fatalf("%d chunks stored, want %d (a losing copy left chunks behind)", chunks, wantChunks)
	}
	errs := make(chan error, 2)
	for _, s := range []*Database{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.CompleteUpload(ctx, key, id, []Part{{Number: 1, ETag: stored}})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	if got := readAll(t, b, key, 0, -1); !bytes.Equal(got, want) {
		t.Fatal("the object read through the other server differs")
	}
}

// Two uploads of the same key: with the same content both succeed (the
// second's copy is dropped); with other content the second is refused.
func TestDatabaseSameKeyTwice(t *testing.T) {
	s, _, db := newDB(t, DatabaseOptions{})
	data := randomBytes(5000)
	putObject(t, s, "same/k", data)
	putObject(t, s, "same/k", data)
	if err := upload(ctx, s, "same/k", randomBytes(5000)); err == nil || !strings.Contains(err.Error(), "never overwritten") {
		t.Fatalf("other content under the same key: %v", err)
	}
	if got := readAll(t, s, "same/k", 0, -1); !bytes.Equal(got, data) {
		t.Fatal("the object changed")
	}
	_, err := s.Collect(ctx)
	must(t, err)
	if n := count(t, db, "blob_chunks"); n != 1 {
		t.Fatalf("%d chunks after collecting, want the object's 1", n)
	}
}

// A server that dies mid-upload leaves no object, only parts nobody sees;
// two days later Collect removes them. Its finished parts of an upload that
// another server carries on are kept.
func TestDatabaseAbandonedUploads(t *testing.T) {
	clk := &testClock{t: time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)}
	s, _, db := newDB(t, DatabaseOptions{Now: clk.Now})
	dead, err := s.CreateUpload(ctx, "abandon/dead")
	must(t, err)
	_, err = s.UploadPart(ctx, "abandon/dead", dead, 1, randomBytes(2*DatabaseChunkSize))
	must(t, err)
	live, err := s.CreateUpload(ctx, "abandon/live")
	must(t, err)
	_, err = s.UploadPart(ctx, "abandon/live", live, 1, randomBytes(100))
	must(t, err)
	if _, err := s.Stat(ctx, "abandon/dead"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unfinished upload is visible: %v", err)
	}
	clk.Add(AbandonAfter - time.Hour)
	_, err = s.UploadPart(ctx, "abandon/live", live, 2, randomBytes(100)) // still going
	must(t, err)
	clk.Add(2 * time.Hour)
	st, err := s.Collect(ctx)
	must(t, err)
	if st.Abandoned != 1 || st.Purged != 1 {
		t.Fatalf("collect: %+v, want one upload abandoned and purged", st)
	}
	if n := count(t, db, "blob_chunks"); n != 2 {
		t.Fatalf("%d chunks left, want the live upload's 2", n)
	}
	if _, err := s.UploadPart(ctx, "abandon/dead", dead, 2, []byte("late")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a part for an abandoned upload: %v", err)
	}
}

// The limits: a part that would take the store past its total, and an
// object past the largest, are refused with errors that say so.
func TestDatabaseLimits(t *testing.T) {
	s, _, _ := newDB(t, DatabaseOptions{MaxBytes: 3 * DatabaseChunkSize, MaxObjectBytes: 2 * DatabaseChunkSize})
	putObject(t, s, "limits/a", randomBytes(2*DatabaseChunkSize))
	err := upload(ctx, s, "limits/b", randomBytes(DatabaseChunkSize+1))
	if !errors.Is(err, ErrFull) || !strings.Contains(err.Error(), "max_total_gib") {
		t.Fatalf("past the total: %v", err)
	}
	must(t, s.Delete(ctx, "limits/a"))
	u, err := s.Usage(ctx)
	must(t, err)
	if u.UsedBytes != 0 || u.Objects != 0 {
		t.Fatalf("usage after deleting everything: %+v", u)
	}
	// An object past the largest, in two parts.
	id, err := s.CreateUpload(ctx, "limits/c")
	must(t, err)
	_, err = s.UploadPart(ctx, "limits/c", id, 1, randomBytes(DatabaseChunkSize+10))
	must(t, err)
	if _, err := s.UploadPart(ctx, "limits/c", id, 2, randomBytes(DatabaseChunkSize)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("past the largest object: %v", err)
	}
	must(t, s.AbortUpload(ctx, "limits/c", id))
	if _, err := NewDatabase(s.db, s.pg, DatabaseOptions{MaxBytes: 1, MaxObjectBytes: 2}); err == nil {
		t.Fatal("a largest object above the total was accepted")
	}
}

// An object deleted while it is read: the read fails rather than hand out
// part of it.
func TestDatabaseDeletedWhileRead(t *testing.T) {
	s, _, _ := newDB(t, DatabaseOptions{})
	putObject(t, s, "gone/x", randomBytes(3*DatabaseChunkSize))
	r, err := s.Open(ctx, "gone/x", 0, -1)
	must(t, err)
	defer r.Close()
	buf := make([]byte, DatabaseChunkSize)
	_, err = io.ReadFull(r, buf)
	must(t, err)
	must(t, s.Delete(ctx, "gone/x"))
	if _, err := io.ReadAll(r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reading on after the delete: %v", err)
	}
}

func TestDatabaseKeys(t *testing.T) {
	s, _, _ := newDB(t, DatabaseOptions{})
	for i := range 5 {
		putObject(t, s, fmt.Sprintf("artifacts/k%d", i), []byte("x"))
	}
	putObject(t, s, "artifactsx/other", []byte("x"))
	putObject(t, s, "launchway-marker/m_1", []byte("x"))
	keys, err := s.Keys(context.Background(), "artifacts/", "", 3)
	must(t, err)
	if strings.Join(keys, ",") != "artifacts/k0,artifacts/k1,artifacts/k2" {
		t.Fatalf("first page %v", keys)
	}
	keys, err = s.Keys(context.Background(), "artifacts/", keys[2], 10)
	must(t, err)
	if strings.Join(keys, ",") != "artifacts/k3,artifacts/k4" {
		t.Fatalf("second page %v", keys)
	}
	keys, err = s.Keys(context.Background(), "launchway-marker/m_", "", 10)
	must(t, err)
	if len(keys) != 1 {
		t.Fatalf("an underscore in the prefix: %v", keys)
	}
}
