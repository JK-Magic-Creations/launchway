package registry_test

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/registry"
)

func TestChunkedResumableUploadAndRangeReads(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	data := make([]byte, 21<<20+17) // spans several 8 MiB stored segments
	_, _ = rand.Read(data)
	digest := registry.SHA256(data)

	res, b := e.raw(t, "tok", "POST", "/v2/cart/big/blobs/uploads/", nil, nil)
	e.expect(t, res, b, 202)
	loc := res.Header.Get("Location")
	if !strings.HasPrefix(loc, "/v2/cart/big/blobs/uploads/") || res.Header.Get("Docker-Upload-UUID") == "" {
		t.Fatalf("location %q", loc)
	}
	// First chunk, then a status check, then a chunk at the wrong offset, then the rest.
	first := 10 << 20
	res, b = e.raw(t, "tok", "PATCH", loc, data[:first], map[string]string{"Content-Type": "application/octet-stream", "Content-Range": fmt.Sprintf("0-%d", first-1)})
	e.expect(t, res, b, 202)
	if res.Header.Get("Range") != fmt.Sprintf("0-%d", first-1) {
		t.Fatalf("range %q", res.Header.Get("Range"))
	}
	res, b = e.raw(t, "tok", "GET", loc, nil, nil)
	e.expect(t, res, b, 204)
	if res.Header.Get("Range") != fmt.Sprintf("0-%d", first-1) {
		t.Fatalf("status range %q", res.Header.Get("Range"))
	}
	res, b = e.raw(t, "tok", "PATCH", loc, data[first+5:first+100], map[string]string{"Content-Range": fmt.Sprintf("%d-%d", first+5, first+99)})
	e.expect(t, res, b, 416)
	res, b = e.raw(t, "tok", "PATCH", loc, data[first:first+(1<<20)], map[string]string{"Content-Range": fmt.Sprintf("%d-%d", first, first+(1<<20)-1)})
	e.expect(t, res, b, 202)
	// The last chunk travels with the closing PUT.
	res, b = e.raw(t, "tok", "PUT", loc+"?digest="+digest, data[first+(1<<20):], nil)
	e.expect(t, res, b, 201)
	if res.Header.Get("Docker-Content-Digest") != digest {
		t.Fatalf("digest header %q", res.Header.Get("Docker-Content-Digest"))
	}
	// The upload is gone.
	res, b = e.raw(t, "tok", "GET", loc, nil, nil)
	e.expect(t, res, b, 404)

	res, b = e.raw(t, "tok", "HEAD", "/v2/cart/big/blobs/"+digest, nil, nil)
	e.expect(t, res, b, 200)
	if res.Header.Get("Content-Length") != fmt.Sprint(len(data)) {
		t.Fatalf("length %q", res.Header.Get("Content-Length"))
	}
	res, b = e.raw(t, "tok", "GET", "/v2/cart/big/blobs/"+digest, nil, nil)
	e.expect(t, res, b, 200)
	if !bytes.Equal(b, data) {
		t.Fatal("the blob read back differs")
	}
	res, b = e.raw(t, "tok", "GET", "/v2/cart/big/blobs/"+digest, nil, map[string]string{"Range": "bytes=100-199"})
	e.expect(t, res, b, 206)
	if !bytes.Equal(b, data[100:200]) || res.Header.Get("Content-Range") != fmt.Sprintf("bytes 100-199/%d", len(data)) {
		t.Fatalf("range read: %q %d", res.Header.Get("Content-Range"), len(b))
	}
	res, b = e.raw(t, "tok", "GET", "/v2/cart/big/blobs/"+digest, nil, map[string]string{"Range": "bytes=-50"})
	e.expect(t, res, b, 206)
	if !bytes.Equal(b, data[len(data)-50:]) {
		t.Fatal("suffix range")
	}
	res, b = e.raw(t, "tok", "GET", "/v2/cart/big/blobs/"+digest, nil, map[string]string{"Range": fmt.Sprintf("bytes=%d-", len(data)+5)})
	e.expect(t, res, b, 416)
}

func TestDigestIsVerifiedOnEveryPath(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	good := []byte("the real bytes")
	wrong := registry.SHA256([]byte("something else"))
	// Monolithic.
	res, b := e.raw(t, "tok", "POST", "/v2/cart/x/blobs/uploads/?digest="+wrong, good, nil)
	e.expect(t, res, b, 400)
	if !strings.Contains(string(b), "DIGEST_INVALID") {
		t.Fatalf("code: %s", b)
	}
	// Session.
	res, b = e.raw(t, "tok", "POST", "/v2/cart/x/blobs/uploads/", nil, nil)
	e.expect(t, res, b, 202)
	loc := res.Header.Get("Location")
	res, b = e.raw(t, "tok", "PATCH", loc, good, nil)
	e.expect(t, res, b, 202)
	res, b = e.raw(t, "tok", "PUT", loc+"?digest="+wrong, nil, nil)
	e.expect(t, res, b, 400)
	// After the refusal the upload still exists and the right digest finishes it.
	res, b = e.raw(t, "tok", "PUT", loc+"?digest="+registry.SHA256(good), nil, nil)
	e.expect(t, res, b, 201)
	// A malformed digest.
	res, b = e.raw(t, "tok", "PUT", loc+"?digest=md5:abc", nil, nil)
	e.expect(t, res, b, 404) // the upload is already finished
	res, b = e.raw(t, "tok", "POST", "/v2/cart/x/blobs/uploads/?digest=md5:abc", good, nil)
	e.expect(t, res, b, 400)
	// A manifest sent to a digest address must hash to it.
	res, b = e.raw(t, "tok", "PUT", "/v2/cart/x/manifests/"+wrong, []byte(`{"schemaVersion":2}`), map[string]string{"Content-Type": registry.MediaOCIManifest})
	e.expect(t, res, b, 400)
}

func TestManifestMustNameOnlyWhatTheRepositoryHas(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart", "shop")})
	layer := []byte("layer")
	e.pushImage(t, "tok", "cart/a", "1", layer)
	// shop/a has none of it: the same manifest cannot be pushed there.
	cfg := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]},"x":5}`)
	man := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"%s","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%d},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"%s","size":5}]}`,
		registry.MediaOCIManifest, registry.SHA256(cfg), len(cfg), registry.SHA256(layer))
	e.pushBlob(t, "tok", "shop/a", []byte("unrelated")) // makes shop/a exist
	res, b := e.raw(t, "tok", "PUT", "/v2/shop/a/manifests/1", []byte(man), map[string]string{"Content-Type": registry.MediaOCIManifest})
	e.expect(t, res, b, 400)
	if !strings.Contains(string(b), "MANIFEST_BLOB_UNKNOWN") {
		t.Fatalf("code: %s", b)
	}
	// And a blob digest does not open another repository's content.
	res, b = e.raw(t, "tok", "GET", "/v2/shop/a/blobs/"+registry.SHA256(layer), nil, nil)
	e.expect(t, res, b, 404)
	// A mount from a repository the caller may read brings it over.
	res, b = e.raw(t, "tok", "POST", "/v2/shop/a/blobs/uploads/?mount="+registry.SHA256(layer)+"&from=cart/a", nil, nil)
	e.expect(t, res, b, 201)
	res, b = e.raw(t, "tok", "GET", "/v2/shop/a/blobs/"+registry.SHA256(layer), nil, nil)
	e.expect(t, res, b, 200)
	// An unsupported media type, schema 1 and invalid JSON.
	res, b = e.raw(t, "tok", "PUT", "/v2/cart/a/manifests/2", []byte(`{"schemaVersion":1}`), map[string]string{"Content-Type": "application/vnd.docker.distribution.manifest.v1+json"})
	e.expect(t, res, b, 400)
	res, b = e.raw(t, "tok", "PUT", "/v2/cart/a/manifests/3", []byte(`not json`), map[string]string{"Content-Type": registry.MediaOCIManifest})
	e.expect(t, res, b, 400)
	// A tag that is not a tag, a repository name that is not a name.
	res, b = e.raw(t, "tok", "PUT", "/v2/cart/a/manifests/.bad", []byte(man), map[string]string{"Content-Type": registry.MediaOCIManifest})
	e.expect(t, res, b, 400)
	res, b = e.raw(t, "tok", "POST", "/v2/Cart/A/blobs/uploads/", nil, nil)
	e.expect(t, res, b, 400)
}

func TestSizeLimitsRefuseWithAPlainSentence(t *testing.T) {
	e := newEnv(t, func(o *registry.Options) { o.Limits = registry.Limits{MaxManifestBytes: 1024, MaxBlobBytes: 64 << 10} })
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	res, b := e.raw(t, "tok", "POST", "/v2/cart/x/blobs/uploads/?digest="+registry.SHA256(make([]byte, 100<<10)), make([]byte, 100<<10), nil)
	e.expect(t, res, b, 413)
	if !strings.Contains(string(b), "TOO_LARGE") || !strings.Contains(string(b), `"next"`) {
		t.Fatalf("body %s", b)
	}
	big := bytes.Repeat([]byte(" "), 2048)
	res, b = e.raw(t, "tok", "PUT", "/v2/cart/x/manifests/1", big, map[string]string{"Content-Type": registry.MediaOCIManifest})
	e.expect(t, res, b, 413)
	// A chunked upload past the limit is refused as it grows.
	res, b = e.raw(t, "tok", "POST", "/v2/cart/x/blobs/uploads/", nil, nil)
	e.expect(t, res, b, 202)
	res, b = e.raw(t, "tok", "PATCH", res.Header.Get("Location"), make([]byte, 100<<10), nil)
	e.expect(t, res, b, 413)
}

func TestCancelAnUpload(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	res, b := e.raw(t, "tok", "POST", "/v2/cart/x/blobs/uploads/", nil, nil)
	e.expect(t, res, b, 202)
	loc := res.Header.Get("Location")
	res, b = e.raw(t, "tok", "PATCH", loc, []byte("abc"), nil)
	e.expect(t, res, b, 202)
	res, b = e.raw(t, "tok", "DELETE", loc, nil, nil)
	e.expect(t, res, b, 204)
	res, b = e.raw(t, "tok", "GET", loc, nil, nil)
	e.expect(t, res, b, 404)
}

func TestAnEmptyBlobAndRacingChunksOfOneUpload(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	// A blob with no bytes (an empty layer) is a blob like any other.
	empty := e.pushBlob(t, "tok", "cart/x", []byte{})
	res, b := e.raw(t, "tok", "GET", "/v2/cart/x/blobs/"+empty, nil, nil)
	e.expect(t, res, b, 200)
	if len(b) != 0 {
		t.Fatalf("empty blob read back %d bytes", len(b))
	}
	// Two requests send the same chunk of one upload at once: exactly one wins, the other is told
	// the offset, and the upload still finishes with the winner's bytes.
	res, b = e.raw(t, "tok", "POST", "/v2/cart/x/blobs/uploads/", nil, nil)
	e.expect(t, res, b, 202)
	loc := res.Header.Get("Location")
	data := bytes.Repeat([]byte("racing chunk "), 1000)
	statuses := make(chan int, 4)
	for i := 0; i < 4; i++ {
		go func() {
			r, _ := e.raw(t, "tok", "PATCH", loc, data, map[string]string{"Content-Range": "0-" + fmt.Sprint(len(data)-1)})
			statuses <- r.StatusCode
		}()
	}
	won := 0
	for i := 0; i < 4; i++ {
		switch s := <-statuses; s {
		case 202:
			won++
		case 416:
		default:
			t.Fatalf("status %d", s)
		}
	}
	if won != 1 {
		t.Fatalf("%d of 4 racing chunks were accepted, want 1", won)
	}
	res, b = e.raw(t, "tok", "PUT", loc+"?digest="+registry.SHA256(data), nil, nil)
	e.expect(t, res, b, 201)
	res, b = e.raw(t, "tok", "GET", "/v2/cart/x/blobs/"+registry.SHA256(data), nil, nil)
	e.expect(t, res, b, 200)
	if !bytes.Equal(b, data) {
		t.Fatal("the blob differs from what the winner sent")
	}
}
