package githostaccept

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const repoPrefix = "lw-acceptance-"

var httpc = &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// do sends one API request with the token and returns the status and body.
// A nil token (anon) sends no credential.
func (c *Config) do(method, path string, body any, anon bool) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.URL+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if !anon {
		req.Header.Set("Authorization", "token "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %s", method, path, c.scrub(err.Error()))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return resp.StatusCode, b, nil
}

// api calls the JSON API and decodes into out (may be nil). It fails the test
// unless the status is one of want.
func (c *Config) api(t *testing.T, method, path string, body, out any, want ...int) int {
	t.Helper()
	st, b, err := c.do(method, "/api/v1"+path, body, false)
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, w := range want {
		ok = ok || st == w
	}
	if !ok {
		t.Fatalf("%s %s: status %d, want %v: %.300s", method, path, st, want, c.scrub(string(b)))
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: bad JSON: %v", method, path, err)
		}
	}
	return st
}

// whoami finds the token's user; it is the owner of the test repositories.
func (c *Config) whoami() error {
	st, b, err := c.do("GET", "/api/v1/user", nil, false)
	if err != nil {
		return err
	}
	var u struct {
		Login string `json:"login"`
	}
	if st != 200 || json.Unmarshal(b, &u) != nil || u.Login == "" {
		return fmt.Errorf("GET /api/v1/user with the token: status %d", st)
	}
	c.User = u.Login
	return nil
}

// newRepo creates a throwaway private repository named lw-acceptance-<random>
// and deletes it when the test ends. It returns the repository's name.
func (c *Config) newRepo(t *testing.T) string {
	t.Helper()
	name := repoPrefix + randHex(4)
	body := map[string]any{"name": name, "private": true, "default_branch": "main"}
	// A token without write:user cannot use /user/repos; an admin token creates the repository for its own user instead.
	if st, _, err := c.do("POST", "/api/v1/user/repos", body, false); err != nil || st != 201 {
		c.api(t, "POST", "/admin/users/"+c.User+"/repos", body, nil, 201)
	}
	c.repos = append(c.repos, name)
	t.Cleanup(func() { c.deleteRepo(t, name) })
	// A host that runs pipelines on every push (the homelab's scoped workflow does) must not run any for a test repository.
	c.api(t, "PATCH", c.repoPath(name), map[string]any{"has_actions": false}, nil, 200)
	return name
}

// deleteRepo deletes a repository this run created and no other.
func (c *Config) deleteRepo(t *testing.T, name string) {
	own := false
	for _, r := range c.repos {
		own = own || r == name
	}
	if !own || !strings.HasPrefix(name, repoPrefix) {
		t.Errorf("refusing to delete %q: not created by this run", name)
		return
	}
	st, _, err := c.do("DELETE", "/api/v1/repos/"+c.User+"/"+name, nil, false)
	if err != nil || (st != 204 && st != 404) {
		t.Errorf("cleanup of %s failed (status %d, %v): delete it by hand", name, st, err)
	}
}

func (c *Config) repoURL(name string) string  { return c.URL + "/" + c.User + "/" + name + ".git" }
func (c *Config) repoPath(name string) string { return "/repos/" + c.User + "/" + name }

// eventually polls f every half second until it returns true or d passes.
func eventually(d time.Duration, f func() bool) (time.Duration, bool) {
	start := time.Now()
	for time.Since(start) < d {
		if f() {
			return time.Since(start), true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return time.Since(start), f()
}
