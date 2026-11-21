package mirror_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

// oldServer is another Git server on this machine: a Gitea-compatible REST
// API listing the bare repositories under root, and git's own http-backend
// serving them, both behind a token.
type oldServer struct {
	*httptest.Server
	root  string
	token string
	mu    sync.Mutex
	// private names repositories ("owner/name") that are private; broken ones answer 500.
	private, broken map[string]bool
	// foreign gives a repository's clone_url another host.
	foreign map[string]string
	seen    []string // Authorization headers of git requests, to see where the token went
}

func newOldServer(t *testing.T, token string) *oldServer {
	t.Helper()
	o := &oldServer{root: t.TempDir(), token: token, private: map[string]bool{}, broken: map[string]bool{}, foreign: map[string]string{}}
	gp, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{Path: filepath.Join(strings.TrimSpace(string(gp)), "git-http-backend"),
		Env: []string{"GIT_PROJECT_ROOT=" + o.root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}}
	o.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token "+o.token {
			u, p, ok := r.BasicAuth()
			if !ok || (p != o.token && u != o.token) {
				w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
				http.Error(w, "unauthorised", http.StatusUnauthorized)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			o.api(w, r)
			return
		}
		o.mu.Lock()
		o.seen = append(o.seen, r.Header.Get("Authorization"))
		repo := strings.Join(strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[:2], "/")
		bad := o.broken[strings.TrimSuffix(repo, ".git")]
		o.mu.Unlock()
		if bad {
			http.Error(w, "broken", http.StatusInternalServerError)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(o.Close)
	return o
}

func (o *oldServer) api(w http.ResponseWriter, r *http.Request) {
	type repo struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
		Private  bool   `json:"private"`
		Empty    bool   `json:"empty"`
		CloneURL string `json:"clone_url"`
		Default  string `json:"default_branch"`
	}
	var all []repo
	owners, _ := os.ReadDir(o.root)
	for _, ow := range owners {
		names, _ := os.ReadDir(filepath.Join(o.root, ow.Name()))
		for _, n := range names {
			name := strings.TrimSuffix(n.Name(), ".git")
			full := ow.Name() + "/" + name
			x := repo{Name: name, FullName: full, Private: o.private[full], Default: "main", CloneURL: o.URL + "/" + full + ".git"}
			x.Owner.Login = ow.Name()
			if f := o.foreign[full]; f != "" {
				x.CloneURL = f + "/" + full + ".git"
			}
			out, _ := exec.Command("git", "-C", filepath.Join(o.root, ow.Name(), n.Name()), "for-each-ref", "--count=1").Output()
			x.Empty = len(out) == 0
			all = append(all, x)
		}
	}
	page := r.URL.Query().Get("page")
	if page != "" && page != "1" {
		all = nil
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	switch {
	case p == "repos/search":
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": all})
	case strings.HasPrefix(p, "orgs/") && strings.HasSuffix(p, "/repos"):
		http.NotFound(w, r) // not an organisation: the client then asks for a user
	case strings.HasPrefix(p, "users/") && strings.HasSuffix(p, "/repos"):
		who := strings.Split(p, "/")[1]
		var mine []repo
		for _, x := range all {
			if x.Owner.Login == who {
				mine = append(mine, x)
			}
		}
		if len(mine) == 0 && page != "" && page != "1" {
			json.NewEncoder(w).Encode([]repo{})
			return
		}
		if len(mine) == 0 {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(mine)
	default:
		http.NotFound(w, r)
	}
}

// add makes a repository on the server, with content from fill.
func (o *oldServer) add(t *testing.T, path string, fill func(*source)) *source {
	t.Helper()
	owner, name, _ := strings.Cut(path, "/")
	if err := os.MkdirAll(filepath.Join(o.root, owner), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newSource(t)
	if fill != nil {
		fill(s)
	}
	runGit(t, o.root, "clone", "-q", "--mirror", s.bare, filepath.Join(o.root, owner, name+".git"))
	s.bare = filepath.Join(o.root, owner, name+".git")
	s.git("remote", "set-url", "origin", s.bare)
	return s
}

const oldToken = "t0k3n-of-the-old-server-9931"

func importWorld(t *testing.T, o *oldServer) *world {
	t.Helper()
	allow, _ := egress.New([]string{"127.0.0.1"})
	allow.AllowFiles = true // for the repositories the test makes itself
	w := newWorld(t, worldOpts{guard: allow})
	t.Setenv("OLD_TOKEN", oldToken)
	if _, err := w.h.Forge.CreateConnection(context.Background(), forge.Connection{Name: "old", Kind: forge.KindGitea, BaseURL: o.URL,
		Credential: forge.SecretRef{Env: "OLD_TOKEN"}, Enabled: true}, "ada"); err != nil {
		t.Fatal(err)
	}
	return w
}

func fillTwo(s *source) { populate(s) }
func fillOne(s *source) { s.commit("only"); s.pushAll() }

func TestImportDryRunListsAndChangesNothing(t *testing.T) {
	o := newOldServer(t, oldToken)
	o.add(t, "acme/web", fillTwo)
	o.add(t, "acme/svc", fillOne)
	o.private["acme/svc"] = true
	w, ctx := importWorld(t, o), context.Background()
	res, err := w.h.Mirrors.Import(ctx, mirror.ImportRequest{SourceRequest: mirror.SourceRequest{Connection: "old"}, DryRun: true}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 || res.Items[0].Repository != "acme/svc" || res.Items[0].Visibility != forge.Private || res.Items[1].Visibility != forge.Public {
		t.Fatalf("%+v", res.Items)
	}
	for _, it := range res.Items {
		if it.Action != mirror.ActionCreate || it.State != mirror.StatePlanned || !strings.Contains(it.Reason, "would be created") {
			t.Errorf("%+v", it)
		}
	}
	if page, _ := w.h.Provider.Repositories().List(ctx, ""); len(page.Items) != 0 {
		t.Fatalf("a dry run made %d repositories", len(page.Items))
	}
	all, _ := w.h.Mirrors.All(ctx)
	if len(all) != 0 {
		t.Fatal("a dry run made mirrors")
	}
}

func TestImportCopiesEveryRefVerifiesAndLeavesPullMirrors(t *testing.T) {
	o := newOldServer(t, oldToken)
	web := o.add(t, "acme/web", fillTwo)
	o.add(t, "acme/svc", fillOne)
	o.add(t, "ops/empty", nil)
	o.private["acme/svc"] = true
	w, ctx := importWorld(t, o), context.Background()
	req := mirror.ImportRequest{SourceRequest: mirror.SourceRequest{Connection: "old"}, Wait: true}
	res, err := w.h.Mirrors.Import(ctx, req, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary["verified"] != 3 || res.Summary["total"] != 3 {
		t.Fatalf("%+v", res.Items)
	}
	for _, it := range res.Items {
		if !it.Verified || it.SourceRefs != it.HostedRefs || it.Error != "" {
			t.Errorf("%+v", it)
		}
	}
	repo, err := w.h.Provider.Repositories().Get(ctx, "acme/web")
	if err != nil || !repo.Mirror || repo.Visibility != forge.Public {
		t.Fatalf("%+v %v", repo, err)
	}
	if got := w.hostedRefs(repo); !equal(got, refsOf(t, web.bare)) {
		t.Fatalf("refs differ:\n%s\n%s", describe(got), describe(refsOf(t, web.bare)))
	}
	w.fsck(repo)
	if r, _ := w.h.Provider.Repositories().Get(ctx, "acme/svc"); r.Visibility != forge.Private {
		t.Errorf("visibility %s", r.Visibility)
	}
	// Running it again is a no-op that checks again: nothing is made twice.
	res2, err := w.h.Mirrors.Import(ctx, req, "ada")
	if err != nil || res2.Summary["verified"] != 3 {
		t.Fatalf("%v %+v", err, res2.Items)
	}
	for _, it := range res2.Items {
		if it.Action != mirror.ActionResume {
			t.Errorf("a second run did %s", it.Action)
		}
	}
	if all, _ := w.h.Mirrors.All(ctx); len(all) != 3 {
		t.Errorf("%d mirrors", len(all))
	}
	// The token is on the old server's requests but nowhere in the results or the database.
	if out, _ := json.Marshal(res); strings.Contains(string(out), oldToken) {
		t.Error("the token is in the result")
	}
	for _, m := range mustAll(t, w) {
		if strings.Contains(fmt.Sprintf("%+v", m), oldToken) {
			t.Error("the token is stored in a mirror")
		}
	}
}

func mustAll(t *testing.T, w *world) []mirror.Mirror {
	t.Helper()
	all, err := w.h.Mirrors.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func TestImportIsResumableAndNeverTouchesARepositoryOfItsOwn(t *testing.T) {
	o := newOldServer(t, oldToken)
	o.add(t, "acme/good", fillOne)
	o.add(t, "acme/flaky", fillTwo)
	o.add(t, "acme/mine", fillOne)
	w, ctx := importWorld(t, o), context.Background()
	// A repository of the same name that was made here and has its own content.
	own := w.newRepo("acme/mine")
	ownSrc := newSource(t)
	ownSrc.commit("mine")
	ownSrc.pushAll()
	w.mirrorOnceFrom(own, ownSrc)
	if _, err := w.h.Mirrors.Promote(ctx, own.Path, false, "ada"); err != nil {
		t.Fatal(err)
	}
	before := w.hostedRefs(own)

	o.broken["acme/flaky"] = true
	req := mirror.ImportRequest{SourceRequest: mirror.SourceRequest{Connection: "old"}, Wait: true}
	res, err := w.h.Mirrors.Import(ctx, req, "ada")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]mirror.ImportItem{}
	for _, it := range res.Items {
		got[it.Repository] = it
	}
	if got["acme/good"].State != mirror.StateVerified || got["acme/flaky"].State != mirror.StateFailed || got["acme/mine"].State != mirror.StateSkipped {
		t.Fatalf("%+v", res.Items)
	}
	if !strings.Contains(got["acme/mine"].Reason, "left alone") || !equal(w.hostedRefs(own), before) {
		t.Errorf("a repository of its own was touched: %+v", got["acme/mine"])
	}
	if strings.Contains(got["acme/flaky"].Error, oldToken) {
		t.Error("the token is in an error")
	}
	// Fixed, the same request completes the rest and leaves the finished one alone.
	o.broken["acme/flaky"] = false
	res, err = w.h.Mirrors.Import(ctx, req, "ada")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range res.Items {
		switch it.Repository {
		case "acme/good":
			if it.Action != mirror.ActionResume || it.State != mirror.StateVerified {
				t.Errorf("%+v", it)
			}
		case "acme/flaky":
			if it.State != mirror.StateVerified {
				t.Errorf("%+v", it)
			}
		}
	}
}

func TestImportOnceLeavesOrdinaryRepositoriesAndOneRepositoryOrOwnerCanBeChosen(t *testing.T) {
	o := newOldServer(t, oldToken)
	o.add(t, "acme/a", fillOne)
	o.add(t, "acme/b", fillOne)
	o.add(t, "ops/c", fillOne)
	w, ctx := importWorld(t, o), context.Background()
	no := false
	res, err := w.h.Mirrors.Import(ctx, mirror.ImportRequest{SourceRequest: mirror.SourceRequest{Connection: "old", Owner: "acme", TargetOwner: "moved"}, Mirror: &no, Wait: true}, "ada")
	if err != nil || len(res.Items) != 2 || res.Items[0].Repository != "moved/a" || res.Items[0].State != mirror.StateVerified {
		t.Fatalf("%v %+v", err, res.Items)
	}
	r, _ := w.h.Provider.Repositories().Get(ctx, "moved/a")
	if r.Mirror || len(mustAll(t, w)) != 0 {
		t.Errorf("not an ordinary repository: %+v", r)
	}
	if _, err := w.h.Mirrors.Import(ctx, mirror.ImportRequest{SourceRequest: mirror.SourceRequest{Connection: "old"}, Mirror: &no}, "ada"); err == nil {
		t.Error("an ordinary import without waiting was accepted")
	}
	one, err := w.h.Mirrors.Import(ctx, mirror.ImportRequest{SourceRequest: mirror.SourceRequest{Connection: "old", Repository: "ops/c"}, Wait: true}, "ada")
	if err != nil || len(one.Items) != 1 || one.Items[0].Repository != "ops/c" {
		t.Fatalf("%v %+v", err, one.Items)
	}
	if _, err := w.h.Mirrors.Import(ctx, mirror.ImportRequest{SourceRequest: mirror.SourceRequest{Connection: "old", Repository: "ops/none"}}, "ada"); err == nil {
		t.Error("a repository the source lacks was accepted")
	}
}

func TestImportFromAPlainListOfAddresses(t *testing.T) {
	o := newOldServer(t, oldToken)
	o.add(t, "acme/web", fillTwo)
	w, ctx := importWorld(t, o), context.Background()
	if _, err := w.h.Forge.CreateConnection(ctx, forge.Connection{Name: "list", Kind: forge.KindPlain, BaseURL: o.URL, Credential: forge.SecretRef{Env: "OLD_TOKEN"}, Enabled: true}, "ada"); err != nil {
		t.Fatal(err)
	}
	res, err := w.h.Mirrors.Import(ctx, mirror.ImportRequest{SourceRequest: mirror.SourceRequest{Connection: "list", URLs: []string{o.URL + "/acme/web.git"}}, Wait: true}, "ada")
	if err != nil || res.Summary["verified"] != 1 {
		t.Fatalf("%v %+v", err, res.Items)
	}
	if _, err := w.h.Mirrors.Import(ctx, mirror.ImportRequest{SourceRequest: mirror.SourceRequest{URLs: []string{"https://u:pw@git.example.com/a/b.git"}}, DryRun: true}, "ada"); err == nil || strings.Contains(err.Error(), "pw@") && !strings.Contains(err.Error(), "***") {
		t.Errorf("credentials in a listed address: %v", err)
	}
}

func TestImportSendsTheCredentialOnlyToTheConnectionsHost(t *testing.T) {
	o := newOldServer(t, oldToken)
	o.add(t, "acme/web", fillOne)
	o.foreign["acme/web"] = "http://localhost:1" // an API that points the clone elsewhere
	w, ctx := importWorld(t, o), context.Background()
	res, err := w.h.Mirrors.Import(ctx, mirror.ImportRequest{SourceRequest: mirror.SourceRequest{Connection: "old"}, Wait: true}, "ada")
	if err != nil || res.Items[0].State != mirror.StateSkipped || !strings.Contains(res.Items[0].Reason, "credential is not sent") {
		t.Fatalf("%v %+v", err, res.Items)
	}
}

func TestImportRefusesAnInternalSourceUnlessAllowedAndSaysSo(t *testing.T) {
	o := newOldServer(t, oldToken)
	o.add(t, "acme/web", fillOne)
	strict, _ := egress.New(nil)
	w := newWorld(t, worldOpts{guard: strict})
	ctx := context.Background()
	t.Setenv("OLD_TOKEN", oldToken)
	w.h.Forge.CreateConnection(ctx, forge.Connection{Name: "old", Kind: forge.KindGitea, BaseURL: o.URL, Credential: forge.SecretRef{Env: "OLD_TOKEN"}, Enabled: true}, "ada")
	_, err := w.h.Mirrors.Import(ctx, mirror.ImportRequest{SourceRequest: mirror.SourceRequest{Connection: "old"}, DryRun: true}, "ada")
	if err == nil || !strings.Contains(err.Error(), "forge.mirror_allow") || !strings.Contains(err.Error(), "internal address") {
		t.Fatalf("%v", err)
	}
	// A source that is allowed but whose clone addresses are not.
	allowAPI, _ := egress.New([]string{"127.0.0.1"})
	w2 := newWorld(t, worldOpts{guard: allowAPI})
	w2.h.Forge.CreateConnection(ctx, forge.Connection{Name: "old", Kind: forge.KindGitea, BaseURL: o.URL, Credential: forge.SecretRef{Env: "OLD_TOKEN"}, Enabled: true}, "ada")
	if _, err := w2.h.Mirrors.Import(ctx, mirror.ImportRequest{SourceRequest: mirror.SourceRequest{Connection: "nothere"}, DryRun: true}, "ada"); err == nil {
		t.Error("a missing connection was accepted")
	}
}
