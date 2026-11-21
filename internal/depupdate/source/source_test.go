package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

func versions(t *testing.T, s Source, name string) []string {
	t.Helper()
	rs, err := s.Versions(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rs {
		out = append(out, r.Version+"|"+r.Module)
	}
	sort.Strings(out)
	return out
}

func TestGoProxy(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		switch r.URL.Path {
		case "/github.com/!burnt!sushi/toml/@v/list":
			fmt.Fprint(w, "v1.2.0\nv1.3.0\n")
		case "/github.com/!burnt!sushi/toml/v2/@v/list":
			fmt.Fprint(w, "v2.0.0\n")
		case "/github.com/!burnt!sushi/toml/@v/v1.3.0.info":
			fmt.Fprint(w, `{"Version":"v1.3.0","Time":"2026-11-01T10:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	g := &GoProxy{F: &Fetcher{Client: srv.Client(), Cache: &MemCache{}}, BaseURL: srv.URL}
	got := versions(t, g, "github.com/BurntSushi/toml")
	want := []string{"v1.2.0|", "v1.3.0|", "v2.0.0|github.com/BurntSushi/toml/v2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
	d, err := g.Detail(context.Background(), "github.com/BurntSushi/toml", "v1.3.0")
	if err != nil || d.Time.Year() != 2026 || !strings.Contains(d.NotesURL, "github.com/BurntSushi/toml/releases/tag/v1.3.0") {
		t.Fatalf("%+v %v", d, err)
	}
	// cached: a second call makes no request
	n := g.F.Requests
	versions(t, g, "github.com/BurntSushi/toml")
	if g.F.Requests != n {
		t.Errorf("not cached: %d -> %d", n, g.F.Requests)
	}
	if _, err := g.Versions(context.Background(), "nope/nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestMajorSplit(t *testing.T) {
	for in, want := range map[string]int{"a/b": 1, "a/b/v2": 2, "a/b/v10": 10, "a/v/x": 1, "a/b/v1": 1} {
		if _, n := MajorSplit(in); n != want {
			t.Errorf("%s: %d want %d", in, n, want)
		}
	}
}

func TestOffline(t *testing.T) {
	f := &Fetcher{Offline: true}
	for _, s := range []Source{&GoProxy{F: f}, &NPMRegistry{F: f}, &PyPI{F: f}, &OCI{F: f}} {
		if _, err := s.Versions(context.Background(), "x"); !errors.Is(err, ErrOffline) {
			t.Errorf("%s: %v", s.Ecosystem(), err)
		}
	}
	if f.Requests != 0 {
		t.Error("offline mode made a request")
	}
	// a mirror makes it work
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "v1.0.0\n") }))
	defer srv.Close()
	f.Client = srv.Client()
	if _, err := (&GoProxy{F: f, BaseURL: srv.URL}).Versions(context.Background(), "a/b"); err != nil {
		t.Errorf("mirror in offline mode: %v", err)
	}
}

func TestNPM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/@scope%2Fpkg" && r.URL.Path != "/vite" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"versions":{"1.0.0":{},"1.1.0":{"deprecated":"use 2"},"2.0.0-beta.1":{}},"time":{"1.0.0":"2026-01-01T00:00:00Z","1.1.0":"2026-02-01T00:00:00Z","2.0.0-beta.1":"2026-03-01T00:00:00Z"}}`)
	}))
	defer srv.Close()
	n := &NPMRegistry{F: &Fetcher{Client: srv.Client()}, BaseURL: srv.URL}
	for _, name := range []string{"vite", "@scope/pkg"} {
		rs, err := n.Versions(context.Background(), name)
		if err != nil || len(rs) != 3 {
			t.Fatalf("%s: %v %v", name, rs, err)
		}
		dep := 0
		for _, r := range rs {
			if r.Deprecated != "" {
				dep++
				if r.Version != "1.1.0" {
					t.Error("wrong deprecated")
				}
			}
		}
		if dep != 1 {
			t.Errorf("deprecated count %d", dep)
		}
	}
	d, err := n.Detail(context.Background(), "vite", "1.0.0")
	if err != nil || d.Time.Month() != 1 {
		t.Errorf("%+v %v", d, err)
	}
}

func TestPyPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pypi/requests/json" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"info":{},"releases":{"2.28.1":[{"upload_time_iso_8601":"2022-06-29T00:00:00.000000Z","yanked":false}],"2.29.0":[{"upload_time_iso_8601":"2023-05-22T00:00:00Z","yanked":true}],"2.30.0":[],"2.31.0":[{"upload_time_iso_8601":"2023-05-22T00:00:00Z","yanked":false},{"upload_time_iso_8601":"2023-05-21T00:00:00Z","yanked":false}]}}`)
	}))
	defer srv.Close()
	p := &PyPI{F: &Fetcher{Client: srv.Client()}, BaseURL: srv.URL}
	rs, err := p.Versions(context.Background(), "requests")
	if err != nil || len(rs) != 3 {
		t.Fatalf("%v %v", rs, err)
	}
	for _, r := range rs {
		if r.Version == "2.29.0" && !r.Yanked {
			t.Error("2.29.0 is yanked")
		}
		if r.Version == "2.31.0" && r.Time.Day() != 21 {
			t.Errorf("earliest upload time expected: %v", r.Time)
		}
	}
}

func TestOCIWithTokenFlow(t *testing.T) {
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("the token request must be anonymous")
		}
		fmt.Fprint(w, `{"token":"abc"}`)
	})
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer abc" {
				w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="reg",scope="repository:library/node:pull"`, base))
				w.WriteHeader(401)
				return
			}
			h(w, r)
		}
	}
	d1 := "sha256:" + strings.Repeat("a", 64)
	mux.HandleFunc("/v2/library/node/tags/list", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"name":"library/node","tags":["22","24.22.3-alpine3.23","24.23.0-alpine3.23","latest"]}`)
	}))
	mux.HandleFunc("/v2/library/node/manifests/24.23.0-alpine3.23", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Content-Digest", d1)
		if r.Method == http.MethodHead {
			return
		}
		fmt.Fprint(w, `{"manifests":[{"digest":"sha256:arm","platform":{"architecture":"arm64","os":"linux"}},{"digest":"sha256:amd","platform":{"architecture":"amd64","os":"linux"}}]}`)
	}))
	mux.HandleFunc("/v2/library/node/manifests/sha256:amd", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"config":{"digest":"sha256:cfg"}}`)
	}))
	mux.HandleFunc("/v2/library/node/blobs/sha256:cfg", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"created":"2026-11-10T12:00:00Z"}`)
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL
	o := &OCI{F: &Fetcher{Client: srv.Client()}, Mirrors: map[string]string{"docker.io": srv.URL}}
	rs, err := o.Versions(context.Background(), "docker.io/library/node")
	if err != nil || len(rs) != 4 {
		t.Fatalf("%v %v", rs, err)
	}
	d, err := o.Detail(context.Background(), "node", "24.23.0-alpine3.23")
	if err != nil || d.Digest != d1 || !d.Time.Equal(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestSplit(t *testing.T) {
	cases := map[string][2]string{
		"node": {"docker.io", "library/node"}, "grafana/grafana": {"docker.io", "grafana/grafana"},
		"docker.io/library/node": {"docker.io", "library/node"}, "ghcr.io/a/b": {"ghcr.io", "a/b"},
		"localhost:5000/x": {"localhost:5000", "x"},
	}
	for in, w := range cases {
		h, r := Split(in)
		if h != w[0] || r != w[1] {
			t.Errorf("%s: %s %s", in, h, r)
		}
	}
}

func TestResponseLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", 100)) }))
	defer srv.Close()
	f := &Fetcher{Client: srv.Client(), MaxBytes: 10}
	if _, err := f.Get(context.Background(), srv.URL, nil); err == nil {
		t.Error("an oversized answer must fail")
	}
}
