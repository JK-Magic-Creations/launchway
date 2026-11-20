package engine

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// Once the database holds the installation's settings, the engine opens with
// the artifact store they say, not the one it was given.
func TestEngineOpensWithTheStoredArtifactStore(t *testing.T) {
	addr := testdb.New(t)
	clk := &clock{t: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}
	first := open(t, addr, clk)
	rev, _, err := first.ConfigSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.WriteConfig(rev, "ada", []ConfigWrite{{Kind: "installation", Name: "installation", Action: ConfigUpdate,
		Spec: json.RawMessage(`{"artifacts":{"store":"filesystem"}}`), IfVersion: -1}}, nil); err != nil {
		t.Fatal(err)
	}
	first.Close()

	dir := filepath.Join(t.TempDir(), "kept")
	called := 0
	e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now,
		ArtifactsFromDatabase: func(spec []byte) (ArtifactOptions, error) {
			called++
			fs, err := blobstore.NewFilesystem(dir)
			return ArtifactOptions{Store: fs, Choice: StoreGiven}, err
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if called != 1 {
		t.Fatalf("the hook was called %d times", called)
	}
	if got := e.ArtifactOptions().Store; got == nil || got.Name() != "filesystem" {
		t.Errorf("the engine's store: %v", got)
	}
}
