package githubfake

import (
	"net/http"
	"testing"
)

// The fake is strict: these must fail, and be recorded.
func TestFakeIsStrict(t *testing.T) {
	h := New()
	defer h.Close()
	do := func(method, path, token string) int {
		req, _ := http.NewRequest(method, h.URL+path, nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := do("GET", "/api/v3/user", ""); c != 401 {
		t.Errorf("no credential: %d", c)
	}
	if c := do("GET", "/api/v3/user", "wrong"); c != 401 {
		t.Errorf("wrong credential: %d", c)
	}
	if c := do("GET", "/api/v3/nothing", Token); c != 404 {
		t.Errorf("unknown path: %d", c)
	}
	if c := do("DELETE", "/api/v3/repos/acme/app", Token); c != 404 {
		t.Errorf("wrong method: %d", c)
	}
	if len(h.Violations) != 4 {
		t.Errorf("violations %v", h.Violations)
	}
	if c := do("GET", "/api/v3/user", Token); c != 200 {
		t.Errorf("good call: %d", c)
	}
}
