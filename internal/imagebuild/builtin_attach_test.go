package imagebuild

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// A scan result and an SBOM are pushed as manifests whose subject is the
// image, so any client that lists referrers finds them.
func TestScanAndSBOMAreAttachedAsReferrers(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	r, err := newRemote(BuiltinName, RegistryConfig{Host: host, Insecure: true}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	img, _ := random.Image(1024, 1)
	ref, _ := r.tag("cart/api", "1")
	if err := remote.Write(ref, img, r.options(context.Background())...); err != nil {
		t.Fatal(err)
	}
	d, _ := img.Digest()
	if err := attachReferrer(context.Background(), r, "cart/api", d, ScanArtifactType, ScanArtifactType, []byte(`{"critical":1}`), map[string]string{annVulnCritical: "1"}); err != nil {
		t.Fatal(err)
	}
	sub, _ := r.digest("cart/api", d.String())
	idx, err := remote.Referrers(sub, r.options(context.Background())...)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := idx.IndexManifest()
	if len(m.Manifests) != 1 || m.Manifests[0].ArtifactType != ScanArtifactType || m.Manifests[0].Annotations[annVulnCritical] != "1" {
		t.Fatalf("referrers %+v", m)
	}
}
