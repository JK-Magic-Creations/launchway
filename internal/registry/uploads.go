package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// An upload is a session of chunks. Its state is in the database (the row
// and one row per chunk) and each chunk's bytes are a small object in the
// blob store, so the next chunk may arrive at any server of a group, and the
// last is checked against the digest by whichever server gets it.

// Upload is an upload in progress.
type Upload struct {
	ID        string
	Repo      string
	Org       string
	Size      int64
	CreatedAt int64
	UpdatedAt int64
}

// chunkKey names a staged chunk. It is unique on purpose: two servers that
// race to write the same chunk each write an object of their own, the loser
// removes its own, and never the winner's.
func chunkKey(id string, seq int) string {
	return fmt.Sprintf("registry/uploads/%s/%06d-%s", id, seq, randHex(4))
}

// StartUpload opens an upload to a repository, which comes into being if it
// is new, and returns it.
func (s *Service) StartUpload(ctx context.Context, repo, by string) (Upload, error) {
	if err := s.ensureStore(); err != nil {
		return Upload{}, err
	}
	r, err := s.ensureRepo(ctx, repo, by)
	if err != nil {
		return Upload{}, err
	}
	if err := s.refuseOverQuota(ctx, r.Project, 0); err != nil {
		return Upload{}, err
	}
	u := Upload{ID: ids.New("upl"), Repo: repo, Org: r.Org, CreatedAt: s.now()}
	u.UpdatedAt = u.CreatedAt
	_, err = s.db.Exec(ctx, `INSERT INTO registry_uploads(id, repo, org, created_at, updated_at, created_by) VALUES(?, ?, ?, ?, ?, ?)`,
		u.ID, u.Repo, u.Org, u.CreatedAt, u.UpdatedAt, by)
	return u, err
}

// GetUpload returns an upload of a repository.
func (s *Service) GetUpload(ctx context.Context, repo, id string) (Upload, error) {
	var u Upload
	if !uploadIDRe.MatchString(id) {
		return u, newErr(http.StatusNotFound, CodeBlobUploadUnknown, "no such upload", "")
	}
	err := s.db.QueryRow(ctx, `SELECT id, repo, org, size, created_at, updated_at FROM registry_uploads WHERE id = ? AND repo = ?`, id, repo).
		Scan(&u.ID, &u.Repo, &u.Org, &u.Size, &u.CreatedAt, &u.UpdatedAt)
	if isNoRows(err) {
		return u, newErr(http.StatusNotFound, CodeBlobUploadUnknown, "no such upload; it may have been completed, cancelled or collected after being idle", "Start the upload again.")
	}
	return u, err
}

// AppendChunk adds what body holds to an upload. start, when it is not
// negative, is the offset the client says the chunk begins at, which must be
// the upload's size. It returns the upload as it is afterwards. A body is
// stored in segments, each committed, so a body cut off half way keeps the
// part that arrived (the client asks for the upload's size and goes on from
// there).
func (s *Service) AppendChunk(ctx context.Context, u Upload, start int64, body io.Reader) (Upload, error) {
	if err := s.ensureStore(); err != nil {
		return u, err
	}
	if start >= 0 && start != u.Size {
		return u, &Error{Status: http.StatusRequestedRangeNotSatisfiable, Code: CodeRangeInvalid,
			Message: fmt.Sprintf("this chunk starts at %d but the upload holds %d bytes", start, u.Size),
			Next:    "Send the next chunk from the offset in the Range header of a GET on the upload.", Detail: u.Size}
	}
	buf := make([]byte, partSize)
	for {
		n, rerr := io.ReadFull(body, buf)
		if n > 0 {
			if u.Size+int64(n) > s.limits.MaxBlobBytes {
				return u, newErr(http.StatusRequestEntityTooLarge, CodeTooLarge,
					fmt.Sprintf("this upload is larger than the %d MiB a blob may be", s.limits.MaxBlobBytes>>20), "Split the layer, or ask an administrator to raise registry.max_blob_mib.")
			}
			next, err := s.stageSegment(ctx, u, buf[:n])
			if err != nil {
				return u, err
			}
			u = next
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				return u, nil
			}
			return u, rerr
		}
	}
}

// stageSegment stores one segment as an object and records it, in this
// order: a server that stops between the two leaves an object no row names,
// which collection removes; the reverse would leave a row that names nothing.
func (s *Service) stageSegment(ctx context.Context, u Upload, data []byte) (Upload, error) {
	var seq int
	if err := s.db.QueryRow(ctx, `SELECT next_seq FROM registry_uploads WHERE id = ? AND size = ?`, u.ID, u.Size).Scan(&seq); err != nil {
		if isNoRows(err) {
			return u, &Error{Status: http.StatusRequestedRangeNotSatisfiable, Code: CodeRangeInvalid, Message: "another request changed this upload at the same time",
				Next: "Ask for the upload's size and send the next chunk from there."}
		}
		return u, err
	}
	key := chunkKey(u.ID, seq)
	if err := s.putBytes(ctx, key, data); err != nil {
		return u, err
	}
	now := s.now()
	err := s.withTx(ctx, func(tx Tx) error {
		res, err := tx.Exec(ctx, `UPDATE registry_uploads SET size = size + ?, next_seq = next_seq + 1, updated_at = ? WHERE id = ? AND size = ? AND next_seq = ?`,
			len(data), now, u.ID, u.Size, seq)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errRaced
		}
		_, err = tx.Exec(ctx, `INSERT INTO registry_upload_chunks(upload_id, seq, size, store_key) VALUES(?, ?, ?, ?)`, u.ID, seq, len(data), key)
		return err
	})
	if err != nil {
		_ = s.blobs.Delete(context.WithoutCancel(ctx), key)
		if errors.Is(err, errRaced) {
			return u, &Error{Status: http.StatusRequestedRangeNotSatisfiable, Code: CodeRangeInvalid, Message: "another request changed this upload at the same time",
				Next: "Ask for the upload's size and send the next chunk from there."}
		}
		return u, err
	}
	u.Size += int64(len(data))
	u.UpdatedAt = now
	return u, nil
}

var errRaced = errors.New("raced")

// chunkReader reads an upload's chunks, in order, as one stream.
type chunkReader struct {
	ctx  context.Context
	s    *Service
	keys []string
	cur  io.ReadCloser
}

func (c *chunkReader) Read(p []byte) (int, error) {
	for {
		if c.cur == nil {
			if len(c.keys) == 0 {
				return 0, io.EOF
			}
			rc, err := c.s.blobs.Open(c.ctx, c.keys[0], 0, -1)
			if err != nil {
				return 0, err
			}
			c.cur, c.keys = rc, c.keys[1:]
		}
		n, err := c.cur.Read(p)
		if errors.Is(err, io.EOF) {
			_ = c.cur.Close()
			c.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *chunkReader) Close() error {
	if c.cur != nil {
		return c.cur.Close()
	}
	return nil
}

func (s *Service) chunkKeys(ctx context.Context, id string) ([]string, error) {
	return column(ctx, s.db, `SELECT store_key FROM registry_upload_chunks WHERE upload_id = ? ORDER BY seq`, id)
}

// FinishUpload ends an upload: what is left in tail is appended, the whole is
// checked against digest, stored once for the organization, and linked to the
// repository. A digest that does not match refuses the upload and keeps
// nothing; the upload stays so the client may try again.
func (s *Service) FinishUpload(ctx context.Context, u Upload, digest string, tail io.Reader, by string) (Blob, error) {
	if err := ValidDigest(digest); err != nil {
		return Blob{}, newErr(http.StatusBadRequest, CodeDigestInvalid, err.Error(), "")
	}
	if tail != nil {
		var err error
		if u, err = s.AppendChunk(ctx, u, -1, tail); err != nil {
			return Blob{}, err
		}
	}
	r, err := s.repoRow(ctx, u.Repo)
	if err != nil {
		return Blob{}, err
	}
	keys, err := s.chunkKeys(ctx, u.ID)
	if err != nil {
		return Blob{}, err
	}
	if u.Size > s.limits.MaxBlobBytes {
		return Blob{}, newErr(http.StatusRequestEntityTooLarge, CodeTooLarge, "this upload is larger than a blob may be", "")
	}
	if err := s.checkQuota(ctx, r, digest, u.Size); err != nil {
		return Blob{}, err
	}
	cr := &chunkReader{ctx: ctx, s: s, keys: keys}
	defer cr.Close()
	key := blobKey(r.Org, digest)
	size, got, err := s.putStream(ctx, key, cr, digest, s.limits.MaxBlobBytes)
	if err != nil {
		return Blob{}, err
	}
	if got != digest || size != u.Size {
		_ = s.blobs.Delete(context.WithoutCancel(ctx), key)
		return Blob{}, newErr(http.StatusBadRequest, CodeDigestInvalid,
			fmt.Sprintf("the upload's content is %s (%d bytes), not the %s the client named", got, size, digest), "Push again; if it keeps happening, the network or the client is damaging the data.")
	}
	b, err := s.commitBlob(ctx, r.Name, r.Org, digest, size, key, by)
	if err != nil {
		return Blob{}, err
	}
	s.dropUpload(ctx, u.ID)
	return b, nil
}

// PutBlobDirect stores a blob sent in one request (a monolithic upload),
// without a session: the stream is checked as it is stored.
func (s *Service) PutBlobDirect(ctx context.Context, repo, digest string, body io.Reader, by string) (Blob, error) {
	if err := s.ensureStore(); err != nil {
		return Blob{}, err
	}
	if err := ValidDigest(digest); err != nil {
		return Blob{}, newErr(http.StatusBadRequest, CodeDigestInvalid, err.Error(), "")
	}
	r, err := s.ensureRepo(ctx, repo, by)
	if err != nil {
		return Blob{}, err
	}
	if err := s.refuseOverQuota(ctx, r.Project, 0); err != nil {
		return Blob{}, err
	}
	key := blobKey(r.Org, digest)
	size, got, err := s.putStream(ctx, key, body, digest, s.limits.MaxBlobBytes)
	if err != nil {
		return Blob{}, err
	}
	if got != digest {
		_ = s.blobs.Delete(context.WithoutCancel(ctx), key)
		return Blob{}, newErr(http.StatusBadRequest, CodeDigestInvalid, fmt.Sprintf("the content is %s, not the %s the client named", got, digest), "Push again.")
	}
	if err := s.checkQuota(ctx, r, digest, size); err != nil {
		_ = s.blobs.Delete(context.WithoutCancel(ctx), key)
		return Blob{}, err
	}
	return s.commitBlob(ctx, r.Name, r.Org, digest, size, key, by)
}

// MountBlob links a blob of another repository the caller may read to this
// one (a cross-repository mount): only within the organization, and only
// when the source repository has the blob. It reports false when there is
// nothing to mount, and the client uploads as usual.
func (s *Service) MountBlob(ctx context.Context, repo, from, digest, by string) (bool, error) {
	if err := ValidDigest(digest); err != nil {
		return false, nil
	}
	src, err := s.repoRow(ctx, from)
	if err != nil {
		return false, nil
	}
	dst, err := s.ensureRepo(ctx, repo, by)
	if err != nil {
		return false, err
	}
	if src.Org != dst.Org {
		return false, nil // never across organizations
	}
	b, err := s.BlobInfo(ctx, from, digest)
	if err != nil {
		return false, nil
	}
	if err := s.checkQuota(ctx, dst, digest, b.Size); err != nil {
		return false, err
	}
	err = s.withTx(ctx, func(tx Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE registry_blobs SET touched_at = ? WHERE org = ? AND digest = ?`, s.now(), dst.Org, digest); err != nil {
			return err
		}
		return s.link(ctx, tx, dst.Name, digest, s.now())
	})
	return err == nil, err
}

// CancelUpload throws an upload away.
func (s *Service) CancelUpload(ctx context.Context, u Upload) error {
	s.dropUpload(ctx, u.ID)
	return nil
}

// dropUpload removes an upload's rows and staged objects.
func (s *Service) dropUpload(ctx context.Context, id string) {
	ctx = context.WithoutCancel(ctx)
	keys, err := s.chunkKeys(ctx, id)
	if err != nil {
		s.log.Warn("listing the chunks of an upload to remove them", "upload", id, "err", err)
	}
	_, _ = s.db.Exec(ctx, `DELETE FROM registry_upload_chunks WHERE upload_id = ?`, id)
	_, _ = s.db.Exec(ctx, `DELETE FROM registry_uploads WHERE id = ?`, id)
	for _, k := range keys {
		if err := s.blobs.Delete(ctx, k); err != nil {
			s.log.Warn("removing a staged chunk", "key", k, "err", err)
		}
	}
}

// expireUploads removes uploads idle for longer than the limit.
func (s *Service) expireUploads(ctx context.Context, dry bool) (int, error) {
	cutoff := s.o.Now().Add(-s.limits.UploadTTL).UnixMilli()
	idsList, err := column(ctx, s.db, `SELECT id FROM registry_uploads WHERE updated_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	sort.Strings(idsList)
	if !dry {
		for _, id := range idsList {
			s.dropUpload(ctx, id)
		}
	}
	return len(idsList), nil
}
