package registry_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/registry"
)

func TestNothingIsAnonymous(t *testing.T) {
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	e.pushImage(t, "tok", "cart/api", "1", []byte("x"))
	for _, c := range []struct{ method, path string }{
		{"GET", "/v2/"}, {"GET", "/v2/_catalog"}, {"GET", "/v2/cart/api/tags/list"}, {"GET", "/v2/cart/api/manifests/1"},
		{"HEAD", "/v2/cart/api/manifests/1"}, {"GET", "/v2/cart/api/blobs/sha256:" + strings.Repeat("a", 64)},
		{"POST", "/v2/cart/api/blobs/uploads/"}, {"PUT", "/v2/cart/api/manifests/1"}, {"DELETE", "/v2/cart/api/manifests/1"},
		{"GET", "/v2/nobody/nothing/tags/list"}, {"GET", "/v2/token"}, {"GET", "/v2/cart/api/referrers/sha256:" + strings.Repeat("a", 64)},
	} {
		res, b := e.raw(t, "", c.method, c.path, nil, nil)
		if res.StatusCode != 401 {
			t.Errorf("%s %s without credentials: %d %s", c.method, c.path, res.StatusCode, b)
		}
		if res.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s %s: no challenge", c.method, c.path)
		}
	}
	// The existing and the missing repository are told apart by nobody.
	r1, b1 := e.raw(t, "", "GET", "/v2/cart/api/tags/list", nil, nil)
	r2, b2 := e.raw(t, "", "GET", "/v2/cart/missing/tags/list", nil, nil)
	if r1.StatusCode != r2.StatusCode || strings.ReplaceAll(string(b1), "cart/api", "X") != strings.ReplaceAll(string(b2), "cart/missing", "X") {
		t.Errorf("anonymous answers differ: %d %s / %d %s", r1.StatusCode, b1, r2.StatusCode, b2)
	}
	// A wrong password and a malformed header.
	res, b := e.raw(t, "wrong", "GET", "/v2/", nil, nil)
	e.expect(t, res, b, 401)
	res, b = e.raw(t, "", "GET", "/v2/", nil, map[string]string{"Authorization": "Bearer garbage"})
	e.expect(t, res, b, 401)
	res, b = e.raw(t, "", "GET", "/v2/", nil, map[string]string{"Authorization": "Basic !!!"})
	e.expect(t, res, b, 401)
	if e.auth.fails == 0 {
		t.Error("failed logins were not counted")
	}
}

func TestPermissionMatrix(t *testing.T) {
	e := newEnv(t)
	e.grant("admin", who{subject: "root", can: rw("cart", "shop")})
	e.grant("cartdev", who{subject: "dev", can: rw("cart")})
	e.grant("shopviewer", who{subject: "vi", can: ro("shop")})
	e.grant("cartviewer", who{subject: "cv", can: ro("cart")})
	d := e.pushImage(t, "admin", "cart/api", "1", []byte("cart layer"))
	e.pushImage(t, "admin", "shop/web", "1", []byte("shop layer"))
	type step struct {
		secret, method, path string
		want                 int
	}
	for _, s := range []step{
		{"cartdev", "GET", "/v2/cart/api/manifests/1", 200},
		{"cartdev", "GET", "/v2/shop/web/manifests/1", 401},     // another project: not even told it exists
		{"cartdev", "GET", "/v2/shop/nothing/manifests/1", 401}, // identical to a missing one
		{"cartdev", "POST", "/v2/shop/web/blobs/uploads/", 401},
		{"cartviewer", "GET", "/v2/cart/api/manifests/1", 200},
		{"cartviewer", "POST", "/v2/cart/api/blobs/uploads/", 403}, // may pull, not push: says so
		{"cartviewer", "DELETE", "/v2/cart/api/manifests/" + d, 403},
		{"shopviewer", "GET", "/v2/cart/api/manifests/1", 401},
		{"cartdev", "DELETE", "/v2/cart/api/manifests/" + d, 202},
	} {
		res, b := e.raw(t, s.secret, s.method, s.path, nil, nil)
		if res.StatusCode != s.want {
			t.Errorf("%s %s %s: %d, want %d: %s", s.secret, s.method, s.path, res.StatusCode, s.want, b)
		}
	}
	if len(e.auth.denied) == 0 {
		t.Error("refusals were not reported")
	}
	// A mount may not read another project's blobs by naming them.
	e.pushBlob(t, "admin", "shop/web", []byte("secret layer"))
	res, b := e.raw(t, "cartdev", "POST", "/v2/cart/api/blobs/uploads/?mount="+registry.SHA256([]byte("secret layer"))+"&from=shop/web", nil, nil)
	e.expect(t, res, b, 202) // not mounted: an ordinary upload starts
	res, b = e.raw(t, "cartdev", "GET", "/v2/cart/api/blobs/"+registry.SHA256([]byte("secret layer")), nil, nil)
	e.expect(t, res, b, 404)
	// The catalog lists only what the caller may pull.
	res, b = e.raw(t, "cartdev", "GET", "/v2/_catalog", nil, nil)
	e.expect(t, res, b, 200)
	if strings.Contains(string(b), "shop") {
		t.Fatalf("catalog leaks: %s", b)
	}
}

func TestBearerFlowGrantsOnlyWhatIsAllowed(t *testing.T) {
	e := newEnv(t)
	e.grant("cartviewer", who{subject: "cv", can: ro("cart")})
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:cartviewer"))
	res, b := e.raw(t, "", "GET", "/v2/token?service=launchway-registry&scope=repository:cart/api:pull,push&scope=repository:shop/web:pull", nil, map[string]string{"Authorization": basic})
	e.expect(t, res, b, 200)
	tok := jsonField(t, b, "token")
	if tok == "" || jsonField(t, b, "access_token") != tok {
		t.Fatalf("token response %s", b)
	}
	call := func(method, path string) int {
		req, _ := http.NewRequest(method, e.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if c := call("GET", "/v2/cart/api/tags/list"); c != 404 { // pull is granted; the repository does not exist yet
		t.Fatalf("pull with the token: %d", c)
	}
	if c := call("POST", "/v2/cart/api/blobs/uploads/"); c != 401 && c != 403 {
		t.Fatalf("push with a token that has pull only: %d", c)
	}
	if c := call("GET", "/v2/shop/web/tags/list"); c != 401 {
		t.Fatalf("another project with the token: %d", c)
	}
	// The token expires.
	e.clock.Add(registry.TokenTTL + time.Second)
	if c := call("GET", "/v2/cart/api/tags/list"); c != 401 {
		t.Fatalf("expired token: %d", c)
	}
	// The token endpoint needs credentials, and refuses a token as a service.
	res, b = e.raw(t, "", "GET", "/v2/token", nil, nil)
	e.expect(t, res, b, 401)
	res, b = e.raw(t, "cartviewer", "GET", "/v2/token?service=other", nil, nil)
	e.expect(t, res, b, 400)
}

func TestRunCredentials(t *testing.T) {
	e := newEnv(t)
	e.grant("admin", who{subject: "root", can: rw("cart", "shop", "lib")})
	e.pushImage(t, "admin", "lib/base", "1", []byte("lib layer"))
	e.pushImage(t, "admin", "shop/web", "1", []byte("shop layer"))
	// lib shares its repositories with runs of cart.
	if err := e.svc.SetPolicy(t.Context(), registry.ScopeProject, "lib", registry.Policy{PullFrom: []string{"cart"}}, "root"); err != nil {
		t.Fatal(err)
	}
	push, _ := e.signer.MintRun("cart", "run_1", true, time.Hour)
	noPush, _ := e.signer.MintRun("cart", "run_2", false, time.Hour)
	for _, c := range []struct {
		name, secret, method, path string
		want                       int
	}{
		{"push to its own project", push, "POST", "/v2/cart/api/blobs/uploads/", 202},
		{"pull its own project", push, "GET", "/v2/cart/api/tags/list", 200},
		{"pull a project that shares", push, "GET", "/v2/lib/base/manifests/1", 200},
		{"pull a project that does not", push, "GET", "/v2/shop/web/manifests/1", 401},
		{"push to a project that shares for pulling", push, "POST", "/v2/lib/base/blobs/uploads/", 403},
		{"push to another project", push, "POST", "/v2/shop/web/blobs/uploads/", 401},
		{"delete anything", push, "DELETE", "/v2/cart/api/manifests/1", 403},
		{"a run that may not push", noPush, "POST", "/v2/cart/api/blobs/uploads/", 403},
		{"a run that may not push can pull", noPush, "GET", "/v2/lib/base/manifests/1", 200},
		{"a forged credential", strings.Replace(push, "lwi_", "lwi_x", 1), "GET", "/v2/lib/base/manifests/1", 401},
	} {
		res, b := e.raw(t, c.secret, c.method, c.path, nil, nil)
		if res.StatusCode != c.want {
			t.Errorf("%s: %d, want %d: %s", c.name, res.StatusCode, c.want, b)
		}
	}
	// Past its expiry a credential is nothing.
	e.clock.Add(2 * time.Hour)
	res, b := e.raw(t, push, "GET", "/v2/lib/base/manifests/1", nil, nil)
	e.expect(t, res, b, 401)
	// A token made from a run credential never outlives it.
	short, _ := e.signer.MintRun("cart", "run_3", true, 2*time.Minute)
	p, _ := e.signer.VerifyRun(short)
	tok, exp, err := e.signer.IssueBearer(e.svc.RunPrincipal(p), []registry.Access{{Name: "cart/a", Actions: []registry.Action{registry.Pull}}}, registry.TokenTTL)
	if err != nil || exp.After(p.Expires) || tok == "" {
		t.Fatalf("bearer %v %v %v", exp, p.Expires, err)
	}
}

func jsonField(t *testing.T, b []byte, key string) string {
	t.Helper()
	i := strings.Index(string(b), `"`+key+`":"`)
	if i < 0 {
		return ""
	}
	rest := string(b)[i+len(key)+4:]
	return rest[:strings.Index(rest, `"`)]
}
