package registry_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/registry"
)

func registryWho() registry.Who { return registry.Who{Name: "test"} }

// raw sends a request with a token (Basic: the registry accepts a token
// directly) and returns the response and its body.
func (e *env) raw(t *testing.T, secret, method, path string, body []byte, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		req.SetBasicAuth("u", secret)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func (e *env) expect(t *testing.T, res *http.Response, body []byte, status int) {
	t.Helper()
	if res.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d: %s", res.Request.Method, res.Request.URL.Path, res.StatusCode, status, body)
	}
}

// pushBlob uploads data in one request and returns its digest.
func (e *env) pushBlob(t *testing.T, secret, repo string, data []byte) string {
	t.Helper()
	d := registry.SHA256(data)
	res, b := e.raw(t, secret, "POST", "/v2/"+repo+"/blobs/uploads/?digest="+d, data, nil)
	e.expect(t, res, b, 201)
	return d
}

// pushImage pushes a one-layer image by hand and returns the manifest's digest.
func (e *env) pushImage(t *testing.T, secret, repo, tag string, layer []byte) string {
	t.Helper()
	cfg := []byte(fmt.Sprintf(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]},"x":%d}`, len(layer)))
	cd := e.pushBlob(t, secret, repo, cfg)
	ld := e.pushBlob(t, secret, repo, layer)
	man, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": registry.MediaOCIManifest,
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": cd, "size": len(cfg)},
		"layers": []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": ld, "size": len(layer)}},
	})
	res, b := e.raw(t, secret, "PUT", "/v2/"+repo+"/manifests/"+tag, man, map[string]string{"Content-Type": registry.MediaOCIManifest})
	e.expect(t, res, b, 201)
	return registry.SHA256(man)
}

// pushReferrer pushes an artifact manifest whose subject is subject.
func (e *env) pushReferrer(t *testing.T, secret, repo, subject, artifactType string, ann map[string]string) string {
	t.Helper()
	empty := e.pushBlob(t, secret, repo, []byte("{}"))
	content := []byte(fmt.Sprintf(`{"attached":"%s"}`, artifactType))
	cd := e.pushBlob(t, secret, repo, content)
	man, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": registry.MediaOCIManifest, "artifactType": artifactType,
		"config":      map[string]any{"mediaType": "application/vnd.oci.empty.v1+json", "digest": empty, "size": 2},
		"layers":      []any{map[string]any{"mediaType": artifactType, "digest": cd, "size": len(content)}},
		"subject":     map[string]any{"mediaType": registry.MediaOCIManifest, "digest": subject, "size": 1},
		"annotations": ann,
	})
	d := registry.SHA256(man)
	res, b := e.raw(t, secret, "PUT", "/v2/"+repo+"/manifests/"+d, man, map[string]string{"Content-Type": registry.MediaOCIManifest})
	e.expect(t, res, b, 201)
	if res.Header.Get("OCI-Subject") != subject {
		t.Fatalf("OCI-Subject %q", res.Header.Get("OCI-Subject"))
	}
	return d
}
