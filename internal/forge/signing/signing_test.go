package signing

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

type fixture struct {
	t    *testing.T
	dir  string
	st   *Store
	svc  *Service
	now  time.Time
	keys map[string]string // name -> private key path
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	for _, bin := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	f := &fixture{t: t, dir: t.TempDir(), st: NewStore(eng.Database()), keys: map[string]string{}, now: time.Now()}
	f.svc = &Service{Store: f.st, Emails: func(_ context.Context, u string) []string {
		if u == "dana" {
			return []string{"dana@example.com"}
		}
		return nil
	}}
	f.git("init", "-q", "-b", "main")
	return f
}

func (f *fixture) run(name string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = f.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=dana@example.com",
		"GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=dana@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) git(args ...string) string { return f.run("git", args...) }

// key makes an SSH key and returns its public line.
func (f *fixture) key(name string) string {
	path := filepath.Join(f.dir, "key-"+name)
	f.run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", path)
	f.keys[name] = path
	b, err := os.ReadFile(path + ".pub")
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func (f *fixture) commit(msg string, signWith string, env ...string) string {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "f.txt"), []byte(msg), 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.git("add", "f.txt")
	args := []string{"-c", "gpg.format=ssh", "commit", "-q", "-m", msg}
	if signWith != "" {
		args = []string{"-c", "gpg.format=ssh", "-c", "user.signingkey=" + f.keys[signWith], "commit", "-q", "-S", "-m", msg}
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = f.dir
	cmd.Env = append(append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=dana@example.com",
		"GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=dana@example.com"), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("commit: %v\n%s", err, out)
	}
	return f.git("rev-parse", "HEAD")
}

func (f *fixture) verify(kind, sha string) Result {
	f.t.Helper()
	raw := f.git("cat-file", kind, sha)
	return f.svc.verifier().Verify(context.Background(), sha, kind, []byte(raw+"\n"), "")
}

func TestCommitStates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pub := f.key("dana-laptop")
	signed := f.commit("signed", "dana-laptop")
	unsigned := f.commit("plain", "")

	// Unknown key until it is registered.
	if r := f.verify("commit", signed); r.State != UnknownKey || r.KeyFingerprint == "" {
		t.Fatalf("unregistered: %+v", r)
	}
	k, err := f.st.Add(ctx, NewKey{Owner: "dana", Name: "laptop", PublicKey: pub})
	if err != nil {
		t.Fatal(err)
	}
	r := f.verify("commit", signed)
	if r.State != Verified || r.Signer != "dana" || !r.SignerIsAuthor || !r.SignerIsCommitter || r.KeyName != "laptop" {
		t.Fatalf("registered: %+v", r)
	}
	if r := f.verify("commit", unsigned); r.State != Unsigned {
		t.Fatalf("unsigned: %+v", r)
	}
	// Someone else's address: verified, but the signer is not the author.
	other := f.commit("by someone else", "dana-laptop", "GIT_AUTHOR_EMAIL=eve@example.com", "GIT_COMMITTER_EMAIL=eve@example.com")
	if r := f.verify("commit", other); r.State != Verified || r.SignerIsAuthor || r.SignerIsCommitter || !strings.Contains(r.Explanation, "do not include") {
		t.Fatalf("other author: %+v", r)
	}

	// A changed object is a bad signature, and the verdict is cached.
	raw := f.git("cat-file", "commit", signed)
	tampered := strings.Replace(raw, "\nsigned", "\nsigned (edited)", 1)
	if tampered == raw {
		t.Fatal("the test did not change the commit")
	}
	bad := f.svc.verifier().Verify(ctx, "tampered", "commit", []byte(tampered+"\n"), "")
	if bad.State != BadSignature {
		t.Fatalf("tampered: %+v", bad)
	}
	if c, ok := f.st.GetCached(ctx, "tampered"); !ok || c.CryptoOK {
		t.Fatalf("cache: %+v %v", c, ok)
	}
	if c, ok := f.st.GetCached(ctx, signed); !ok || !c.CryptoOK {
		t.Fatalf("cache of the good commit: %+v %v", c, ok)
	}

	// Revoked an hour ago: a commit dated after that is revoked. Revoked in the future: still verified.
	if _, err := f.st.db.Exec(ctx, `UPDATE signing_keys SET revoked_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).UnixMilli(), k.ID); err != nil {
		t.Fatal(err)
	}
	if r := f.verify("commit", signed); r.State != KeyRevoked {
		t.Fatalf("revoked: %+v", r)
	}
	if _, err := f.st.db.Exec(ctx, `UPDATE signing_keys SET revoked_at = ? WHERE id = ?`, time.Now().Add(time.Hour).UnixMilli(), k.ID); err != nil {
		t.Fatal(err)
	}
	if r := f.verify("commit", signed); r.State != Verified {
		t.Fatalf("revoked later than the commit: %+v", r)
	}
	if err := f.st.Revoke(ctx, k.ID); err != nil { // already revoked: no change, no error
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(ctx, `UPDATE signing_keys SET revoked_at = 0, expires_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).UnixMilli(), k.ID); err != nil {
		t.Fatal(err)
	}
	if r := f.verify("commit", signed); r.State != KeyExpired {
		t.Fatalf("expired before the commit: %+v", r)
	}
	if err := f.st.Delete(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if r := f.verify("commit", signed); r.State != UnknownKey {
		t.Fatalf("deleted: %+v", r)
	}
}

func TestAnnotatedTagAndOpenPGP(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pub := f.key("rel")
	if _, err := f.st.Add(ctx, NewKey{Owner: "dana", Name: "rel", PublicKey: pub}); err != nil {
		t.Fatal(err)
	}
	f.commit("c", "")
	f.git("-c", "gpg.format=ssh", "-c", "user.signingkey="+f.keys["rel"], "tag", "-s", "-m", "release one", "v1")
	tag := f.git("rev-parse", "v1")
	if r := f.verify("tag", tag); r.State != Verified || r.Kind != "tag" || !r.SignerIsAuthor {
		t.Fatalf("signed tag: %+v", r)
	}
	f.git("tag", "-a", "-m", "plain", "v2")
	if r := f.verify("tag", f.git("rev-parse", "v2")); r.State != Unsigned {
		t.Fatalf("unsigned tag: %+v", r)
	}
	pgp := "tree 1\nauthor A <a@x> 1 +0000\ncommitter A <a@x> 1 +0000\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n abc\n -----END PGP SIGNATURE-----\n\nmsg\n"
	if r := f.svc.verifier().Verify(ctx, "pgp", "commit", []byte(pgp), ""); r.State != Unsupported || !strings.Contains(r.Explanation, "OpenPGP") {
		t.Fatalf("openpgp: %+v", r)
	}
	if _, err := f.st.Add(ctx, NewKey{Owner: "dana", PublicKey: "-----BEGIN PGP PUBLIC KEY BLOCK-----"}); err == nil || !strings.Contains(err.Error(), "OpenPGP") {
		t.Fatalf("a PGP key must be refused for now: %v", err)
	}
}

type fakeSealer struct{ m map[string]string }

func (s *fakeSealer) Seal(_ context.Context, row string, plain []byte) (string, error) {
	s.m[row] = "sealed:" + string(plain)
	return "sealed:" + string(plain), nil
}
func (s *fakeSealer) Open(_ context.Context, _, stored string) ([]byte, error) {
	return []byte(strings.TrimPrefix(stored, "sealed:")), nil
}

// A commit Launchway signs itself is verified by Git, and by Launchway.
func TestInstallationKeySignsCommitsGitVerifies(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seal := &fakeSealer{m: map[string]string{}}
	f.svc.Seal = func(context.Context) Sealer { return seal }
	text, fp, err := f.svc.PublicKey(ctx)
	if err != nil || !strings.HasPrefix(text, "ssh-ed25519 ") || !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("%q %q %v", text, fp, err)
	}
	// A second server (a new Service over the same database) reads the same key back.
	again := &Service{Store: f.st, Seal: f.svc.Seal}
	if t2, fp2, err := again.PublicKey(ctx); err != nil || fp2 != fp || t2 != text {
		t.Fatalf("second: %q %q %v", t2, fp2, err)
	}
	if strings.Contains(seal.m["installation"], "ssh-ed25519") {
		t.Fatal("the sealed value looks like a public key")
	}
	f.commit("base", "")
	tree := f.git("rev-parse", "HEAD^{tree}")
	raw := f.git("commit-tree", tree, "-m", "made by the product") + ""
	body := f.git("cat-file", "commit", raw) + "\n"
	p, err := Parse("commit", []byte(body))
	if err != nil || p.Signed() {
		t.Fatalf("%+v %v", p, err)
	}
	sig, err := f.svc.SignPayload(ctx, p.Payload)
	if err != nil {
		t.Fatal(err)
	}
	signedRaw, err := AddCommitSignature([]byte(body), sig)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir = f.dir
	cmd.Stdin = strings.NewReader(string(signedRaw))
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(out))
	allowed := filepath.Join(f.dir, "allowed")
	if err := os.WriteFile(allowed, []byte("launchway "+text+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.git("-c", "gpg.ssh.allowedSignersFile="+allowed, "verify-commit", sha) // Git itself accepts it
	r := f.verify("commit", sha)
	if r.State != Verified || !r.Installation || r.Signer != InstallationOwner {
		t.Fatalf("%+v", r)
	}
}
