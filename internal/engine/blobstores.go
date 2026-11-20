package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Which blob store keeps artifacts, and moving between stores.
//
// With nothing configured (StoreAuto), a server on PostgreSQL keeps
// artifacts in the database itself (blobstore.Database), so that any number
// of servers share them with nothing else to run. Two cases keep the
// filesystem instead:
//
//   - SQLite: one server, whose database file and artifact directory sit on
//     the same disk. The filesystem is faster there, and big writes would
//     queue behind SQLite's single writer with every other write.
//   - A server whose own directory is the marked store (it holds the marker
//     the database records) and holds artifacts kept before schema 17: a
//     single server with a persistent data directory, as it was. Switching
//     it by itself would strand them; it says how to move them (`launchway
//     blobs copy`) instead, and once they are copied the database is chosen.
//
// Everywhere else (a new installation; servers whose data directory is an
// emptyDir, as with the Helm chart, so that nothing from before survived
// the upgrade's restart; servers that never had the marked directory), the
// database is chosen and becomes the marked store. Files recorded in
// another store are not forgotten: the server says, at start, on the
// Runners page and on every download of one, where they are and how to
// copy them.

// Store choices, in ArtifactOptions.Choice.
const (
	// StoreGiven: ArtifactOptions.Store is the store (nil: none).
	StoreGiven = ""
	// StoreDatabase: keep artifacts in the database, as configured.
	StoreDatabase = "database"
	// StoreAuto: nothing was configured; see above. Store is the filesystem
	// store to use when the database store is not chosen.
	StoreAuto = "auto"
)

// databaseStoreName is blobstore.Database's Name.
const databaseStoreName = "database"

// chooseStore settles which store this server uses, before
// checkArtifactStore checks it. Open calls it.
func (e *Engine) chooseStore() error {
	o := &e.arts
	switch o.Choice {
	case StoreDatabase:
		if !e.knows(schemaBlobs) {
			o.Store, o.StoreProblem = nil, fmt.Sprintf("the database artifact store needs schema %d; this build knows %d", schemaBlobs, e.schema)
			return nil
		}
		store, err := e.newDatabaseStore()
		if err != nil {
			return err
		}
		o.Store, o.StoreProblem = store, ""
	case StoreAuto:
		if !e.db.pg {
			e.storeWhy = "Nothing is configured and the database is SQLite (one server), so artifacts are kept on its disk."
			return nil
		}
		if !e.knows(schemaBlobs) {
			return nil
		}
		store, err := e.newDatabaseStore()
		if err != nil {
			return err
		}
		keep, legacy, err := e.keepsLegacyFiles()
		if err != nil {
			return err
		}
		if !keep {
			o.Store, o.StoreProblem = store, ""
			e.storeWhy = "Nothing is configured and the database is PostgreSQL, so artifacts are kept in it, shared by every server: nothing else to run."
			return nil
		}
		e.storeWhy = "Nothing is configured, but this server's directory holds artifacts kept before the database store existed (" +
			legacy + "), so it keeps using the filesystem. To keep them in the database, shared by every server, copy them with " +
			"`launchway blobs copy --from filesystem --to database` (the server may keep running); after that the database is chosen by itself."
		e.log.Warn("artifacts stay on the filesystem", "why", e.storeWhy)
	}
	return nil
}

// StoreWhy says why the artifact store was chosen, when nothing was
// configured; "" otherwise.
func (e *Engine) StoreWhy() string { return e.storeWhy }

// newDatabaseStore makes the database store, sharing the engine's
// connections (and on SQLite its write lock).
func (e *Engine) newDatabaseStore() (*blobstore.Database, error) {
	lim := e.arts.DatabaseLimits
	lim.WriteLock = e.writeLock
	lim.Now = e.now
	store, err := blobstore.NewDatabase(e.db.DB, e.db.pg, lim)
	if err != nil {
		return nil, fmt.Errorf("artifacts: %w", err)
	}
	if _, most := store.Limits(); most > 0 {
		e.objectCap = most
	}
	if _, most := store.Limits(); e.live().arts.MaxFileBytes > most {
		e.log.Warn("artifacts: files larger than the database store's largest object cannot be kept",
			"max_file", sizeText(e.live().arts.MaxFileBytes), "largest_object", sizeText(most))
	}
	return store, nil
}

// keepsLegacyFiles reports whether this server, with nothing configured,
// should keep the filesystem: its own directory (the filesystem store it
// was given) is the marked store, and the database records files kept
// outside the database store. It describes those files.
func (e *Engine) keepsLegacyFiles() (bool, string, error) {
	fs := e.arts.Store
	if fs == nil {
		return false, "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	marker, err := e.storeMarker()
	if err != nil || marker == "" {
		return false, "", err
	}
	if _, err := fs.Stat(ctx, marker); err != nil {
		return false, "", nil // not the marked directory (or unreadable): it holds nothing of use
	}
	other, err := e.blobsElsewhere(databaseStoreName)
	if err != nil {
		return false, "", err
	}
	return len(other) > 0, describeElsewhere(other), nil
}

func (e *Engine) storeMarker() (string, error) {
	var key string
	err := e.db.QueryRow(`SELECT value FROM meta WHERE key = 'artifact_store_marker'`).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return key, err
}

// StoreCount is how many blobs (files' contents) a store is recorded to
// keep, and their bytes.
type StoreCount struct {
	// Store is database, filesystem or s3; "" for blobs kept before schema
	// 17, which did not record it.
	Store string `json:"store"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
}

// blobsElsewhere counts the blobs stored or uploading that are not
// recorded in the store named.
func (e *Engine) blobsElsewhere(name string) ([]StoreCount, error) {
	if !e.knows(schemaArtifacts) {
		return nil, nil
	}
	q := `SELECT '', COUNT(*), COALESCE(SUM(size), 0) FROM artifact_blobs WHERE state IN (?, ?) GROUP BY 1`
	args := []any{blobStored, blobUploading}
	if e.knows(schemaBlobs) {
		q = `SELECT store, COUNT(*), COALESCE(SUM(size), 0) FROM artifact_blobs WHERE state IN (?, ?) AND store <> ? GROUP BY store ORDER BY store`
		args = append(args, name)
	}
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoreCount
	for rows.Next() {
		var c StoreCount
		if err := rows.Scan(&c.Store, &c.Files, &c.Bytes); err != nil {
			return nil, err
		}
		if c.Files > 0 {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

func describeElsewhere(list []StoreCount) string {
	var parts []string
	for _, c := range list {
		where := "in the " + c.Store + " store"
		if c.Store == "" {
			where = "from before schema 17, which did not record their store (by default the filesystem)"
		}
		parts = append(parts, fmt.Sprintf("%d %s of %s %s", c.Files, plural(c.Files, "file", "files"), sizeText(c.Bytes), where))
	}
	return strings.Join(parts, "; ")
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// adoptMarker makes store the marked store: it writes a new marker into it
// and records that one, if the marker is still old. It is for the database
// store, which every server shares by definition.
func (e *Engine) adoptMarker(ctx context.Context, store blobstore.Store, old string) (string, error) {
	mine := "launchway-marker/" + ids.New("m")
	if err := putSmall(ctx, store, mine, []byte("This object tells Launchway servers that they share this store. Keep it.\n")); err != nil {
		return "", err
	}
	unlock := e.writeLock()
	res, err := e.db.Exec(`UPDATE meta SET value = ? WHERE key = 'artifact_store_marker' AND value = ?`, mine, old)
	unlock()
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		e.log.Info("this store is now the one every server uses (its marker is recorded)", "store", store.Name(), "marker", mine)
		return mine, nil
	}
	_ = store.Delete(ctx, mine) // another server got there first
	return e.storeMarker()
}

// missingContent explains a blob its store does not have.
func (e *Engine) missingContent(store blobstore.Store, b *blobRow, path string) error {
	here := store.Name()
	kept := ""
	if e.knows(schemaBlobs) {
		_ = e.db.QueryRow(`SELECT store FROM artifact_blobs WHERE id = ?`, b.ID).Scan(&kept)
	}
	switch {
	case kept == "":
		return fmt.Errorf("%w: the content of %s is not in this server's %s store. It was kept before Launchway recorded which store "+
			"holds each file, so it is probably in the store configured then (by default the filesystem under the data directory). "+
			"If artifacts.store was changed, copy the old store's contents over with `launchway blobs copy --from filesystem --to %s` "+
			"(servers may keep running meanwhile)", ErrNotFound, path, here, here)
	case kept != here:
		return fmt.Errorf("%w: the content of %s is in the %s store, where it was kept, not in this server's %s store. "+
			"If artifacts.store was changed, copy the old store's contents over with `launchway blobs copy --from %s --to %s` "+
			"(servers may keep running meanwhile)", ErrNotFound, path, kept, here, kept, here)
	}
	return fmt.Errorf("%w: the content of %s is missing from the %s store, which should have it (it was removed outside Launchway, "+
		"or the store was restored from an older backup than the database)", ErrNotFound, path, here)
}

// withStoreRequires returns the pipeline a new run records, marked as
// needing schema 17 ("requires": 17) when it keeps or fetches artifacts and
// this server keeps them in the database store. A server that does not
// know that store must not run such a run: it would keep the artifacts
// elsewhere or, its own store not being the shared one, none at all. The
// mark makes older servers read the pipeline as not understood (servers of
// schema 14 to 16 say "rollout steps or keys that control a rollout",
// their only words for a pipeline's requires) and gives the run
// min_schema 17, so they leave it alone, while still listing and showing
// it.
func (e *Engine) withStoreRequires(p *pipeline.Pipeline) *pipeline.Pipeline {
	if !e.knows(schemaBlobs) || p == nil || p.Requires >= schemaBlobs {
		return p
	}
	if _, ok := e.arts.Store.(*blobstore.Database); !ok {
		return p
	}
	for _, s := range p.Steps {
		if len(s.Artifacts) > 0 || len(s.Fetch) > 0 || (s.Reports != nil && len(s.Reports.SBOM) > 0) {
			marked := *p
			marked.Requires = schemaBlobs
			return &marked
		}
	}
	return p
}

// collectStore does the store's own housekeeping, for the database store:
// giving up abandoned uploads, removing the content of dead ones in small
// batches, and removing objects under artifacts/ that no artifact_blobs row
// refers to (left by a copy into this store that crossed a removal, or by a
// server that stopped between the two). The pruner calls it.
func (e *Engine) collectStore(ctx context.Context) {
	d, ok := e.arts.Store.(*blobstore.Database)
	if !ok || !e.knows(schemaBlobs) {
		return
	}
	if st, err := d.Collect(ctx); err != nil {
		e.log.Warn("collecting the database store's dead uploads; trying again later", "err", err)
	} else if st.Abandoned+st.Purged > 0 {
		e.log.Info("collected the database store's dead uploads", "abandoned", st.Abandoned, "purged", st.Purged)
	}
	if err := e.removeStrays(ctx, d); err != nil {
		e.log.Warn("removing objects of the database store nothing refers to; trying again later", "err", err)
	}
}

// strayGrace is how old an object must be before it counts as a stray: a
// blob's object is completed before its row says stored, but after the row
// exists, so only objects whose row is gone are strays, and only after this
// long (a copy writes the object before it records it).
const strayGrace = 6 * time.Hour

// removeStrays deletes objects under artifacts/ that no blob row refers to.
// It reads keys in pages from where the last pass stopped.
func (e *Engine) removeStrays(ctx context.Context, d *blobstore.Database) error {
	keys, err := d.Keys(ctx, "artifacts/", e.strayAfter, pruneBatchArtifacts)
	if err != nil {
		return err
	}
	if len(keys) < pruneBatchArtifacts {
		e.strayAfter = "" // start over next time
	} else {
		e.strayAfter = keys[len(keys)-1]
	}
	cutoff := e.now().Add(-strayGrace).UnixMilli()
	removed := 0
	for _, k := range keys {
		var one int
		err := e.db.QueryRow(`SELECT 1 FROM artifact_blobs WHERE store_key = ?`, k).Scan(&one)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var created int64
		if err := e.db.QueryRow(`SELECT created_at FROM blob_objects WHERE key = ?`, k).Scan(&created); err != nil || created > cutoff {
			continue
		}
		if err := d.Delete(ctx, k); err != nil {
			return err
		}
		removed++
	}
	if removed > 0 {
		e.log.Info("removed objects of the database store that no artifact refers to", "count", removed)
	}
	return nil
}

// StoreStatus says where this server keeps artifacts, why, and how full
// the store is.
type StoreStatus struct {
	// Store is database, filesystem or s3; empty if none works.
	Store     string `json:"store"`
	Available bool   `json:"available"`
	Problem   string `json:"problem,omitempty"`
	// Why the store was chosen, when nothing was configured.
	Why string `json:"why,omitempty"`
	// Usage is the store's own account, for stores that keep one (the
	// database store).
	Usage *blobstore.Usage `json:"usage,omitempty"`
	// Kept is what the database records as kept in this store; Elsewhere is
	// what it records as kept in other stores, which this server cannot
	// read.
	Kept      StoreCount   `json:"kept"`
	Elsewhere []StoreCount `json:"elsewhere"`
	// Advice says what to do, if anything: the store is filling up, or
	// some content is elsewhere.
	Advice []string `json:"advice"`
}

// StoreStatus reports on this server's artifact store.
func (e *Engine) StoreStatus(ctx context.Context) (*StoreStatus, error) {
	out := &StoreStatus{Available: e.arts.Store != nil, Problem: e.arts.StoreProblem, Why: e.storeWhy, Elsewhere: []StoreCount{}, Advice: []string{}}
	if !e.knows(schemaArtifacts) {
		return out, nil
	}
	name := ""
	if e.arts.Store != nil {
		name = e.arts.Store.Name()
		out.Store, out.Problem = name, ""
	}
	if u, ok := e.arts.Store.(blobstore.UsageReporter); ok {
		usage, err := u.Usage(ctx)
		if err != nil {
			return nil, err
		}
		out.Usage = &usage
		if usage.LimitBytes > 0 && usage.UsedBytes*10 >= usage.LimitBytes*8 {
			out.Advice = append(out.Advice, fmt.Sprintf("The store holds %s of its limit of %s. Delete artifacts or shorten their retention, "+
				"raise artifacts.database.max_total_gib, or move to S3.", sizeText(usage.UsedBytes), sizeText(usage.LimitBytes)))
		}
	}
	if e.knows(schemaBlobs) && name != "" {
		err := e.db.QueryRowContext(ctx, rebind(e.db.pg, `SELECT COUNT(*), COALESCE(SUM(size), 0) FROM artifact_blobs WHERE state = ? AND store = ?`),
			blobStored, name).Scan(&out.Kept.Files, &out.Kept.Bytes)
		if err != nil {
			return nil, err
		}
		out.Kept.Store = name
		other, err := e.blobsElsewhere(name)
		if err != nil {
			return nil, err
		}
		out.Elsewhere = append(out.Elsewhere, other...)
		for _, c := range other {
			from := c.Store
			if from == "" {
				from = "filesystem"
			}
			desc := describeElsewhere([]StoreCount{c})
			out.Advice = append(out.Advice, fmt.Sprintf("%s%s. This server cannot read %s from its %s store: copy %s with "+
				"`launchway blobs copy --from %s --to %s`.", strings.ToUpper(desc[:1]), desc[1:], plural(c.Files, "it", "them"), name,
				plural(c.Files, "it", "them"), from, name))
		}
	}
	return out, nil
}

// ---- maintenance: launchway blobs copy ----

// BlobAdmin works on the blobs of a database for `launchway blobs`: it
// opens the database without starting an engine, and never migrates it.
type BlobAdmin struct {
	db *dbx
}

// OpenBlobAdmin opens the database at addr for blob maintenance. The
// database must be at schema 17 or newer, and this build must be allowed
// to run against it: upgrade the servers first.
func OpenBlobAdmin(addr string, wait time.Duration) (*BlobAdmin, error) {
	known, err := knownSchema()
	if err != nil {
		return nil, err
	}
	var d *dbx
	if isPostgres(addr) {
		db, err := sql.Open("pgx", addr)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(8)
		d = &dbx{DB: db, pg: true, dsn: addr}
	} else {
		q := url.Values{}
		q.Add("_pragma", "busy_timeout(10000)")
		q.Add("_pragma", "journal_mode(WAL)")
		q.Set("_txlock", "immediate")
		db, err := sql.Open("sqlite", "file:"+addr+"?"+q.Encode())
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(4)
		d = &dbx{DB: db}
	}
	if err := connect(d, wait, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		d.Close()
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}
	info, err := readSchema(d)
	if err != nil {
		d.Close()
		return nil, err
	}
	info.Code = known
	switch {
	case info.Database < schemaBlobs:
		d.Close()
		return nil, fmt.Errorf("the database is at schema %d; copying blobs needs schema %d. Upgrade the servers first (they migrate the database)", info.Database, schemaBlobs)
	case !info.Compatible():
		d.Close()
		return nil, errors.New(info.Refusal())
	}
	return &BlobAdmin{db: d}, nil
}

// Close closes the database.
func (a *BlobAdmin) Close() error { return a.db.Close() }

// DatabaseStore returns the database's own blob store.
func (a *BlobAdmin) DatabaseStore(o blobstore.DatabaseOptions) (*blobstore.Database, error) {
	return blobstore.NewDatabase(a.db.DB, a.db.pg, o)
}

// Marker returns the key of the store marker, or "".
func (a *BlobAdmin) Marker() (string, error) {
	var key string
	err := a.db.QueryRow(`SELECT value FROM meta WHERE key = 'artifact_store_marker'`).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return key, err
}

// BlobRef is one stored blob, as the copy reads it.
type BlobRef struct {
	ID     string
	SHA256 string
	Size   int64
	Key    string
	Store  string
}

// StoredBlobs lists stored blobs not recorded in the store named notIn,
// after the ID after, in order of ID.
func (a *BlobAdmin) StoredBlobs(ctx context.Context, notIn, after string, limit int) ([]BlobRef, error) {
	rows, err := a.db.QueryContext(ctx, rebind(a.db.pg, `SELECT id, sha256, size, store_key, store FROM artifact_blobs
		WHERE state = ? AND store <> ? AND id > ? ORDER BY id LIMIT ?`), blobStored, notIn, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlobRef
	for rows.Next() {
		var b BlobRef
		if err := rows.Scan(&b.ID, &b.SHA256, &b.Size, &b.Key, &b.Store); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Uploading counts blobs being uploaded, which a copy cannot take yet.
func (a *BlobAdmin) Uploading(ctx context.Context) (int64, error) {
	var n int64
	err := a.db.QueryRowContext(ctx, rebind(a.db.pg, `SELECT COUNT(*) FROM artifact_blobs WHERE state = ?`), blobUploading).Scan(&n)
	return n, err
}

// Counts counts the stored blobs by the store they are recorded in.
func (a *BlobAdmin) Counts(ctx context.Context) ([]StoreCount, error) {
	return a.counts(ctx, `SELECT store, COUNT(*), COALESCE(SUM(size), 0) FROM artifact_blobs
		WHERE state = ? GROUP BY store ORDER BY store`, blobStored)
}

// Elsewhere counts the stored blobs not recorded in the store named.
func (a *BlobAdmin) Elsewhere(ctx context.Context, name string) ([]StoreCount, error) {
	return a.counts(ctx, `SELECT store, COUNT(*), COALESCE(SUM(size), 0) FROM artifact_blobs
		WHERE state = ? AND store <> ? GROUP BY store ORDER BY store`, blobStored, name)
}

func (a *BlobAdmin) counts(ctx context.Context, q string, args ...any) ([]StoreCount, error) {
	rows, err := a.db.QueryContext(ctx, rebind(a.db.pg, q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoreCount
	for rows.Next() {
		var c StoreCount
		if err := rows.Scan(&c.Store, &c.Files, &c.Bytes); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkCopied records that a blob is now kept in the store named, if it is
// still stored. It reports false if the blob is being removed or is gone:
// the copy must then remove what it wrote.
func (a *BlobAdmin) MarkCopied(ctx context.Context, id, store string) (bool, error) {
	res, err := a.db.ExecContext(ctx, rebind(a.db.pg, `UPDATE artifact_blobs SET store = ? WHERE id = ? AND state = ?`), store, id, blobStored)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DescribeElsewhere says in words what a list of counts holds.
func DescribeElsewhere(list []StoreCount) string { return describeElsewhere(list) }
