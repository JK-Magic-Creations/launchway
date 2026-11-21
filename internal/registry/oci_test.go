package registry_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"

	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/jk-magic-creations/launchway/internal/registry"
)

func (e *env) auth_(secret string) remote.Option {
	return remote.WithAuth(&authn.Basic{Username: "u", Password: secret})
}

func TestPushPullRoundTripWithTheRegistryClient(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	ref, err := name.ParseReference(e.host()+"/cart/api:1.0", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	img, err := random.Image(1<<20, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img, e.auth_("tok")); err != nil {
		t.Fatal(err)
	}
	got, err := remote.Image(ref, e.auth_("tok"))
	if err != nil {
		t.Fatal(err)
	}
	d1, _ := img.Digest()
	d2, _ := got.Digest()
	if d1 != d2 {
		t.Fatalf("digest %s != %s", d1, d2)
	}
}

func (e *env) ref(t *testing.T, s string) name.Reference {
	t.Helper()
	r, err := name.ParseReference(e.host()+"/"+s, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestIndexMultiArchTagsCatalogDeleteAndHead(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart", "shop")})
	idx, err := random.Index(2<<10, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(e.ref(t, "cart/web:v1").(name.Tag), idx, e.auth_("tok")); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"a", "b", "c", "d", "e"} {
		img, _ := random.Image(512, 1)
		if err := remote.Write(e.ref(t, "cart/web:"+tag), img, e.auth_("tok")); err != nil {
			t.Fatal(err)
		}
	}
	// A tag list reads through pages of 2 (the client follows the Link header).
	tags, err := remote.List(e.ref(t, "cart/web:v1").Context(), e.auth_("tok"))
	if err != nil || len(tags) != 6 {
		t.Fatalf("tags %v %v", tags, err)
	}
	h, err := remote.Head(e.ref(t, "cart/web:v1"), e.auth_("tok"))
	want, _ := idx.Digest()
	if err != nil || h.Digest != want {
		t.Fatalf("head %v %v, want %s", h, err, want)
	}
	cat, err := remote.Catalog(t.Context(), mustRegistry(t, e), e.auth_("tok"))
	if err != nil || len(cat) != 1 || cat[0] != "cart/web" {
		t.Fatalf("catalog %v %v", cat, err)
	}
	im, err := remote.Index(e.ref(t, "cart/web:v1"), e.auth_("tok"))
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := im.IndexManifest(); len(m.Manifests) != 3 {
		t.Fatalf("children %d", len(m.Manifests))
	}
	info, err := e.svc.InspectImage(t.Context(), "cart/web", "v1")
	if err != nil || len(info.Children) != 3 || info.TotalSize < 4<<10 {
		t.Fatalf("inspect %+v %v", info, err)
	}
	// Delete by digest removes the image and its tag.
	if err := remote.Delete(e.ref(t, "cart/web@"+want.String()), e.auth_("tok")); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Head(e.ref(t, "cart/web:v1"), e.auth_("tok")); err == nil {
		t.Fatal("deleted image still found")
	}
	if len(e.eventsOf("push")) < 6 || len(e.eventsOf("delete")) != 1 {
		t.Fatalf("events: %d pushes, %d deletes", len(e.eventsOf("push")), len(e.eventsOf("delete")))
	}
}

func mustRegistry(t *testing.T, e *env) name.Registry {
	t.Helper()
	r, err := name.NewRegistry(e.host(), name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReferrersListSignaturesAndSBOMs(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	subject := e.pushImage(t, "tok", "cart/api", "1", []byte("layer bytes"))
	sbom := e.pushReferrer(t, "tok", "cart/api", subject, "application/vnd.cyclonedx+json", map[string]string{"org.example": "x"})
	scan := e.pushReferrer(t, "tok", "cart/api", subject, registry.TypeScan, map[string]string{registry.AnnVulnCritical: "2", registry.AnnVulnHigh: "5", registry.AnnScanner: "trivy"})
	res, b := e.raw(t, "tok", "GET", "/v2/cart/api/referrers/"+subject, nil, nil)
	e.expect(t, res, b, 200)
	var idx struct {
		Manifests []struct {
			Digest       string `json:"digest"`
			ArtifactType string `json:"artifactType"`
		} `json:"manifests"`
	}
	_ = json.Unmarshal(b, &idx)
	if len(idx.Manifests) != 2 {
		t.Fatalf("referrers %s", b)
	}
	res, b = e.raw(t, "tok", "GET", "/v2/cart/api/referrers/"+subject+"?artifactType=application/spdx%2Bjson", nil, nil)
	e.expect(t, res, b, 200)
	if res.Header.Get("OCI-Filters-Applied") != "artifactType" || bytes.Contains(b, []byte(sbom)) {
		t.Fatalf("filtered: %s %v", b, res.Header)
	}
	img, err := e.svc.InspectImage(t.Context(), "cart/api", "1")
	if err != nil || !img.HasSBOM || len(img.Attached) != 2 || img.Vulnerabilities == nil || img.Vulnerabilities.Critical != 2 || img.Vulnerabilities.High != 5 {
		t.Fatalf("inspect %+v %v", img, err)
	}
	// Attachments are not images of their own in the list.
	list, err := e.svc.Images(t.Context(), "cart/api")
	if err != nil || len(list) != 1 {
		t.Fatalf("images %d %v", len(list), err)
	}
	// Deleting the image takes the attachments with it.
	gone, err := e.svc.DeleteManifest(t.Context(), "cart/api", subject, registryWho())
	if err != nil || len(gone) != 3 {
		t.Fatalf("deleted %v %v", gone, err)
	}
	for _, d := range []string{sbom, scan} {
		if _, _, err := e.svc.ResolveManifest(t.Context(), "cart/api", d, false); err == nil {
			t.Fatal("an attachment survived its image")
		}
	}
}
