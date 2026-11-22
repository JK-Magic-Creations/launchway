package executor

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// fileFP is what a workspace entry was when a step's pod was filled: enough to
// tell, when the pod hands its files back, whether the step changed it.
type fileFP struct {
	dir     bool
	size    int64
	mtime   int64 // seconds
	exec    bool
	link    string
	settled bool // its time was old enough that a change within the same second is not a worry
}

// fingerprint is what an entry of a tar stream is. An entry modified within
// the last two seconds before settled cannot be told from a change made in
// the same second, so it is never equal to anything.
func fingerprint(h *tar.Header, settled time.Time) fileFP {
	f := fileFP{dir: h.Typeflag == tar.TypeDir, size: h.Size, mtime: h.ModTime.Unix(), exec: h.FileInfo().Mode()&0o111 != 0, link: h.Linkname}
	if f.dir {
		f.size, f.mtime, f.exec = 0, 0, false // a directory's time changes with what is in it
	}
	f.settled = f.dir || h.ModTime.Before(settled)
	if h.Typeflag == tar.TypeSymlink {
		f.settled = true
	}
	return f
}

// same reports whether the entry a pod hands back is the one it was filled with.
func (b fileFP) same(h *tar.Header) bool {
	if !b.settled {
		return false
	}
	now := fingerprint(h, time.Time{})
	now.settled = b.settled
	return now == b
}

// extractWorkspace writes a tar stream of a workspace over the directory dst,
// as a step's pod hands its files back (and as the checkout is copied in the
// first time): directories, regular files (with their modes' execute bit and
// their times, which a cache and a build tool compare) and symbolic links,
// as links. Nothing is written outside dst, whatever the names.
//
// base, if not nil, is what the pod was filled with: an entry that is as it
// was is left alone, so that a file another step of the run changed or removed
// meanwhile is not put back as it was. Every path that arrives, as it was or
// changed, is noted in seen, if it is not nil.
func extractWorkspace(r io.Reader, dst string, lim ExportLimits, base map[string]fileFP, seen map[string]bool) error {
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer root.Close()
	x := &extractor{root: root, tr: tar.NewReader(r), lim: lim, base: base, seen: seen}
	for {
		h, err := x.tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the workspace: %w", err)
		}
		if err := x.entry(h); err != nil {
			return err
		}
	}
}

// extractor writes the entries of one tar stream below a root.
type extractor struct {
	root  *os.Root
	tr    *tar.Reader
	lim   ExportLimits
	base  map[string]fileFP
	seen  map[string]bool
	total int64
	files int
}

func (x *extractor) entry(h *tar.Header) error {
	name := path.Clean(strings.TrimPrefix(h.Name, "./"))
	if name == "." {
		return nil
	}
	if !validRelative(name) {
		return fmt.Errorf("the workspace has an entry %q outside it", h.Name)
	}
	if x.files++; x.lim.MaxFiles > 0 && x.files > x.lim.MaxFiles {
		return fmt.Errorf("%w: more than %d files", ErrTooLarge, x.lim.MaxFiles)
	}
	if b, ok := x.base[name]; ok && b.same(h) {
		x.note(name)
		return nil
	}
	local := filepath.FromSlash(name)
	existing, lerr := x.root.Lstat(local)
	var err error
	switch h.Typeflag {
	case tar.TypeDir:
		err = x.dir(local, existing, lerr == nil)
	case tar.TypeReg:
		err = x.file(h, local, lerr == nil)
	case tar.TypeSymlink:
		err = x.symlink(h, local, existing, lerr == nil)
	case tar.TypeLink:
		err = x.hardlink(h, local, lerr == nil)
	default:
		return nil // devices, pipes and sockets are not part of a workspace
	}
	if err == nil {
		x.note(name)
	}
	return err
}

func (x *extractor) note(name string) {
	if x.seen != nil {
		x.seen[name] = true
	}
}

// parent makes the directory the entry goes in.
func (x *extractor) parent(local string) error {
	if dir := filepath.Dir(local); dir != "." {
		return x.root.MkdirAll(dir, 0o755)
	}
	return nil
}

func (x *extractor) dir(local string, existing fs.FileInfo, have bool) error {
	if have && !existing.IsDir() {
		if err := x.root.RemoveAll(local); err != nil {
			return err
		}
	}
	return x.root.MkdirAll(local, 0o755)
}

func (x *extractor) file(h *tar.Header, local string, have bool) error {
	if x.total += h.Size; x.lim.MaxBytes > 0 && x.total > x.lim.MaxBytes {
		return fmt.Errorf("%w: more than %s", ErrTooLarge, bytesText(x.lim.MaxBytes))
	}
	mode := fs.FileMode(0o644)
	if h.FileInfo().Mode()&0o111 != 0 {
		mode = 0o755
	}
	if err := x.parent(local); err != nil {
		return err
	}
	if have {
		if err := x.root.RemoveAll(local); err != nil {
			return err
		}
	}
	f, err := x.root.OpenFile(local, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.CopyN(f, x.tr, h.Size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := x.root.Chmod(local, mode); err != nil { // the umask may have taken bits away
		return err
	}
	if h.ModTime.IsZero() {
		return nil
	}
	return x.root.Chtimes(local, h.ModTime, h.ModTime)
}

func (x *extractor) symlink(h *tar.Header, local string, existing fs.FileInfo, have bool) error {
	if have {
		if existing.Mode()&fs.ModeSymlink != 0 {
			if cur, err := x.root.Readlink(local); err == nil && cur == h.Linkname {
				return nil
			}
		}
		if err := x.root.RemoveAll(local); err != nil {
			return err
		}
	}
	if err := x.parent(local); err != nil {
		return err
	}
	// The link is kept as a link; nothing here follows it.
	return x.root.Symlink(h.Linkname, local)
}

// hardlink writes a second name for a file already written: a copy of it.
func (x *extractor) hardlink(h *tar.Header, local string, have bool) error {
	target := path.Clean(strings.TrimPrefix(h.Linkname, "./"))
	if !validRelative(target) {
		return fmt.Errorf("the workspace has a link %q to %q outside it", h.Name, h.Linkname)
	}
	src, err := x.root.Open(filepath.FromSlash(target))
	if err != nil {
		return err
	}
	defer src.Close()
	if err := x.parent(local); err != nil {
		return err
	}
	if have {
		if err := x.root.RemoveAll(local); err != nil {
			return err
		}
	}
	f, err := x.root.OpenFile(local, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
