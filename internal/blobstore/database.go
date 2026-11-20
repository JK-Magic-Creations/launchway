package blobstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Database keeps objects in the SQL database Launchway already needs
// (PostgreSQL, or SQLite), so that several servers sharing that database
// share their artifacts with nothing else to run: no object store, no shared
// filesystem.
//
// Four tables hold it (made by the engine's schema 17; DatabaseSchema is
// their definition):
//
//	blob_uploads  one row per multipart upload: its key and state
//	              (open, complete, dead)
//	blob_parts    one row per uploaded part: size, SHA-256 (its ETag)
//	blob_chunks   the bytes, DatabaseChunkSize per row, each with its own
//	              SHA-256
//	blob_objects  one row per finished object: the upload whose parts are
//	              its content, its size, and the parts in order
//
// Every write is a short transaction of its own: one part (at most
// MaxDatabasePartBytes) is stored in one transaction, and completing an
// upload only writes the object's row, never copies bytes. So no upload ever
// holds a transaction open for long, and a server that dies mid-upload leaves
// whole parts or none, never half an object: an object becomes visible in
// the one transaction that completes it. Reads fetch one chunk per query, so
// a slow download holds no transaction and no snapshot either, and every
// chunk's SHA-256 is checked as it is read.
//
// Parts are never bigger than MaxDatabasePartBytes, and do not need to be
// at least MinPartSize (the store does not enforce it). An upload that is
// abandoned (no part for AbandonAfter) and the content of deleted objects
// are removed in small batches by Collect.
type Database struct {
	db        *sql.DB
	pg        bool
	lock      func() (unlock func())
	maxBytes  int64
	maxObject int64
	now       func() time.Time

	mu        sync.Mutex
	used      int64 // bytes in blob_parts, as last read
	usedAt    time.Time
	usedKnown bool
}

// DatabaseOptions configure a Database store. Zero fields take defaults.
type DatabaseOptions struct {
	// MaxBytes is how many bytes the store may hold in all, counting uploads
	// in progress and content waiting to be collected. Default
	// DefaultDatabaseMaxBytes.
	MaxBytes int64
	// MaxObjectBytes is the largest object. Default
	// DefaultDatabaseMaxObjectBytes.
	MaxObjectBytes int64
	// WriteLock, if set, is taken around every write transaction (the
	// engine's SQLite write lock, so the store's writes queue with the
	// engine's instead of failing with "database is locked").
	WriteLock func() (unlock func())
	// Now is the clock; default time.Now.
	Now func() time.Time
}

// Defaults and limits of the Database store.
const (
	// DatabaseChunkSize is the size of every chunk row but the last of a
	// part. Small enough that a ranged read fetches little it does not
	// need, large enough that a gigabyte is a thousand queries.
	DatabaseChunkSize = 1 << 20
	// MaxDatabasePartBytes is the largest part: one part is written in one
	// transaction, and this bounds it.
	MaxDatabasePartBytes = 64 << 20
	// DefaultDatabaseMaxBytes is the default limit on everything the store
	// holds: 100 GiB.
	DefaultDatabaseMaxBytes = 100 << 30
	// DefaultDatabaseMaxObjectBytes is the default largest object: 2 GiB.
	DefaultDatabaseMaxObjectBytes = 2 << 30
	// AbandonAfter is how long an upload may go without a part before
	// Collect gives it up. The engine abandons its own uploads after a day
	// without progress; this catches uploads it never recorded (a server
	// that stopped between starting an upload and recording it).
	AbandonAfter = 48 * time.Hour
	// collectBatch is how many chunks one transaction of Collect deletes.
	collectBatch = 16
)

// Upload states, in blob_uploads.state.
const (
	uploadOpen     = "open"
	uploadComplete = "complete"
	uploadDead     = "dead" // aborted, superseded, or its object deleted: its chunks are collected
)

// ErrFull is returned (wrapped) when a store has no room for more bytes.
var ErrFull = errors.New("the artifact store is full")

// ErrTooLarge is returned (wrapped) for an object bigger than a store keeps.
var ErrTooLarge = errors.New("the object is larger than the store keeps")

// DatabaseSchema is the SQL that makes the Database store's tables, with
// {{BIG}} for a 64-bit integer type and {{BYTES}} for a byte string type.
// The engine's migration 17 is exactly this; it has shipped, so it never
// changes: a change is a new migration.
const DatabaseSchema = `
CREATE TABLE blob_uploads (
  id         TEXT    PRIMARY KEY,
  key        TEXT    NOT NULL,
  state      TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL,
  updated_at {{BIG}} NOT NULL
);
CREATE INDEX blob_uploads_state ON blob_uploads(state, updated_at);

CREATE TABLE blob_parts (
  upload_id  TEXT    NOT NULL,
  n          INTEGER NOT NULL,
  size       {{BIG}} NOT NULL,
  sha256     TEXT    NOT NULL,
  chunk_size INTEGER NOT NULL,
  created_at {{BIG}} NOT NULL,
  PRIMARY KEY (upload_id, n)
);

CREATE TABLE blob_chunks (
  upload_id TEXT      NOT NULL,
  part      INTEGER   NOT NULL,
  seq       INTEGER   NOT NULL,
  sha256    TEXT      NOT NULL,
  data      {{BYTES}} NOT NULL,
  PRIMARY KEY (upload_id, part, seq)
);

CREATE TABLE blob_objects (
  key        TEXT    PRIMARY KEY,
  upload_id  TEXT    NOT NULL,
  size       {{BIG}} NOT NULL,
  digest     TEXT    NOT NULL,
  parts      TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL
);
CREATE INDEX blob_objects_upload ON blob_objects(upload_id);
`

// RenderSchema fills in DatabaseSchema's types for PostgreSQL or SQLite.
func RenderSchema(pg bool, schema string) string {
	if pg {
		return strings.NewReplacer("{{BIG}}", "BIGINT", "{{BYTES}}", "BYTEA").Replace(schema)
	}
	return strings.NewReplacer("{{BIG}}", "INTEGER", "{{BYTES}}", "BLOB").Replace(schema)
}

// NewDatabase returns a store keeping objects in db, whose tables must exist
// (DatabaseSchema). pg says whether db is PostgreSQL; otherwise it is
// SQLite.
func NewDatabase(db *sql.DB, pg bool, o DatabaseOptions) (*Database, error) {
	if db == nil {
		return nil, errors.New("the database artifact store needs a database")
	}
	if o.MaxBytes < 0 || o.MaxObjectBytes < 0 {
		return nil, errors.New("the database artifact store's limits cannot be negative")
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = DefaultDatabaseMaxBytes
	}
	if o.MaxObjectBytes == 0 {
		o.MaxObjectBytes = DefaultDatabaseMaxObjectBytes
	}
	if o.MaxObjectBytes > o.MaxBytes {
		return nil, fmt.Errorf("the database artifact store's largest object (%d bytes) cannot be more than all it holds (%d bytes)", o.MaxObjectBytes, o.MaxBytes)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.WriteLock == nil {
		o.WriteLock = func() func() { return func() {} }
	}
	return &Database{db: db, pg: pg, lock: o.WriteLock, maxBytes: o.MaxBytes, maxObject: o.MaxObjectBytes, now: o.Now}, nil
}

// Name returns "database".
func (d *Database) Name() string { return "database" }

// Limits returns the most the store holds in all and the largest object.
func (d *Database) Limits() (maxBytes, maxObjectBytes int64) { return d.maxBytes, d.maxObject }

// q rewrites ? placeholders for PostgreSQL.
func (d *Database) q(s string) string {
	if !d.pg || !strings.Contains(s, "?") {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// lockFor returns a row-locking clause, which SQLite does without (its
// write transactions are exclusive anyway).
func (d *Database) lockFor(mode string) string {
	if d.pg {
		return " FOR " + mode
	}
	return ""
}

// tx runs fn in a transaction of its own, retrying a few times when
// PostgreSQL reports a conflict between servers.
func (d *Database) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	for attempt := 1; ; attempt++ {
		err := func() error {
			unlock := d.lock()
			defer unlock()
			tx, err := d.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := fn(tx); err != nil {
				return err
			}
			return tx.Commit()
		}()
		if err == nil || attempt == 5 || !sqlConflict(err) {
			return err
		}
		time.Sleep(time.Duration(attempt) * 5 * time.Millisecond)
	}
}

// sqlConflict reports whether err is PostgreSQL saying another transaction
// got in the way (deadlock, serialization failure, unique violation).
func sqlConflict(err error) bool {
	var st interface{ SQLState() string }
	if errors.As(err, &st) {
		switch st.SQLState() {
		case "40001", "40P01", "23505":
			return true
		}
	}
	return false
}

func (d *Database) nowMs() int64 { return d.now().UnixMilli() }

// Check writes, reads back and deletes a small probe object, going through
// a whole multipart upload.
func (d *Database) Check(ctx context.Context) error {
	return checkStore(ctx, d, func(key string, data []byte) error {
		id, err := d.CreateUpload(ctx, key)
		if err != nil {
			return err
		}
		etag, err := d.UploadPart(ctx, key, id, 1, data)
		if err == nil {
			err = d.CompleteUpload(ctx, key, id, []Part{{Number: 1, ETag: etag}})
		}
		if err != nil {
			_ = d.AbortUpload(context.WithoutCancel(ctx), key, id)
		}
		return err
	})
}

// CreateUpload starts an upload of key.
func (d *Database) CreateUpload(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	id := randomHex(16)
	now := d.nowMs()
	err := d.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, d.q(`INSERT INTO blob_uploads(id, key, state, created_at, updated_at) VALUES(?, ?, ?, ?, ?)`),
			id, key, uploadOpen, now, now)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("starting an upload of %s: %w", key, err)
	}
	return id, nil
}

// openUpload checks, inside a transaction, that uploadID is an open upload
// of key, locking its row in mode (SHARE while a part is written, UPDATE to
// complete or abort). It returns the upload's state, or ErrNotFound.
func (d *Database) openUpload(ctx context.Context, tx *sql.Tx, key, uploadID, mode string) (string, error) {
	var k, state string
	err := tx.QueryRowContext(ctx, d.q(`SELECT key, state FROM blob_uploads WHERE id = ?`+d.lockFor(mode)), uploadID).Scan(&k, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("upload %s of %s: %w", uploadID, key, ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	if k != key {
		return "", fmt.Errorf("upload %s is for another object, not %s", uploadID, key)
	}
	return state, nil
}

func (d *Database) checkUploadArgs(key, uploadID string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if !validUploadID(uploadID) {
		return fmt.Errorf("%q is not an upload ID of this store (32 lower-case hex digits)", uploadID)
	}
	return nil
}

// UploadPart stores part n in one transaction, replacing any earlier copy
// of it. The ETag is the hex SHA-256 of the part.
func (d *Database) UploadPart(ctx context.Context, key, uploadID string, n int, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := checkPartNumber(n); err != nil {
		return "", err
	}
	if err := d.checkUploadArgs(key, uploadID); err != nil {
		return "", err
	}
	size := int64(len(data))
	switch {
	case size > MaxDatabasePartBytes:
		return "", fmt.Errorf("part %d is %d bytes; the database store takes parts of at most %d", n, size, MaxDatabasePartBytes)
	case size > d.maxObject:
		return "", fmt.Errorf("%w: part %d alone is %s, and the database store keeps objects of at most %s", ErrTooLarge, n, mib(size), mib(d.maxObject))
	}
	if err := d.reserve(ctx, size); err != nil {
		return "", err
	}
	whole := sha256.Sum256(data)
	etag := hex.EncodeToString(whole[:])
	now := d.nowMs()
	err := d.tx(ctx, func(tx *sql.Tx) error {
		state, err := d.openUpload(ctx, tx, key, uploadID, "SHARE")
		if err != nil {
			return err
		}
		if state != uploadOpen {
			return fmt.Errorf("upload %s of %s: %w (it was aborted or completed)", uploadID, key, ErrNotFound)
		}
		var others int64
		if err := tx.QueryRowContext(ctx, d.q(`SELECT COALESCE(SUM(size), 0) FROM blob_parts WHERE upload_id = ? AND n <> ?`),
			uploadID, n).Scan(&others); err != nil {
			return err
		}
		if others+size > d.maxObject {
			return fmt.Errorf("%w: with part %d the upload of %s would be %s; the database store keeps objects of at most %s",
				ErrTooLarge, n, key, mib(others+size), mib(d.maxObject))
		}
		// The part's row first: it locks the part, so two servers storing the
		// same part take turns, and the second removes the first's chunks.
		if _, err := tx.ExecContext(ctx, d.q(`INSERT INTO blob_parts(upload_id, n, size, sha256, chunk_size, created_at) VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(upload_id, n) DO UPDATE SET size = excluded.size, sha256 = excluded.sha256, chunk_size = excluded.chunk_size,
			created_at = excluded.created_at`), uploadID, n, size, etag, DatabaseChunkSize, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, d.q(`DELETE FROM blob_chunks WHERE upload_id = ? AND part = ?`), uploadID, n); err != nil {
			return err
		}
		ins, err := tx.PrepareContext(ctx, d.q(`INSERT INTO blob_chunks(upload_id, part, seq, sha256, data) VALUES(?, ?, ?, ?, ?)`))
		if err != nil {
			return err
		}
		defer ins.Close()
		for seq, off := 0, 0; off < len(data) || (off == 0 && len(data) == 0); seq++ {
			chunk := data[off:min(off+DatabaseChunkSize, len(data))]
			cs := sha256.Sum256(chunk)
			if _, err := ins.ExecContext(ctx, uploadID, n, seq, hex.EncodeToString(cs[:]), chunk); err != nil {
				return err
			}
			off += len(chunk)
			if len(data) == 0 {
				break
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("storing part %d of upload %s: %w", n, uploadID, err)
	}
	d.addUsed(size)
	return etag, nil
}

// objectPart is one part of a finished object, as blob_objects.parts keeps
// it: [number, size, chunk size].
type objectPart [3]int64

// CompleteUpload makes the object from the parts, in order, by writing its
// row: the bytes stay where the parts put them. Parts not listed are
// dropped. If the key already has an object, completing succeeds only if it
// has the same content (the same parts' SHA-256s): objects are never
// overwritten.
func (d *Database) CompleteUpload(ctx context.Context, key, uploadID string, parts []Part) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkParts(parts); err != nil {
		return err
	}
	if err := d.checkUploadArgs(key, uploadID); err != nil {
		return err
	}
	now := d.nowMs()
	return d.tx(ctx, func(tx *sql.Tx) error {
		state, err := d.openUpload(ctx, tx, key, uploadID, "UPDATE")
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err != nil || state != uploadOpen {
			// Completed before (by this server or another), or gone: done if
			// the object is there.
			var one int
			switch err := tx.QueryRowContext(ctx, d.q(`SELECT 1 FROM blob_objects WHERE key = ?`), key).Scan(&one); {
			case err == nil:
				return nil
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
			return fmt.Errorf("upload %s of %s: %w (it was aborted)", uploadID, key, ErrNotFound)
		}
		have := map[int][2]string{} // n -> size, sha256
		rows, err := tx.QueryContext(ctx, d.q(`SELECT n, size, sha256, chunk_size FROM blob_parts WHERE upload_id = ?`), uploadID)
		if err != nil {
			return err
		}
		chunkOf := map[int]int64{}
		for rows.Next() {
			var n int
			var size, chunk int64
			var sum string
			if err := rows.Scan(&n, &size, &sum, &chunk); err != nil {
				rows.Close()
				return err
			}
			have[n] = [2]string{strconv.FormatInt(size, 10), sum}
			chunkOf[n] = chunk
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var total int64
		list := make([]objectPart, 0, len(parts))
		h := sha256.New()
		used := map[int]bool{}
		for _, p := range parts {
			got, ok := have[p.Number]
			if !ok {
				return fmt.Errorf("part %d of upload %s was never uploaded", p.Number, uploadID)
			}
			if got[1] != p.ETag {
				return fmt.Errorf("part %d of upload %s does not match the ETag given for it (the part was uploaded again since, or the ETag is wrong)", p.Number, uploadID)
			}
			size, _ := strconv.ParseInt(got[0], 10, 64)
			total += size
			list = append(list, objectPart{int64(p.Number), size, chunkOf[p.Number]})
			h.Write([]byte(got[1]))
			used[p.Number] = true
		}
		if total > d.maxObject {
			return fmt.Errorf("%w: %s would be %s; the database store keeps objects of at most %s", ErrTooLarge, key, mib(total), mib(d.maxObject))
		}
		digest := hex.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(parts))
		listJSON, _ := json.Marshal(list)
		res, err := tx.ExecContext(ctx, d.q(`INSERT INTO blob_objects(key, upload_id, size, digest, parts, created_at) VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(key) DO NOTHING`), key, uploadID, total, digest, string(listJSON), now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Another upload made this key first.
			var size int64
			var other string
			if err := tx.QueryRowContext(ctx, d.q(`SELECT size, digest FROM blob_objects WHERE key = ?`), key).Scan(&size, &other); err != nil {
				return err
			}
			if size != total || other != digest {
				return fmt.Errorf("%s already exists with other content (%d bytes); objects are never overwritten", key, size)
			}
			_, err := tx.ExecContext(ctx, d.q(`UPDATE blob_uploads SET state = ?, updated_at = ? WHERE id = ?`), uploadDead, now, uploadID)
			return err
		}
		for n := range have {
			if used[n] {
				continue
			}
			if _, err := tx.ExecContext(ctx, d.q(`DELETE FROM blob_chunks WHERE upload_id = ? AND part = ?`), uploadID, n); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, d.q(`DELETE FROM blob_parts WHERE upload_id = ? AND n = ?`), uploadID, n); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, d.q(`UPDATE blob_uploads SET state = ?, updated_at = ? WHERE id = ?`), uploadComplete, now, uploadID)
		return err
	})
}

// AbortUpload gives an open upload up; its chunks are removed at once, in
// small batches, or later by Collect. An unknown or finished upload is not
// an error, and a completed upload's object is never touched.
func (d *Database) AbortUpload(ctx context.Context, key, uploadID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.checkUploadArgs(key, uploadID); err != nil {
		return err
	}
	killed := false
	err := d.tx(ctx, func(tx *sql.Tx) error {
		state, err := d.openUpload(ctx, tx, key, uploadID, "UPDATE")
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil || state != uploadOpen {
			return err
		}
		_, err = tx.ExecContext(ctx, d.q(`UPDATE blob_uploads SET state = ?, updated_at = ? WHERE id = ?`), uploadDead, d.nowMs(), uploadID)
		killed = err == nil
		return err
	})
	if err != nil {
		return fmt.Errorf("aborting upload %s: %w", uploadID, err)
	}
	if killed {
		_ = d.purge(ctx, uploadID)
	}
	return nil
}

// object reads an object's row.
func (d *Database) object(ctx context.Context, key string) (uploadID string, size int64, parts []objectPart, err error) {
	var list string
	err = d.db.QueryRowContext(ctx, d.q(`SELECT upload_id, size, parts FROM blob_objects WHERE key = ?`), key).Scan(&uploadID, &size, &list)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil, fmt.Errorf("object %s: %w", key, ErrNotFound)
	}
	if err != nil {
		return "", 0, nil, fmt.Errorf("looking up %s: %w", key, err)
	}
	if err := json.Unmarshal([]byte(list), &parts); err != nil {
		return "", 0, nil, fmt.Errorf("object %s: reading its parts: %w", key, err)
	}
	return uploadID, size, parts, nil
}

// Open returns a reader of length bytes of the object from offset (length
// < 0: to the end). It fetches one chunk per query as it is read, checking
// each chunk's SHA-256; a chunk that does not match fails the read.
func (d *Database) Open(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	if err := checkRange(offset); err != nil {
		return nil, err
	}
	uploadID, size, parts, err := d.object(ctx, key)
	if err != nil {
		return nil, err
	}
	if offset >= size || length == 0 {
		return emptyReader{}, nil
	}
	end := size
	if length >= 0 && offset+length < size {
		end = offset + length
	}
	r := &dbReader{d: d, ctx: ctx, key: key, upload: uploadID, pos: offset, end: end}
	var start int64
	for _, p := range parts {
		r.parts = append(r.parts, readPart{n: int(p[0]), start: start, size: p[1], chunk: p[2]})
		start += p[1]
	}
	return r, nil
}

type readPart struct {
	n                  int
	start, size, chunk int64
}

// dbReader reads an object chunk by chunk.
type dbReader struct {
	d        *Database
	ctx      context.Context
	key      string
	upload   string
	parts    []readPart
	pos, end int64
	// The chunk in buf: its part and number, and where it starts in the
	// object.
	buf      []byte
	bufStart int64
	bufOK    bool
	closed   bool
}

func (r *dbReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, errors.New("read from a closed object")
	}
	if r.pos >= r.end {
		return 0, io.EOF
	}
	if !r.bufOK || r.pos < r.bufStart || r.pos >= r.bufStart+int64(len(r.buf)) {
		if err := r.fetch(); err != nil {
			return 0, err
		}
	}
	from := r.pos - r.bufStart
	n := copy(p, r.buf[from:min(int64(len(r.buf)), r.end-r.bufStart)])
	r.pos += int64(n)
	return n, nil
}

// fetch loads the chunk that holds r.pos.
func (r *dbReader) fetch() error {
	var part *readPart
	for i := range r.parts {
		if p := &r.parts[i]; r.pos >= p.start && r.pos < p.start+p.size {
			part = p
			break
		}
	}
	if part == nil || part.chunk <= 0 {
		return fmt.Errorf("object %s: no part holds byte %d", r.key, r.pos)
	}
	seq := (r.pos - part.start) / part.chunk
	want := min(part.chunk, part.size-seq*part.chunk)
	var sum string
	var data []byte
	err := r.d.db.QueryRowContext(r.ctx, r.d.q(`SELECT sha256, data FROM blob_chunks WHERE upload_id = ? AND part = ? AND seq = ?`),
		r.upload, part.n, seq).Scan(&sum, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("object %s: %w (it was deleted while it was read)", r.key, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", r.key, err)
	}
	got := sha256.Sum256(data)
	if int64(len(data)) != want || hex.EncodeToString(got[:]) != sum {
		return fmt.Errorf("object %s is damaged: chunk %d of part %d does not match its SHA-256 (or its size); restore the database from a backup or delete the artifact", r.key, seq, part.n)
	}
	r.buf, r.bufStart, r.bufOK = data, part.start+seq*part.chunk, true
	return nil
}

func (r *dbReader) Close() error {
	r.closed, r.buf = true, nil
	return nil
}

// Stat returns the size of an object.
func (d *Database) Stat(ctx context.Context, key string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := ValidateKey(key); err != nil {
		return 0, err
	}
	_, size, _, err := d.object(ctx, key)
	return size, err
}

// Delete removes an object: its row goes at once, so it is gone for every
// reader, and its chunks are removed in small batches (what is left if this
// is interrupted, Collect removes). A missing object is not an error.
func (d *Database) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	var uploadID string
	err := d.tx(ctx, func(tx *sql.Tx) error {
		uploadID = ""
		err := tx.QueryRowContext(ctx, d.q(`SELECT upload_id FROM blob_objects WHERE key = ?`), key).Scan(&uploadID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, d.q(`DELETE FROM blob_objects WHERE key = ?`), key); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, d.q(`UPDATE blob_uploads SET state = ?, updated_at = ? WHERE id = ?`), uploadDead, d.nowMs(), uploadID)
		return err
	})
	if err != nil {
		return fmt.Errorf("deleting %s: %w", key, err)
	}
	if uploadID != "" {
		_ = d.purge(ctx, uploadID)
	}
	return nil
}

// purge removes a dead upload's chunks, collectBatch per transaction, then
// its parts and its row.
func (d *Database) purge(ctx context.Context, uploadID string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		done := false
		var freed int64
		err := d.tx(ctx, func(tx *sql.Tx) error {
			done, freed = false, 0
			var state string
			err := tx.QueryRowContext(ctx, d.q(`SELECT state FROM blob_uploads WHERE id = ?`), uploadID).Scan(&state)
			if errors.Is(err, sql.ErrNoRows) {
				done = true
				return nil
			}
			if err != nil {
				return err
			}
			if state != uploadDead {
				done = true // never touch a live upload
				return nil
			}
			rows, err := tx.QueryContext(ctx, d.q(`SELECT part, seq FROM blob_chunks WHERE upload_id = ? ORDER BY part, seq LIMIT ?`), uploadID, collectBatch)
			if err != nil {
				return err
			}
			var batch [][2]int64
			for rows.Next() {
				var part, seq int64
				if err := rows.Scan(&part, &seq); err != nil {
					rows.Close()
					return err
				}
				batch = append(batch, [2]int64{part, seq})
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if len(batch) == 0 {
				if err := tx.QueryRowContext(ctx, d.q(`SELECT COALESCE(SUM(size), 0) FROM blob_parts WHERE upload_id = ?`), uploadID).Scan(&freed); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, d.q(`DELETE FROM blob_parts WHERE upload_id = ?`), uploadID); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, d.q(`DELETE FROM blob_uploads WHERE id = ? AND state = ?`), uploadID, uploadDead)
				done = true
				return err
			}
			for _, c := range batch {
				if _, err := tx.ExecContext(ctx, d.q(`DELETE FROM blob_chunks WHERE upload_id = ? AND part = ? AND seq = ?`), uploadID, c[0], c[1]); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("removing the content of upload %s: %w", uploadID, err)
		}
		if done {
			d.addUsed(-freed)
			return nil
		}
	}
}

// CollectStats says what one Collect did.
type CollectStats struct {
	Abandoned int // open uploads given up
	Purged    int // dead uploads whose content was removed
}

// Collect gives up uploads that have had no part for AbandonAfter, and
// removes the content of dead uploads (aborted, abandoned, or of deleted
// objects), in small transactions, until ctx is done. Every server may run
// it at once: each step is safe to repeat.
func (d *Database) Collect(ctx context.Context) (CollectStats, error) {
	var st CollectStats
	now := d.nowMs()
	cutoff := now - AbandonAfter.Milliseconds()
	err := d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, d.q(`UPDATE blob_uploads SET state = ?, updated_at = ?
			WHERE state = ? AND created_at < ? AND NOT EXISTS (SELECT 1 FROM blob_parts p WHERE p.upload_id = blob_uploads.id AND p.created_at >= ?)`),
			uploadDead, now, uploadOpen, cutoff, cutoff)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		st.Abandoned = int(n)
		return nil
	})
	if err != nil {
		return st, fmt.Errorf("giving up abandoned uploads: %w", err)
	}
	rows, err := d.db.QueryContext(ctx, d.q(`SELECT id FROM blob_uploads WHERE state = ? ORDER BY updated_at LIMIT 100`), uploadDead)
	if err != nil {
		return st, err
	}
	var dead []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return st, err
		}
		dead = append(dead, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	for _, id := range dead {
		if err := d.purge(ctx, id); err != nil {
			return st, err
		}
		st.Purged++
	}
	return st, nil
}

// Keys lists the keys of finished objects that start with prefix, after
// the key after, at most limit of them, in order. It is for maintenance
// (finding objects nothing refers to).
func (d *Database) Keys(ctx context.Context, prefix, after string, limit int) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, d.q(`SELECT key FROM blob_objects WHERE key > ? AND key LIKE ? ESCAPE '!' ORDER BY key LIMIT ?`),
		after, likePrefix(prefix), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// likePrefix makes a LIKE pattern (escape character '!') matching strings
// that start with p. Keys have no '%' or '!'; '_' is escaped.
func likePrefix(p string) string {
	return strings.ReplaceAll(p, "_", "!_") + "%"
}

// Usage says how much a store holds and may hold.
type Usage struct {
	// UsedBytes counts everything the store holds: finished objects, uploads
	// in progress, and content waiting to be collected.
	UsedBytes int64 `json:"used_bytes"`
	// ObjectBytes and Objects count finished objects only.
	ObjectBytes int64 `json:"object_bytes"`
	Objects     int64 `json:"objects"`
	// OpenUploads are uploads in progress; CollectBytes is content of dead
	// uploads not yet removed.
	OpenUploads  int64 `json:"open_uploads"`
	CollectBytes int64 `json:"collect_bytes"`
	// LimitBytes and MaxObjectBytes are the store's limits; 0 means none.
	LimitBytes     int64 `json:"limit_bytes"`
	MaxObjectBytes int64 `json:"max_object_bytes"`
}

// UsageReporter is a store that can say how much it holds.
type UsageReporter interface {
	Usage(ctx context.Context) (Usage, error)
}

// Usage reads how much the store holds now.
func (d *Database) Usage(ctx context.Context) (Usage, error) {
	u := Usage{LimitBytes: d.maxBytes, MaxObjectBytes: d.maxObject}
	err := d.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM blob_parts`).Scan(&u.UsedBytes)
	if err == nil {
		err = d.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0), COUNT(*) FROM blob_objects`).Scan(&u.ObjectBytes, &u.Objects)
	}
	if err == nil {
		err = d.db.QueryRowContext(ctx, d.q(`SELECT COUNT(*) FROM blob_uploads WHERE state = ?`), uploadOpen).Scan(&u.OpenUploads)
	}
	if err == nil {
		err = d.db.QueryRowContext(ctx, d.q(`SELECT COALESCE(SUM(p.size), 0) FROM blob_parts p JOIN blob_uploads u ON u.id = p.upload_id
			WHERE u.state = ?`), uploadDead).Scan(&u.CollectBytes)
	}
	if err != nil {
		return u, fmt.Errorf("reading the database store's usage: %w", err)
	}
	d.mu.Lock()
	d.used, d.usedAt, d.usedKnown = u.UsedBytes, d.now(), true
	d.mu.Unlock()
	return u, nil
}

// usageMaxAge is how old the cached total may be when checking room for a
// part. Servers do not see each other's writes in the cache, so the limit
// may be passed by what they write within this time.
const usageMaxAge = 5 * time.Second

// reserve fails with ErrFull if n more bytes would take the store past its
// limit.
func (d *Database) reserve(ctx context.Context, n int64) error {
	d.mu.Lock()
	used, fresh := d.used, d.usedKnown && d.now().Sub(d.usedAt) < usageMaxAge
	d.mu.Unlock()
	if fresh && used+n <= d.maxBytes {
		return nil
	}
	var now int64
	if err := d.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM blob_parts`).Scan(&now); err != nil {
		return fmt.Errorf("reading the database store's usage: %w", err)
	}
	d.mu.Lock()
	d.used, d.usedAt, d.usedKnown = now, d.now(), true
	d.mu.Unlock()
	if now+n > d.maxBytes {
		return fmt.Errorf("%w: it holds %s of its limit of %s (artifacts.database.max_total_gib); "+
			"delete artifacts, shorten their retention, raise the limit, or move to S3 (see docs/artifacts.md)", ErrFull, mib(now), mib(d.maxBytes))
	}
	return nil
}

func (d *Database) addUsed(n int64) {
	d.mu.Lock()
	d.used = max(0, d.used+n)
	d.mu.Unlock()
}

// mib writes a byte count for people.
func mib(n int64) string {
	switch {
	case n >= 10<<30:
		return fmt.Sprintf("%.0f GiB", float64(n)/(1<<30))
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d bytes", n)
}

var (
	_ Store         = (*Database)(nil)
	_ UsageReporter = (*Database)(nil)
)
