// Package blobstore keeps build artifacts as immutable objects, on a local
// (or shared network) filesystem or in an S3-compatible object store.
//
// Runners upload a file to the server in chunks, and every chunk may arrive
// at a different server of a group that shares one database. So the state of
// a multipart upload lives in the store itself, addressed only by the object
// key, the upload ID and the part number, never in one process's memory.
//
// The store keeps bytes faithfully and does not check content: the engine
// verifies the SHA-256 of every artifact itself. Objects are written once,
// under keys the server makes unique per upload, and deleted later by a
// pruner.
package blobstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNotFound is returned (wrapped) by Open and Stat for a missing object,
// and by the upload methods for an upload that does not exist. Test for it
// with errors.Is.
var ErrNotFound = errors.New("not found")

// Part names one uploaded part when completing an upload.
type Part struct {
	Number int    `json:"n"` // 1..10000
	ETag   string `json:"etag"`
}

// Store keeps objects by key. Keys are made by the server: lower-case
// letters, digits, '/', '-', '_', '.', no "..", no leading '/', at most 512
// bytes; anything else is refused with an error (see ValidateKey).
type Store interface {
	// Name is "filesystem" or "s3", for logs and /api/v1/info.
	Name() string
	// Check reports whether the store is reachable and writable: it writes,
	// reads back and deletes a small probe object under a "launchway-check/"
	// prefix.
	Check(ctx context.Context) error
	// CreateUpload starts a multipart upload of key and returns its ID.
	CreateUpload(ctx context.Context, key string) (uploadID string, err error)
	// UploadPart stores part n of an upload. Uploading the same part again
	// replaces it (a retry after a timeout). Every part but the last must be
	// at least MinPartSize bytes.
	UploadPart(ctx context.Context, key, uploadID string, n int, data []byte) (etag string, err error)
	// CompleteUpload makes the object from the parts, in order; afterwards
	// the object is visible whole or not at all. Completing an upload that
	// was already completed with the same parts succeeds if the object exists
	// (idempotent retry).
	CompleteUpload(ctx context.Context, key, uploadID string, parts []Part) error
	// AbortUpload throws an upload and its parts away. An unknown upload is
	// not an error.
	AbortUpload(ctx context.Context, key, uploadID string) error
	// Open reads length bytes from offset (length < 0: to the end). Reading
	// past the end returns what there is.
	Open(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
	// Stat returns the size of an object.
	Stat(ctx context.Context, key string) (size int64, err error)
	// Delete removes an object. Deleting a missing object is not an error.
	Delete(ctx context.Context, key string) error
}

// MinPartSize is S3's minimum size for every part of an upload but the last.
const MinPartSize = 5 << 20

// MaxPartNumber is the highest part number S3 accepts.
const MaxPartNumber = 10000

// MaxKeyLen is the longest key a store accepts, in bytes.
const MaxKeyLen = 512

// checkPrefix is where Check writes its probe objects.
const checkPrefix = "launchway-check/"

// ValidateKey returns an error unless key is a key the stores accept: 1 to
// MaxKeyLen bytes of lower-case letters, digits, '/', '-', '_' and '.', with
// no "..", no leading or trailing '/' and no empty or "." path segment.
func ValidateKey(key string) error {
	switch {
	case key == "":
		return errors.New("the object key is empty")
	case len(key) > MaxKeyLen:
		return fmt.Errorf("the object key is %d bytes long, more than the %d allowed", len(key), MaxKeyLen)
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '/' || c == '-' || c == '_' || c == '.') {
			return fmt.Errorf("the object key %q has a character other than lower-case letters, digits, '/', '-', '_' and '.'", key)
		}
	}
	if strings.Contains(key, "..") {
		return fmt.Errorf("the object key %q contains \"..\"", key)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." {
			return fmt.Errorf("the object key %q has an empty or \".\" path segment (a leading, trailing or doubled '/')", key)
		}
	}
	return nil
}

// checkParts returns an error unless parts is a non-empty list of part
// numbers in strictly increasing order, each within 1..MaxPartNumber, and
// each with an ETag.
func checkParts(parts []Part) error {
	if len(parts) == 0 {
		return errors.New("an upload needs at least one part to complete")
	}
	prev := 0
	for _, p := range parts {
		if err := checkPartNumber(p.Number); err != nil {
			return err
		}
		if p.Number <= prev {
			return fmt.Errorf("the parts must be listed in increasing order of number without repeats; part %d follows part %d", p.Number, prev)
		}
		if p.ETag == "" {
			return fmt.Errorf("part %d has no ETag", p.Number)
		}
		prev = p.Number
	}
	return nil
}

func checkPartNumber(n int) error {
	if n < 1 || n > MaxPartNumber {
		return fmt.Errorf("part number %d is outside 1..%d", n, MaxPartNumber)
	}
	return nil
}

// checkRange returns an error for a negative offset.
func checkRange(offset int64) error {
	if offset < 0 {
		return fmt.Errorf("cannot read from negative offset %d", offset)
	}
	return nil
}

// randomHex returns n random bytes as lower-case hex.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // never fails (crypto/rand panics instead)
	return hex.EncodeToString(b)
}

// emptyReader is what reading past the end of an object returns.
type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }
func (emptyReader) Close() error             { return nil }

// checkStore is the probe behind Check: write a small object, read it back,
// delete it. put writes the probe however suits the store.
func checkStore(ctx context.Context, s Store, put func(key string, data []byte) error) error {
	key := checkPrefix + randomHex(16)
	data := []byte("launchway store check " + key + "\n")
	if err := put(key, data); err != nil {
		return fmt.Errorf("writing the probe object %s: %w", key, err)
	}
	r, err := s.Open(ctx, key, 0, -1)
	if err != nil {
		_ = s.Delete(ctx, key)
		return fmt.Errorf("reading back the probe object %s: %w", key, err)
	}
	got, err := io.ReadAll(io.LimitReader(r, int64(len(data))+1))
	r.Close()
	if err != nil {
		_ = s.Delete(ctx, key)
		return fmt.Errorf("reading back the probe object %s: %w", key, err)
	}
	if string(got) != string(data) {
		_ = s.Delete(ctx, key)
		return fmt.Errorf("the probe object %s read back with different content (%d bytes instead of %d)", key, len(got), len(data))
	}
	if err := s.Delete(ctx, key); err != nil {
		return fmt.Errorf("deleting the probe object %s: %w", key, err)
	}
	return nil
}
