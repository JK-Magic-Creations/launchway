// Package codecommitfake is a fake of AWS CodeCommit for tests: an httptest
// server that serves, on one address, the three things the codecommit provider
// talks to: the JSON 1.1 API (POST / with X-Amz-Target: CodeCommit_20150413.<Action>,
// the calls and fields of https://docs.aws.amazon.com/codecommit/latest/APIReference/),
// the token service's AssumeRole (a signed GET with Action=AssumeRole, the form
// of https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html)
// and Git over HTTPS (/v1/repos/<name>, served by git's own http-backend over a
// real bare repository, so a tag the provider pushes is really there).
//
// It is strict. Every request's Signature Version 4 is recomputed from what
// arrived (the canonical request is built from the headers the Authorization
// header lists as signed, the payload is hashed, the date and the scope are
// checked, the clock may differ by 15 minutes at most), and so is the signed
// password of a Git request (method GIT, the repository's path, the host header
// only, the timestamp with a Z). A key id the fake does not know, a session
// token that is missing, extra or expired, a field or query parameter the
// documentation does not list for the call, a required one that is missing, a
// value of the wrong type or size, an unknown action, a wrong method or content
// type, all fail the request with the answer the host gives and are recorded in
// Violations so that a test can assert there were none (a refused signature is
// a violation of the caller, so a test that tries one on purpose clears them).
package codecommitfake

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// The identities and names of the fake.
const (
	Account = "123456789012"
	Repo    = "app"
	Region  = "us-east-1"
	// AccessKeyID and SigningKey are the one static key pair the fake knows.
	AccessKeyID = "AKFAFAKEFAKEFAKE0001"
	SigningKey  = "fake-secret-access-key-for-tests-only-0001"
	// RoleARN is the one role the key pair may assume.
	RoleARN    = "arn:aws:iam::123456789012:role/launchway-ci"
	ExternalID = "ext-launchway-1"
)

// Host is the fake.
type Host struct {
	*httptest.Server
	mu         sync.Mutex
	Violations []string
	Requests   []string
	// Now is the fake's clock (default time.Now); a test moves it to make a session expire.
	Now func() time.Time
	// Denied lists actions (or "GitPull", "GitPush") the fake answers 403 to.
	Denied map[string]bool
	// RequireExternalID makes AssumeRole demand ExternalID.
	RequireExternalID bool
	// Assumed counts the AssumeRole calls that succeeded.
	Assumed int
	dir     string
	temp    map[string]tempCred
	prs     []*pull
	cmts    map[string]*comment
	nextPR  int
	nextCmt int
	nextKey int
	tmpl    []template
	hours   int // session length in seconds the next AssumeRole gives, 0 for the requested one
}

type tempCred struct {
	secret, token string
	expires       time.Time
	arn           string
}

// New starts the fake: the repository app with the branches main and feature,
// a pull request 1 from feature to main with one approval, and an approval rule
// template of two approvals for main.
func New() *Host {
	dir, err := os.MkdirTemp("", "codecommitfake-")
	if err != nil {
		panic(err)
	}
	h := &Host{Now: time.Now, Denied: map[string]bool{}, dir: dir, temp: map[string]tempCred{}, cmts: map[string]*comment{}, nextPR: 1, nextCmt: 1, nextKey: 1}
	h.seed()
	h.Server = httptest.NewServer(http.HandlerFunc(h.serve))
	return h
}

// Close stops the fake and removes its repository.
func (h *Host) Close() {
	h.Server.Close()
	os.RemoveAll(h.dir)
}

// API is the address a provider's endpoints are set to.
func (h *Host) API() string { return h.URL }

// RepoPath is the repository's name as a connection knows it: account/name.
func (h *Host) RepoPath() string { return Account + "/" + Repo }

// CredentialDoc is the credential a connection of the static key pair holds.
func CredentialDoc() string {
	b, _ := json.Marshal(map[string]string{"access_key_id": AccessKeyID, "secret_access_key": SigningKey})
	return string(b)
}

// RoleCredentialDoc is the credential of a connection that assumes the role.
func RoleCredentialDoc() string {
	b, _ := json.Marshal(map[string]string{"access_key_id": AccessKeyID, "secret_access_key": SigningKey, "role_arn": RoleARN, "external_id": ExternalID, "role_session_name": "launchway"})
	return string(b)
}

func (h *Host) now() time.Time { return h.Now() }

func (h *Host) violate(w http.ResponseWriter, status int, typ, f string, a ...any) {
	h.mu.Lock()
	h.Violations = append(h.Violations, typ+": "+sprintf(f, a...))
	h.mu.Unlock()
	h.fail(w, status, typ, f, a...)
}

// fail answers an error the way the JSON protocol does: a "__type" and a "message".
func (h *Host) fail(w http.ResponseWriter, status int, typ, f string, a ...any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("x-amzn-ErrorType", typ)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"__type": typ, "message": sprintf(f, a...)})
}

// ClearViolations forgets what a test provoked on purpose.
func (h *Host) ClearViolations() {
	h.mu.Lock()
	h.Violations = nil
	h.mu.Unlock()
}

// Seen says a request line was made ("POST CodeCommit_20150413.PutFile", "GIT push" ...).
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

func (h *Host) record(s string) {
	h.mu.Lock()
	h.Requests = append(h.Requests, s)
	h.mu.Unlock()
}

// git runs git on the fake's repository.
func (h *Host) git(env []string, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"--git-dir", h.dir + "/" + Repo}, args...)...) //nolint:gosec // G204: git on the fake's own temporary repository, with arguments the fake builds
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Mary Major", "GIT_AUTHOR_EMAIL=mary@example.com",
		"GIT_COMMITTER_NAME=Mary Major", "GIT_COMMITTER_EMAIL=mary@example.com"}, env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Branch is the commit a branch of the repository ends at, "" when it does not exist.
func (h *Host) Branch(name string) string {
	out, err := h.git(nil, "", "rev-parse", "--verify", "-q", "refs/heads/"+name+"^{commit}")
	if err != nil {
		return ""
	}
	return out
}

// Tag says what a tag of the repository is: its object type ("tag" for an annotated one) and the commit it ends at; "" when absent.
func (h *Host) Tag(name string) (kind, commit string) {
	k, err := h.git(nil, "", "cat-file", "-t", "refs/tags/"+name)
	if err != nil {
		return "", ""
	}
	c, _ := h.git(nil, "", "rev-parse", "refs/tags/"+name+"^{commit}")
	return k, c
}

// TagMessage is the message of an annotated tag.
func (h *Host) TagMessage(name string) string {
	out, _ := h.git(nil, "", "tag", "-l", "--format=%(contents)", name)
	return out
}

// File is the content of a file on a branch ("" and false when it is not there).
func (h *Host) File(branch, path string) (string, bool) {
	out, err := h.git(nil, "", "show", "refs/heads/"+branch+":"+path)
	return out, err == nil
}

// Comment is the content of a comment ("" and false when there is none).
func (h *Host) Comment(id string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.cmts[id]
	if !ok {
		return "", false
	}
	return c.content, true
}

// Comments is how many comments there are.
func (h *Host) Comments() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.cmts)
}

// OpenPull adds a pull request from a branch to main and returns its number.
func (h *Host) OpenPull(src, title string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.addPull(src, "main", title)
}
