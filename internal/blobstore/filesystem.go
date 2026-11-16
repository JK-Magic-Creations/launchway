package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"syscall"
)

const (
	dirPerm  = 0o750
	filePerm = 0o640

	objectsDir = "objects"
	uploadsDir = "uploads"
	// uploadKeyFile, inside an upload's directory, holds the key the upload
	// is for, so a part can never be completed into another object.
	uploadKeyFile = "key"
	// tmpPrefix starts the name of every temporary file. '~' is not allowed
	// in keys, so a temporary file can never be mistaken for an object.
	tmpPrefix = ".tmp~"
)

// Filesystem stores objects under a directory:
//
//	<dir>/objects/<key>                 a finished object
//	<dir>/uploads/<uploadID>/key        the key an upload is for
//	<dir>/uploads/<uploadID>/<n>.part   part n of an upload
//
// Every write goes to a temporary file in the same directory first and is
// renamed into place, so readers see a whole file or none. Several servers
// may use one Filesystem store only if they share the directory (a network
// filesystem); the server checks that separately.
//
// The store does not enforce MinPartSize: parts of any size are accepted.
// Abandoned uploads stay under uploads/ until they are aborted.
type Filesystem struct {
	dir  string
	root *os.Root // every access goes through it, so no name can escape dir
}

// NewFilesystem opens (creating if needed) a filesystem store in dir.
func NewFilesystem(dir string) (*Filesystem, error) {
	if dir == "" {
		return nil, errors.New("the filesystem artifact store needs a directory")
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("creating the artifact directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("opening the artifact directory: %w", err)
	}
	for _, d := range []string{objectsDir, uploadsDir} {
		if err := root.MkdirAll(d, dirPerm); err != nil {
			root.Close()
			return nil, fmt.Errorf("creating %s in the artifact directory %s: %w", d, dir, err)
		}
	}
	return &Filesystem{dir: dir, root: root}, nil
}

// Name returns "filesystem".
func (f *Filesystem) Name() string { return "filesystem" }

// Dir returns the directory the store keeps its files in.
func (f *Filesystem) Dir() string { return f.dir }

// Close releases the store's handle on its directory. The store must not be
// used afterwards.
func (f *Filesystem) Close() error { return f.root.Close() }

// Check writes, reads back and deletes a small probe object, going through
// a whole multipart upload.
func (f *Filesystem) Check(ctx context.Context) error {
	return checkStore(ctx, f, func(key string, data []byte) error {
		id, err := f.CreateUpload(ctx, key)
		if err != nil {
			return err
		}
		etag, err := f.UploadPart(ctx, key, id, 1, data)
		if err != nil {
			_ = f.AbortUpload(ctx, key, id)
			return err
		}
		if err := f.CompleteUpload(ctx, key, id, []Part{{Number: 1, ETag: etag}}); err != nil {
			_ = f.AbortUpload(ctx, key, id)
			return err
		}
		return nil
	})
}

// CreateUpload starts an upload of key. The upload ID is 128 random bits
// in hex.
func (f *Filesystem) CreateUpload(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	id := randomHex(16)
	dir := uploadsDir + "/" + id
	if err := f.root.Mkdir(dir, dirPerm); err != nil {
		return "", fmt.Errorf("creating the directory for upload %s: %w", id, err)
	}
	if err := f.writeFile(dir, uploadKeyFile, []byte(key)); err != nil {
		_ = f.root.RemoveAll(dir)
		return "", fmt.Errorf("recording the key of upload %s: %w", id, err)
	}
	return id, nil
}

// UploadPart stores part n, replacing any earlier copy of it atomically. The
// ETag is the hex SHA-256 of the part.
func (f *Filesystem) UploadPart(ctx context.Context, key, uploadID string, n int, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := checkPartNumber(n); err != nil {
		return "", err
	}
	if err := f.checkUpload(key, uploadID); err != nil {
		return "", err
	}
	if err := f.writeFile(uploadsDir+"/"+uploadID, partName(n), data); err != nil {
		if isNotExist(err) {
			// The upload was aborted or completed while the part was written.
			return "", fmt.Errorf("upload %s of %s: %w (it was aborted or completed)", uploadID, key, ErrNotFound)
		}
		return "", fmt.Errorf("storing part %d of upload %s: %w", n, uploadID, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// CompleteUpload joins the parts, in the order given, into the object. It
// checks each part against its ETag first. If the object is already there
// (an earlier attempt, or another server, completed the upload) it succeeds,
// as long as the object's size matches the parts when they are still there
// to compare.
func (f *Filesystem) CompleteUpload(ctx context.Context, key, uploadID string, parts []Part) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkParts(parts); err != nil {
		return err
	}
	obj := objectsDir + "/" + key
	if err := f.checkUpload(key, uploadID); err != nil {
		if errors.Is(err, ErrNotFound) && f.exists(obj) {
			return nil // completed before: the upload is gone, the object is there
		}
		return err
	}

	// Sizes first: a retry of a completed upload stops here without
	// copying anything.
	var total int64
	for _, p := range parts {
		fi, err := f.root.Stat(uploadsDir + "/" + uploadID + "/" + partName(p.Number))
		if err != nil {
			return f.partGone(obj, uploadID, p.Number, err)
		}
		total += fi.Size()
	}
	if done, err := f.alreadyThere(obj, total); done || err != nil {
		if done {
			f.removeUpload(uploadID)
		}
		return err
	}

	dir := path.Dir(obj)
	if err := f.root.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("creating the directory for %s: %w", key, err)
	}
	tmpName := dir + "/" + tmpPrefix + randomHex(8)
	tmp, err := f.root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return fmt.Errorf("creating a temporary file for %s: %w", key, err)
	}
	closed := false
	defer func() {
		if !closed {
			tmp.Close()
		}
		_ = f.root.Remove(tmpName) // after the rename there is nothing to remove
	}()

	total = 0
	for _, p := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := f.copyPart(tmp, uploadID, p)
		if err != nil {
			if isNotExist(err) {
				return f.partGone(obj, uploadID, p.Number, err)
			}
			return err
		}
		total += n
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("writing %s to disk: %w", key, err)
	}
	closed = true
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s to disk: %w", key, err)
	}

	// Another server may have finished the same upload meanwhile. Its object
	// has the same content, so replacing it would be harmless too, but
	// checking keeps the rule that a different object is never overwritten.
	if done, err := f.alreadyThere(obj, total); done || err != nil {
		if done {
			f.removeUpload(uploadID)
		}
		return err
	}
	if err := f.root.Rename(tmpName, obj); err != nil {
		return fmt.Errorf("moving %s into place: %w", key, err)
	}
	if err := f.syncDir(dir); err != nil {
		return fmt.Errorf("writing the directory of %s to disk: %w", key, err)
	}
	f.removeUpload(uploadID)
	return nil
}

// copyPart appends part p to w, checking it against p.ETag, and returns its
// size.
func (f *Filesystem) copyPart(w io.Writer, uploadID string, p Part) (int64, error) {
	pf, err := f.root.Open(uploadsDir + "/" + uploadID + "/" + partName(p.Number))
	if err != nil {
		return 0, err
	}
	defer pf.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), pf)
	if err != nil {
		return 0, fmt.Errorf("copying part %d of upload %s: %w", p.Number, uploadID, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != p.ETag {
		return 0, fmt.Errorf("part %d of upload %s does not match the ETag given for it (the part was uploaded again since, or the ETag is wrong)", p.Number, uploadID)
	}
	return n, nil
}

// partGone explains a part that could not be found while completing: either
// another server completed the upload meanwhile (the object is there: done),
// or the part was never uploaded.
func (f *Filesystem) partGone(obj, uploadID string, n int, err error) error {
	if f.exists(obj) {
		return nil
	}
	if !f.exists(uploadsDir + "/" + uploadID) {
		return fmt.Errorf("upload %s: %w (it was aborted)", uploadID, ErrNotFound)
	}
	if isNotExist(err) {
		return fmt.Errorf("part %d of upload %s was never uploaded", n, uploadID)
	}
	return fmt.Errorf("reading part %d of upload %s: %w", n, uploadID, err)
}

// alreadyThere reports whether the object exists with the given size (a
// completed upload completed again), or returns an error when it exists
// with another size.
func (f *Filesystem) alreadyThere(obj string, size int64) (bool, error) {
	fi, err := f.root.Lstat(obj)
	switch {
	case isNotExist(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("looking for an existing %s: %w", obj, err)
	case !fi.Mode().IsRegular():
		return false, fmt.Errorf("%s exists and is not a regular file", obj)
	case fi.Size() != size:
		return false, fmt.Errorf("%s already exists with %d bytes, not the %d bytes of the parts; objects are never overwritten", obj, fi.Size(), size)
	}
	return true, nil
}

// AbortUpload removes an upload and its parts. An unknown upload is not an
// error.
func (f *Filesystem) AbortUpload(ctx context.Context, key, uploadID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.checkUpload(key, uploadID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if err := f.root.RemoveAll(uploadsDir + "/" + uploadID); err != nil {
		return fmt.Errorf("removing upload %s: %w", uploadID, err)
	}
	return nil
}

// Open returns a reader of length bytes of the object from offset (length
// < 0: to the end).
func (f *Filesystem) Open(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	if err := checkRange(offset); err != nil {
		return nil, err
	}
	file, err := f.root.Open(objectsDir + "/" + key)
	if err != nil {
		if isNotExist(err) {
			return nil, fmt.Errorf("object %s: %w", key, ErrNotFound)
		}
		return nil, fmt.Errorf("opening %s: %w", key, err)
	}
	fi, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("opening %s: %w", key, err)
	}
	if !fi.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("object %s: %w", key, ErrNotFound)
	}
	size := fi.Size()
	if offset >= size || length == 0 {
		file.Close()
		return emptyReader{}, nil
	}
	n := size - offset
	if length >= 0 && length < n {
		n = length
	}
	return &sectionFile{SectionReader: io.NewSectionReader(file, offset, n), file: file}, nil
}

// sectionFile reads a section of an open file and closes the file.
type sectionFile struct {
	*io.SectionReader
	file *os.File
}

func (s *sectionFile) Close() error { return s.file.Close() }

// Stat returns the size of an object.
func (f *Filesystem) Stat(ctx context.Context, key string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := ValidateKey(key); err != nil {
		return 0, err
	}
	fi, err := f.root.Stat(objectsDir + "/" + key)
	if err != nil {
		if isNotExist(err) {
			return 0, fmt.Errorf("object %s: %w", key, ErrNotFound)
		}
		return 0, fmt.Errorf("looking at %s: %w", key, err)
	}
	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("object %s: %w", key, ErrNotFound)
	}
	return fi.Size(), nil
}

// Delete removes an object. A missing object is not an error. Directories
// left empty are kept: removing them could race with an upload completing
// into them.
func (f *Filesystem) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	obj := objectsDir + "/" + key
	fi, err := f.root.Lstat(obj)
	switch {
	case isNotExist(err):
		return nil
	case err != nil:
		return fmt.Errorf("looking at %s: %w", key, err)
	case fi.IsDir():
		return nil // a directory of other objects, not an object
	}
	if err := f.root.Remove(obj); err != nil && !isNotExist(err) {
		return fmt.Errorf("deleting %s: %w", key, err)
	}
	return nil
}

// checkUpload returns nil if uploadID is a well-formed ID of an upload of
// key, an error wrapping ErrNotFound if there is no such upload, and
// another error otherwise.
func (f *Filesystem) checkUpload(key, uploadID string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if !validUploadID(uploadID) {
		return fmt.Errorf("%q is not an upload ID of this store (32 lower-case hex digits)", uploadID)
	}
	b, err := f.root.ReadFile(uploadsDir + "/" + uploadID + "/" + uploadKeyFile)
	if err != nil {
		if isNotExist(err) {
			return fmt.Errorf("upload %s of %s: %w", uploadID, key, ErrNotFound)
		}
		return fmt.Errorf("reading upload %s: %w", uploadID, err)
	}
	if string(b) != key {
		return fmt.Errorf("upload %s is for another object, not %s", uploadID, key)
	}
	return nil
}

// removeUpload removes a completed upload's directory. Failing to is not an
// error worth failing the completion for: the object is in place, and the
// leftover parts are only wasted space.
func (f *Filesystem) removeUpload(uploadID string) {
	_ = f.root.RemoveAll(uploadsDir + "/" + uploadID)
}

// writeFile writes data to dir/name through a synced temporary file and a
// rename, so the name holds the old or the new content, never a mix.
func (f *Filesystem) writeFile(dir, name string, data []byte) error {
	tmpName := dir + "/" + tmpPrefix + randomHex(8)
	tmp, err := f.root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = f.root.Rename(tmpName, dir+"/"+name)
	}
	if err != nil {
		_ = f.root.Remove(tmpName)
	}
	return err
}

// syncDir flushes a directory, so a rename into it survives a crash.
func (f *Filesystem) syncDir(dir string) error {
	d, err := f.root.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err // some filesystems cannot sync a directory; that is fine
	}
	return nil
}

func (f *Filesystem) exists(name string) bool {
	_, err := f.root.Lstat(name)
	return err == nil
}

func partName(n int) string { return strconv.Itoa(n) + ".part" }

// validUploadID reports whether id is 32 lower-case hex digits, the form
// CreateUpload makes.
func validUploadID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// isNotExist reports whether err means a name is missing, including a path
// running through a file where a directory was expected.
func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

var _ Store = (*Filesystem)(nil)
