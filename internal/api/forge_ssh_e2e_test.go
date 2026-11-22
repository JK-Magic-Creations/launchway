package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// These tests run the real git and ssh clients against the built-in SSH server
// and the HTTPS endpoint of the same server. No client can prompt: the
// environment is testlib.GitEnv, ssh runs in batch mode, and the known-hosts
// file and every key live in the test's temporary directory (never ~/.ssh).

type sshHarness struct {
	e     *authEnv
	port  string
	known string
	dir   string
}

func sshEnv(t *testing.T) *sshHarness { return sshEnvWith(t, envOpts{}, nil) }

func sshEnvWith(t *testing.T, o envOpts, mod func(*SSHOptions)) *sshHarness {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh client")
	}
	// The OpenSSH client refuses to run for a user with no account entry ("No user exists for
	// uid N"), which is how some containers run a step. These tests need the real client, so
	// they are skipped there unless LAUNCHWAY_REQUIRE_SSH_TESTS says they must run.
	if _, err := user.LookupId(strconv.Itoa(os.Getuid())); err != nil {
		if os.Getenv("LAUNCHWAY_REQUIRE_SSH_TESTS") != "" {
			t.Fatalf("the ssh client cannot run here: no account entry for uid %d", os.Getuid())
		}
		t.Skipf("the ssh client cannot run here: no account entry for uid %d", os.Getuid())
	}
	e, _ := hostedEnv(t, o)
	addr := make(chan net.Addr, 1)
	so := SSHOptions{Listen: "127.0.0.1:0", Ready: func(a net.Addr) { addr <- a }}
	if mod != nil {
		mod(&so)
	}
	if err := e.api.StartSSH(t.Context(), so); err != nil {
		t.Fatal(err)
	}
	a := <-addr
	_, port, _ := net.SplitHostPort(a.String())
	e.api.Forge.SSH = SSHInfo{Enabled: true, Host: "127.0.0.1", Port: atoi(port)}
	dir := t.TempDir()
	return &sshHarness{e: e, port: port, known: filepath.Join(dir, "known_hosts"), dir: dir}
}

func atoi(s string) int { var n int; fmt.Sscan(s, &n); return n }

// key makes an ed25519 key pair on disk and returns the private key's path and
// the public key line.
func (h *sshHarness) key(t *testing.T, name string) (path, pub string) {
	t.Helper()
	p, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := ssh.MarshalPrivateKey(priv, name)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(h.dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}
	sp, _ := ssh.NewPublicKey(p)
	return path, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
}

func (h *sshHarness) sshCommand(key string) string {
	return fmt.Sprintf("ssh -F /dev/null -i %s -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s -o ConnectTimeout=10 -p %s",
		key, h.known, h.port)
}

func (h *sshHarness) url(path string) string {
	return "ssh://git@127.0.0.1:" + h.port + "/" + path + ".git"
}

// git runs git over SSH with a key.
func (h *sshHarness) git(t *testing.T, key, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "init.defaultBranch=main", "-c", "protocol.version=2"}, args...)...)
	cmd.Dir = dir
	cmd.Env = testlib.GitEnv(t, "GIT_SSH_COMMAND="+h.sshCommand(key), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (h *sshHarness) mustGit(t *testing.T, key, dir string, args ...string) string {
	t.Helper()
	out, err := h.git(t, key, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// register adds a public key for the browser's user.
func (h *sshHarness) register(t *testing.T, b *browser, name, pub string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "public_key": pub})
	if r := b.do("POST", h.e.srv.URL+"/api/v1/user/ssh-keys", string(body)); r.status != 201 {
		t.Fatalf("adding a key: %d %s", r.status, r.body)
	}
}

func (h *sshHarness) deploy(t *testing.T, b *browser, repo, name, pub string, write bool) response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "public_key": pub, "write": write})
	return b.do("POST", h.e.srv.URL+"/api/v1/repositories/"+repo+"/deploy-keys", string(body))
}

func TestSSHCloneFetchPushAndProtocolV2(t *testing.T) {
	h := sshEnv(t)
	hostedRepo(t, h.e, "acme/shop")
	ada := h.e.signIn(t, "ada")
	key, pub := h.key(t, "ada")
	h.register(t, ada, "laptop", pub)

	src := workingCopy(t)
	h.mustGit(t, key, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/main")
	c := t.TempDir()
	h.mustGit(t, key, c, "clone", "-q", h.url("acme/shop"), "c")
	if got := strings.TrimSpace(mustGit(t, filepath.Join(c, "c"), "log", "-1", "--format=%s")); got != "first" {
		t.Fatalf("cloned %q", got)
	}
	// A second commit travels the other way.
	id := commit(t, filepath.Join(c, "c"), "b.txt", "two\n", "second")
	h.mustGit(t, key, filepath.Join(c, "c"), "push", "-q", "origin", "main")
	h.mustGit(t, key, src, "fetch", "-q", h.url("acme/shop"), "main")
	if got := strings.TrimSpace(mustGit(t, src, "rev-parse", "FETCH_HEAD")); got != id {
		t.Fatalf("fetch saw %s, want %s", got, id)
	}
	// git archive --remote is git-upload-archive over the same connection.
	if tar := h.mustGit(t, key, src, "archive", "--format=tar", "--remote="+h.url("acme/shop"), "main"); !strings.Contains(tar, "a.txt") || !strings.Contains(tar, "b.txt") {
		t.Errorf("the archive does not hold the files (%d bytes)", len(tar))
	}
	// Protocol version 2 was used for the clone.
	cmd := exec.Command("git", "-c", "protocol.version=2", "ls-remote", h.url("acme/shop"))
	cmd.Env = testlib.GitEnv(t, "GIT_SSH_COMMAND="+h.sshCommand(key), "GIT_TRACE_PACKET=1")
	trace, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(trace), "version 2") {
		t.Errorf("protocol v2 was not used: %v\n%s", err, trace)
	}
	// The key shows when and from where it was used.
	var ks struct {
		Items []struct {
			LastUsedFrom string `json:"last_used_from"`
			Fingerprint  string
		}
	}
	ada.get(h.e.srv.URL+"/api/v1/user/ssh-keys").json(t, &ks)
	if len(ks.Items) != 1 || ks.Items[0].LastUsedFrom != "127.0.0.1" {
		t.Errorf("keys: %+v", ks)
	}
	// The push is in the audit trail, with the key.
	var found bool
	for _, a := range h.e.auditEntries(t) {
		if a.Action == "git.push" && a.Outcome == "ok" && strings.Contains(a.Detail, "ssh key SHA256:") && strings.Contains(a.Detail, "refs/heads/main") && a.Source == "127.0.0.1" {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit entry of the SSH push")
	}
	// The host key a client saw is the one the server's page lists.
	var info struct {
		Enabled  bool
		HostKeys []struct{ Fingerprint, Type string } `json:"host_keys"`
	}
	ada.get(h.e.srv.URL+"/api/v1/ssh").json(t, &info)
	if !info.Enabled || len(info.HostKeys) != 3 || info.HostKeys[0].Type != "ssh-ed25519" {
		t.Fatalf("ssh info: %+v", info)
	}
	known, _ := os.ReadFile(h.known)
	if !strings.Contains(string(known), "ssh-ed25519") {
		t.Fatalf("known hosts: %s", known)
	}
}

// A push refused over HTTPS is refused over SSH, with the same words: one decision, one policy.
func TestSSHAndHTTPSRefuseAPushAlike(t *testing.T) {
	h := sshEnv(t)
	hostedRepo(t, h.e, "acme/shop")
	ada, otto, vic := h.e.signIn(t, "ada"), h.e.signIn(t, "otto"), h.e.signIn(t, "vic")
	adaKey, adaPub := h.key(t, "ada")
	ottoKey, ottoPub := h.key(t, "otto")
	vicKey, vicPub := h.key(t, "vic")
	h.register(t, ada, "a", adaPub)
	h.register(t, otto, "o", ottoPub)
	h.register(t, vic, "v", vicPub)
	ottoTok := h.e.tokenFor(t, "otto", "t")
	vicTok := h.e.tokenFor(t, "vic", "t")

	src := workingCopy(t)
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/main")
	if r := ada.do("PUT", h.e.srv.URL+"/api/v1/repositories/acme/shop/protected-branches", `{"patterns":["main","release/*"]}`); r.status != 200 {
		t.Fatalf("protect: %d %s", r.status, r.body)
	}
	if r := otto.do("PUT", h.e.srv.URL+"/api/v1/repositories/acme/shop/protected-branches", `{"patterns":[]}`); r.status != 403 {
		t.Errorf("an operator changed the protection: %d", r.status)
	}
	commit(t, src, "c.txt", "x\n", "more")
	httpsURL := withToken(h.e.srv.URL, "/git/acme/shop.git", ottoTok)

	reason := func(out string) string {
		for _, l := range strings.Split(out, "\n") {
			if i := strings.Index(l, "protected"); i >= 0 {
				return l[strings.Index(l, "the branch"):]
			}
		}
		return ""
	}
	sshOut, sshErr := h.git(t, ottoKey, src, "push", h.url("acme/shop"), "HEAD:refs/heads/main")
	httpsOut, httpsErr := git(t, src, "push", httpsURL, "HEAD:refs/heads/main")
	if sshErr == nil || httpsErr == nil {
		t.Fatalf("a protected branch took a push: ssh %v https %v", sshErr, httpsErr)
	}
	if a, b := reason(sshOut), reason(httpsOut); a == "" || a != b {
		t.Errorf("the refusals differ:\nssh:   %q\nhttps: %q\n%s\n%s", a, b, sshOut, httpsOut)
	}
	// Deleting a protected branch is refused for everyone, an administrator too.
	if out, err := h.git(t, adaKey, src, "push", h.url("acme/shop"), ":refs/heads/main"); err == nil || !strings.Contains(out, "cannot be deleted") {
		t.Errorf("delete of a protected branch: %v\n%s", err, out)
	}
	// An administrator may push to it; anyone may push to another branch.
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/main")
	h.mustGit(t, ottoKey, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/topic")
	// A viewer may not push anywhere: the permission message is the same on both.
	sshOut, sshErr = h.git(t, vicKey, src, "push", h.url("acme/shop"), "HEAD:refs/heads/topic2")
	httpsOut, httpsErr = git(t, src, "push", withToken(h.e.srv.URL, "/git/acme/shop.git", vicTok), "HEAD:refs/heads/topic2")
	const want = "you need the operator role on acme/shop to push to it"
	if sshErr == nil || httpsErr == nil || !strings.Contains(sshOut, want) || !strings.Contains(httpsOut, want) {
		t.Errorf("a viewer's push: ssh %v https %v\n%s\n%s", sshErr, httpsErr, sshOut, httpsOut)
	}
	// The refusals are audited as denied, over both transports.
	var denied int
	for _, a := range h.e.auditEntries(t) {
		if a.Action == "git.push" && a.Outcome == "denied" && strings.Contains(a.Detail, want) {
			denied++
		}
	}
	if denied != 2 {
		t.Errorf("%d denied audit entries for the viewer, want 2", denied)
	}
}

func TestSSHDeployKeys(t *testing.T) {
	h := sshEnv(t)
	hostedRepo(t, h.e, "acme/shop")
	hostedRepo(t, h.e, "acme/other")
	ada := h.e.signIn(t, "ada")
	adaKey, adaPub := h.key(t, "ada")
	h.register(t, ada, "a", adaPub)
	src := workingCopy(t)
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/main")
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/other"), "HEAD:refs/heads/main")

	roKey, roPub := h.key(t, "ro")
	rwKey, rwPub := h.key(t, "rw")
	if r := h.deploy(t, ada, "acme/shop", "ci", roPub, false); r.status != 201 {
		t.Fatalf("read-only deploy key: %d %s", r.status, r.body)
	}
	if r := h.deploy(t, ada, "acme/shop", "release", rwPub, true); r.status != 201 {
		t.Fatalf("write deploy key: %d %s", r.status, r.body)
	}
	// A key belongs to one owner: the same key cannot be a person's key or another repository's deploy key.
	if r := h.deploy(t, ada, "acme/other", "again", roPub, false); r.status != 409 {
		t.Errorf("a key registered twice: %d %s", r.status, r.body)
	}
	body, _ := json.Marshal(map[string]string{"name": "mine", "public_key": roPub})
	if r := ada.do("POST", h.e.srv.URL+"/api/v1/user/ssh-keys", string(body)); r.status != 409 {
		t.Errorf("a deploy key added as a person's key: %d %s", r.status, r.body)
	}

	c := t.TempDir()
	h.mustGit(t, roKey, c, "clone", "-q", h.url("acme/shop"), "c")
	commit(t, filepath.Join(c, "c"), "d.txt", "x\n", "from ci")
	out, err := h.git(t, roKey, filepath.Join(c, "c"), "push", "origin", "main")
	if err == nil || !strings.Contains(out, "this deploy key is read-only") {
		t.Errorf("a read-only deploy key pushed: %v\n%s", err, out)
	}
	h.mustGit(t, rwKey, filepath.Join(c, "c"), "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/ci")
	// A deploy key opens its own repository only; any other answers "not found".
	out, err = h.git(t, rwKey, t.TempDir(), "clone", h.url("acme/other"), "x")
	if err == nil || !strings.Contains(out, "repository not found") {
		t.Errorf("a deploy key reached another repository: %v\n%s", err, out)
	}
	// Deleting a key stops it at once.
	var ks struct{ Items []struct{ ID, Name string } }
	ada.get(h.e.srv.URL+"/api/v1/repositories/acme/shop/deploy-keys").json(t, &ks)
	if len(ks.Items) != 2 {
		t.Fatalf("deploy keys: %+v", ks)
	}
	for _, k := range ks.Items {
		if r := ada.do("DELETE", h.e.srv.URL+"/api/v1/repositories/acme/shop/deploy-keys/"+k.ID, ""); r.status != 204 {
			t.Fatalf("delete: %d %s", r.status, r.body)
		}
	}
	if out, err := h.git(t, roKey, t.TempDir(), "clone", h.url("acme/shop"), "x"); err == nil {
		t.Errorf("a deleted key cloned:\n%s", out)
	}
}

func TestSSHRefusesWhatItShould(t *testing.T) {
	h := sshEnv(t)
	hostedRepo(t, h.e, "acme/shop")
	ada := h.e.signIn(t, "ada")
	adaKey, adaPub := h.key(t, "ada")
	h.register(t, ada, "a", adaPub)
	strangerKey, _ := h.key(t, "stranger")

	// An unknown key is refused at sign-in.
	out, err := h.git(t, strangerKey, t.TempDir(), "clone", h.url("acme/shop"), "x")
	if err == nil || !strings.Contains(out, "Permission denied") {
		t.Errorf("an unknown key: %v\n%s", err, out)
	}
	// A repository that does not exist.
	out, err = h.git(t, adaKey, t.TempDir(), "clone", h.url("acme/nothing"), "x")
	if err == nil || !strings.Contains(out, "repository not found") {
		t.Errorf("a missing repository: %v\n%s", err, out)
	}
	run := func(args ...string) string {
		cmd := exec.Command("sh", "-c", h.sshCommand(adaKey)+" "+strings.Join(args, " "))
		cmd.Env = testlib.GitEnv(t)
		o, _ := cmd.CombinedOutput()
		return string(o)
	}
	// A shell command is refused with a plain line.
	if o := run("-T", "git@127.0.0.1", "ls", "/"); !strings.Contains(o, "shell access is not provided") || strings.Count(launchwayLines(o), "\n") > 1 {
		t.Errorf("a shell command: %q", o)
	}
	// A bare ssh -T is greeted by name.
	if o := run("-T", "git@127.0.0.1"); !strings.Contains(o, "Hi ada! You are signed in to Launchway; shell access is not provided.") {
		t.Errorf("greeting: %q", o)
	}
	// A path that is not owner/name.
	if o := run("-T", "git@127.0.0.1", "git-upload-pack", "'../../etc/passwd'"); !strings.Contains(o, "owner/name") {
		t.Errorf("a path outside the repositories: %q", o)
	}
	// Port forwarding, agent forwarding and ptys are refused by the protocol itself.
	signer := loadSigner(t, adaKey)
	cl, err := ssh.Dial("tcp", "127.0.0.1:"+h.port, &ssh.ClientConfig{User: "git", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if c, err := cl.Dial("tcp", "127.0.0.1:22"); err == nil {
		c.Close()
		t.Error("port forwarding was accepted")
	}
	sess, _ := cl.NewSession()
	if err := sess.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err == nil {
		t.Error("a pty was granted")
	}
	if ok, _, _ := cl.SendRequest("tcpip-forward", true, ssh.Marshal(struct {
		Addr string
		Port uint32
	}{"0.0.0.0", 9999})); ok {
		t.Error("remote forwarding was accepted")
	}
	// Old and weak algorithms are not offered.
	for _, bad := range []string{"aes128-cbc", "3des-cbc"} {
		c := ssh.ClientConfig{User: "git", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second}
		c.Ciphers = []string{bad}
		if cc, err := ssh.Dial("tcp", "127.0.0.1:"+h.port, &c); err == nil {
			cc.Close()
			t.Errorf("cipher %s was accepted", bad)
		}
	}
}

func loadSigner(t *testing.T, path string) ssh.Signer {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSSHKeyRulesAndExpiry(t *testing.T) {
	h := sshEnv(t)
	hostedRepo(t, h.e, "acme/shop")
	ada, otto := h.e.signIn(t, "ada"), h.e.signIn(t, "otto")
	url := h.e.srv.URL + "/api/v1/user/ssh-keys"
	_, pub := h.key(t, "ada")
	rsaWeak, _ := rsa.GenerateKey(rand.Reader, 2048)
	weak, _ := ssh.NewPublicKey(&rsaWeak.PublicKey)
	add := func(b *browser, body map[string]string) response {
		raw, _ := json.Marshal(body)
		return b.do("POST", url, string(raw))
	}
	for name, c := range map[string]struct{ key, want string }{
		"a short RSA key": {string(ssh.MarshalAuthorizedKey(weak)), "2048 bits and at least 3072"},
		"a DSA key":       {"ssh-dss AAAAB3NzaC1kc3MAAACBAP== dsa", "DSA keys"},
		"a private key":   {"-----BEGIN OPENSSH PRIVATE KEY-----\nxx", "that is a private key"},
		"not a key":       {"hello", "not a public key"},
		"an option":       {`command="ls" ` + pub, "options"},
		"an empty key":    {"", "paste a public key"},
	} {
		r := add(ada, map[string]string{"name": "k", "public_key": c.key})
		if r.status != 400 || !strings.Contains(string(r.body), c.want) {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	if r := add(ada, map[string]string{"name": "past", "public_key": pub, "expires_at": h.e.clk.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}); r.status != 400 {
		t.Errorf("an expiry in the past: %d %s", r.status, r.body)
	}
	if r := add(ada, map[string]string{"name": "ok", "public_key": pub}); r.status != 201 {
		t.Fatalf("add: %d %s", r.status, r.body)
	}
	if r := add(otto, map[string]string{"name": "mine now", "public_key": pub}); r.status != 409 {
		t.Errorf("a key with two owners: %d %s", r.status, r.body)
	}
	var mine struct {
		Items []struct{ ID, Fingerprint string }
	}
	ada.get(url).json(t, &mine)
	if len(mine.Items) != 1 || !strings.HasPrefix(mine.Items[0].Fingerprint, "SHA256:") {
		t.Fatalf("keys: %+v", mine)
	}
	// Someone else's key is not there for otto to delete.
	if r := otto.do("DELETE", url+"/"+mine.Items[0].ID, ""); r.status != 404 {
		t.Errorf("deleting another person's key: %d", r.status)
	}
	if r := ada.do("DELETE", url+"/"+mine.Items[0].ID, ""); r.status != 204 {
		t.Errorf("delete: %d", r.status)
	}
	// An expired key is refused at sign-in.
	k2, pub2 := h.key(t, "soon")
	soon := h.e.clk.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if r := add(ada, map[string]string{"name": "soon", "public_key": pub2, "expires_at": soon}); r.status != 201 {
		t.Fatalf("add with expiry: %d %s", r.status, r.body)
	}
	if _, err := h.git(t, k2, t.TempDir(), "ls-remote", h.url("acme/shop")); err != nil {
		t.Fatalf("a key before its expiry: %v", err)
	}
	h.e.clk.Add(2 * time.Hour)
	if out, err := h.git(t, k2, t.TempDir(), "ls-remote", h.url("acme/shop")); err == nil {
		t.Errorf("an expired key worked:\n%s", out)
	}
}

// Several pushes to one branch from the same base: exactly one wins, as over HTTPS.
func TestSSHConcurrentPushesToOneBranchOneWins(t *testing.T) {
	h := sshEnv(t)
	hostedRepo(t, h.e, "acme/shop")
	ada := h.e.signIn(t, "ada")
	key, pub := h.key(t, "ada")
	h.register(t, ada, "a", pub)
	src := workingCopy(t)
	h.mustGit(t, key, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/main")
	const n = 4
	clones := make([]string, n)
	for i := range clones {
		d := t.TempDir()
		h.mustGit(t, key, d, "clone", "-q", h.url("acme/shop"), "c")
		clones[i] = filepath.Join(d, "c")
		commit(t, clones[i], fmt.Sprintf("f%d.txt", i), fmt.Sprint(i), fmt.Sprintf("racer %d", i))
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range clones {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = h.git(t, key, clones[i], "push", "origin", "main")
		}()
	}
	close(start)
	wg.Wait()
	wins := 0
	for _, e := range errs {
		if e == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("%d pushes won, want exactly one: %v", wins, errs)
	}
	out := h.mustGit(t, key, src, "ls-remote", h.url("acme/shop"), "refs/heads/main")
	if !strings.Contains(out, "refs/heads/main") {
		t.Errorf("ls-remote: %s", out)
	}
}

func TestSSHStatusIsOffWithoutTheServer(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada := e.signIn(t, "ada")
	var info struct{ Enabled bool }
	r := ada.get(e.srv.URL + "/api/v1/ssh")
	r.json(t, &info)
	if r.status != http.StatusOK || info.Enabled {
		t.Errorf("ssh info with SSH off: %d %s", r.status, r.body)
	}
	_ = forge.Public
}

// launchwayLines keeps the lines the server wrote (the client may add its own warnings).
func launchwayLines(out string) string {
	var b strings.Builder
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "Launchway:") {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

// A certificate signed by the configured authority signs a person in by its
// first principal; one signed by another authority, an expired one and one
// with a critical option are refused; with no authority configured,
// certificates are not accepted at all.
func TestSSHCertificatesFromTheConfiguredAuthority(t *testing.T) {
	p, err := auth.NewPolicy([]auth.Binding{{User: "cert-user", Role: auth.Operator, Projects: []string{"acme/*"}}})
	if err != nil {
		t.Fatal(err)
	}
	caPub, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	ca, _ := ssh.NewSignerFromKey(caPriv)
	caKey, _ := ssh.NewPublicKey(caPub)
	h := sshEnvWith(t, envOpts{policy: p}, func(o *SSHOptions) { o.UserCAs = []ssh.PublicKey{caKey} })
	hostedRepo(t, h.e, "acme/shop")

	sign := func(name string, signer ssh.Signer, mod func(*ssh.Certificate)) string {
		keyPath, pub := h.key(t, name)
		pk, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(pub))
		now := time.Now()
		cert := &ssh.Certificate{Key: pk, Serial: 7, CertType: ssh.UserCert, KeyId: name, ValidPrincipals: []string{"cert-user"},
			ValidAfter: uint64(now.Add(-time.Hour).Unix()), ValidBefore: uint64(now.Add(time.Hour).Unix())}
		if mod != nil {
			mod(cert)
		}
		if err := cert.SignCert(rand.Reader, signer); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath+"-cert.pub", ssh.MarshalAuthorizedKey(cert), 0o644); err != nil {
			t.Fatal(err)
		}
		return keyPath
	}
	good := sign("good", ca, nil)
	// ssh offers <key>-cert.pub next to the identity file by itself.
	src := workingCopy(t)
	h.mustGit(t, good, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/topic")
	h.mustGit(t, good, t.TempDir(), "clone", "-q", h.url("acme/shop"), "x")

	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherCA, _ := ssh.NewSignerFromKey(otherPriv)
	for name, key := range map[string]string{
		"a certificate from another authority": sign("foreign", otherCA, nil),
		"an expired certificate": sign("expired", ca, func(c *ssh.Certificate) {
			c.ValidAfter, c.ValidBefore = uint64(time.Now().Add(-2*time.Hour).Unix()), uint64(time.Now().Add(-time.Hour).Unix())
		}),
		"a certificate with a critical option": sign("critical", ca, func(c *ssh.Certificate) {
			c.CriticalOptions = map[string]string{"force-command": "ls"}
		}),
		"a host certificate": sign("host", ca, func(c *ssh.Certificate) { c.CertType = ssh.HostCert }),
	} {
		if out, err := h.git(t, key, t.TempDir(), "ls-remote", h.url("acme/shop")); err == nil {
			t.Errorf("%s was accepted:\n%s", name, out)
		}
	}
	// The person is the principal; a push is audited as theirs, with the certificate.
	var found bool
	for _, a := range h.e.auditEntries(t) {
		found = found || a.Action == "git.push" && a.User == "cert-user" && strings.Contains(a.Detail, "ssh key certificate 7")
	}
	if !found {
		t.Error("the push by certificate is not in the audit trail")
	}
	// With no authority configured, a certificate gets nowhere.
	plain := sshEnvWith(t, envOpts{policy: p}, nil)
	hostedRepo(t, plain.e, "acme/shop")
	if out, err := plain.git(t, good, t.TempDir(), "ls-remote", plain.url("acme/shop")); err == nil {
		t.Errorf("a certificate was accepted with no authority configured:\n%s", out)
	}
}
