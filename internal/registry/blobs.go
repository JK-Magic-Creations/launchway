package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
)

// partSize is the size of the parts a blob is sent to the store in: the
// smallest the stores accept for all but the last, rounded up.
const partSize = 8 << 20

// Blob is what the registry knows of a stored blob.
type Blob struct {
	Org    string
	Digest string
	Size   int64
	Key    string
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// blobKey is a new, unique key for the bytes of a blob. It is unique on
// purpose: collecting a blob never deletes an object that a later push of the
// same content has since been given.
func blobKey(org, digest string) string {
	_, hexd, _ := strings.Cut(digest, ":")
	return "registry/" + orgDir(org) + "/blobs/" + hexd[:2] + "/" + hexd + "-" + randHex(4)
}

// putStream copies src to a new object, in parts, and returns its size and
// the digest of what was copied in the algorithm of want ("" or
// "sha256:..."). More than limit bytes fails with a TOO_LARGE refusal and
// nothing is kept.
func (s *Service) putStream(ctx context.Context, key string, src io.Reader, want string, limit int64) (int64, string, error) {
	h, prefix := newHash(want)
	id, err := s.blobs.CreateUpload(ctx, key)
	if err != nil {
		return 0, "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = s.blobs.AbortUpload(context.WithoutCancel(ctx), key, id)
		}
	}()
	var (
		parts []blobstore.Part
		total int64
		buf   = make([]byte, partSize)
	)
	for n := 1; ; n++ {
		got, rerr := io.ReadFull(src, buf)
		if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
			return 0, "", rerr
		}
		last := rerr != nil // a short or empty read ends the stream
		if got == 0 && !last {
			continue
		}
		total += int64(got)
		if limit > 0 && total > limit {
			return 0, "", newErr(http.StatusRequestEntityTooLarge, CodeTooLarge,
				fmt.Sprintf("this blob is larger than the %d MiB the registry accepts", limit>>20), "Split the layer, or ask an administrator to raise registry.max_blob_mib.")
		}
		_, _ = h.Write(buf[:got])
		if got > 0 || n == 1 {
			etag, err := s.blobs.UploadPart(ctx, key, id, n, buf[:got])
			if err != nil {
				return 0, "", err
			}
			parts = append(parts, blobstore.Part{Number: n, ETag: etag})
		}
		if last {
			break
		}
	}
	if err := s.blobs.CompleteUpload(ctx, key, id, parts); err != nil {
		return 0, "", err
	}
	ok = true
	return total, prefix + hex.EncodeToString(h.Sum(nil)), nil
}

// putBytes stores a small object (a staged chunk) under key.
func (s *Service) putBytes(ctx context.Context, key string, data []byte) error {
	id, err := s.blobs.CreateUpload(ctx, key)
	if err != nil {
		return err
	}
	etag, err := s.blobs.UploadPart(ctx, key, id, 1, data)
	if err != nil {
		_ = s.blobs.AbortUpload(context.WithoutCancel(ctx), key, id)
		return err
	}
	if err := s.blobs.CompleteUpload(ctx, key, id, []blobstore.Part{{Number: 1, ETag: etag}}); err != nil {
		_ = s.blobs.AbortUpload(context.WithoutCancel(ctx), key, id)
		return err
	}
	return nil
}

// commitBlob records a blob that was just stored under key (or finds that its
// content is already there), and links it to a repository. The returned
// bool is true when the bytes just stored are the ones kept; when false the
// organization already had these bytes and the new object is removed.
func (s *Service) commitBlob(ctx context.Context, repo, org, digest string, size int64, key, by string) (Blob, error) {
	now := s.now()
	var kept = Blob{Org: org, Digest: digest, Size: size, Key: key}
	err := s.withTx(ctx, func(tx Tx) error {
		res, err := tx.Exec(ctx, `INSERT INTO registry_blobs(org, digest, size, store_key, created_at, touched_at) VALUES(?, ?, ?, ?, ?, ?) ON CONFLICT (org, digest) DO NOTHING`,
			org, digest, size, key, now, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Already stored for this organization: keep that one, and mark it
			// in use now so a collection in progress leaves it.
			if _, err := tx.Exec(ctx, `UPDATE registry_blobs SET touched_at = ? WHERE org = ? AND digest = ?`, now, org, digest); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT size, store_key FROM registry_blobs WHERE org = ? AND digest = ?`, org, digest).Scan(&kept.Size, &kept.Key); err != nil {
				return err
			}
		}
		return s.link(ctx, tx, repo, digest, now)
	})
	if err != nil {
		_ = s.blobs.Delete(context.WithoutCancel(ctx), key)
		return Blob{}, err
	}
	if kept.Key != key {
		_ = s.blobs.Delete(context.WithoutCancel(ctx), key)
	}
	return kept, nil
}

// link lets a repository see a blob (and refreshes the link's age, which
// garbage collection respects).
func (s *Service) link(ctx context.Context, tx execer, repo, digest string, now int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO registry_repo_blobs(repo, digest, linked_at) VALUES(?, ?, ?) ON CONFLICT (repo, digest) DO UPDATE SET linked_at = excluded.linked_at`,
		repo, digest, now)
	return err
}

// BlobInfo returns a blob as a repository sees it, or an error answered as
// BLOB_UNKNOWN: a blob that exists but is not linked to this repository is
// unknown to it, so a digest never opens another repository's content.
func (s *Service) BlobInfo(ctx context.Context, repo, digest string) (Blob, error) {
	r, err := s.repoRow(ctx, repo)
	if err != nil {
		return Blob{}, err
	}
	var b Blob
	b.Org, b.Digest = r.Org, digest
	err = s.db.QueryRow(ctx, `SELECT b.size, b.store_key FROM registry_blobs b JOIN registry_repo_blobs l ON l.digest = b.digest
		WHERE l.repo = ? AND b.org = ? AND b.digest = ?`, repo, r.Org, digest).Scan(&b.Size, &b.Key)
	if isNoRows(err) {
		return b, newErr(http.StatusNotFound, CodeBlobUnknown, "this repository has no blob "+digest, "")
	}
	return b, err
}

// OpenBlob reads length bytes of a blob from offset (length < 0: to the end).
func (s *Service) OpenBlob(ctx context.Context, b Blob, offset, length int64) (io.ReadCloser, error) {
	rc, err := s.blobs.Open(ctx, b.Key, offset, length)
	if errors.Is(err, blobstore.ErrNotFound) {
		s.log.Error("a registry blob's object is missing from the blob store", "digest", b.Digest, "key", b.Key)
		return nil, newErr(http.StatusInternalServerError, CodeUnavailable, "the bytes of this blob are missing from the blob store; tell an administrator", "")
	}
	return rc, err
}

// readAll reads a small blob (a manifest) fully.
func (s *Service) readAll(ctx context.Context, b Blob) ([]byte, error) {
	rc, err := s.OpenBlob(ctx, b, 0, -1)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, s.limits.MaxManifestBytes+1))
}

// Repository is a row of registry_repositories.
type Repository struct {
	Name      string `json:"name"`
	Project   string `json:"project"`
	Org       string `json:"org"`
	CreatedAt int64  `json:"created_at"`
	CreatedBy string `json:"created_by"`
	PushedAt  int64  `json:"pushed_at"`
}

func (s *Service) repoRow(ctx context.Context, name string) (Repository, error) {
	var r Repository
	err := s.db.QueryRow(ctx, `SELECT name, project, org, created_at, created_by, pushed_at FROM registry_repositories WHERE name = ?`, name).
		Scan(&r.Name, &r.Project, &r.Org, &r.CreatedAt, &r.CreatedBy, &r.PushedAt)
	if isNoRows(err) {
		return r, newErr(http.StatusNotFound, CodeNameUnknown, "no repository "+name, "")
	}
	return r, err
}

// ensureRepo returns the repository, creating it (a repository comes into
// being by its first push) within the project's repository limit.
func (s *Service) ensureRepo(ctx context.Context, name, by string) (Repository, error) {
	if err := ValidName(name); err != nil {
		return Repository{}, newErr(http.StatusBadRequest, CodeNameInvalid, err.Error(), "")
	}
	if r, err := s.repoRow(ctx, name); err == nil {
		return r, nil
	} else if asError(err).Code != CodeNameUnknown {
		return r, err
	}
	project := ProjectOf(name)
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM registry_repositories WHERE project = ?`, project).Scan(&n); err != nil {
		return Repository{}, err
	}
	if n >= s.limits.MaxRepositoriesPerProject {
		return Repository{}, newErr(http.StatusForbidden, CodeDenied,
			fmt.Sprintf("project %s already has %d repositories, the most the registry allows", project, n), "Delete a repository you no longer use, or ask an administrator to raise the limit.")
	}
	r := Repository{Name: name, Project: project, Org: s.org(project), CreatedAt: s.now(), CreatedBy: by}
	_, err := s.db.Exec(ctx, `INSERT INTO registry_repositories(name, project, org, created_at, created_by) VALUES(?, ?, ?, ?, ?) ON CONFLICT (name) DO NOTHING`,
		r.Name, r.Project, r.Org, r.CreatedAt, r.CreatedBy)
	if err != nil {
		return r, err
	}
	return s.repoRow(ctx, name)
}

func (s *Service) touchRepo(ctx context.Context, tx execer, name string) error {
	_, err := tx.Exec(ctx, `UPDATE registry_repositories SET pushed_at = ? WHERE name = ?`, s.now(), name)
	return err
}
