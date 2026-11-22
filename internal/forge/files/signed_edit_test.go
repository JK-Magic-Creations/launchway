package files

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/jk-magic-creations/launchway/internal/forge/signing"
)

func catCommit(t *testing.T, s *Service, sha string) string {
	t.Helper()
	var raw string
	if err := s.Git.WithCache(context.Background(), repoID, func(dir string) error {
		out, err := exec.Command("git", "--git-dir", dir, "cat-file", "commit", sha).Output()
		raw = string(out)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return raw
}

// An edit made in the web UI is signed with the installation's key, as a merge is; with no key to sign with, or
// with signing off, it is made unsigned and not refused.
func TestWebEditsAreSignedWithTheInstallationKey(t *testing.T) {
	s, st := newService(t)
	ctx := context.Background()
	seed(t, s, st, "main", map[string]string{"README.md": "hello\n"})
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	sg, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	s.Sign = func(_ context.Context, payload []byte) ([]byte, error) { return signing.SignSSH(sg, payload) }
	w, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "README.md", Content: []byte("changed\n"), Message: "edit", Actor: "ada", ExpectedSHA: mustSHA(t, s)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := catCommit(t, s, w.Commit)
	if !strings.Contains(raw, "gpgsig -----BEGIN SSH SIGNATURE-----") {
		t.Fatalf("the edit is not signed:\n%s", raw)
	}
	p, err := signing.Parse("commit", []byte(raw))
	if err != nil || !p.Signed() {
		t.Fatalf("%v", err)
	}
	sig, err := signing.ParseSSHSignature(p.Signature)
	if err != nil || sig.Verify(p.Payload) != nil {
		t.Fatalf("the signature does not verify: %v", err)
	}
	// A signer that cannot sign (no root key) leaves the edit unsigned and the edit goes through.
	s.Sign = func(context.Context, []byte) ([]byte, error) { return nil, errors.New("no root key") }
	w2, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "README.md", Content: []byte("again\n"), Message: "edit 2", Actor: "ada", ExpectedSHA: mustSHA(t, s)}, nil)
	if err != nil {
		t.Fatalf("an edit was refused because signing failed: %v", err)
	}
	if strings.Contains(catCommit(t, s, w2.Commit), "gpgsig") {
		t.Fatal("a commit that could not be signed carries a signature")
	}
	s.Sign = nil
	if _, err := s.Put(ctx, repoID, Write{Branch: "main", Path: "README.md", Content: []byte("third\n"), Message: "edit 3", Actor: "ada", ExpectedSHA: mustSHA(t, s)}, nil); err != nil {
		t.Fatalf("signing off: %v", err)
	}
}

func mustSHA(t *testing.T, s *Service) string {
	t.Helper()
	f, err := s.Read(context.Background(), repoID, "main", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	return f.SHA
}
