package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkStepWorkspaceHandOver measures what a pod for each step costs the
// Station besides starting the pod: filling the pod's workspace from the
// Station's copy, and reading it back, for a step that does nothing. The
// "cluster" is the fake of the tests (see stepCluster), whose pod starts at
// once and whose tar runs on this machine, so the figures are the work of the
// Station and of tar, not of a network: a real cluster adds the pod's start
// (a node, an image that is on it) and the transfer through the API server,
// which the notes in docs/development/automatic-resources.md give from a
// rehearsal. Run: go test ./internal/executor -run x -bench HandOver -benchtime 20x
func BenchmarkStepWorkspaceHandOver(b *testing.B) {
	for _, size := range []struct{ files, kib int }{{100, 4}, {2000, 10}, {20000, 4}, {100, 1024}} {
		b.Run(fmt.Sprintf("%d_files_of_%dKiB", size.files, size.kib), func(b *testing.B) {
			ws := b.TempDir()
			data := make([]byte, size.kib<<10)
			for i := 0; i < size.files; i++ {
				p := filepath.Join(ws, fmt.Sprintf("d%d", i%50), fmt.Sprintf("f%d", i))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					b.Fatal(err)
				}
				if err := os.WriteFile(p, data, 0o644); err != nil {
					b.Fatal(err)
				}
			}
			c := newStepCluster(b, KubernetesOptions{})
			s := stepSpec(b, ws, "run_bench", "s", "64Mi", "true")
			if code, _, err := c.run(s); err != nil || code != 0 { // the copy of the checkout is made once
				b.Fatal(code, err)
			}
			b.SetBytes(int64(size.files) * int64(size.kib) << 10)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.LeaseID = fmt.Sprint("lease", i)
				s.Name = "launchway-" + s.LeaseID
				if code, err := c.k.Run(context.Background(), s, func(string, string) {}); err != nil || code != 0 {
					b.Fatal(code, err)
				}
				b.StopTimer() // the fake pod's directory goes with the pod
				entries, _ := os.ReadDir(c.dir)
				for _, e := range entries {
					os.RemoveAll(filepath.Join(c.dir, e.Name()))
				}
				b.StartTimer()
			}
		})
	}
}
