package gitops

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/deploy/edit"
)

// One manifest file holds eight services' images, as apps/mcp-servers/base.yaml
// does, with comments. Eight deploy runners (eight clones, no lock between
// them) write their own service at the same moment. Every change lands, none
// overwrites another's, and the file keeps its comments.
func TestEightWritersOneFileNoneLost(t *testing.T) {
	s := newServer(t)
	var doc strings.Builder
	doc.WriteString("# the MCP servers\nitems:\n")
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&doc, "  - name: svc%d   # service %d\n    image: harbor.example.com/homelab/svc%d:old\n", i, i, i)
	}
	s.commit(map[string]string{"base.yaml": doc.String()}, "Seed the manifest")

	digest := func(i int) string { return fmt.Sprintf("sha256:%064x", i+1) }
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &Repo{URL: s.path, Branch: "main", Dir: filepath.Join(t.TempDir(), fmt.Sprintf("clone%d", i)),
				Author: Author{Name: "Launchway", Email: "launchway@example.com"}}
			_, errs[i] = r.Apply(context.Background(), ChangeSet{
				Deployment: fmt.Sprintf("dep_%d", i),
				Message:    fmt.Sprintf("deploy(svc%d)", i),
				Files: []FileEdit{{Path: "base.yaml", Edit: func(_ string, data []byte) ([]byte, error) {
					out, _, err := edit.ImageTagDigest(data, fmt.Sprintf("homelab/svc%d", i), fmt.Sprintf("2026112%d-abcdef12", i), digest(i))
					return out, err
				}}},
			})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	got := s.file("base.yaml")
	for i := 0; i < 8; i++ {
		want := fmt.Sprintf("image: harbor.example.com/homelab/svc%d:2026112%d-abcdef12@%s", i, i, digest(i))
		if !strings.Contains(got, want) {
			t.Errorf("service %d is missing from the file:\n%s", i, got)
		}
		if !strings.Contains(got, fmt.Sprintf("# service %d", i)) {
			t.Errorf("the comment of service %d was lost", i)
		}
	}
	if !strings.HasPrefix(got, "# the MCP servers\n") {
		t.Error("the leading comment was lost")
	}
	// One commit for each writer on top of the two earlier ones; none was rewritten.
	if n := run(t, s.path, "rev-list", "--count", "main"); n != "10" {
		t.Errorf("%s commits on main, want 10", n)
	}
}
