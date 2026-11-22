package signing

import (
	"bytes"
	"context"
	"crypto"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// pgpWorld is a signing store and verifier on a throwaway database, with keys made by the OpenPGP library
// itself. No key here is a real one.
type pgpWorld struct {
	t   *testing.T
	st  *Store
	svc *Service
}

func newPGPWorld(t *testing.T) *pgpWorld {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	st := NewStore(eng.Database())
	return &pgpWorld{t: t, st: st, svc: &Service{Store: st, Emails: func(_ context.Context, u string) []string {
		if u == "dana" {
			return []string{"dana@example.com"}
		}
		return nil
	}}}
}

var testNow = time.Now().UTC().Truncate(time.Second)

// newEntity makes a key of the named kind, made at "at" with the given lifetime in seconds (0 for none).
func newEntity(t *testing.T, kind string, at time.Time, lifetime uint32) *openpgp.Entity {
	t.Helper()
	cfg := &packet.Config{Time: func() time.Time { return at }, KeyLifetimeSecs: lifetime}
	switch kind {
	case "rsa":
		cfg.Algorithm, cfg.RSABits = packet.PubKeyAlgoRSA, 3072
	case "ecdsa":
		cfg.Algorithm, cfg.Curve = packet.PubKeyAlgoECDSA, packet.CurveNistP256
	case "ed25519":
		cfg.Algorithm = packet.PubKeyAlgoEdDSA
		cfg.Curve = packet.Curve25519
	default:
		t.Fatalf("kind %s", kind)
	}
	e, err := openpgp.NewEntity("Dana", "", "dana@example.com", cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func armoredPublic(t *testing.T, e *openpgp.Entity) string {
	t.Helper()
	var b bytes.Buffer
	w, err := armor.Encode(&b, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return b.String()
}

func armoredPrivate(t *testing.T, e *openpgp.Entity) string {
	t.Helper()
	var b bytes.Buffer
	w, _ := armor.Encode(&b, openpgp.PrivateKeyType, nil)
	if err := e.SerializePrivate(w, nil); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return b.String()
}

// pgpCommit returns a raw commit signed by e at time at; the message tells commits apart.
func pgpCommit(t *testing.T, e *openpgp.Entity, at time.Time, msg string) []byte {
	t.Helper()
	unsigned := fmt.Sprintf("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor Dana <dana@example.com> %d +0000\ncommitter Dana <dana@example.com> %d +0000\n\n%s\n", at.Unix(), at.Unix(), msg)
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, e, strings.NewReader(unsigned), &packet.Config{Time: func() time.Time { return at }}); err != nil {
		t.Fatal(err)
	}
	raw, err := AddCommitSignature([]byte(unsigned), sig.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// pgpCommitAnyway is pgpCommit for a key that the library would refuse to sign with at that time (an expired or
// revoked one): it writes the signature packet itself, as a client with a wrong clock or an old key would.
func pgpCommitAnyway(t *testing.T, e *openpgp.Entity, at time.Time, msg string) []byte {
	t.Helper()
	unsigned := fmt.Sprintf("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor Dana <dana@example.com> %d +0000\ncommitter Dana <dana@example.com> %d +0000\n\n%s\n", at.Unix(), at.Unix(), msg)
	pk := e.PrivateKey
	id := pk.KeyId
	sig := &packet.Signature{Version: pk.Version, SigType: packet.SigTypeBinary, PubKeyAlgo: pk.PubKeyAlgo, Hash: crypto.SHA256, CreationTime: at, IssuerKeyId: &id, IssuerFingerprint: pk.Fingerprint}
	cfg := &packet.Config{Time: func() time.Time { return at }}
	h, err := sig.PrepareSign(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.Write([]byte(unsigned))
	if err := sig.Sign(h, pk, cfg); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w, _ := armor.Encode(&b, openpgp.SignatureType, nil)
	if err := sig.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	raw, err := AddCommitSignature([]byte(unsigned), b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (w *pgpWorld) verify(raw []byte) Result {
	w.t.Helper()
	return w.svc.verifier().Verify(context.Background(), fmt.Sprintf("%x", raw[:20]), "commit", raw, "")
}

func TestOpenPGPCommitsAreVerifiedWithRSAECDSAAndEd25519Keys(t *testing.T) {
	for _, kind := range []string{"rsa", "ecdsa", "ed25519"} {
		t.Run(kind, func(t *testing.T) {
			w := newPGPWorld(t)
			ctx := context.Background()
			e := newEntity(t, kind, testNow.Add(-48*time.Hour), 0)
			raw := pgpCommit(t, e, testNow.Add(-time.Hour), "signed with "+kind)
			if r := w.verify(raw); r.State != UnknownKey || !strings.Contains(r.Explanation, "nobody has registered") || r.SignatureType != "openpgp" {
				t.Fatalf("before registering: %+v", r)
			}
			k, err := w.st.Add(ctx, NewKey{Owner: "dana", Name: "laptop " + kind, PublicKey: armoredPublic(t, e)})
			if err != nil {
				t.Fatal(err)
			}
			if k.Type != "openpgp" || k.Fingerprint != fpHex(e.PrimaryKey.Fingerprint) {
				t.Fatalf("the registered key: %+v", k)
			}
			if r := w.verify(raw); r.State != Verified || r.Signer != "dana" || !r.SignerIsAuthor || r.KeyName != "laptop "+kind {
				t.Fatalf("after registering: %+v", r)
			}
			// A changed commit is a bad signature.
			bad := bytes.Replace(raw, []byte("signed with"), []byte("edited with"), 1)
			if r := w.verify(bad); r.State != BadSignature {
				t.Fatalf("changed commit: %+v", r)
			}
			// The same key cannot be registered twice, by anyone.
			if _, err := w.st.Add(ctx, NewKey{Owner: "eve", Name: "stolen", PublicKey: armoredPublic(t, e)}); err == nil {
				t.Fatal("a key was registered for two people")
			}
		})
	}
}

func TestOpenPGPSigningSubkeysAndTheirFollowingOfThePrimaryKey(t *testing.T) {
	w := newPGPWorld(t)
	ctx := context.Background()
	e := newEntity(t, "ed25519", testNow.Add(-48*time.Hour), 0)
	if err := e.AddSigningSubkey(&packet.Config{Time: func() time.Time { return testNow.Add(-47 * time.Hour) }, Algorithm: packet.PubKeyAlgoEdDSA, Curve: packet.Curve25519}); err != nil {
		t.Fatal(err)
	}
	raw := pgpCommit(t, e, testNow.Add(-time.Hour), "signed by the subkey")
	sig, err := parsePGPSignature(sigOf(t, raw))
	if err != nil || sig.fingerprint == fpHex(e.PrimaryKey.Fingerprint) {
		t.Fatalf("the test needs a signature made by the subkey: %v %+v", err, sig)
	}
	k, err := w.st.Add(ctx, NewKey{Owner: "dana", Name: "laptop", PublicKey: armoredPublic(t, e)})
	if err != nil {
		t.Fatal(err)
	}
	if r := w.verify(raw); r.State != Verified || r.Signer != "dana" {
		t.Fatalf("a subkey's signature: %+v", r)
	}
	if keys, _ := w.st.List(ctx, "dana"); len(keys) != 1 {
		t.Fatalf("the subkey is listed as a key of its own: %+v", keys)
	}
	// Revoking the key revokes its subkeys: a commit dated after is "key revoked".
	if err := w.st.Revoke(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if r := w.verify(pgpCommit(t, e, testNow.Add(time.Hour), "after the revocation")); r.State != KeyRevoked {
		t.Fatalf("after revoking: %+v", r)
	}
	if err := w.st.Delete(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if r := w.verify(raw); r.State != UnknownKey {
		t.Fatalf("after deleting, the subkey must not stay behind: %+v", r)
	}
}

// sigOf returns the armored signature of a raw commit.
func sigOf(t *testing.T, raw []byte) []byte {
	t.Helper()
	p, err := Parse("commit", raw)
	if err != nil || !p.Signed() {
		t.Fatalf("not a signed commit: %v", err)
	}
	return p.Signature
}

func TestOpenPGPExpiredAndRevokedKeys(t *testing.T) {
	w := newPGPWorld(t)
	ctx := context.Background()
	// A key made two days ago that lasts one day: it has expired, so it cannot be registered ...
	old := newEntity(t, "ed25519", testNow.Add(-48*time.Hour), 24*3600)
	if _, err := w.st.Add(ctx, NewKey{Owner: "dana", Name: "old", PublicKey: armoredPublic(t, old)}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("an expired key was registered: %v", err)
	}
	// ... while a key that lasts a week is accepted; a commit signed after its end is "key expired", and one signed
	// within its life stays verified.
	e := newEntity(t, "ed25519", testNow.Add(-72*time.Hour), 7*24*3600)
	if _, err := w.st.Add(ctx, NewKey{Owner: "dana", Name: "week", PublicKey: armoredPublic(t, e)}); err != nil {
		t.Fatal(err)
	}
	if r := w.verify(pgpCommit(t, e, testNow.Add(-time.Hour), "within its life")); r.State != Verified {
		t.Fatalf("within its life: %+v", r)
	}
	if r := w.verify(pgpCommitAnyway(t, e, testNow.Add(30*24*time.Hour), "after its end")); r.State != KeyExpired {
		t.Fatalf("after its end: %+v", r)
	}
	// A key that carries its own revocation: refused at registration; and a key revoked after it was registered
	// (the person registers the updated block under a new entity) shows "key revoked" for what it signs after.
	rev := newEntity(t, "ecdsa", testNow.Add(-72*time.Hour), 0)
	pubBefore := armoredPublic(t, rev)
	signedBefore := pgpCommit(t, rev, testNow.Add(-48*time.Hour), "before the revocation")
	signedAfter := pgpCommit(t, rev, testNow.Add(-time.Hour), "after the revocation") // made before the key is revoked here, dated after the revocation
	if err := rev.RevokeKey(packet.KeyCompromised, "test", &packet.Config{Time: func() time.Time { return testNow.Add(-24 * time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.Add(ctx, NewKey{Owner: "dana", Name: "revoked", PublicKey: armoredPublic(t, rev)}); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("a revoked key was registered: %v", err)
	}
	if _, err := w.st.Add(ctx, NewKey{Owner: "dana", Name: "ecdsa", PublicKey: pubBefore}); err != nil {
		t.Fatal(err)
	}
	if r := w.verify(signedBefore); r.State != Verified {
		t.Fatalf("signed before it was revoked: %+v", r)
	}
	// The registered text is replaced by the revoked block (the owner updates it): signatures after the revocation fail.
	if _, err := w.st.db.Exec(ctx, `UPDATE signing_keys SET public_text = ? WHERE name = 'ecdsa'`, armoredPublic(t, rev)); err != nil {
		t.Fatal(err)
	}
	if r := w.verify(signedAfter); r.State != KeyRevoked {
		t.Fatalf("signed after it was revoked: %+v", r)
	}
}

func TestOpenPGPRegistrationRefusals(t *testing.T) {
	w := newPGPWorld(t)
	ctx := context.Background()
	e := newEntity(t, "ed25519", testNow.Add(-time.Hour), 0)
	two := armoredPublic(t, e) + "\n" + armoredPublic(t, newEntity(t, "ed25519", testNow.Add(-time.Hour), 0))
	for name, text := range map[string]string{
		"a private key": armoredPrivate(t, e),
		"two keys":      two,
		"garbage":       "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nnot a key\n-----END PGP PUBLIC KEY BLOCK-----",
		"a small RSA key": armoredPublic(t, func() *openpgp.Entity {
			s, err := openpgp.NewEntity("Dana", "", "dana@example.com", &packet.Config{Algorithm: packet.PubKeyAlgoRSA, RSABits: 2048, MinRSABits: 2048})
			if err != nil {
				t.Fatal(err)
			}
			return s
		}()),
	} {
		if _, err := w.st.Add(ctx, NewKey{Owner: "dana", Name: "k", PublicKey: text}); err == nil {
			t.Errorf("%s was registered", name)
		}
	}
	if keys, _ := w.st.List(ctx, "dana"); len(keys) != 0 {
		t.Errorf("a refused key left rows behind: %+v", keys)
	}
}

func TestX509SignaturesStayNotChecked(t *testing.T) {
	w := newPGPWorld(t)
	raw := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor Dana <dana@example.com> 1 +0000\ncommitter Dana <dana@example.com> 1 +0000\ngpgsig -----BEGIN SIGNED MESSAGE-----\n \n x\n -----END SIGNED MESSAGE-----\n\nm\n")
	if r := w.verify(raw); r.State != Unsupported || !strings.Contains(r.Explanation, "X.509") {
		t.Fatalf("%+v", r)
	}
}

// With gpg installed: a key made by the library is imported into gpg, a commit signed by gpg is verified here,
// and a commit signed here is verified by "git verify-commit" with gpg's keyring.
func TestOpenPGPRoundTripThroughGit(t *testing.T) {
	for _, bin := range []string{"git", "gpg"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	w := newPGPWorld(t)
	ctx := context.Background()
	home, err := os.MkdirTemp("", "gnupg") // a short path: gpg's agent socket must fit in it
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("gpgconf", "--homedir", home, "--kill", "all").Run()
		os.RemoveAll(home)
	})
	repo := t.TempDir()
	env := append(os.Environ(), "GNUPGHOME="+home, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=dana@example.com", "GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=dana@example.com")
	run := func(stdin string, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env = repo, env
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	e := newEntity(t, "ed25519", time.Now().Add(-time.Hour), 0)
	run(armoredPrivate(t, e), "gpg", "--batch", "--import")
	fp := fpHex(e.PrimaryKey.Fingerprint)
	if _, err := w.st.Add(ctx, NewKey{Owner: "dana", Name: "round trip", PublicKey: armoredPublic(t, e)}); err != nil {
		t.Fatal(err)
	}
	run("", "git", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("", "git", "add", "f.txt")
	run("", "git", "-c", "gpg.program=gpg", "-c", "user.signingkey="+fp, "commit", "-q", "-S", "-m", "signed by gpg")
	sha := run("", "git", "rev-parse", "HEAD")
	raw := run("", "git", "cat-file", "commit", sha)
	if r := w.svc.verifier().Verify(ctx, sha, "commit", []byte(raw+"\n"), ""); r.State != Verified || r.Signer != "dana" {
		t.Fatalf("a commit signed by gpg: %+v", r)
	}
	// And the other way: gpg says a commit signed by the library is good.
	ours := pgpCommit(t, e, time.Now(), "signed by the library")
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir, cmd.Env, cmd.Stdin = repo, env, bytes.NewReader(ours)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if v := run("", "git", "-c", "gpg.program=gpg", "verify-commit", "--raw", strings.TrimSpace(string(out))); !strings.Contains(v, "GOODSIG") {
		t.Fatalf("git verify-commit: %s", v)
	}
}
