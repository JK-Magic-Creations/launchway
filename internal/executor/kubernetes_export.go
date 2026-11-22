package executor

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Image steps on Kubernetes: the workspace is in the run's pod, and the
// builder is not, so the runner copies the build context out of the pod
// (through the helper container, as a tar stream) into a directory of its
// own and sends it from there. Only directories, regular files and
// symbolic links (as links, never followed) are taken, within limits.

// DirExporter is implemented by executors whose workspace is not a
// directory on the runner's own disk.
type DirExporter interface {
	// ExportContext copies the build context c of the run's workspace into
	// dst: what its ignore file keeps (see buildcontext.go), checked against
	// lim after the ignore file is applied. It reports false, and copies
	// nothing, when the run has no pod yet (the step is the run's first):
	// the runner's checkout is then the workspace.
	ExportContext(ctx context.Context, s Spec, c ContextSpec, dst string, lim ExportLimits) (bool, error)
	// ExportFile copies one regular file of the workspace.
	ExportFile(ctx context.Context, s Spec, rel, dst string, max int64) (bool, error)
}

// ExportLimits bound what is copied out.
type ExportLimits struct {
	MaxBytes int64 // all files together
	MaxFiles int
}

// ErrTooLarge says a directory holds more than the limits allow.
var ErrTooLarge = errors.New("too large")

// maxListing bounds the listing of a context's files that is read from the pod.
const maxListing = 96 << 20

// ExportContext implements DirExporter. The ignore file is read first; the
// pod's files are listed with their sizes (a find, which does not follow
// symbolic links), the ignore rules and the limits are applied here, and
// only what is kept is read out, as a tar stream of an explicit list of
// names. Nothing that the ignore file leaves out leaves the pod.
func (k *Kubernetes) ExportContext(ctx context.Context, s Spec, c ContextSpec, dst string, lim ExportLimits) (bool, error) {
	if s.RunID == "" {
		return false, errors.New("kubernetes: the step has no run")
	}
	pod := k.podFor(s.RunID)
	if ok, err := k.podExists(ctx, pod); err != nil || !ok {
		return false, err
	}
	if !validRelative(c.Dir) && c.Dir != "." {
		return true, fmt.Errorf("%q is not a path in the workspace", c.Dir)
	}
	if !validRelative(c.Dockerfile) {
		return true, fmt.Errorf("%q is not a path in the workspace", c.Dockerfile)
	}
	tmp, err := os.MkdirTemp("", "launchway-ignore-")
	if err != nil {
		return true, err
	}
	defer os.RemoveAll(tmp)
	filter, err := ContextIgnore(c, func(rel string) ([]byte, bool, error) {
		local := filepath.Join(tmp, "ignore")
		if _, err := k.copyOut(ctx, pod, rel, local, 1<<20); err != nil {
			if errors.Is(err, ErrNotKept) {
				return nil, false, nil // no such file, or not a file
			}
			return nil, false, err
		}
		b, err := os.ReadFile(local)
		_ = os.Remove(local)
		return b, err == nil, err
	})
	if err != nil {
		return true, err
	}
	plan, err := k.planContext(ctx, pod, c, filter)
	if err != nil {
		return true, err
	}
	if err := filter.Finish(plan, c.Dir, lim, func(paths []string) int64 { return k.sizeOfDirs(ctx, pod, c.Dir, paths) }); err != nil {
		return true, err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return true, err
	}
	if len(plan.Keep) == 0 {
		return true, nil
	}
	names := make([]string, len(plan.Keep))
	for i, e := range plan.Keep {
		names[i] = e.Path
	}
	root := path.Join("/workspace", c.Dir)
	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		err := k.exec(ctx, pod, helperName, []string{"sh", "-c", exportScript(root)}, strings.NewReader(copyOutManyInput(names)), pw, &stderr)
		pw.CloseWithError(err)
		done <- err
	}()
	rerr := extractTar(pr, dst, lim)
	if rerr == nil {
		_, _ = io.Copy(io.Discard, pr) // the end of the archive is padding; let tar finish writing it
	}
	pr.CloseWithError(errors.New("stopped reading"))
	err = <-done
	if rerr != nil {
		return true, rerr
	}
	if err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return true, errors.New(oneLine(msg))
		}
		return true, infra("copying %s out of pod %s: %v", c.Dir, pod, err)
	}
	return true, nil
}

// exportScript is what runs in the pod to read the kept files: copyOutManyScript,
// after a check that the context itself is inside the workspace once links
// are resolved (a context that is a link to somewhere else is refused, as it
// is where the context is on the runner's disk).
func exportScript(root string) string {
	q := shellQuote(root)
	return `case "$(readlink -f ` + q + `)" in /workspace|/workspace/*) ;; *) echo "the build context leads outside the workspace (through a symbolic link); refused" >&2; exit 3;; esac; ` +
		copyOutManyScript(root)
}

// listScript lists what is under the starting points given as arguments, in
// the directory $1: one record a line, "<kind>|<size>|<path>", the kind being
// what stat calls the file ("directory", "regular file", "symbolic link").
// find does not follow links. busybox find has no -printf, so stat prints
// the records. The arguments after the starting points are find's own
// options (-mindepth and -maxdepth).
const listScript = `case "$(readlink -f "$1")" in /workspace|/workspace/*) ;; *) echo "the build context leads outside the workspace (through a symbolic link); refused" >&2; exit 3;; esac; cd "$1" || exit 1; shift; find "$@" \( -type d -o -type f -o -type l \) -exec stat -c '%F|%s|%n' {} +`

// planContext lists the context in the pod and applies the ignore rules.
// The top level is listed first; a directory is looked into only if the
// rules keep it or something in it could be re-included, so that a
// directory that is left out, like node_modules, is never walked.
func (k *Kubernetes) planContext(ctx context.Context, pod string, c ContextSpec, f *ContextFilter) (*ContextPlan, error) {
	plan := f.NewPlan()
	root := path.Join("/workspace", c.Dir)
	top, err := k.listContext(ctx, pod, root, []string{".", "-mindepth", "1", "-maxdepth", "1"})
	if err != nil {
		return nil, err
	}
	var deeper []string
	for _, e := range top {
		if f.Add(plan, e) && e.Dir {
			deeper = append(deeper, e.Path)
		}
	}
	// What the rules look into is listed with everything below it, a few
	// directories to a walk. Inside, the rules decide each entry again.
	for len(deeper) > 0 {
		n := min(len(deeper), 200)
		args := append(append([]string{}, deeper[:n]...), "-mindepth", "1")
		deeper = deeper[n:]
		entries, err := k.listContext(ctx, pod, root, args)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			f.Add(plan, e)
		}
	}
	return plan, nil
}

// listContext runs find in the pod (arguments: starting points, then
// options) and reads its records. A name with a line break in it cannot be
// listed safely and is left out of the context.
func (k *Kubernetes) listContext(ctx context.Context, pod, root string, findArgs []string) ([]ContextEntry, error) {
	args := append([]string{"sh", "-c", listScript, "sh", root}, findArgs...)
	var list, stderr bytes.Buffer
	lw := &limitedWriter{w: &list, n: maxListing}
	err := k.exec(ctx, pod, helperName, args, nil, lw, &stderr)
	if lw.full {
		return nil, fmt.Errorf("%w: the build context lists more than %s of file names; add what the build does not read to .dockerignore", ErrTooLarge, bytesText(maxListing))
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if msg := strings.TrimSpace(stderr.String()); strings.HasSuffix(msg, "refused") {
			return nil, errors.New(msg)
		}
		return nil, infra("listing the build context in pod %s: %v %s", pod, err, strings.TrimSpace(stderr.String()))
	}
	return parseContextList(list.String()), nil
}

// parseContextList reads the records of listScript, leaving out any line that
// is not one, and any path that is not inside the context.
func parseContextList(out string) []ContextEntry {
	var es []ContextEntry
	for _, line := range strings.Split(out, "\n") {
		kind, rest, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		size, name, ok := strings.Cut(rest, "|")
		if !ok {
			continue
		}
		name = strings.TrimPrefix(name, "./")
		if !validRelative(name) {
			continue
		}
		e := ContextEntry{Path: name}
		switch {
		case kind == "directory":
			e.Dir = true
		case kind == "symbolic link":
			e.Link = true
		case strings.HasPrefix(kind, "regular"):
			n, err := strconv.ParseInt(size, 10, 64)
			if err != nil {
				continue
			}
			e.Size = n
		default:
			continue // devices, pipes and sockets are not part of a context
		}
		es = append(es, e)
	}
	return es
}

// sizeOfDirs says how many bytes the directories take, for an error message.
func (k *Kubernetes) sizeOfDirs(ctx context.Context, pod, dir string, paths []string) int64 {
	var out bytes.Buffer
	args := append([]string{"sh", "-c", `cd "$1" && shift && du -sk -- "$@" 2>/dev/null`, "sh", path.Join("/workspace", dir)}, paths...)
	_ = k.exec(ctx, pod, helperName, args, nil, &out, io.Discard)
	var n int64
	for _, line := range strings.Split(out.String(), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 {
			if kb, err := strconv.ParseInt(f[0], 10, 64); err == nil {
				n += kb << 10
			}
		}
	}
	return n
}

// ExportFile implements DirExporter.
func (k *Kubernetes) ExportFile(ctx context.Context, s Spec, rel, dst string, max int64) (bool, error) {
	pod := k.podFor(s.RunID)
	if ok, err := k.podExists(ctx, pod); err != nil || !ok {
		return false, err
	}
	if !validRelative(rel) {
		return true, fmt.Errorf("%q is not a path in the workspace", rel)
	}
	_, err := k.copyOut(ctx, pod, rel, dst, max)
	return true, err
}

func (k *Kubernetes) podExists(ctx context.Context, pod string) (bool, error) {
	_, err := k.client.CoreV1().Pods(k.o.Namespace).Get(ctx, pod, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, infra("looking for pod %s: %v", pod, err)
	}
	return true, nil
}

// extractTar writes a tar stream's directories, regular files and symbolic
// links under dst, refusing any name that would leave it.
func extractTar(r io.Reader, dst string, lim ExportLimits) error {
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(r)
	var total int64
	files := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the workspace: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." {
			continue
		}
		if !validRelative(name) {
			return fmt.Errorf("the workspace has an entry %q outside it", h.Name)
		}
		files++
		if lim.MaxFiles > 0 && files > lim.MaxFiles {
			return fmt.Errorf("%w: more than %d files", ErrTooLarge, lim.MaxFiles)
		}
		local := filepath.FromSlash(name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(local, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if lim.MaxBytes > 0 && total > lim.MaxBytes {
				return fmt.Errorf("%w: more than %s", ErrTooLarge, bytesText(lim.MaxBytes))
			}
			if dir := filepath.Dir(local); dir != "." {
				if err := root.MkdirAll(dir, 0o755); err != nil {
					return err
				}
			}
			mode := fs.FileMode(0o644)
			if h.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			f, err := root.OpenFile(local, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.CopyN(f, tr, h.Size)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if dir := filepath.Dir(local); dir != "." {
				if err := root.MkdirAll(dir, 0o755); err != nil {
					return err
				}
			}
			// The link is kept as a link; nothing follows it here, and the
			// builder sends links as links.
			if err := root.Symlink(h.Linkname, local); err != nil {
				return err
			}
		case tar.TypeLink:
			// A second name for a file already written: a copy of it.
			target := path.Clean(strings.TrimPrefix(h.Linkname, "./"))
			if !validRelative(target) {
				return fmt.Errorf("the workspace has a link %q to %q outside it", h.Name, h.Linkname)
			}
			src, err := root.Open(filepath.FromSlash(target))
			if err != nil {
				return err
			}
			f, err := root.OpenFile(local, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err == nil {
				_, err = io.Copy(f, src)
				if cerr := f.Close(); err == nil {
					err = cerr
				}
			}
			src.Close()
			if err != nil {
				return err
			}
		default:
			// Devices and pipes are not part of a build context.
		}
	}
}
