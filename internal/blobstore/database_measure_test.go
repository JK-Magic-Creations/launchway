package blobstore

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestDatabaseMeasure measures the database store: upload and download
// throughput for objects of 1 MB, 100 MB and 1 GB, uploaded in the
// engine's 8 MiB parts and read back whole, and how much the database grows.
// The numbers in docs/artifacts.md come from it. It runs only when asked:
//
//	LAUNCHWAY_TEST_POSTGRES=postgres://... LAUNCHWAY_TEST_MEASURE=1 \
//	  go test -run DatabaseMeasure -v -timeout 30m ./internal/blobstore/
func TestDatabaseMeasure(t *testing.T) {
	if os.Getenv("LAUNCHWAY_TEST_MEASURE") == "" {
		t.Skip("set LAUNCHWAY_TEST_MEASURE=1 to measure")
	}
	s, _, db := newDB(t, DatabaseOptions{MaxBytes: 1 << 40, MaxObjectBytes: 4 << 30})
	const part = 8 << 20
	sizes := []int64{1 << 20, 100 << 20, 1 << 30}
	if v := os.Getenv("LAUNCHWAY_TEST_MEASURE_SIZES"); v != "" {
		sizes = nil
		for _, f := range strings.Split(v, ",") {
			var n int64
			fmt.Sscan(f, &n)
			sizes = append(sizes, n<<20)
		}
	}
	t.Logf("database: %s; chunk %d KiB; parts of %d MiB", map[bool]string{true: "PostgreSQL", false: "SQLite"}[s.pg], DatabaseChunkSize>>10, part>>20)
	before := dbSize(t, db, s.pg)
	var lsn string
	if s.pg {
		must(t, db.QueryRow(`SELECT pg_current_wal_lsn()::text`).Scan(&lsn))
	}
	buf := make([]byte, part)
	for _, size := range sizes {
		key := fmt.Sprintf("measure/%d", size)
		// Random content: artifacts are mostly compressed already, and random
		// bytes defeat TOAST's compression, so this is the worst case for size.
		h := sha256.New()
		start := time.Now()
		id, err := s.CreateUpload(ctx, key)
		must(t, err)
		var parts []Part
		for n, left := 1, size; left > 0; n++ {
			chunk := buf[:min(left, part)]
			rand.Read(chunk)
			h.Write(chunk)
			etag, err := s.UploadPart(ctx, key, id, n, chunk)
			must(t, err)
			parts = append(parts, Part{Number: n, ETag: etag})
			left -= int64(len(chunk))
		}
		must(t, s.CompleteUpload(ctx, key, id, parts))
		up := time.Since(start)
		start = time.Now()
		r, err := s.Open(ctx, key, 0, -1)
		must(t, err)
		h2 := sha256.New()
		n, err := io.Copy(h2, r)
		must(t, err)
		r.Close()
		down := time.Since(start)
		if n != size || string(h2.Sum(nil)) != string(h.Sum(nil)) {
			t.Fatalf("%s read back wrong", key)
		}
		grown := dbSize(t, db, s.pg) - before
		t.Logf("%7s: upload %6.1f MB/s (%v), download %6.1f MB/s (%v); store tables now %s over %s stored (%.3fx)",
			mib(size), float64(size)/1e6/up.Seconds(), up.Round(time.Millisecond), float64(size)/1e6/down.Seconds(), down.Round(time.Millisecond),
			mib(grown), mib(total(sizes, size)), float64(grown)/float64(total(sizes, size)))
	}
	if s.pg {
		// The whole server's WAL: other databases' writes count too.
		var wal int64
		must(t, db.QueryRow(`SELECT pg_wal_lsn_diff(pg_current_wal_lsn(), $1::pg_lsn)::bigint`, lsn).Scan(&wal))
		t.Logf("WAL written while uploading: %s", mib(wal))
	}
	for _, size := range sizes {
		must(t, s.Delete(ctx, fmt.Sprintf("measure/%d", size)))
	}
	_, err := s.Collect(ctx)
	must(t, err)
	t.Logf("after deleting everything: store tables %s (space is reused by later writes; VACUUM FULL or pg_repack gives it back to the system)", mib(dbSize(t, db, s.pg)-before))
}

// total is the sum of sizes up to and including last.
func total(sizes []int64, last int64) int64 {
	var n int64
	for _, s := range sizes {
		n += s
		if s == last {
			break
		}
	}
	return n
}

// dbSize is the space the store's tables take, with their TOAST and
// indexes (PostgreSQL), or the whole file (SQLite).
func dbSize(t *testing.T, db *sql.DB, pg bool) int64 {
	t.Helper()
	var n int64
	if pg {
		must(t, db.QueryRow(`SELECT COALESCE(SUM(pg_total_relation_size(c.oid)), 0) FROM pg_class c
			JOIN pg_namespace ns ON ns.oid = c.relnamespace
			WHERE ns.nspname = current_schema() AND c.relkind = 'r' AND c.relname LIKE 'blob_%'`).Scan(&n))
		return n
	}
	must(t, db.QueryRow(`SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&n))
	return n
}
