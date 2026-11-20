package executor

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
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Artifacts. After a step, the runner copies the files of each artifact it
// keeps out of the workspace into a staging directory of its own, hashing
// them as it goes (Stage), and uploads the copies: what is hashed is what is
// sent, even if something in the workspace changes meanwhile. Before a
// step, it puts the files of the artifacts the step fetches into the
// workspace (Place).
//
// Paths are globs as for reports (see Collect), with one addition: a glob
// that matches a directory keeps every file below it, so paths: [dist]
// keeps the whole dist directory. Only regular files are kept; symbolic
// links are not followed, so a step cannot make the runner read anything
// outside its workspace. An artifact is kept whole or not at all: a file
// over the size limit, or too many files, means none is kept.

// StageLimits bound what one artifact may hold.
type StageLimits struct {
	MaxFiles     int   // default 10000
	MaxFileBytes int64 // default 1 GiB
	MaxBytes     int64 // all files together; default 4 GiB
	// NoGit leaves out whatever is in a .git directory, however the globs
	// came to match it (build caches keep nothing of the repository).
	NoGit bool
}

func (l StageLimits) withDefaults() StageLimits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = 10000
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = 1 << 30
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = 4 << 30
	}
	return l
}

// StagedFile is a file of an artifact, copied out of the workspace.
type StagedFile struct {
	Path   string // relative to the workspace, with /
	Local  string // the copy, on the runner
	Size   int64
	SHA256 string // hex
	Mode   uint32 // 0644, or 0755 when someone may run it
}

// Staged is an artifact's files, copied out.
type Staged struct {
	Files []StagedFile
	// Notes say, in plain words, what was left out and why (a glob that
	// matched nothing, a symbolic link).
	Notes []string
}

// ErrNotKept means an artifact cannot be kept; the message says why.
var ErrNotKept = errors.New("not kept")

func notKept(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNotKept, fmt.Sprintf(format, a...))
}

// FileStager is implemented by executors whose workspace is not a directory
// on the runner's own disk.
type FileStager interface {
	StageFiles(ctx context.Context, s Spec, globs []string, lim StageLimits, dir string) (*Staged, error)
}

// Stage copies the files of a step's workspace that the globs name into dir,
// a directory of the runner's. An error wrapping ErrNotKept says why the
// artifact cannot be kept; another error is a failure to look at all.
func Stage(ctx context.Context, ex Executor, s Spec, globs []string, lim StageLimits, dir string) (*Staged, error) {
	lim = lim.withDefaults()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if fs, ok := ex.(FileStager); ok {
		return fs.StageFiles(ctx, s, globs, lim, dir)
	}
	return stageLocal(ctx, s.Workspace, globs, lim, dir)
}

// matchArtifact reports whether a file, relative to the workspace, is named
// by a glob: the glob matches it or one of the directories it is in.
func matchArtifact(glob, name string) bool {
	if matchGlob(glob, name) {
		return true
	}
	for d := path.Dir(name); d != "." && d != "/"; d = path.Dir(d) {
		if matchGlob(glob, d) {
			return true
		}
	}
	return false
}

// artifactPicker gathers the files the globs name.
type artifactPicker struct {
	globs []string
	hits  map[string]int
	names map[string]bool
	notes []string
	// sawGit is whether the note about .git files was given.
	sawGit bool
}

func newPicker(globs []string) *artifactPicker {
	return &artifactPicker{globs: globs, hits: map[string]int{}, names: map[string]bool{}}
}

// offer considers a file found while searching; link says it is a
// symbolic link or something else that is not a regular file.
func (p *artifactPicker) offer(name string, regular bool) {
	for _, g := range p.globs {
		if matchArtifact(g, name) {
			p.hits[g]++
			if regular {
				p.names[name] = true
			} else if len(p.notes) < 20 {
				p.notes = append(p.notes, name+" is not a regular file (a symbolic link, say); it is not kept")
			}
			return
		}
	}
}

func (p *artifactPicker) chosen(lim StageLimits) ([]string, error) {
	for _, g := range p.globs {
		if p.hits[g] == 0 {
			p.notes = append(p.notes, g+" matched no files")
		}
	}
	names := make([]string, 0, len(p.names))
	for n := range p.names {
		if lim.NoGit && inGitDir(n) {
			if !p.sawGit {
				p.sawGit = true
				p.notes = append(p.notes, "files in a .git directory are never kept: they hold the repository's history and often credentials")
			}
			continue
		}
		names = append(names, n)
	}
	slices.Sort(names)
	if len(names) == 0 {
		return nil, notKept("its paths matched no files")
	}
	if len(names) > lim.MaxFiles {
		return nil, notKept("its paths matched %d files; an artifact holds at most %d", len(names), lim.MaxFiles)
	}
	return names, nil
}

// stageLocal stages from a workspace directory on this machine, through
// os.Root, so nothing outside it is ever opened.
func stageLocal(ctx context.Context, workspace string, globs []string, lim StageLimits, dir string) (*Staged, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("opening the workspace: %w", err)
	}
	defer root.Close()
	p := newPicker(globs)
	listed := 0
	walked := map[string]bool{}
	errStop := errors.New("stop")
	for _, g := range globs {
		base, _ := globBase(g)
		if walked[base] {
			continue
		}
		walked[base] = true
		info, err := root.Lstat(base)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			p.offer(base, info.Mode().IsRegular())
			continue
		}
		err = fs.WalkDir(root.FS(), base, func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if listed++; listed > maxListed {
				return errStop
			}
			if name == base {
				return nil
			}
			if d.IsDir() {
				if slices.Contains(skippedDirs, d.Name()) && !explicitlyNamed(globs, name) && !namedExactly(globs, name) {
					return fs.SkipDir
				}
				return nil
			}
			p.offer(name, d.Type().IsRegular())
			return nil
		})
		switch {
		case errors.Is(err, errStop):
			return nil, notKept("the workspace has more than %d files and directories where its paths look", maxListed)
		case err != nil:
			return nil, err
		}
	}
	names, err := p.chosen(lim)
	if err != nil {
		return &Staged{Notes: p.notes}, err
	}
	out := &Staged{Notes: p.notes}
	var total int64
	for i, name := range names {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		local := filepath.Join(dir, strconv.Itoa(i))
		f, err := copyLocal(root, name, local, lim)
		if err != nil {
			return out, err
		}
		total += f.Size
		if total > lim.MaxBytes {
			return out, notKept("its files come to more than %s", bytesText(lim.MaxBytes))
		}
		out.Files = append(out.Files, f)
	}
	return out, nil
}

// namedExactly reports whether a glob is exactly the directory.
func namedExactly(globs []string, dir string) bool { return slices.Contains(globs, dir) }

// copyLocal copies a regular file of the workspace, hashing it.
func copyLocal(root *os.Root, name, local string, lim StageLimits) (StagedFile, error) {
	src, err := root.OpenFile(name, os.O_RDONLY|openNonBlock, 0)
	if err != nil {
		return StagedFile{}, notKept("%s could not be read: %v", name, unwrapPath(err))
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return StagedFile{}, err
	}
	if !info.Mode().IsRegular() {
		return StagedFile{}, notKept("%s is not a regular file", name)
	}
	mode := uint32(0o644)
	if info.Mode()&0o111 != 0 {
		mode = 0o755
	}
	f, err := copyHashed(src, name, local, lim.MaxFileBytes)
	f.Mode = mode
	return f, err
}

// copyHashed copies r into local, at most max bytes, hashing what it copies.
func copyHashed(r io.Reader, name, local string, max int64) (StagedFile, error) {
	dst, err := os.OpenFile(local, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return StagedFile{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(r, max+1))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return StagedFile{}, fmt.Errorf("copying %s: %w", name, err)
	}
	if n > max {
		return StagedFile{}, notKept("%s is larger than %s, the most one file may be", name, bytesText(max))
	}
	return StagedFile{Path: name, Local: local, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// PlacedFile is a file to put in a step's workspace.
type PlacedFile struct {
	Path  string // relative to the workspace, with /
	Local string // where it is on the runner
	Mode  uint32
}

// FilePlacer is implemented by executors whose workspace is not a
// directory on the runner's own disk.
type FilePlacer interface {
	PlaceFiles(ctx context.Context, s Spec, files []PlacedFile, out Sink) error
}

// Place puts files in a step's workspace, replacing files of the same
// name. On the runner's own disk it works through os.Root, so a symbolic
// link an earlier step left cannot send a file outside the workspace.
func Place(ctx context.Context, ex Executor, s Spec, files []PlacedFile, out Sink) error {
	for _, f := range files {
		if !validRelative(f.Path) {
			return fmt.Errorf("%q is not a path inside the workspace", f.Path)
		}
	}
	if fp, ok := ex.(FilePlacer); ok {
		return fp.PlaceFiles(ctx, s, files, out)
	}
	root, err := os.OpenRoot(s.Workspace)
	if err != nil {
		return fmt.Errorf("opening the workspace: %w", err)
	}
	defer root.Close()
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d := path.Dir(f.Path); d != "." {
			if err := root.MkdirAll(d, 0o777); err != nil {
				return fmt.Errorf("making the directory for %s: %w", f.Path, unwrapPath(err))
			}
		}
		if err := root.Remove(f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("replacing %s: %w", f.Path, unwrapPath(err))
		}
		if err := placeOne(root, f); err != nil {
			return err
		}
	}
	return nil
}

func placeOne(root *os.Root, f PlacedFile) error {
	src, err := os.Open(f.Local)
	if err != nil {
		return err
	}
	defer src.Close()
	mode := os.FileMode(0o644)
	if f.Mode&0o111 != 0 {
		mode = 0o755
	}
	dst, err := root.OpenFile(f.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("writing %s: %w", f.Path, unwrapPath(err))
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// The umask may have taken bits away; containers may run as
		// another user, so the file is readable by all.
		err = root.Chmod(f.Path, mode)
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", f.Path, unwrapPath(err))
	}
	return nil
}

// validRelative reports whether p is a clean relative path that stays
// inside the directory it is relative to.
func validRelative(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != "." && p != ".." &&
		!strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\x00\\")
}
