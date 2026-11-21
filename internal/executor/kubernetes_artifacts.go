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
	if len(names) > 1 {
		// One tar stream for all of them: a file at a time is an exec
		// each, which is far too slow for the thousands of files a cache
		// holds.
		files, err := k.copyOutMany(ctx, pod, names, dir, lim)
		out.Files = files
		return out, err
	}
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
			`cd /workspace && [ -f "$1" ] && [ ! -L "$1" ] && [ "$(readlink -f "./$1")" = "/workspace/$1" ] || { echo "not a regular file inside the workspace" >&2; exit 3; }; ` +
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
	var stderr bytes.Buffer
	err = k.exec(ctx, rp.name, helperName, []string{"sh", "-c", "tar xf - -C /workspace"}, pr, io.Discard, &stderr)
	pr.Close()
	if err == nil && len(files) > 0 && !k.o.restricted() {
		// The names go in by standard input, a NUL after each, and xargs
		// splits them into as many chmod calls as the system's limit on
		// arguments needs: a cache restores many thousands of files, far
		// more than one command line holds.
		err = k.exec(ctx, rp.name, helperName, []string{"sh", "-c", placedChmodCommand},
			strings.NewReader(placedChmodInput(files)), io.Discard, &stderr)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return k.podGone(ctx, s.RunID, rp, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String())))
	}
	return nil
}

// placedChmodCommand makes the placed files writable by all, as the
// workspace is, when the pod is not restricted to one user. Its input is
// placedChmodInput.
const placedChmodCommand = "cd /workspace && xargs -0 chmod a+rw"

// placedChmodInput lists the files for placedChmodCommand: each name
// relative to the workspace and started with ./ so that none can be taken
// for an option, a NUL after each.
func placedChmodInput(files []PlacedFile) string {
	var b strings.Builder
	for _, f := range files {
		b.WriteString("./" + f.Path + "\x00")
	}
	return b.String()
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
		mtime := now
		if f.ModTime > 0 {
			mtime = time.Unix(0, f.ModTime)
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: f.Path, Mode: mode, Size: info.Size(), ModTime: mtime}); err != nil {
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

// WorkspaceInPod implements WorkspaceStager: the workspace is in the pod
// once the runner has set it up for the run's workspace.
func (k *Kubernetes) WorkspaceInPod(s Spec) bool {
	k.mu.Lock()
	rp := k.pods[s.RunID]
	k.mu.Unlock()
	if rp == nil {
		return false
	}
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.ready && rp.workspace == s.WorkspaceID
}

// copyOutMany copies regular files out of the pod with one tar stream. The
// names are those the listing found, which does not follow symbolic links;
// what comes back is accepted only if it is a regular file with a name that
// was asked for (a link put in its place meanwhile arrives as a link, and
// is refused).
func (k *Kubernetes) copyOutMany(ctx context.Context, pod string, names []string, dir string, lim StageLimits) ([]StagedFile, error) {
	want := make(map[string]int, len(names))
	for i, n := range names {
		want[n] = i
	}
	pr, pw := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		err := k.exec(ctx, pod, helperName, []string{"sh", "-c", copyOutManyScript("/workspace")},
			strings.NewReader(copyOutManyInput(names)), pw, &stderr)
		pw.CloseWithError(err)
		done <- err
	}()
	files := make([]StagedFile, len(names))
	got := make([]bool, len(names))
	var total int64
	rerr := func() error {
		tr := tar.NewReader(pr)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			name := path.Clean(strings.TrimPrefix(h.Name, "./"))
			i, ok := want[name]
			switch {
			case h.Typeflag == tar.TypeDir:
				continue
			case !ok:
				return notKept("%s was not asked for", name)
			case h.Typeflag != tar.TypeReg:
				return notKept("%s is not a regular file", name)
			case got[i]:
				return notKept("%s arrived twice", name)
			}
			f, err := copyHashed(tr, name, filepath.Join(dir, strconv.Itoa(i)), lim.MaxFileBytes)
			if err != nil {
				return err
			}
			f.Mode = 0o644
			if h.FileInfo().Mode()&0o111 != 0 {
				f.Mode = 0o755
			}
			if !h.ModTime.IsZero() {
				f.ModTime = h.ModTime.UnixNano()
			}
			if total += f.Size; total > lim.MaxBytes {
				return notKept("its files come to more than %s", bytesText(lim.MaxBytes))
			}
			files[i], got[i] = f, true
		}
	}()
	pr.CloseWithError(errors.New("stopped reading"))
	err := <-done
	if rerr != nil {
		if errors.Is(rerr, ErrNotKept) {
			return nil, rerr
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return nil, notKept("the files could not be read: %s", oneLine(msg))
		}
		return nil, rerr
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, notKept("the files could not be read: %s", oneLine(msg))
	}
	for i, ok := range got {
		if !ok {
			return nil, notKept("%s could not be read", names[i])
		}
	}
	return files, nil
}

// copyOutManyScript is what runs in the pod to read many files with one tar
// stream. Like copyOut for one file, it refuses a path that leaves the
// workspace through a symbolic link: tar names only the last component of
// each file, so a directory on the way that was made a link to somewhere
// else (the step's own script can do that) would send the files of that
// other place. The directories come first on standard input, one a line and
// ended by an empty line; each must be its own resolved path. What follows
// are the files, for tar.
func copyOutManyScript(root string) string {
	q := shellQuote(root)
	return `cd ` + q + ` || exit 1; ` +
		`while IFS= read -r d && [ -n "$d" ]; do ` +
		`[ "$(readlink -f "./$d")" = ` + q + `"/$d" ] || { echo "not inside the workspace: $d" >&2; exit 3; }; done; ` +
		`exec tar -c -f - -T -`
}

// copyOutManyInput is the standard input of copyOutManyScript: every
// directory the names are in (the deepest ones suffice, as resolving one
// resolves the ones above it, but all are listed so that each is checked
// by its own name), an empty line, then the names.
func copyOutManyInput(names []string) string {
	seen := map[string]bool{}
	var b strings.Builder
	for _, n := range names {
		d := path.Dir(n)
		if d == "." || seen[d] {
			continue
		}
		seen[d] = true
		b.WriteString(d + "\n")
	}
	b.WriteString("\n" + strings.Join(names, "\n") + "\n")
	return b.String()
}
