package engine

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// Every change of a project's cache entries takes the project's usage row
// first and the entry second, so servers working on one project wait in one
// order. Ending an entry took the entry and then the usage, the reverse of
// BeginCache, and a server trimming a project to a lowered quota (which does
// not take the usage row before it ends entries) met a server reserving
// bytes there and deadlocked until PostgreSQL gave up on one of them.
//
// A transaction here holds the usage row, as a server reserving bytes
// would, and the trimming pruner ends an entry: it must wait at the usage
// row without having taken the entry, so that the transaction can still
// reach the entry.
// SQLite has one writer at a time, so the order cannot show there.
func TestCacheTrimTakesTheUsageRowBeforeTheEntry(t *testing.T) {
	if os.Getenv(testdb.EnvVar) == "" {
		t.Skip("the order of row locks is only observable on PostgreSQL (" + testdb.EnvVar + ")")
	}
	r := newCacheRig(t)
	setPolicy(t, r.e, "", CachePolicy{Enabled: true, MaxProjectGiB: 4.0 / 1024, MaxEntryGiB: 2.0 / 1024, IdleDays: 7})
	j := r.start("demo", cacheOnMain)
	r.mustSave(j, "pip", cacheKey("big"), big(3*mib/2, 3))
	setPolicy(t, r.e, "", CachePolicy{Enabled: true, MaxProjectGiB: 1.0 / 1024, MaxEntryGiB: 1.0 / 1024, IdleDays: 7})
	var id string
	if err := r.e.db.QueryRow(`SELECT id FROM caches WHERE project = 'demo'`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	tx, err := r.e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE cache_usage SET bytes = bytes WHERE project = 'demo'`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var pruneErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		pruneErr = r.e.trimCaches()
	}()
	time.Sleep(300 * time.Millisecond) // the pruner reaches the usage row and waits

	start := time.Now()
	if _, err := tx.Exec(`UPDATE caches SET hits = hits WHERE id = ?`, id); err != nil {
		t.Fatalf("the entry was taken by the pruner, which waits for the usage row this transaction holds: %v", err)
	}
	if d := time.Since(start); d > testlib.Slow(500*time.Millisecond) {
		t.Fatalf("reaching the entry took %v: the pruner holds it while it waits for the usage row, so the two wait for each other", d)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if pruneErr != nil {
		t.Fatal(pruneErr)
	}
	if got := r.entries("demo"); len(got) != 0 {
		t.Fatalf("the entry over the lowered quota should have been ended: %v", got)
	}
	if got := r.usage("demo"); got != 0 {
		t.Fatalf("usage %d", got)
	}
}
