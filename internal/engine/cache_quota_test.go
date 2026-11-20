package engine

import (
	"errors"
	"testing"
	"time"
)

func (r *cacheRig) usage(project string) int64 {
	r.t.Helper()
	var used int64
	if err := r.e.db.QueryRow(`SELECT bytes FROM cache_usage WHERE project = ?`, project).Scan(&used); err != nil {
		r.t.Fatal(err)
	}
	return used
}

func (r *cacheRig) begin(j *Job, key string, n int64) (*BegunCache, error) {
	r.t.Helper()
	return r.e.BeginCache(j.LeaseID, BeginCache{Name: "pip", Key: cacheKey(key), Platform: linux, Bytes: n, Files: 1, SHA256: sum([]byte(key))})
}

// An entry that is being uploaded takes room in the project's quota from the
// moment it is declared. Otherwise a loop of declarations, none of them ever
// finished, would park the entry limit's worth of bytes for each key for
// the 48 hours an upload may stall.
func TestCacheUploadsCountAgainstTheQuota(t *testing.T) {
	r := newCacheRig(t)
	setPolicy(t, r.e, "", CachePolicy{Enabled: true, MaxProjectGiB: 3.5 / 1024, MaxEntryGiB: 2.0 / 1024, IdleDays: 7})
	j := r.start("demo", cacheOnMain)
	var ids []string
	for _, k := range []string{"a", "b", "c"} {
		b, err := r.begin(j, k, mib)
		if err != nil {
			t.Fatalf("declaring %s: %v", k, err)
		}
		ids = append(ids, b.ID)
	}
	if got := r.usage("demo"); got != 3*mib {
		t.Fatalf("three uploads in progress are charged %d bytes, want %d", got, 3*mib)
	}
	if _, err := r.begin(j, "d", mib); !errors.Is(err, ErrQuota) {
		t.Fatalf("a fourth declaration, past the quota with nothing kept to end: %v", err)
	}
	if got := r.usage("demo"); got != 3*mib {
		t.Fatalf("a refused declaration is not charged: %d", got)
	}
	l, err := r.e.Caches("demo")
	if err != nil || l.Totals.Bytes != 3*mib {
		t.Fatalf("the project's listing counts the uploads against the quota: %+v %v", l.Totals, err)
	}

	// Ending one gives its bytes back, and so does one that stalled.
	if _, err := r.e.DeleteCaches("demo", ids[0], "", "admin"); err != nil {
		t.Fatal(err)
	}
	if got := r.usage("demo"); got != 2*mib {
		t.Fatalf("a deleted upload gives its bytes back: %d", got)
	}
	if _, err := r.begin(j, "d", mib); err != nil {
		t.Fatalf("room again: %v", err)
	}
	r.advance(cacheStaleUpload + time.Hour)
	r.e.pruneCaches()
	if got := r.usage("demo"); got != 0 {
		t.Fatalf("uploads that stalled give their bytes back: %d", got)
	}
	// And one whose bytes did not match what was declared.
	b, err := r.begin(j, "e", mib)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.PutCacheChunk(t.Context(), j.LeaseID, b.ID, 0, sum(make([]byte, mib)), make([]byte, mib)); err == nil {
		t.Fatal("a mismatching archive was accepted")
	}
	if got := r.usage("demo"); got != 0 {
		t.Fatalf("a refused upload gives its bytes back: %d", got)
	}
	// Entries that finish keep counting until they end.
	r.mustSave(j, "pip", cacheKey("kept"), big(mib, 1))
	if got := r.usage("demo"); got != mib {
		t.Fatalf("a kept entry is counted once: %d", got)
	}
}

// When a project is over its quota the entries of pull requests and branches
// go before the default branch's: every run restores the default branch's,
// and many pull requests must not be able to push it out.
func TestCacheEvictionSparesTheDefaultScope(t *testing.T) {
	r := newCacheRig(t)
	setPolicy(t, r.e, "", CachePolicy{Enabled: true, MaxProjectGiB: 3.5 / 1024, MaxEntryGiB: 2.0 / 1024, IdleDays: 7})
	main := r.start("demo", cacheOnMain)
	r.mustSave(main, "pip", cacheKey("m1"), big(mib, 1)) // the least recently used of all
	r.advance(time.Minute)
	r.mustSave(main, "pip", cacheKey("m2"), big(mib, 2))
	for i, n := range []int{5, 6, 7, 8} {
		pr := r.start("demo", cacheOnPull(n))
		r.advance(time.Minute)
		b, _, err := r.save(r.e, pr, "pip", cacheKey("p"+string(rune('a'+i))), big(mib, byte(10+i)))
		if err != nil {
			t.Fatalf("pull request %d: %v", n, err)
		}
		if i > 0 && b.Evicted != 1 {
			t.Errorf("pull request %d ended %d entries, want 1", n, b.Evicted)
		}
		ok(t, r.e, pr, nil)
	}
	have := map[string]string{}
	for _, v := range r.entries("demo") {
		have[v.Key] = v.Scope
	}
	for _, k := range []string{"m1", "m2"} {
		if have[cacheKey(k)] != "default" {
			t.Errorf("the default branch's %s was evicted by pull requests: %v", k, have)
		}
	}
	if len(have) != 3 {
		t.Errorf("%d entries kept, want the two of main and the newest pull request's: %v", len(have), have)
	}

	// Lowering the quota trims the same way.
	setPolicy(t, r.e, "", CachePolicy{Enabled: true, MaxProjectGiB: 2.5 / 1024, MaxEntryGiB: 2.0 / 1024, IdleDays: 7})
	if err := r.e.trimCaches(); err != nil {
		t.Fatal(err)
	}
	have = map[string]string{}
	for _, v := range r.entries("demo") {
		have[v.Key] = v.Scope
	}
	if len(have) != 2 || have[cacheKey("m1")] == "" || have[cacheKey("m2")] == "" {
		t.Errorf("trimming ended the default branch's entries first: %v", have)
	}
}
