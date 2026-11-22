package api

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

// hashedName matches the name the web build gives a file: name-HASH.ext, where HASH is a hash of the content. A file
// with such a name never changes, so it is cached for a year, and the hash is its ETag.
var hashedName = regexp.MustCompile(`-([A-Za-z0-9_-]{8})\.[a-z0-9]+$`)

// textFiles serves the text files of the web build as serveText says and every other file with next.
func textFiles(ui fs.FS, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !serveText(w, r, ui, strings.TrimPrefix(r.URL.Path, "/")) {
			next.ServeHTTP(w, r)
		}
	})
}

// serveText sends one of the text files of the web build (assets/*.json: the long text of the pages, the word table).
// The build writes name.json.gz beside each; a browser that accepts gzip gets that file as it is, with the headers
// that let it keep and re-check it. It reports false when the path is not such a file, so the caller serves it as any
// other file.
func serveText(w http.ResponseWriter, r *http.Request, ui fs.FS, name string) bool {
	if !strings.HasPrefix(name, "assets/") || !strings.HasSuffix(name, ".json") {
		return false
	}
	h := w.Header()
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Vary", "Accept-Encoding")
	if m := hashedName.FindStringSubmatch(path.Base(name)); m != nil {
		etag := `"` + m[1] + `"`
		h.Set("ETag", etag)
		if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	file := name
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		if _, err := fs.Stat(ui, name+".gz"); err == nil {
			file = name + ".gz"
			h.Set("Content-Encoding", "gzip")
		}
	}
	f, err := ui.Open(file)
	if err != nil {
		h.Del("Content-Encoding")
		http.NotFound(w, r)
		return true
	}
	defer f.Close()
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, f)
	}
	return true
}
