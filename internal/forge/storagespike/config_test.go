package storagespike

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
)

// CfgEnv names the file that tells a hook or wrapper process what it serves.
const CfgEnv = "LAUNCHWAY_SPIKE_CFG"

// KillPGEnv carries the process group of the whole push (the client's git and
// the wrapper) to the git processes, so an injected kill takes the "machine"
// down with everything on it.
const KillPGEnv = "LAUNCHWAY_SPIKE_KILLPG"

// Config is what one server of a candidate is, written to a file for the
// hook and wrapper processes git starts.
type Config struct {
	Mode         string // "ledger" (B) or "volume" (A)
	DBAddr       string
	BlobKind     string // "fs" or "db"
	BlobDir      string
	RepoID       string
	Cache        string // the bare repository on this server (B: a cache; A: the volume)
	Server       string
	VerifyUpload bool
	// Fsync is core.fsync for candidate A ("" leaves git's default).
	Fsync string
	// Binary is the executable the hook symlinks point at.
	Binary string
}

// Write stores the config next to the repository and returns its path.
func (c Config) Write(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, _ := json.Marshal(c)
	p := filepath.Join(dir, "config.json")
	return p, os.WriteFile(p, data, 0o644)
}

// LoadConfig reads the config the environment points at.
func LoadConfig() (Config, error) {
	var c Config
	p := os.Getenv(CfgEnv)
	if p == "" {
		return c, fmt.Errorf("%s is not set", CfgEnv)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(data, &c)
}

// OpenBlob opens the blob store the config names, wrapped with the process's
// injected faults.
func (c Config) OpenBlob(l *Ledger, f Fault) (blobstore.Store, error) {
	var st blobstore.Store
	switch c.BlobKind {
	case "fs":
		fs, err := blobstore.NewFilesystem(c.BlobDir)
		if err != nil {
			return nil, err
		}
		st = fs
	case "db":
		db, err := blobstore.NewDatabase(l.DB(), l.PG, blobstore.DatabaseOptions{})
		if err != nil {
			return nil, err
		}
		st = db
	default:
		return nil, fmt.Errorf("unknown blob store %q", c.BlobKind)
	}
	return &FaultStore{Store: st, F: f}, nil
}

// MarkerKey is the blob that carries the store marker.
const MarkerKey = "forge/store-marker"

// InitSchema creates the ledger tables and, for the database blob store, its
// tables, and sets the store marker in both places.
func InitSchema(ctx context.Context, c Config) error {
	l, err := OpenLedger(c.DBAddr)
	if err != nil {
		return err
	}
	defer l.Close()
	if err := l.Init(ctx); err != nil {
		return err
	}
	if c.BlobKind == "db" {
		for _, stmt := range splitSQL(blobstore.RenderSchema(l.PG, blobstore.DatabaseSchema)) {
			if _, err := l.DB().ExecContext(ctx, stmt); err != nil && !isExists(err) {
				return fmt.Errorf("blob store tables: %w", err)
			}
		}
	}
	if c.BlobKind == "" || c.RepoID == "" {
		return nil
	}
	st, err := c.OpenBlob(l, Fault{})
	if err != nil {
		return err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	id, err := l.Marker(ctx, hex.EncodeToString(b[:]))
	if err != nil {
		return err
	}
	if _, err := st.Stat(ctx, MarkerKey); err == nil {
		return nil
	}
	up, err := st.CreateUpload(ctx, MarkerKey)
	if err != nil {
		return err
	}
	et, err := st.UploadPart(ctx, MarkerKey, up, 1, []byte(id))
	if err != nil {
		return err
	}
	return st.CompleteUpload(ctx, MarkerKey, up, []blobstore.Part{{Number: 1, ETag: et}})
}

// CheckMarker refuses a blob store that does not carry the marker the
// database holds.
func CheckMarker(ctx context.Context, l *Ledger, st blobstore.Store) error {
	var want string
	if err := l.DB().QueryRowContext(ctx, l.q(`SELECT value FROM spike_meta WHERE key = 'repo_store_marker'`)).Scan(&want); err != nil {
		return fmt.Errorf("the database has no repository store marker: %w", err)
	}
	r, err := st.Open(ctx, MarkerKey, 0, -1)
	if err != nil {
		return fmt.Errorf("this server's blob store has no marker (%v); refusing to host, it is not the store this database was set up with", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if string(got) != want {
		return fmt.Errorf("this server's blob store is not the one this database was set up with; refusing to host")
	}
	return nil
}
