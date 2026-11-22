package outside

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
)

const testToken = "not-a-real-credential"

// fakeHost is a local stand-in for the outside host's API.
type fakeHost struct {
	mu       sync.Mutex
	srv      *httptest.Server
	tags     map[string]bool
	releases map[string]map[string]any
	assets   map[string][]byte
	log      []string
	badToken bool
}

func newFake(t *testing.T) *fakeHost {
	f := &fakeHost{tags: map[string]bool{}, releases: map[string]map[string]any{}, assets: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHost) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if f.badToken || r.Header.Get("Authorization") != "Bearer "+testToken {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"message":"bad credentials %s"}`, testToken) // a host that echoes the token
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/repos/acme/app")
	switch {
	case r.Method == "GET" && strings.HasPrefix(p, "/git/ref/tags/"):
		if !f.tags[strings.TrimPrefix(p, "/git/ref/tags/")] {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `{}`)
	case r.Method == "GET" && strings.HasPrefix(p, "/releases/tags/"):
		rel, ok := f.releases[strings.TrimPrefix(p, "/releases/tags/")]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(rel)
	case r.Method == "POST" && p == "/releases":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		tag := in["tag_name"].(string)
		in["id"] = 7
		in["html_url"] = f.srv.URL + "/acme/app/releases/tag/" + tag
		in["upload_url"] = f.srv.URL + "/uploads/acme/app/7/assets{?name,label}"
		in["assets"] = []any{}
		f.releases[tag] = in
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(in)
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/uploads/"):
		b, _ := io.ReadAll(r.Body)
		name := r.URL.Query().Get("name")
		f.assets[name] = b
		for _, rel := range f.releases {
			rel["assets"] = append(rel["assets"].([]any), map[string]any{"id": len(f.assets), "name": name, "size": len(b)})
		}
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"name":%q,"size":%d}`, name, len(b))
	default:
		w.WriteHeader(404)
	}
}

func publisher(t *testing.T) *REST {
	g, _ := egress.New([]string{"127.0.0.1"})
	return NewREST(g)
}

func target(f *fakeHost) Target {
	return Target{URL: "https://git.example.test/acme/app.git", API: f.srv.URL, Credential: forge.SecretRef{Env: "OUTSIDE_TEST_TOKEN"}}
}

func file(name, body string) File {
	return File{Name: name, Size: int64(len(body)), Open: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(body)), nil }}
}

func TestPublishCreatesTheReleaseAndUploadsTheFiles(t *testing.T) {
	t.Setenv("OUTSIDE_TEST_TOKEN", testToken)
	f := newFake(t)
	f.tags["v1.2.0"] = true
	res, err := publisher(t).Publish(context.Background(), target(f), Release{Tag: "v1.2.0", Title: "Release 1.2.0", Notes: "## Added\n- x", Prerelease: true,
		Files: []File{file("app-linux.tar.gz", "bytes-one"), file("empty.txt", "")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != f.srv.URL+"/acme/app/releases/tag/v1.2.0" {
		t.Errorf("url %q", res.URL)
	}
	rel := f.releases["v1.2.0"]
	if rel["name"] != "Release 1.2.0" || rel["body"] != "## Added\n- x" || rel["prerelease"] != true || rel["draft"] != false {
		t.Errorf("release %v", rel)
	}
	if string(f.assets["app-linux.tar.gz"]) != "bytes-one" || len(f.assets) != 2 {
		t.Errorf("assets %v", f.assets)
	}
}

func TestPublishWaitsForTheTagAndNeverCreatesIt(t *testing.T) {
	t.Setenv("OUTSIDE_TEST_TOKEN", testToken)
	f := newFake(t)
	_, err := publisher(t).Publish(context.Background(), target(f), Release{Tag: "v1.2.0", Title: "x"})
	if err == nil || !strings.Contains(err.Error(), "not there yet") || !isMissing(err) {
		t.Fatalf("%v", err)
	}
	if len(f.releases) != 0 {
		t.Fatalf("a release was made for a missing tag")
	}
}

func isMissing(err error) bool { return strings.Contains(err.Error(), ErrTagMissing.Error()) }

func TestPublishAgainOnlyAddsWhatIsMissing(t *testing.T) {
	t.Setenv("OUTSIDE_TEST_TOKEN", testToken)
	f := newFake(t)
	f.tags["v1"] = true
	rel := Release{Tag: "v1", Title: "x", Files: []File{file("a.bin", "aaa")}}
	if _, err := publisher(t).Publish(context.Background(), target(f), rel); err != nil {
		t.Fatal(err)
	}
	rel.Files = append(rel.Files, file("b.bin", "bbbb"))
	if _, err := publisher(t).Publish(context.Background(), target(f), rel); err != nil {
		t.Fatal(err)
	}
	posts := 0
	for _, l := range f.log {
		if l == "POST /repos/acme/app/releases" {
			posts++
		}
	}
	if posts != 1 || len(f.assets) != 2 {
		t.Errorf("releases made %d, assets %v", posts, f.assets)
	}
}

func TestAnErrorNeverHoldsTheCredential(t *testing.T) {
	t.Setenv("OUTSIDE_TEST_TOKEN", testToken)
	f := newFake(t)
	f.badToken = true
	_, err := publisher(t).Publish(context.Background(), target(f), Release{Tag: "v1", Title: "x"})
	if err == nil || strings.Contains(err.Error(), testToken) || !strings.Contains(err.Error(), "may not be allowed to make releases") {
		t.Fatalf("%v", err)
	}
}

func TestTheGuardRefusesAnInternalHostAndTheCredentialStaysHome(t *testing.T) {
	t.Setenv("OUTSIDE_TEST_TOKEN", testToken)
	f := newFake(t)
	strict, _ := egress.New(nil)
	_, err := NewREST(strict).Publish(context.Background(), target(f), Release{Tag: "v1", Title: "x"})
	if err == nil || !strings.Contains(err.Error(), "internal address") || len(f.log) != 0 {
		t.Fatalf("%v %v", err, f.log)
	}
}
