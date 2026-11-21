package storagespike

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(float64(len(s)-1) * p)
	return s[i]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// Result is one line of the measurement table.
type Result struct {
	System        string  `json:"system"`
	SmallPushP50  float64 `json:"small_push_p50_ms"`
	SmallPushP95  float64 `json:"small_push_p95_ms"`
	SmallCloneP50 float64 `json:"small_clone_p50_ms"`
	ColdSyncSmall float64 `json:"small_cold_clone_ms,omitempty"`
	LargeMB       int     `json:"large_mb,omitempty"`
	LargePushS    float64 `json:"large_push_s,omitempty"`
	LargeWarmS    float64 `json:"large_warm_clone_s,omitempty"`
	LargeColdS    float64 `json:"large_cold_clone_s,omitempty"`
	TinyOnLargeMS float64 `json:"tiny_push_on_large_ms,omitempty"`
	OtherServerS  float64 `json:"tiny_then_other_server_clone_s,omitempty"`
	RawMB         float64 `json:"raw_mb,omitempty"`
	StoredMB      float64 `json:"stored_mb,omitempty"`
	DBMB          float64 `json:"database_mb,omitempty"`
	BlobMB        float64 `json:"blob_store_mb,omitempty"`
	CacheMB       float64 `json:"cache_mb,omitempty"`
	VolumeMB      float64 `json:"volume_mb,omitempty"`
}

// TestMeasure is the measurement run of the spike report. It is opt-in
// (FORGE_SPIKE_MEASURE=1) and writes its table as JSON to FORGE_SPIKE_OUT.
// FORGE_SPIKE_LARGE_MB (default 500) is the size of the large repository.
func TestMeasure(t *testing.T) {
	if os.Getenv("FORGE_SPIKE_MEASURE") == "" {
		t.Skip("set FORGE_SPIKE_MEASURE=1 to measure")
	}
	ctx := context.Background()
	largeMB := envInt("FORGE_SPIKE_LARGE_MB", 500)
	var all []Result
	for _, kind := range []string{"A", "A-fsync", "B-fs", "B-db", "B-fs-verify"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			addr := filepath.Join(root, "ledger.db")
			var sys System
			var err error
			switch kind {
			case "A":
				sys, err = NewVolumeSystem(ctx, root, addr, "rep1", "")
			case "A-fsync":
				sys, err = NewVolumeSystem(ctx, root, addr, "rep1", "all")
			case "B-fs", "B-fs-verify":
				ls, e := NewLedgerSystem(ctx, root, addr, "fs", "rep1")
				if ls != nil {
					ls.VerifyUpload = kind == "B-fs-verify"
				}
				sys, err = ls, e
			case "B-db":
				sys, err = NewLedgerSystem(ctx, root, addr, "db", "rep1")
			}
			if err != nil {
				t.Fatal(err)
			}
			c, _ := NewClient(ctx, filepath.Join(root, "client"))
			e := &env{sys: sys, c: c, system: kind}
			s1, _ := sys.NewServer("s1")
			s2, _ := sys.NewServer("s2")
			r := Result{System: kind}

			// Small repository: a push of a few kilobytes, a clone.
			var pushes, clones []time.Duration
			for i := 0; i < 41; i++ {
				out, _ := e.commitAndPush(t, s1, Fault{}, 4096)
				if !out.Acked {
					t.Fatalf("%s", out.Output)
				}
				if i > 0 {
					pushes = append(pushes, out.Duration)
				}
			}
			for i := 0; i < 15; i++ {
				start := time.Now()
				if err := s1.Clone(ctx, filepath.Join(root, fmt.Sprintf("clone-%d.git", i))); err != nil {
					t.Fatal(err)
				}
				clones = append(clones, time.Since(start))
			}
			r.SmallPushP50, r.SmallPushP95, r.SmallCloneP50 = ms(pct(pushes, .5)), ms(pct(pushes, .95)), ms(pct(clones, .5))
			if _, err := WipeLocalOrNil(s2); err == nil {
				start := time.Now()
				if err := s2.Clone(ctx, filepath.Join(root, "cold-small.git")); err != nil {
					t.Fatal(err)
				}
				r.ColdSyncSmall = ms(time.Since(start))
			}

			// Large repository: about largeMB of data that does not compress.
			if largeMB > 0 {
				r.LargeMB = largeMB
				for i := 0; i < largeMB/10; i++ {
					if err := writeRandom(filepath.Join(c.Dir, fmt.Sprintf("big%03d.bin", i)), 10<<20, int64(1000+i)); err != nil {
						t.Fatal(err)
					}
				}
				c.git(ctx, "add", "-A")
				c.git(ctx, "commit", "-q", "-m", "large")
				start := time.Now()
				out := e.push(t, s1, Fault{})
				if !out.Acked {
					t.Fatalf("large push: %s", out.Output)
				}
				r.LargePushS = time.Since(start).Seconds()
				start = time.Now()
				if err := s1.Clone(ctx, filepath.Join(root, "large-warm.git")); err != nil {
					t.Fatal(err)
				}
				r.LargeWarmS = time.Since(start).Seconds()
				os.RemoveAll(filepath.Join(root, "large-warm.git"))
				// A tiny push on top of the large repository, then the other
				// server (one push behind, 500 MB already in its cache) serves it.
				warmOther := filepath.Join(root, "other-warm.git")
				s2.Clone(ctx, warmOther) // brings s2 current
				os.RemoveAll(warmOther)
				out, _ = e.commitAndPush(t, s1, Fault{}, 4096)
				r.TinyOnLargeMS = ms(out.Duration)
				start = time.Now()
				s2.Clone(ctx, filepath.Join(root, "other-after.git"))
				r.OtherServerS = time.Since(start).Seconds()
				os.RemoveAll(filepath.Join(root, "other-after.git"))
				// Cold: everything local is lost, then a fetch.
				if kind[0] == 'B' {
					sys.LoseNode(ctx)
					start = time.Now()
					if err := s1.Clone(ctx, filepath.Join(root, "large-cold.git")); err != nil {
						t.Fatal(err)
					}
					r.LargeColdS = time.Since(start).Seconds()
				}
			}
			// Storage.
			raw := float64(DirSize(c.Dir)) - float64(DirSize(filepath.Join(c.Dir, ".git")))
			r.RawMB = raw / 1e6
			mb := func(dir string) float64 { return float64(DirSize(dir)) / 1e6 }
			switch s := sys.(type) {
			case *VolumeSystem:
				r.VolumeMB = mb(s.Repo())
				r.StoredMB = r.VolumeMB
				r.DBMB = float64(fileSize(addr)+fileSize(addr+"-wal")) / 1e6
			case *LedgerSystem:
				r.DBMB = float64(fileSize(addr)+fileSize(addr+"-wal")) / 1e6
				if s.BlobKind == "fs" {
					r.BlobMB = mb(filepath.Join(root, "blobs"))
				}
				r.CacheMB = mb(filepath.Join(root, "servers", "s1"))
				r.StoredMB = r.BlobMB + r.DBMB
			}
			all = append(all, r)
			data, _ := json.Marshal(r)
			t.Logf("RESULT %s", data)
		})
	}
	if out := os.Getenv("FORGE_SPIKE_OUT"); out != "" {
		data, _ := json.MarshalIndent(all, "", "  ")
		os.WriteFile(out, data, 0o644)
	}
}

func fileSize(p string) int64 {
	if st, err := os.Stat(p); err == nil {
		return st.Size()
	}
	return 0
}
