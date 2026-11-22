// Command registry is a fake package registry for the dependency updater's
// end-to-end checks: a Go module proxy, an npm registry, an OCI registry (tags
// and manifests) and a directory of OSV records, all on one local address.
// It serves recorded-shape data and nothing else; no network is used.
//
//	registry -listen 127.0.0.1:18089 -osv-dir DIR
//
// POST /admin/release?kind=go|npm|oci&name=N&version=V publishes one more
// version while it runs, so a test can supersede a proposal.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type server struct {
	mu   sync.Mutex
	addr string
	goV  map[string][]string // module -> versions
	npmV map[string][]string
	ociT map[string][]string // repo -> tags
	age  map[string]time.Duration
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18089", "address")
	osvDir := flag.String("osv-dir", "", "write the OSV advisory records here")
	flag.Parse()
	s := &server{addr: *listen,
		goV:  map[string][]string{"example.com/lib": {"v1.4.0", "v1.4.2"}},
		npmV: map[string][]string{"tinyfmt": {"1.0.0", "1.1.0", "2.0.0"}},
		ociT: map[string][]string{"team/base": {"3.19", "3.20", "3.21"}}, age: map[string]time.Duration{}}
	if *osvDir != "" {
		if err := writeOSV(*osvDir); err != nil {
			log.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/release", s.release)
	mux.HandleFunc("/goproxy/", s.goproxy)
	mux.HandleFunc("/npm/", s.npm)
	mux.HandleFunc("/v2/", s.oci)
	log.Printf("fake registry on %s", *listen)
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func name2(r *http.Request) string { return r.URL.Query().Get("name") }

func n2(kind, name, v string) string { return kind + "|" + name + "|" + v }

func writeOSV(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	adv := `{"id":"GHSA-test-lib-0001","summary":"Path traversal in example.com/lib","aliases":["CVE-2026-0001"],
 "severity":[{"type":"CVSS_V3","score":"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}],
 "affected":[{"package":{"ecosystem":"Go","name":"example.com/lib"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.4.2"}]}]}],
 "references":[{"type":"ADVISORY","url":"https://example.invalid/advisory/GHSA-test-lib-0001"}]}`
	return os.WriteFile(filepath.Join(dir, "GHSA-test-lib-0001.json"), []byte(adv), 0o600)
}

func (s *server) release(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, v := r.URL.Query().Get("name"), r.URL.Query().Get("version")
	if d := r.URL.Query().Get("age_days"); d != "" {
		var n int
		_, _ = fmt.Sscan(d, &n)
		s.age[n2(r.URL.Query().Get("kind"), name2(r), v)] = time.Duration(n) * 24 * time.Hour
	}
	switch r.URL.Query().Get("kind") {
	case "go":
		s.goV[n] = append(s.goV[n], v)
	case "npm":
		s.npmV[n] = append(s.npmV[n], v)
	case "oci":
		s.ociT[n] = append(s.ociT[n], v)
	default:
		http.Error(w, "kind is go, npm or oci", 400)
		return
	}
	fmt.Fprintln(w, "ok")
}

// ---- Go module proxy ----

func (s *server) goproxy(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/goproxy/")
	i := strings.Index(p, "/@v/")
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	module, file := p[:i], p[i+4:]
	vs, ok := s.goV[module]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasSuffix(file, ".zip") {
		w.Header().Set("Content-Type", "application/zip")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	switch {
	case file == "list":
		fmt.Fprintln(w, strings.Join(vs, "\n"))
	case strings.HasSuffix(file, ".info"):
		v := strings.TrimSuffix(file, ".info")
		// 40 days old for every version but the newest, which is 5 days old
		age := 40 * 24 * time.Hour
		if len(vs) > 0 && v == vs[len(vs)-1] {
			age = 5 * 24 * time.Hour
		}
		fmt.Fprintf(w, `{"Version":%q,"Time":%q}`, v, time.Now().Add(-age).UTC().Format(time.RFC3339)) //nolint:gosec // %q quotes it; a test fake
	case strings.HasSuffix(file, ".mod"):
		fmt.Fprintf(w, "module %s\n\ngo 1.21\n", module) //nolint:gosec // the module is one the fake knows (checked above); a test fake
	case strings.HasSuffix(file, ".zip"):
		v := strings.TrimSuffix(file, ".zip")
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		f, _ := zw.Create(module + "@" + v + "/go.mod")
		fmt.Fprintf(f, "module %s\n\ngo 1.21\n", module)
		f, _ = zw.Create(module + "@" + v + "/lib.go")
		fmt.Fprintf(f, "package lib\n\nconst Version = %q\n", v)
		zw.Close()
		_, _ = w.Write(buf.Bytes())
	default:
		http.NotFound(w, r)
	}
}

// ---- npm registry ----

func tarball(name, v string) ([]byte, string, string) {
	var tb bytes.Buffer
	gz := gzip.NewWriter(&tb)
	tw := tar.NewWriter(gz)
	body := fmt.Sprintf(`{"name":%q,"version":%q}`, name, v)
	_ = tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(body))})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()
	sum := sha512.Sum512(tb.Bytes())
	return tb.Bytes(), "sha512-" + base64.StdEncoding.EncodeToString(sum[:]), hex.EncodeToString(sum[:20])
}

func (s *server) npm(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/npm/")
	if strings.Contains(p, "/-/") { // /npm/tinyfmt/-/tinyfmt-1.1.0.tgz
		name := p[:strings.Index(p, "/-/")]
		file := p[strings.Index(p, "/-/")+3:]
		v := strings.TrimSuffix(strings.TrimPrefix(file, name+"-"), ".tgz")
		b, _, _ := tarball(name, v)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(b)
		return
	}
	vs, ok := s.npmV[p]
	if !ok {
		http.NotFound(w, r)
		return
	}
	versions, times := map[string]any{}, map[string]string{}
	for i, v := range vs {
		_, integrity, shasum := tarball(p, v)
		versions[v] = map[string]any{"name": p, "version": v,
			"dist": map[string]string{"tarball": fmt.Sprintf("http://%s/npm/%s/-/%s-%s.tgz", s.addr, p, p, v), "integrity": integrity, "shasum": shasum}}
		age := time.Duration(len(vs)-i) * 20 * 24 * time.Hour
		if a, ok := s.age[n2("npm", p, v)]; ok {
			age = a
		}
		times[v] = time.Now().Add(-age).UTC().Format(time.RFC3339)
	}
	sorted := append([]string(nil), vs...)
	sort.Strings(sorted)
	_ = json.NewEncoder(w).Encode(map[string]any{"name": p, "versions": versions, "time": times, "dist-tags": map[string]string{"latest": vs[len(vs)-1]}})
}

// ---- OCI registry ----

func (s *server) oci(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/v2/")
	switch {
	case strings.Contains(p, "/tags/list"):
		repo := p[:strings.Index(p, "/tags/list")]
		tags, ok := s.ociT[repo]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
	case strings.Contains(p, "/manifests/"):
		i := strings.Index(p, "/manifests/")
		repo, ref := p[:i], p[i+11:]
		cfg := fmt.Sprintf(`{"created":%q}`, time.Now().Add(-30*24*time.Hour).UTC().Format(time.RFC3339))
		cfgSum := sha256.Sum256([]byte(cfg))
		man := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:%x","size":%d}}`, cfgSum, len(cfg))
		sum := sha256.Sum256([]byte(man + repo + ref))
		w.Header().Set("Docker-Content-Digest", fmt.Sprintf("sha256:%x", sum))
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		if r.Method != http.MethodHead {
			fmt.Fprint(w, man)
		}
	case strings.Contains(p, "/blobs/"):
		cfg := fmt.Sprintf(`{"created":%q}`, time.Now().Add(-30*24*time.Hour).UTC().Format(time.RFC3339))
		fmt.Fprint(w, cfg)
	default:
		http.NotFound(w, r)
	}
}
