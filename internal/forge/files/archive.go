package files

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// ArchiveFormats are the formats a snapshot of a ref can be had in.
var ArchiveFormats = []string{"tar.gz", "zip"}

// ErrTooLarge is a snapshot that is bigger than the installation allows.
var ErrTooLarge = errors.New("too large")

// ArchiveInfo is what the writer of a snapshot is told before the first byte: the commit the ref names and how many
// bytes the files add up to (before compression).
type ArchiveInfo struct {
	Commit string
	Bytes  int64
	Files  int
}

// Archive streams the files of a ref as a tar.gz or a zip archive, every path under prefix/. It never holds the
// archive in memory or on disk. The size of the files is read from the tree first and the snapshot is refused with
// ErrTooLarge when it is more than max bytes (0: no limit), so the refusal comes before any byte is sent; open is
// called with what was found and returns where the archive goes (and may refuse). The files' own content is what
// git archive reads, so .gitattributes export-ignore and export-subst are honoured as they are by every Git host.
func (s *Service) Archive(ctx context.Context, repoID, ref, format, prefix string, max int64, open func(ArchiveInfo) (io.Writer, error)) error {
	if format != "tar.gz" && format != "zip" {
		return invalid("%q is not an archive format: use tar.gz or zip", format)
	}
	if prefix == "" || strings.ContainsAny(prefix, "\x00\n\r") || strings.Contains(prefix, "..") || strings.HasPrefix(prefix, "/") {
		return invalid("%q is not an archive folder name", prefix)
	}
	return s.Git.WithCache(ctx, repoID, func(dir string) error {
		g := runner{dir: dir, limits: s.Git.Limits()}
		commit, err := g.resolve(ctx, ref)
		if err != nil {
			return err
		}
		info := ArchiveInfo{Commit: commit}
		// Sum the sizes of the blobs, reading the listing as it comes: a huge tree costs time, not memory.
		pr, pw := io.Pipe()
		done := make(chan error, 1)
		go func() {
			sc := bufio.NewScanner(pr)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			sc.Split(splitNUL)
			var err error
			for sc.Scan() {
				// <mode> SP <type> SP <sha> SP* <size> TAB <path>
				head, _, _ := strings.Cut(sc.Text(), "\t")
				f := strings.Fields(head)
				if len(f) == 4 && f[1] == "blob" {
					n, _ := strconv.ParseInt(f[3], 10, 64)
					info.Bytes += n
					info.Files++
					if max > 0 && info.Bytes > max {
						err = fmt.Errorf("%w: %s is more than %d MiB of files, which is the most a snapshot may be; an administrator can raise the limit", ErrTooLarge, ref, max>>20)
						break
					}
				}
			}
			if err == nil {
				err = sc.Err()
			}
			io.Copy(io.Discard, pr) //nolint:errcheck // drain so git can finish
			done <- err
		}()
		_, lerr := gitexec.Run(ctx, gitexec.Spec{GitDir: dir, Args: []string{"ls-tree", "-r", "-l", "-z", "--end-of-options", commit}, Timeout: 5 * time.Minute, Limits: g.limits, Stdout: pw})
		pw.Close()
		if err := <-done; err != nil {
			return err
		}
		if lerr != nil {
			return lerr
		}
		w, err := open(info)
		if err != nil {
			return err
		}
		var out io.Writer = w
		var gz *gzip.Writer
		args := []string{"archive", "--format=zip", "--prefix=" + strings.TrimSuffix(prefix, "/") + "/", commit}
		if format == "tar.gz" {
			gz = gzip.NewWriter(w)
			out = gz
			args = []string{"archive", "--format=tar", "--prefix=" + strings.TrimSuffix(prefix, "/") + "/", commit}
		}
		// A limit on what is written guards against a tree that grew between the listing and the archive.
		if max > 0 {
			out = &limitedWriter{w: out, left: max + max/4 + (1 << 20)}
		}
		_, err = gitexec.Run(ctx, gitexec.Spec{GitDir: dir, Args: args, Timeout: 30 * time.Minute, IdleTimeout: 2 * time.Minute, Limits: g.limits, Stdout: out})
		if gz != nil {
			if cerr := gz.Close(); err == nil {
				err = cerr
			}
		}
		return err
	})
}

type limitedWriter struct {
	w    io.Writer
	left int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.left {
		return 0, ErrTooLarge
	}
	l.left -= int64(len(p))
	return l.w.Write(p)
}

func splitNUL(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// readArchive is for tests: the names and contents of an archive.
func readArchive(format string, b []byte) (map[string]string, error) {
	out := map[string]string{}
	if format == "zip" {
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if strings.HasSuffix(f.Name, "/") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			c, _ := io.ReadAll(rc)
			rc.Close()
			out[f.Name] = string(c)
		}
		return out, nil
	}
	gr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg {
			c, _ := io.ReadAll(tr)
			out[h.Name] = string(c)
		}
	}
}
