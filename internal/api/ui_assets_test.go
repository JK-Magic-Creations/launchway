package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	return b.Bytes()
}

// The text files of the web build are sent gzipped, with a content-hash ETag and a year of caching; a browser that
// cannot take gzip gets the plain file; a repeat visit with the ETag gets 304.
func TestUITextFilesAreGzippedAndCached(t *testing.T) {
	const body = `{"t0":"A long sentence."}`
	ui := fstest.MapFS{
		"index.html":                        {Data: []byte("<html></html>")},
		"assets/pages.Run-Ab12_xYz.json":    {Data: []byte(body)},
		"assets/pages.Run-Ab12_xYz.json.gz": {Data: gz(t, body)},
		"assets/plain-Ab12_xYz.json":        {Data: []byte(body)},
	}
	h := (&Server{UI: ui}).ui()
	get := func(p string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", p, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/assets/pages.Run-Ab12_xYz.json", map[string]string{"Accept-Encoding": "gzip, br"})
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip request: %d, encoding %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(zr); string(got) != body {
		t.Fatalf("body %q", got)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("cache-control %q", cc)
	}
	if rec.Header().Get("ETag") != `"Ab12_xYz"` || rec.Header().Get("Vary") != "Accept-Encoding" || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("headers %v", rec.Header())
	}

	rec = get("/assets/pages.Run-Ab12_xYz.json", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != body {
		t.Fatalf("plain request: %d %q %q", rec.Code, rec.Header().Get("Content-Encoding"), rec.Body.String())
	}

	rec = get("/assets/pages.Run-Ab12_xYz.json", map[string]string{"If-None-Match": `"Ab12_xYz"`, "Accept-Encoding": "gzip"})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("revalidation: %d, %d bytes", rec.Code, rec.Body.Len())
	}

	// Without a .gz beside it the file is sent as it is, even to a browser that takes gzip.
	rec = get("/assets/plain-Ab12_xYz.json", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != body {
		t.Fatalf("no .gz: %d %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
}
