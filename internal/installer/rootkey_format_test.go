package installer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/keyring"
)

// The root key the installer makes must be exactly the file `launchway encryption init` writes and
// the chart's hook Job creates: 32 random bytes in standard base64, one line, mode 0600. The server
// opens it with keyring.OpenKeyFile, which refuses anything else.
func TestInstallerRootKeyIsTheFormatTheServerReads(t *testing.T) {
	k, err := randomKey()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "root.key")
	if wrote, err := writeIfAbsent(path, []byte(k+"\n"), 0o600); err != nil || !wrote {
		t.Fatalf("write: %v %v", wrote, err)
	}
	h, err := keyring.OpenKeyFile(path)
	if err != nil {
		t.Fatalf("the server cannot open the installer's root key: %v", err)
	}
	if h.Kind() != "key_file" {
		t.Fatalf("kind %s", h.Kind())
	}
	b, _ := os.ReadFile(path)
	if len(b) != 45 || b[44] != '\n' {
		t.Fatalf("expected 44 characters of base64 and a newline, got %d bytes", len(b))
	}
}
