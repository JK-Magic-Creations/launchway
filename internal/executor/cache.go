package executor

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
)

// Build caches on the runner. A cache entry is one tar archive compressed
// with zstd, made of the regular files its step's paths name, with paths
// relative to the workspace. Making and reading it use the same machinery
// as artifacts, so that every executor works the same way:
//
//   - Saving stages the files out of the workspace (Stage: through os.Root
//     on the runner's own disk, through the helper container for a pod),
//     then packs the staged copies, so that what is hashed and archived is
//     what was copied, whatever the step's processes do meanwhile.
//   - Restoring reads the archive into numbered files in a directory of the
//     runner's own, never under the names in the archive, and hands the
//     result to Place, which writes into the workspace through os.Root (or
//     as a tar stream into a pod) with the same checks as a fetched
//     artifact.
//
// The archive is the store's bytes, which a run of untrusted code may have
// written under a scope its own readers trust, so reading it is treated as
// hostile input: only regular files, clean relative names, only names the
// step's own paths cover, bounded counts, sizes and compression ratios,
// and no member is ever written under the name it claims.

// CacheFormat is mixed into every key, so that a change of how entries are
// made starts every cache over instead of restoring what a newer runner
// cannot read.
const CacheFormat = "launchway-cache-v1"

// ErrCacheArchive says an archive cannot be restored: it is damaged, or not
// what a run of this project would have made, or too large.
var ErrCacheArchive = errors.New("the cache entry cannot be restored")

func badArchive(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrCacheArchive, fmt.Sprintf(format, a...))
}

// CacheKey works out the key of a cache: the platform (operating system and
// architecture of the runner), the cache's name, the literal text, and the
// path and SHA-256 of each key file, in sorted order. The files must be
// those Stage returns for the key's globs.
func CacheKey(platform, name, text string, files []StagedFile) string {
	h := sha256.New()
	put := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	put(CacheFormat)
	put(platform)
	put(name)
	put(text)
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(a, b StagedFile) int { return strings.Compare(a.Path, b.Path) })
	put(strconv.Itoa(len(sorted)))
	for _, f := range sorted {
		put(f.Path)
		put(f.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CacheArchive is a packed entry, in a file.
type CacheArchive struct {
	Path   string
	Bytes  int64 // of the archive
	SHA256 string
	Files  int
	// Raw is the files' size together, before compression.
	Raw int64
}

// PackCache writes the staged files as a tar archive compressed with zstd
// into dst. Only regular files, with their permissions reduced to 0644 or
// 0755; directories are made again when the entry is restored.
func PackCache(files []StagedFile, dst string) (*CacheArchive, error) {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	bw := bufio.NewWriterSize(io.MultiWriter(f, h), 1<<20)
	enc, err := zstd.NewWriter(bw, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(2), zstd.WithWindowSize(8<<20))
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(enc)
	out := &CacheArchive{Path: dst, Files: len(files)}
	for _, sf := range files {
		if !validRelative(sf.Path) {
			enc.Close()
			return nil, fmt.Errorf("%q is not a path in the workspace", sf.Path)
		}
		mode := int64(0o644)
		if sf.Mode&0o111 != 0 {
			mode = 0o755
		}
		src, err := os.Open(sf.Local)
		if err != nil {
			enc.Close()
			return nil, err
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: sf.Path, Mode: mode, Size: sf.Size, Format: tar.FormatPAX}); err != nil {
			src.Close()
			enc.Close()
			return nil, err
		}
		n, err := io.Copy(tw, io.LimitReader(src, sf.Size))
		src.Close()
		if err == nil && n != sf.Size {
			err = fmt.Errorf("%s changed while it was packed", sf.Path)
		}
		if err != nil {
			enc.Close()
			return nil, err
		}
		out.Raw += n
	}
	if err := tw.Close(); err != nil {
		enc.Close()
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	if err := bw.Flush(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	st, err := os.Stat(dst)
	if err != nil {
		return nil, err
	}
	out.Bytes, out.SHA256 = st.Size(), hex.EncodeToString(h.Sum(nil))
	return out, nil
}

// UnpackLimits bound what an archive may expand to.
type UnpackLimits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
	// MaxRatio is how many times larger than its compressed bytes the
	// content may grow, once it is larger than ratioFloor.
	MaxRatio int
}

// ratioFloor is how much an archive may expand to before the ratio is
// looked at: small archives of repetitive text compress very well and are
// harmless.
const ratioFloor = 256 << 20

func (l UnpackLimits) withDefaults() UnpackLimits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = 1_000_000
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = 8 << 30
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = 32 << 30
	}
	if l.MaxRatio <= 0 {
		l.MaxRatio = 100
	}
	return l
}

// Unpacked is an archive's files, written under a directory of the runner's
// own, ready for Place.
type Unpacked struct {
	Files []PlacedFile
	Bytes int64
	// Skipped counts members the step's paths do not cover; they are left
	// out.
	Skipped int
}

// countingReader counts the compressed bytes read, so that the ratio can be
// checked.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// UnpackCache reads an archive made by PackCache and writes the regular
// files that the globs cover into dir, as numbered files; the names in the
// archive are kept in the result and nowhere else. It refuses, whole, an
// archive with a member that is not a regular file or directory, a name
// that is not a clean relative path, a name listed twice, or that is larger
// than the limits or compresses too well: such a thing was not made by
// PackCache.
func UnpackCache(ctx context.Context, src, dir string, globs []string, lim UnpackLimits) (*Unpacked, error) {
	lim = lim.withDefaults()
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cr := &countingReader{r: bufio.NewReaderSize(f, 1<<20)}
	dec, err := zstd.NewReader(cr, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxWindow(64<<20),
		zstd.WithDecoderMaxMemory(256<<20))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	g := &expansionGuard{lim: lim, read: &cr.n}
	tr := tar.NewReader(dec)
	out := &Unpacked{}
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, badArchive("it does not read as an archive made by Launchway: %v", err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return nil, badArchive("member %q is not a regular file (type %q); an entry holds only files", clipName(h.Name), string(h.Typeflag))
		}
		if msg := memberProblem(h.Name); msg != "" {
			return nil, badArchive("member %q: %s", clipName(h.Name), msg)
		}
		if seen[h.Name] {
			return nil, badArchive("member %q is listed twice", clipName(h.Name))
		}
		seen[h.Name] = true
		if len(seen) > lim.MaxFiles {
			return nil, badArchive("it holds more than %d files", lim.MaxFiles)
		}
		if h.Size < 0 || h.Size > lim.MaxFileBytes {
			return nil, badArchive("member %q is %s, more than the %s one file may be", clipName(h.Name), bytesText(h.Size), bytesText(lim.MaxFileBytes))
		}
		covered := false
		for _, gl := range globs {
			if matchArtifact(gl, h.Name) {
				covered = true
				break
			}
		}
		// Every member counts against the limits, restored or not: the bytes
		// are read either way.
		w := io.Writer(io.Discard)
		var local *os.File
		n := len(out.Files)
		if covered {
			local, err = root.OpenFile(strconv.Itoa(n), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, err
			}
			w = local
		}
		written, err := io.Copy(g.wrap(w), io.LimitReader(tr, h.Size+1))
		if local != nil {
			if cerr := local.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			if errors.Is(err, ErrCacheArchive) {
				return nil, err
			}
			return nil, badArchive("reading member %q: %v", clipName(h.Name), err)
		}
		if written != h.Size {
			return nil, badArchive("member %q is %d bytes, not the %d its header says", clipName(h.Name), written, h.Size)
		}
		if !covered {
			out.Skipped++
			continue
		}
		mode := uint32(0o644)
		if h.Mode&0o111 != 0 {
			mode = 0o755
		}
		out.Files = append(out.Files, PlacedFile{Path: h.Name, Local: filepath.Join(dir, strconv.Itoa(n)), Mode: mode})
		out.Bytes += h.Size
	}
}

// expansionGuard fails a copy that expands past the limits.
type expansionGuard struct {
	lim   UnpackLimits
	total int64
	read  *int64 // compressed bytes read so far
}

type guardedWriter struct {
	w io.Writer
	g *expansionGuard
}

func (g *expansionGuard) wrap(w io.Writer) io.Writer { return guardedWriter{w, g} }

func (gw guardedWriter) Write(p []byte) (int, error) {
	g := gw.g
	g.total += int64(len(p))
	switch {
	case g.total > g.lim.MaxTotalBytes:
		return 0, badArchive("it expands to more than %s", bytesText(g.lim.MaxTotalBytes))
	case g.total > ratioFloor && g.total > int64(g.lim.MaxRatio)*(*g.read+1<<20):
		return 0, badArchive("it expands to more than %d times its size, which no cache of files does", g.lim.MaxRatio)
	}
	return gw.w.Write(p)
}

func clipName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return '?'
		}
		return r
	}, s)
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// memberProblem says why a name in an archive is not the clean relative
// path PackCache writes, or "".
func memberProblem(name string) string {
	switch {
	case name == "" || len(name) > 1024:
		return "the name is empty or longer than 1024 bytes"
	case !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) || strings.ContainsAny(name, "\\"):
		return "the name is not text"
	case strings.HasPrefix(name, "/"):
		return "the name is absolute"
	case !validRelative(name):
		return "the name is not a clean path inside the workspace (. or .. parts, a doubled /, or a trailing /)"
	}
	return ""
}

// WorkspaceStager is implemented by executors that may keep the workspace
// somewhere that does not exist yet: a pod that is started at a run's first
// step. Until then the runner's own checkout is the workspace.
type WorkspaceStager interface {
	// WorkspaceInPod reports whether the workspace of the step's run is in
	// a pod this runner has set up.
	WorkspaceInPod(s Spec) bool
}

// StageWorkspace is Stage for a step that has not run yet: if the
// executor's workspace is not set up, the checkout on the runner's disk is
// read instead.
func StageWorkspace(ctx context.Context, ex Executor, s Spec, globs []string, lim StageLimits, dir string) (*Staged, error) {
	if ws, ok := ex.(WorkspaceStager); ok && !ws.WorkspaceInPod(s) {
		lim = lim.withDefaults()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		return stageLocal(ctx, s.Workspace, globs, lim, dir)
	}
	return Stage(ctx, ex, s, globs, lim, dir)
}
