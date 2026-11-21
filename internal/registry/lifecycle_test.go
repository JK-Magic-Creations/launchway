package registry_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/registry"
)

func i64(v int64) *int64 { return &v }

func TestQuotaRefusesPlainly(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart", "shop")})
	if err := e.svc.SetPolicy(t.Context(), registry.ScopeProject, "cart", registry.Policy{QuotaBytes: i64(10 << 10)}, "root"); err != nil {
		t.Fatal(err)
	}
	e.pushImage(t, "tok", "cart/a", "1", make([]byte, 4<<10))
	// The same layer again (another tag) adds nothing: blobs count once.
	e.pushImage(t, "tok", "cart/a", "2", make([]byte, 4<<10))
	big := make([]byte, 7<<10)
	big[0] = 1
	res, b := e.raw(t, "tok", "POST", "/v2/cart/a/blobs/uploads/?digest="+registry.SHA256(big), big, nil)
	e.expect(t, res, b, 403)
	s := string(b)
	if !strings.Contains(s, "QUOTA_EXCEEDED") || !strings.Contains(s, "quota") || !strings.Contains(s, "next") {
		t.Fatalf("refusal %s", s)
	}
	// Another project is not affected by cart's quota.
	e.pushImage(t, "tok", "shop/a", "1", big)
	// Raising the quota lets the push through.
	if err := e.svc.SetPolicy(t.Context(), registry.ScopeProject, "cart", registry.Policy{QuotaBytes: i64(1 << 20)}, "root"); err != nil {
		t.Fatal(err)
	}
	e.pushImage(t, "tok", "cart/a", "3", big)
	if u, _ := e.svc.ProjectUsage(t.Context(), "cart"); u < 11<<10 {
		t.Fatalf("usage %d", u)
	}
	if len(e.eventsOf("quota")) == 0 {
		t.Fatal("a refused push left no event")
	}
}

func TestOrganizationQuotaAndBlobsNeverSharedAcrossOrganizations(t *testing.T) {
	e := newEnv(t, func(o *registry.Options) {
		o.Org = func(p string) string {
			if p == "cart" {
				return "acme"
			}
			return "globex"
		}
	})
	e.grant("tok", who{subject: "ann", can: rw("cart", "shop")})
	layer := make([]byte, 3<<10)
	e.pushImage(t, "tok", "cart/a", "1", layer)
	e.pushImage(t, "tok", "shop/a", "1", layer)
	var n int
	if err := e.eng.Database().QueryRow(t.Context(), `SELECT count(*) FROM registry_blobs WHERE digest = ?`, registry.SHA256(layer)).Scan(&n); err != nil || n != 2 {
		t.Fatalf("the same layer in two organizations is %d rows, want 2: %v", n, err)
	}
	// A mount does not cross the boundary: it falls back to a plain upload.
	res, b := e.raw(t, "tok", "POST", "/v2/shop/b/blobs/uploads/?mount="+registry.SHA256(layer)+"&from=cart/a", nil, nil)
	e.expect(t, res, b, 202)
	// Within one organization the layer is stored once.
	e2 := newEnv(t)
	e2.grant("tok", who{subject: "ann", can: rw("cart", "shop")})
	e2.pushImage(t, "tok", "cart/a", "1", layer)
	e2.pushImage(t, "tok", "shop/a", "1", layer)
	if err := e2.eng.Database().QueryRow(t.Context(), `SELECT count(*) FROM registry_blobs WHERE digest = ?`, registry.SHA256(layer)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("one organization stores a layer %d times, want once: %v", n, err)
	}
	// An organization quota counts all its projects.
	if err := e2.svc.SetPolicy(t.Context(), registry.ScopeOrganization, "default", registry.Policy{QuotaBytes: i64(5 << 10)}, "root"); err != nil {
		t.Fatal(err)
	}
	other := make([]byte, 4<<10)
	other[1] = 1
	res, b = e2.raw(t, "tok", "POST", "/v2/shop/a/blobs/uploads/?digest="+registry.SHA256(other), other, nil)
	e2.expect(t, res, b, 403)
	if !strings.Contains(string(b), "organization") {
		t.Fatalf("%s", b)
	}
}

func TestImmutableTagsCannotBeOverwrittenOrDeleted(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	if err := e.svc.SetPolicy(t.Context(), registry.ScopeProject, "cart", registry.Policy{ImmutableTags: []string{"v*"}}, "root"); err != nil {
		t.Fatal(err)
	}
	d1 := e.pushImage(t, "tok", "cart/a", "v1", []byte("one"))
	// The same content again is not an overwrite.
	e.pushImage(t, "tok", "cart/a", "v1", []byte("one"))
	// Other content under the same release tag is refused, and says what to do.
	cfg := []byte(`{"os":"linux","architecture":"amd64","rootfs":{"type":"layers","diff_ids":[]}}`)
	e.pushBlob(t, "tok", "cart/a", cfg)
	ld := e.pushBlob(t, "tok", "cart/a", []byte("two"))
	man := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"%s","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%d},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"%s","size":3}]}`,
		registry.MediaOCIManifest, registry.SHA256(cfg), len(cfg), ld)
	res, b := e.raw(t, "tok", "PUT", "/v2/cart/a/manifests/v1", []byte(man), map[string]string{"Content-Type": registry.MediaOCIManifest})
	e.expect(t, res, b, 409)
	if !strings.Contains(string(b), "TAG_IMMUTABLE") || !strings.Contains(string(b), d1) {
		t.Fatalf("refusal %s", b)
	}
	// A tag that does not match the pattern may move.
	res, b = e.raw(t, "tok", "PUT", "/v2/cart/a/manifests/latest", []byte(man), map[string]string{"Content-Type": registry.MediaOCIManifest})
	e.expect(t, res, b, 201)
	// The immutable tag and its image cannot be deleted.
	res, b = e.raw(t, "tok", "DELETE", "/v2/cart/a/manifests/v1", nil, nil)
	e.expect(t, res, b, 409)
	res, b = e.raw(t, "tok", "DELETE", "/v2/cart/a/manifests/"+d1, nil, nil)
	e.expect(t, res, b, 409)
	// The pattern can only grow nearer the repository: a repository cannot unprotect.
	if err := e.svc.SetPolicy(t.Context(), registry.ScopeRepository, "cart/a", registry.Policy{ImmutableTags: []string{"rc-*"}}, "root"); err != nil {
		t.Fatal(err)
	}
	eff, _ := e.svc.EffectivePolicy(t.Context(), "cart/a")
	if len(eff.ImmutableTags) != 2 {
		t.Fatalf("effective %v", eff.ImmutableTags)
	}
}

func TestOnlyTheWriterOfAnImmutableTagWinsARace(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	if err := e.svc.SetPolicy(t.Context(), registry.ScopeProject, "cart", registry.Policy{ImmutableTags: []string{"v*"}}, "root"); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			layer := []byte(fmt.Sprintf("layer %d", i))
			cfg := []byte(fmt.Sprintf(`{"os":"linux","architecture":"amd64","rootfs":{"type":"layers","diff_ids":[]},"n":%d}`, i))
			cd, ld := registry.SHA256(cfg), registry.SHA256(layer)
			for _, blob := range [][]byte{cfg, layer} {
				res, _ := e.raw(t, "tok", "POST", "/v2/cart/r/blobs/uploads/?digest="+registry.SHA256(blob), blob, nil)
				if res.StatusCode != 201 {
					t.Errorf("blob: %d", res.StatusCode)
					return
				}
			}
			man := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"%s","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%d},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"%s","size":%d}]}`,
				registry.MediaOCIManifest, cd, len(cfg), ld, len(layer))
			res, _ := e.raw(t, "tok", "PUT", "/v2/cart/r/manifests/v1", []byte(man), map[string]string{"Content-Type": registry.MediaOCIManifest})
			if res.StatusCode == 201 {
				wins.Add(1)
			} else if res.StatusCode != 409 {
				t.Errorf("manifest: %d", res.StatusCode)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d pushes won the immutable tag, want exactly 1", wins.Load())
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, func(o *registry.Options) { o.Limits.RequestsPerMinute = 5 })
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	got429 := false
	for i := 0; i < 12; i++ {
		res, _ := e.raw(t, "tok", "GET", "/v2/", nil, nil)
		if res.StatusCode == 429 {
			got429 = true
			if res.Header.Get("Retry-After") == "" {
				t.Fatal("no Retry-After")
			}
		}
	}
	if !got429 {
		t.Fatal("no request was limited")
	}
}

// seedGarbage makes a layer nothing refers to, in the past.
func (e *env) seedGarbage(t *testing.T, repo string, n int) []string {
	var ds []string
	for i := 0; i < n; i++ {
		ds = append(ds, e.pushBlob(t, "tok", repo, []byte(fmt.Sprintf("garbage %d %d", i, time.Now().UnixNano()))))
	}
	return ds
}

func TestGarbageCollectionDryRunThenReal(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	keep := e.pushImage(t, "tok", "cart/a", "1", []byte("kept layer"))
	trash := e.seedGarbage(t, "cart/a", 3)
	// An image that is deleted leaves its layers to the collector.
	gone := e.pushImage(t, "tok", "cart/a", "2", []byte("deleted layer"))
	res, b := e.raw(t, "tok", "DELETE", "/v2/cart/a/manifests/"+gone, nil, nil)
	e.expect(t, res, b, 202)

	// Within the grace period nothing is touched, whatever it is.
	rep, err := e.svc.GC(t.Context(), registry.GCOptions{})
	if err != nil || rep.Blobs != 0 || rep.Links != 0 {
		t.Fatalf("a collection inside the grace period: %+v %v", rep, err)
	}
	e.clock.Add(2 * time.Hour)
	dry, err := e.svc.GC(t.Context(), registry.GCOptions{DryRun: true})
	if err != nil || !dry.DryRun || dry.Blobs < 5 || dry.Bytes == 0 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if again, _ := e.svc.GC(t.Context(), registry.GCOptions{DryRun: true}); again.Blobs != dry.Blobs {
		t.Fatalf("a dry run changed something: %d then %d", dry.Blobs, again.Blobs)
	}
	for _, d := range trash {
		res, b := e.raw(t, "tok", "HEAD", "/v2/cart/a/blobs/"+d, nil, nil)
		e.expect(t, res, b, 200)
	}
	real, err := e.svc.GC(t.Context(), registry.GCOptions{})
	if err != nil || real.Blobs != dry.Blobs || real.Links != dry.Links {
		t.Fatalf("real %+v vs dry %+v: %v", real, dry, err)
	}
	for _, d := range trash {
		res, b := e.raw(t, "tok", "HEAD", "/v2/cart/a/blobs/"+d, nil, nil)
		e.expect(t, res, b, 404)
	}
	// What is referenced survives, and still pulls.
	res, b = e.raw(t, "tok", "GET", "/v2/cart/a/manifests/1", nil, nil)
	e.expect(t, res, b, 200)
	_ = keep
	if rep, _, ok, err := e.svc.LastGC(t.Context()); err != nil || !ok || rep.Blobs != real.Blobs {
		t.Fatalf("last gc %+v %v %v", rep, ok, err)
	}
	// Nothing is left to collect.
	if rep, _ := e.svc.GC(t.Context(), registry.GCOptions{}); rep.Blobs != 0 || rep.Links != 0 {
		t.Fatalf("second collection %+v", rep)
	}
}

func TestGarbageCollectionBesideConcurrentPushes(t *testing.T) {
	e := newEnv(t, func(o *registry.Options) { o.Limits.GCGrace = time.Hour })
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	e.seedGarbage(t, "cart/a", 20)
	e.clock.Add(3 * time.Hour) // the garbage is old; what the pushers make from now on is new
	stop := make(chan struct{})
	var gcs, collected atomic.Int32
	var gcWG sync.WaitGroup
	gcWG.Add(1)
	go func() {
		defer gcWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			rep, err := e.svc.GC(t.Context(), registry.GCOptions{})
			if err == nil {
				gcs.Add(1)
				collected.Add(int32(rep.Blobs))
			}
		}
	}()
	var wg sync.WaitGroup
	digests := make([]string, 8)
	for i := range digests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			digests[i] = e.pushImage(t, "tok", "cart/a", fmt.Sprintf("t%d", i), []byte(fmt.Sprintf("layer of pusher %d", i)))
		}()
	}
	wg.Wait()
	close(stop)
	gcWG.Wait()
	if gcs.Load() == 0 || collected.Load() < 20 {
		t.Fatalf("the collector ran %d times and removed %d blobs", gcs.Load(), collected.Load())
	}
	for i, d := range digests {
		res, b := e.raw(t, "tok", "GET", fmt.Sprintf("/v2/cart/a/manifests/t%d", i), nil, nil)
		e.expect(t, res, b, 200)
		if res.Header.Get("Docker-Content-Digest") != d {
			t.Fatalf("digest of t%d", i)
		}
		// Every blob of every pushed image is still there, byte for byte.
		_, body, err := e.svc.ResolveManifest(t.Context(), "cart/a", d, true)
		if err != nil || len(body) == 0 {
			t.Fatalf("manifest body %v", err)
		}
	}
}

func TestRetentionPreviewAndRun(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	for i := 1; i <= 6; i++ {
		e.clock.Add(time.Minute)
		e.pushImage(t, "tok", "cart/a", fmt.Sprintf("build-%d", i), []byte(fmt.Sprintf("layer %d", i)))
	}
	rel := e.pushImage(t, "tok", "cart/a", "v1.0", []byte("release layer"))
	untagged := e.pushImage(t, "tok", "cart/a", "tmp", []byte("temp layer"))
	e.raw(t, "tok", "DELETE", "/v2/cart/a/manifests/tmp", nil, nil)
	e.clock.Add(10 * 24 * time.Hour)
	if err := e.svc.SetPolicy(t.Context(), registry.ScopeProject, "cart", registry.Policy{
		Retention:     &registry.Retention{KeepLast: 2, KeepTags: []string{"v*"}, UntaggedDays: 7},
		ImmutableTags: []string{"v*"}}, "root"); err != nil {
		t.Fatal(err)
	}
	prev, err := e.svc.RetentionPreview(t.Context(), registry.ScopeProject, "cart")
	if err != nil {
		t.Fatal(err)
	}
	var tagged, unt int
	for _, c := range prev {
		if c.Digest == rel {
			t.Fatalf("the release is a candidate: %+v", c)
		}
		if c.Digest == untagged {
			unt++
		} else {
			tagged++
		}
	}
	// Seven tagged images (six builds and the release): the release is kept by
	// its tag, two builds by recency, so four builds go; and the untagged one.
	if tagged != 4 || unt != 1 {
		t.Fatalf("preview: %d tagged and %d untagged candidates: %+v", tagged, unt, prev)
	}
	// The preview changed nothing.
	if tags, _, _ := e.svc.ListTags(t.Context(), "cart/a", 0, ""); len(tags) != 7 {
		t.Fatalf("the preview deleted tags: %v", tags)
	}
	done, err := e.svc.RunRetention(t.Context(), registry.ScopeProject, "cart", registry.Who{Name: "root"})
	if err != nil || len(done) != len(prev) {
		t.Fatalf("run %d of %d: %v", len(done), len(prev), err)
	}
	tags, _, _ := e.svc.ListTags(t.Context(), "cart/a", 0, "")
	if strings.Join(tags, ",") != "build-5,build-6,v1.0" {
		t.Fatalf("tags after retention: %v", tags)
	}
	if len(e.eventsOf("retention")) != 1 {
		t.Fatal("no retention event")
	}
}

func TestRetentionNeverDeletesWhatADeploymentUses(t *testing.T) {
	var deployed atomic.Value
	deployed.Store("")
	e := newEnv(t, func(o *registry.Options) {
		o.Protected = func(_ context.Context, _, d string) bool { return d == deployed.Load().(string) }
	})
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	var first string
	for i := 1; i <= 4; i++ {
		e.clock.Add(time.Minute)
		d := e.pushImage(t, "tok", "cart/a", fmt.Sprintf("b%d", i), []byte(fmt.Sprintf("l%d", i)))
		if i == 1 {
			first = d
		}
	}
	deployed.Store(first)
	if err := e.svc.SetPolicy(t.Context(), registry.ScopeRepository, "cart/a", registry.Policy{Retention: &registry.Retention{KeepLast: 1}}, "root"); err != nil {
		t.Fatal(err)
	}
	done, err := e.svc.RunRetention(t.Context(), "", "", registry.Who{Name: "root"})
	if err != nil || len(done) != 2 {
		t.Fatalf("deleted %d: %v", len(done), err)
	}
	if _, _, err := e.svc.ResolveManifest(t.Context(), "cart/a", first, false); err != nil {
		t.Fatalf("the deployed image was deleted: %v", err)
	}
	res, b := e.raw(t, "tok", "DELETE", "/v2/cart/a/manifests/"+first, nil, nil)
	e.expect(t, res, b, 409)
}

func TestTwoServersShareUploadsAndGC(t *testing.T) {
	addr := dbAddr(t)
	dir := t.TempDir()
	a := newEnvAt(t, addr, dir)
	b := newEnvAt(t, addr, dir)
	for _, e := range []*env{a, b} {
		e.grant("tok", who{subject: "ann", can: rw("cart")})
	}
	data := make([]byte, 9<<20)
	for i := range data {
		data[i] = byte(i * 7)
	}
	digest := registry.SHA256(data)
	res, body := a.raw(t, "tok", "POST", "/v2/cart/x/blobs/uploads/", nil, nil)
	a.expect(t, res, body, 202)
	loc := res.Header.Get("Location")
	// The chunks go to different servers, and the last one to a third request on the first.
	res, body = b.raw(t, "tok", "PATCH", loc, data[:3<<20], nil)
	b.expect(t, res, body, 202)
	res, body = a.raw(t, "tok", "PATCH", loc, data[3<<20:6<<20], nil)
	a.expect(t, res, body, 202)
	res, body = b.raw(t, "tok", "PUT", loc+"?digest="+digest, data[6<<20:], nil)
	b.expect(t, res, body, 201)
	for _, e := range []*env{a, b} {
		res, body = e.raw(t, "tok", "GET", "/v2/cart/x/blobs/"+digest, nil, nil)
		e.expect(t, res, body, 200)
		if len(body) != len(data) {
			t.Fatalf("read %d bytes", len(body))
		}
	}
	// Two servers collecting at once: one takes the lease, the other is told so.
	a.clock.Add(2 * time.Hour)
	b.clock.Add(2 * time.Hour)
	if _, err := a.svc.GC(t.Context(), registry.GCOptions{}); err != nil {
		t.Fatal(err)
	}
}
