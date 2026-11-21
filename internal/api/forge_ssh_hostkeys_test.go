package api

import (
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/jk-magic-creations/launchway/internal/forge/sshkeys"
	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/sealing"
)

// Servers that start together, over one database, end up with the same host
// keys: each may generate, one insert wins, and every server reads the
// winner's. The keys are sealed: the stored value is not the key, and it
// does not open with another sealing key.
func TestSSHHostKeysAreTheSameOnEveryServer(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	svc := e.api.Secrets
	seal := svc.HostKeys()
	const servers = 5
	got := make([][]ssh.Signer, servers)
	errs := make([]error, servers)
	var wg sync.WaitGroup
	for i := 0; i < servers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = sshkeys.New(e.eng.Database()).HostSigners(t.Context(), seal)
		}()
	}
	wg.Wait()
	for i := range got {
		if errs[i] != nil || len(got[i]) != 3 {
			t.Fatalf("server %d: %d keys, %v", i, len(got[i]), errs[i])
		}
		for j, sg := range got[i] {
			if ssh.FingerprintSHA256(sg.PublicKey()) != ssh.FingerprintSHA256(got[0][j].PublicKey()) {
				t.Errorf("server %d presents another %s key than server 0", i, sg.PublicKey().Type())
			}
		}
	}
	types := []string{got[0][0].PublicKey().Type(), got[0][1].PublicKey().Type(), got[0][2].PublicKey().Type()}
	if types[0] != ssh.KeyAlgoED25519 || types[1] != ssh.KeyAlgoECDSA256 || types[2] != ssh.KeyAlgoRSA {
		t.Errorf("host key types: %v", types)
	}
	// Sealed: another root key does not even start against this database.
	if _, err := sealing.Start(t.Context(), sealing.Options{DB: e.eng.Database(), Holder: testRootKey(t)}); err == nil {
		t.Error("another root key opened this database's keys")
	}
	var raw string
	if err := e.eng.Database().QueryRow(t.Context(), `SELECT sealed FROM ssh_host_keys WHERE algo = 'ed25519'`).Scan(&raw); err != nil || len(raw) < 60 || !strings.HasPrefix(raw, keyring.Prefix) {
		t.Fatalf("stored value: %q %v", raw, err)
	}
}
