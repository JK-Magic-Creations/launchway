// Package anygitfake is a fake of a plain Git server for tests: an httptest
// server that serves a real bare repository over Git's smart HTTP protocol with
// git's own http-backend, so every tag and file the provider pushes is really
// there. It is strict: every request must carry the HTTP Basic Authorization
// header the test's credential makes (a request without it, or with another, is
// refused 401 and recorded as a violation, because Launchway sends its
// credential with the first request, never waiting for a challenge), only the
// one repository path exists, and a push may be refused by the server's own
// hook with a message of its own (a protected branch).
package anygitfake

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504: a test fake serving git's own http-backend; no request header becomes an environment variable of a proxy
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// What the fake knows.
const (
	// Prefix is where the fake's repositories live below its address: the base address of a connection ends with it.
	Prefix = "/git"
	// Path is the repository as a connection names it, exactly as the clone address ends.
	Path = "acme/app.git"
	// User and Password are the one credential the fake accepts (a user name and a password).
	User     = "mary"
	Password = "fake-password-for-tests-only-0001"
	// Token is the one token the fake accepts, sent as the password with the user name "git".
	Token = "fake-token-for-tests-only-0001"
)

// Host is the fake.
type Host struct {
	*httptest.Server
	mu         sync.Mutex
	Violations []string
	Requests   []string
	// Denied makes the fake answer 403 to a push ("push") or a read ("pull").
	Denied map[string]bool
	// Accept is the user name and password the fake takes; the default is Token with the user name "git".
	Accept [2]string
	dir    string
}

// New starts the fake: the repository acme/app.git with main (a README) and feature (one more file).
func New() *Host {
	dir, err := os.MkdirTemp("", "anygitfake-")
	if err != nil {
		panic(err)
	}
	h := &Host{Denied: map[string]bool{}, Accept: [2]string{"git", Token}, dir: dir}
	h.seed()
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	return h
}

// Close stops the fake and removes its repository.
func (h *Host) Close() {
	h.Server.Close()
	os.RemoveAll(h.dir)
}

// Base is the connection's base address.
func (h *Host) Base() string { return h.URL + Prefix }

// Header is the Authorization header value a request must carry for a user name and password.
func Header(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func (h *Host) gitDir() string { return filepath.Join(h.dir, "acme", "app.git") }

// ClearViolations forgets what a test provoked on purpose.
func (h *Host) ClearViolations() {
	h.mu.Lock()
	h.Violations = nil
	h.mu.Unlock()
}

// Seen counts the requests whose line starts with prefix ("GIT push", "GIT pull").
func (h *Host) Seen(prefix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.Requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func (h *Host) violate(w http.ResponseWriter, status int, f string, a ...any) {
	h.mu.Lock()
	h.Violations = append(h.Violations, fmt.Sprintf(f, a...))
	h.mu.Unlock()
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
	}
	http.Error(w, fmt.Sprintf(f, a...), status)
}

func (h *Host) record(s string) {
	h.mu.Lock()
	h.Requests = append(h.Requests, s)
	h.mu.Unlock()
}

// serve checks the credential, the path and the service and hands the request to git's http-backend.
func (h *Host) serve(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, Prefix+"/")
	if !ok || !strings.HasPrefix(rest, Path+"/") {
		h.violate(w, http.StatusNotFound, "no such repository path %q (a repository is at %s%s/%s)", r.URL.Path, Prefix, "/", Path)
		return
	}
	user, pass, given := r.BasicAuth()
	if !given {
		h.violate(w, http.StatusUnauthorized, "a request without the Authorization header (the credential is sent with the first request)")
		return
	}
	if subtle.ConstantTimeCompare([]byte(user+":"+pass), []byte(h.Accept[0]+":"+h.Accept[1])) != 1 {
		h.violate(w, http.StatusUnauthorized, "the credential is not accepted")
		return
	}
	if r.URL.Path != Prefix+"/"+Path+"/info/refs" && !strings.HasSuffix(r.URL.Path, "/git-upload-pack") && !strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
		h.violate(w, http.StatusNotFound, "not a smart HTTP path: %s", r.URL.Path)
		return
	}
	push := strings.HasSuffix(r.URL.Path, "/git-receive-pack") || r.URL.Query().Get("service") == "git-receive-pack"
	verb := map[bool]string{true: "push", false: "pull"}[push]
	h.record("GIT " + verb)
	if h.Denied[verb] {
		http.Error(w, "the account may not "+verb+" here", http.StatusForbidden)
		return
	}
	gp, _ := exec.CommandContext(r.Context(), "git", "--exec-path").Output() //nolint:gosec // G702: a fixed command line, no request value in it
	backend := &cgi.Handler{Path: filepath.Join(strings.TrimSpace(string(gp)), "git-http-backend"), Root: Prefix,
		Env: []string{"GIT_PROJECT_ROOT=" + h.dir, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=" + user, "GIT_COMMITTER_NAME=Mary", "GIT_COMMITTER_EMAIL=m@example.com",
			"GIT_CONFIG_NOSYSTEM=1", "HOME=" + h.dir}}
	backend.ServeHTTP(w, r)
}

// Protect makes the server's own hook refuse every push to a branch, with a message of the server's own.
func (h *Host) Protect(branch string) {
	hook := "#!/bin/sh\nwhile read old new ref; do\n  if [ \"$ref\" = \"refs/heads/" + branch + "\" ]; then\n    echo \"protected branch: " + branch + " takes changes through a merge request only\" >&2\n    exit 1\n  fi\ndone\n"
	if err := os.MkdirAll(filepath.Join(h.gitDir(), "hooks"), 0o750); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(h.gitDir(), "hooks", "pre-receive"), []byte(hook), 0o755); err != nil { //nolint:gosec // G306: a test hook, executable on purpose
		panic(err)
	}
}

func (h *Host) git(args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"--git-dir", h.gitDir()}, args...)...) //nolint:gosec // G204: git on the fake's own temporary repository, with arguments the fake builds
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Mary Major", "GIT_AUTHOR_EMAIL=mary@example.com",
		"GIT_COMMITTER_NAME=Mary Major", "GIT_COMMITTER_EMAIL=mary@example.com"}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Branch is the commit a branch ends at, "" when it does not exist.
func (h *Host) Branch(name string) string {
	out, err := h.git("rev-parse", "--verify", "-q", "refs/heads/"+name+"^{commit}")
	if err != nil {
		return ""
	}
	return out
}

// Tag says what a tag is: its object type ("tag" for an annotated one) and the commit it ends at; "" when absent.
func (h *Host) Tag(name string) (kind, commit string) {
	k, err := h.git("cat-file", "-t", "refs/tags/"+name)
	if err != nil {
		return "", ""
	}
	c, _ := h.git("rev-parse", "refs/tags/"+name+"^{commit}")
	return k, c
}

// TagMessage is the message of an annotated tag.
func (h *Host) TagMessage(name string) string {
	out, _ := h.git("tag", "-l", "--format=%(contents)", name)
	return out
}

// File is the content of a file on a branch ("" and false when it is not there).
func (h *Host) File(branch, path string) (string, bool) {
	out, err := h.git("show", "refs/heads/"+branch+":"+path)
	return out, err == nil
}

// Author is the author's name and e-mail of a branch's last commit.
func (h *Host) Author(branch string) string {
	out, _ := h.git("log", "-1", "--format=%an <%ae>", "refs/heads/"+branch)
	return out
}

// Parents is how many parents a branch's last commit has.
func (h *Host) Parents(branch string) int {
	out, _ := h.git("log", "-1", "--format=%P", "refs/heads/"+branch)
	return len(strings.Fields(out))
}
