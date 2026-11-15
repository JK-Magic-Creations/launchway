package executor

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Artifacts on Kubernetes: the workspace is in the run's pod, so files are
// listed and read through the helper container, and fetched files are
// written into it as a tar stream.

// podFor returns the name of the run's pod as this runner knows it.
func (k *Kubernetes) podFor(runID string) string {
	pod := podName(runID, k.owner)
	k.mu.Lock()
	if rp := k.pods[runID]; rp != nil {
		pod = rp.name
	}
	k.mu.Unlock()
	return pod
}

// StageFiles copies an artifact's files out of the run's pod.
func (k *Kubernetes) StageFiles(ctx context.Context, s Spec, globs []string, lim StageLimits, dir string) (*Staged, error) {
	pod := k.podFor(s.RunID)
	p := newPicker(globs)
	searched := map[string]bool{}
	for _, g := range globs {
		base, _ := globBase(g)
		if searched[base] {
			continue
		}
		searched[base] = true
		// Every entry below base, each printed as <type> <path> and NUL;
		// find does not follow symbolic links. ** and directories never
		// enter .git or node_modules unless a glob names them.
		var prune []string
		for _, d := range skippedDirs {
			if !explicitlyNamed(globs, path.Join(base, d)) && base != d && !strings.HasPrefix(base, d+"/") {
				prune = append(prune, d)
			}
		}
		args := []string{base}
		if len(prune) > 0 {
			args = append(args, "(")
			for i, d := range prune {
				if i > 0 {
					args = append(args, "-o")
				}
				args = append(args, "-name", d)
			}
			args = append(args, ")", "-prune", "-o")
		}
		// busybox find has no -printf: list regular files and other
		// entries in two passes, marking each.
		script := `cd /workspace || exit 1; [ -e "$1" ] || [ -L "$1" ] || exit 0; ` +
			`if [ -f "$1" ] && [ ! -L "$1" ]; then printf 'f %s\0' "$1"; exit 0; fi; ` +
			`if [ -L "$1" ]; then printf 'o %s\0' "$1"; exit 0; fi; ` +
			`find "$@" -type f -exec printf 'f %s\0' {} + -o ! -type d -exec printf 'o %s\0' {} +`
		var list, stderr bytes.Buffer
		lw := &limitedWriter{w: &list, n: 32 << 20}
		err := k.exec(ctx, pod, helperName, append([]string{"sh", "-c", script, "sh"}, args...), nil, lw, &stderr)
		if lw.full {
			return nil, notKept("the workspace has too many files where its paths look")
		}
		if err != nil {
			return nil, infra("listing files in pod %s: %v %s", pod, err, strings.TrimSpace(stderr.String()))
		}
		for _, entry := range strings.Split(list.String(), "\x00") {
			kind, name, ok := strings.Cut(entry, " ")
			name = strings.TrimPrefix(name, "./")
			if !ok || name == "" || strings.ContainsAny(name, "\n\r") {
				continue
			}
			p.offer(name, kind == "f")
		}
	}
	names, err := p.chosen(lim)
	if err != nil {
		return &Staged{Notes: p.notes}, err
	}
	out := &Staged{Notes: p.notes}
	var total int64
	for i, name := range names {
		local := filepath.Join(dir, strconv.Itoa(i))
		f, err := k.copyOut(ctx, pod, name, local, lim.MaxFileBytes)
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

// copyOut copies one regular file out of the pod. The helper writes one
// byte first, x if someone may run the file and - if not, then the file.
func (k *Kubernetes) copyOut(ctx context.Context, pod, name, local string, max int64) (StagedFile, error) {
	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		err := k.exec(ctx, pod, helperName, []string{"sh", "-c",
			`cd /workspace && [ -f "$1" ] && [ ! -L "$1" ] || { echo "not a regular file" >&2; exit 3; }; ` +
				`if [ -x "$1" ]; then printf x; else printf -; fi; exec head -c "$2" -- "$1"`,
			"sh", name, strconv.FormatInt(max+1, 10)}, nil, pw, &stderr)
		pw.CloseWithError(err)
		done <- err
	}()
	mark := make([]byte, 1)
	_, rerr := io.ReadFull(pr, mark)
	var f StagedFile
	if rerr == nil {
		f, rerr = copyHashed(pr, name, local, max)
	}
	pr.CloseWithError(errors.New("stopped reading"))
	err := <-done
	if rerr != nil && errors.Is(rerr, ErrNotKept) {
		return f, rerr
	}
	if err != nil {
		if ctx.Err() != nil {
			return f, ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return f, notKept("%s could not be read: %s", name, oneLine(msg))
	}
	if rerr != nil {
		return f, rerr
	}
	f.Mode = 0o644
	if mark[0] == 'x' {
		f.Mode = 0o755
	}
	return f, nil
}

// PlaceFiles writes files into the run's workspace in its pod, starting the
// pod if this is the run's first step here.
func (k *Kubernetes) PlaceFiles(ctx context.Context, s Spec, files []PlacedFile, out Sink) error {
	if s.RunID == "" {
		return errors.New("kubernetes: the step has no run")
	}
	rp, err := k.ensurePod(ctx, withDefaultContainer(s), out)
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writePlacedTar(pw, files)) }()
	// Directories are made by tar as the user that runs it, the same user
	// as the step's containers in a restricted pod; otherwise everything is
	// made writable by all, as the workspace is.
	cmd := "tar xf - -C /workspace"
	if !k.o.restricted() {
		var dirs []string
		for _, f := range files {
			dirs = append(dirs, "/workspace/"+f.Path)
		}
		cmd += " && chmod a+rw " + strings.Join(quoteAll(dirs), " ")
	}
	var stderr bytes.Buffer
	err = k.exec(ctx, rp.name, helperName, []string{"sh", "-c", cmd}, pr, io.Discard, &stderr)
	pr.Close()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return k.podGone(ctx, s.RunID, rp, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String())))
	}
	return nil
}

func quoteAll(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = shellQuote(s)
	}
	return out
}

// writePlacedTar writes the files as a tar stream, with the directories
// they need first.
func writePlacedTar(w io.Writer, files []PlacedFile) error {
	tw := tar.NewWriter(w)
	now := time.Now()
	dirs := map[string]bool{}
	var dirList []string
	for _, f := range files {
		for d := path.Dir(f.Path); d != "." && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
			dirList = append(dirList, d)
		}
	}
	slices.Sort(dirList)
	for _, d := range dirList {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d + "/", Mode: 0o777, ModTime: now}); err != nil {
			return err
		}
	}
	for _, f := range files {
		src, err := os.Open(f.Local)
		if err != nil {
			return err
		}
		info, err := src.Stat()
		if err != nil {
			src.Close()
			return err
		}
		mode := int64(0o644)
		if f.Mode&0o111 != 0 {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: f.Path, Mode: mode, Size: info.Size(), ModTime: now}); err != nil {
			src.Close()
			return err
		}
		_, err = io.Copy(tw, src)
		src.Close()
		if err != nil {
			return err
		}
	}
	return tw.Close()
}
