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
	// ExportDir copies the directory rel of the run's workspace into dst.
	// It reports false, and copies nothing, when the run has no pod yet
	// (the step is the run's first): the runner's checkout is then the
	// workspace.
	ExportDir(ctx context.Context, s Spec, rel, dst string, lim ExportLimits) (bool, error)
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

// ExportDir implements DirExporter.
func (k *Kubernetes) ExportDir(ctx context.Context, s Spec, rel, dst string, lim ExportLimits) (bool, error) {
	if s.RunID == "" {
		return false, errors.New("kubernetes: the step has no run")
	}
	pod := k.podFor(s.RunID)
	if ok, err := k.podExists(ctx, pod); err != nil || !ok {
		return false, err
	}
	if !validRelative(rel) && rel != "." {
		return true, fmt.Errorf("%q is not a path in the workspace", rel)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return true, err
	}
	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		err := k.exec(ctx, pod, helperName, []string{"sh", "-c",
			`cd /workspace && [ -d "$1" ] || { echo "$1 is not a directory in the workspace" >&2; exit 3; }; cd "$1" && exec tar -c -f - .`,
			"sh", rel}, nil, pw, &stderr)
		pw.CloseWithError(err)
		done <- err
	}()
	rerr := extractTar(pr, dst, lim)
	pr.CloseWithError(errors.New("stopped reading"))
	err := <-done
	if rerr != nil {
		return true, rerr
	}
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return true, errors.New(oneLine(msg))
		}
		return true, infra("copying %s out of pod %s: %v", rel, pod, err)
	}
	return true, nil
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
