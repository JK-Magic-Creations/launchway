package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Filling a step's pod and reading its files back, moving only what has to move.
//
// A pod starts empty, so what it lacks is the workspace; but the paths a step's
// caches own (Spec.Cache) and the paths the pipeline leaves out (Spec.Exclude)
// are not part of it. A cache is restored into the pod from the cache store
// (through a scratch directory of the step, since the pod does not exist yet when
// the runner restores it) and, when it is saved after the step, read back into the
// same scratch directory; it is never in the run's workspace copy.
//
// What comes back is what the step changed. The Station knows what it sent (the
// size, time and mode of each entry, from the tar headers: no file is read twice or
// hashed). When the pod was filled the pod's clock was read; the pod lists its files
// with their status-change times (a time no program can set backwards, which
// `touch -d` and tools that restore times from an archive cannot), and the Station
// asks for exactly the files whose time is not before that, and for the
// directories that are new, and removes what was sent and is no longer listed.

// treeBase is what was sent from one directory of the Station.
type treeBase struct {
	dir  string
	base map[string]fileFP
}

// fill is what a pod was filled with.
type fill struct {
	mirror, scratch treeBase
	at              int64 // the pod's clock, in seconds, when it was filled
}

// scratchDir is where a step's caches are kept on the Station while the step runs and until they are saved.
func (k *Kubernetes) scratchDir(s Spec) string {
	return filepath.Join(k.workRoot(), ".steps", safeName(s.LeaseID))
}

// DropScratch implements StepScratcher: the step's caches are saved or not wanted any more.
func (k *Kubernetes) DropScratch(s Spec) {
	if !k.perStep() || s.LeaseID == "" {
		return
	}
	dir := k.scratchDir(s)
	k.account(func(a *accounts) { delete(a.scratch, dir) })
	_ = os.RemoveAll(dir) //nolint:gosec // G703: the directory is the Station's own, named from safeName of the lease
}

// StepScratcher is implemented by executors that keep a step's caches on the Station; the runner
// calls DropScratch once the step's caches are saved.
type StepScratcher interface {
	DropScratch(s Spec)
}

// fillPod sends the run's workspace copy (minus what is apart) and the step's restored caches into the pod.
func (k *Kubernetes) fillPod(ctx context.Context, pod string, m *mirror, s Spec) (*fill, error) {
	f := &fill{mirror: treeBase{dir: m.dir, base: map[string]fileFP{}}}
	m.mu.RLock()
	err := k.sendTree(ctx, pod, m.dir, f.mirror.base, s.apart, true)
	m.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if s.Cache != nil {
		if sd := k.scratchDir(s); pathExists(sd) {
			f.scratch = treeBase{dir: sd, base: map[string]fileFP{}}
			if err := k.sendTree(ctx, pod, sd, f.scratch.base, nil, false); err != nil {
				return nil, err
			}
		}
	}
	var out bytes.Buffer
	if err := k.exec(ctx, pod, helperName, []string{"date", "+%s"}, nil, &out, io.Discard); err != nil {
		return nil, fmt.Errorf("reading the pod's clock: %w", err)
	}
	f.at, err = strconv.ParseInt(strings.TrimSpace(out.String()), 10, 64)
	return f, err
}

// podEntry is one line of the pod's listing.
type podEntry struct {
	name  string
	ctime int64
	mtime int64
	size  int64
	dir   bool
	reg   bool // a regular file or a symbolic link: what a tar of names carries
	link  bool
}

// listPod lists everything in the pod's workspace.
func (k *Kubernetes) listPod(ctx context.Context, pod string) ([]podEntry, error) {
	var list, stderr bytes.Buffer
	lw := &limitedWriter{w: &list, n: 1 << 30}
	err := k.exec(ctx, pod, helperName, []string{"sh", "-c", `cd /workspace && find . -mindepth 1 -exec stat -c '%Z|%Y|%F|%s|%n' {} +`}, nil, lw, &stderr)
	if err != nil || lw.full {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if lw.full {
			msg = "the workspace lists more than 1 GiB of file names"
		}
		return nil, errors.New(oneLine(strings.TrimSpace(err.Error() + " " + msg)))
	}
	var out []podEntry
	for _, line := range strings.Split(list.String(), "\n") {
		f := strings.SplitN(line, "|", 5)
		if len(f) != 5 {
			continue // the rest of a name with a line break in it
		}
		ct, err1 := strconv.ParseInt(f[0], 10, 64)
		mt, err3 := strconv.ParseInt(f[1], 10, 64)
		sz, err2 := strconv.ParseInt(f[3], 10, 64)
		name := strings.TrimPrefix(f[4], "./")
		if err1 != nil || err2 != nil || err3 != nil || !validRelative(name) {
			continue
		}
		e := podEntry{name: name, ctime: ct, mtime: mt, size: sz, dir: f[2] == "directory"}
		e.link = f[2] == "symbolic link"
		e.reg = strings.HasPrefix(f[2], "regular") || e.link
		if e.dir || e.reg {
			out = append(out, e)
		}
	}
	return out, nil
}

// handBack reads back what the step changed, into the run's workspace copy and, for the caches that are
// saved, into the step's scratch directory; and checks the sizes against the Station's limits.
func (k *Kubernetes) handBack(ctx context.Context, pod string, m *mirror, s Spec, f *fill, sys func(string, ...any)) error {
	entries, err := k.listPod(ctx, pod)
	if err != nil {
		return err
	}
	var mine, theirs []podEntry
	var mineBytes, theirBytes int64
	for _, e := range entries {
		switch {
		case s.Cache != nil && under(s.Cache.Paths, e.name):
			if under(s.Cache.Back, e.name) {
				theirs = append(theirs, e)
				theirBytes += e.size
			}
		case under(s.Exclude, e.name):
		default:
			mine = append(mine, e)
			mineBytes += e.size
		}
	}
	if err := k.checkSizes(s, mineBytes, theirBytes, sys); err != nil {
		return err
	}
	took := time.Now()
	m.mu.Lock()
	err = k.mergeBack(ctx, pod, f.mirror, mine, f.at, nil)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if len(theirs) > 0 || f.scratch.dir != "" {
		sd := k.scratchDir(s)
		if err := os.MkdirAll(sd, 0o750); err != nil {
			return err
		}
		if f.scratch.dir == "" {
			f.scratch = treeBase{dir: sd, base: map[string]fileFP{}}
		}
		if err := k.mergeBack(ctx, pod, f.scratch, theirs, f.at, func(n string) bool { return under(s.Cache.Back, n) }); err != nil {
			return err
		}
	}
	k.account(func(a *accounts) {
		a.mirror[s.RunID] = mineBytes
		if f.scratch.dir != "" {
			a.scratch[f.scratch.dir] = theirBytes
		}
	})
	_ = took
	_ = os.Chtimes(m.dir, time.Now(), time.Now()) // so that the copy's age says when it was last used
	return nil
}

// mergeBack applies what a pod holds below one tree (owns, if set, says which of its paths this call looks after): the changed files come over, new directories are made,
// and what was sent and is not listed any more is removed.
func (k *Kubernetes) mergeBack(ctx context.Context, pod string, t treeBase, entries []podEntry, at int64, owns func(string) bool) error {
	var names []string
	present := make(map[string]bool, len(entries))
	var dirs []string
	for _, e := range entries {
		present[e.name] = true
		switch {
		case e.dir:
			if _, had := t.base[e.name]; !had {
				dirs = append(dirs, e.name)
			}
		case changed(e, t.base[e.name], at) && !strings.ContainsAny(e.name, "\n\r"):
			names = append(names, e.name)
		}
	}
	root, err := os.OpenRoot(t.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	slices.Sort(dirs)
	for _, d := range dirs {
		if err := root.MkdirAll(filepath.FromSlash(d), 0o755); err != nil {
			return err
		}
	}
	if len(names) > 0 {
		var in strings.Builder
		for _, n := range names {
			in.WriteString("./" + n + "\n")
		}
		pr, pw := io.Pipe()
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			err := k.exec(ctx, pod, helperName, []string{"sh", "-c", "cd /workspace && exec tar -c -f - -T -"}, strings.NewReader(in.String()), pw, &lockedWriter{w: &stderr})
			pw.CloseWithError(err)
			done <- err
		}()
		rerr := extractWorkspace(pr, t.dir, handOverLimits, t.base, nil)
		if rerr == nil {
			_, _ = io.Copy(io.Discard, pr) // the end of the archive is padding; let tar finish writing it
		}
		pr.CloseWithError(errors.New("stopped reading"))
		err := <-done
		switch {
		case rerr != nil:
			return rerr
		case err != nil:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return errors.New(oneLine(msg))
		}
	}
	var gone []string
	for p := range t.base {
		if !present[p] && !strings.ContainsAny(p, "\n\r") && (owns == nil || owns(p)) {
			gone = append(gone, p)
		}
	}
	slices.Sort(gone)
	for _, p := range gone {
		_ = root.RemoveAll(filepath.FromSlash(p))
	}
	return nil
}

func pathExists(p string) bool { _, err := os.Lstat(p); return err == nil }

// allIn reports whether every glob is one of the declared paths.
func allIn(declared, globs []string) bool {
	for _, g := range globs {
		if !slices.Contains(declared, g) {
			return false
		}
	}
	return len(globs) > 0
}

// scratchAge is how long a step's cache directory may be unused before the reaper removes it: a step's caches
// are saved within minutes of its end, so what is older was left by a step that did not get that far.
const scratchAge = time.Hour

// sweepScratch removes the cache directories of steps that ended long ago.
func (k *Kubernetes) sweepScratch() {
	root := filepath.Join(k.workRoot(), ".steps")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if fi, err := os.Stat(dir); err == nil && time.Since(fi.ModTime()) > scratchAge {
			k.account(func(a *accounts) { delete(a.scratch, dir) })
			_ = os.RemoveAll(dir)
		}
	}
}

// changed says whether the step changed an entry. Its status-change time is not before the moment the pod was
// filled for what the step touched; but the files the upload wrote in the last second (the clock reads whole
// seconds) cannot be told apart by that time alone, and for those the entry is compared with what was sent: the
// same size and modification time, and a time older than the upload, is the file that was sent.
func changed(e podEntry, sent fileFP, at int64) bool {
	switch {
	case e.ctime < at:
		return false
	case e.ctime > at:
		return true
	}
	return !sent.settled || sent.dir || sent.size != e.size || sent.mtime != e.mtime
}
