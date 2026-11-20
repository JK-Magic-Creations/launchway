package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
)

// The bytes of an entry that ended stay for a grace period so that a restore
// that began before it ended can finish. A slow download takes longer than
// any fixed grace, so they also stay while one is open, and for a grace
// period after the last byte it read.
func TestCacheBytesStayWhileADownloadIsOpen(t *testing.T) {
	r := newCacheRig(t)
	store, err := r.e.storeOrErr()
	if err != nil {
		t.Fatal(err)
	}
	data := big(3*mib, 9)
	j := r.start("demo", cacheOnMain)
	r.mustSave(j, "pip", cacheKey("slow"), data)
	en := r.lookup(j, "pip", cacheKey("slow"), "")
	if en == nil {
		t.Fatal("no entry")
	}
	var blobKey string
	if err := r.e.db.QueryRow(`SELECT blob_key FROM caches WHERE id = ?`, en.ID).Scan(&blobKey); err != nil {
		t.Fatal(err)
	}

	body, _, _, err := r.e.OpenCache(context.Background(), j.LeaseID, en.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 100)
	if _, err := io.ReadFull(body, got); err != nil {
		t.Fatal(err)
	}
	// The entry is deleted while the reader is slow, and much longer than
	// the grace period passes.
	if _, err := r.e.DeleteCaches("demo", en.ID, "", "ann"); err != nil {
		t.Fatal(err)
	}
	r.advance(3 * cacheEndedGrace)
	r.e.pruneCaches()
	if _, err := store.Stat(context.Background(), blobKey); err != nil {
		t.Fatalf("the bytes were removed under a download in progress: %v", err)
	}
	rest, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(append(got, rest...), data) {
		t.Fatalf("the slow reader got %d bytes of %d: %v", len(got)+len(rest), len(data), err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	// Once it is closed and the grace period has passed, they go.
	r.e.pruneCaches()
	if _, err := store.Stat(context.Background(), blobKey); err != nil {
		t.Fatalf("the bytes went the moment the download ended, without the grace period: %v", err)
	}
	r.advance(cacheEndedGrace + time.Minute)
	r.e.pruneCaches()
	if _, err := store.Stat(context.Background(), blobKey); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("the bytes remain after the download and the grace period: %v", err)
	}
}

// Another server's pruner cannot see this server's open downloads, but sees
// the entry used: the download says so while it reads.
func TestCacheDownloadSaysItIsInProgress(t *testing.T) {
	r := newCacheRig(t)
	j := r.start("demo", cacheOnMain)
	r.mustSave(j, "pip", cacheKey("beat"), big(3*mib, 2))
	en := r.lookup(j, "pip", cacheKey("beat"), "")
	body, _, _, err := r.e.OpenCache(context.Background(), j.LeaseID, en.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	last := func() int64 {
		var at int64
		if err := r.e.db.QueryRow(`SELECT last_used_at FROM caches WHERE id = ?`, en.ID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	before := last()
	r.advance(5 * time.Minute)
	if _, err := io.ReadFull(body, make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	if after := last(); after <= before {
		t.Fatalf("a download that reads %v later left last_used_at at %d", 5*time.Minute, after)
	}
}
