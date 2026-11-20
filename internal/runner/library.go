package runner

// A run's libraries in its workspace. A step of a template works with files
// of the template's library (scripts, configuration) at the commit the run
// recorded when it was created. The runner puts every library of the run in
// the workspace, at .launchway/lib/<name>, so that they reach the step the
// same way whatever the executor: mounted in a container, copied into a
// pod, or as they are on the host.

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// LibDir is where libraries are in a workspace, one directory each.
const LibDir = ".launchway/lib"

// libraryEnv adds LAUNCHWAY_LIB_<NAME> for every library of the run, and
// LAUNCHWAY_LIB for the step's own, as the step sees the workspace.
func libraryEnv(env map[string]string, job *engine.Job, inside string) {
	for _, l := range job.Source.Libraries {
		dir := filepath.Join(inside, filepath.FromSlash(LibDir), l.Name)
		env["LAUNCHWAY_LIB_"+pipeline.EnvName(l.Name)] = dir
		if l.Name == job.Library {
			env["LAUNCHWAY_LIB"] = dir
		}
	}
}

// addLibraries puts the run's libraries in a fresh workspace, and tells Git
// in the workspace to ignore them, so that they are not changes to the
// repository.
func (r *Runner) addLibraries(ctx context.Context, job *engine.Job, ws string, sys func(string, ...any)) error {
	if len(job.Source.Libraries) == 0 {
		return nil
	}
	for _, l := range job.Source.Libraries {
		if !pipeline.ValidLibraryName(l.Name) || !pipeline.IsCommitID(l.Commit) {
			return fmt.Errorf("the server sent a library this runner cannot use: %q at %q", l.Name, l.Commit)
		}
		sys("Library %s at %s from %s", l.Name, short(l.Commit), redact.URLs(l.URL))
		repo, err := r.libraryRepo(ctx, l)
		if err != nil {
			return fmt.Errorf("library %s: %w", l.Name, err)
		}
		dest := filepath.Join(ws, filepath.FromSlash(LibDir), l.Name)
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		if err := export(ctx, repo, l.Commit, dest); err != nil {
			return fmt.Errorf("library %s: %w", l.Name, err)
		}
	}
	exclude := filepath.Join(ws, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "\n# Libraries Launchway put here for the run's templates\n/%s/\n", LibDir)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// libraryRepo returns this runner's mirror of a library, with the commit in
// it: cloned the first time, fetched when the commit is new to it. Mirrors
// are kept between runs, one per address.
func (r *Runner) libraryRepo(ctx context.Context, l engine.LibrarySource) (string, error) {
	sum := sha256.Sum256([]byte(l.URL))
	dir := filepath.Join(r.DataDir, "libraries", hex.EncodeToString(sum[:])[:16]+".git")
	r.libMu.Lock()
	if r.libLocks == nil {
		r.libLocks = map[string]*sync.Mutex{}
	}
	lock := r.libLocks[dir]
	if lock == nil {
		lock = &sync.Mutex{}
		r.libLocks[dir] = lock
	}
	r.libMu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "", err
		}
		tmp, err := os.MkdirTemp(filepath.Dir(dir), ".clone-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(tmp)
		if err := git(ctx, "", "clone", "--mirror", "--quiet", l.URL, tmp); err != nil {
			return "", err
		}
		_ = os.RemoveAll(dir)
		if err := os.Rename(tmp, dir); err != nil {
			return "", err
		}
	}
	has := func() bool { return git(ctx, dir, "cat-file", "-e", l.Commit+"^{commit}") == nil }
	if has() {
		return dir, nil
	}
	if err := git(ctx, dir, "remote", "update", "--prune"); err != nil {
		return "", err
	}
	if has() {
		return dir, nil
	}
	// A commit no branch or tag reaches any more, which some hosts still
	// hand out when asked by its ID.
	_ = git(ctx, dir, "fetch", "--quiet", "origin", l.Commit)
	if has() {
		return dir, nil
	}
	return "", fmt.Errorf("the repository no longer has commit %s", l.Commit)
}

// export writes the files of a commit into dest, keeping which are
// executable, as Git records them.
func export(ctx context.Context, repo, commit, dest string) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := gitexec.Run(ctx, gitexec.Spec{Dir: repo, Args: []string{"archive", "--format=tar", commit}, Stdout: pw, Untrusted: true})
		pw.CloseWithError(err)
		done <- err
	}()
	xerr := untar(pr, dest)
	_, _ = io.Copy(io.Discard, pr)
	if err := <-done; err != nil {
		var ee *gitexec.ExitError
		msg := err.Error()
		if errors.As(err, &ee) {
			msg = ee.Stderr
		}
		return fmt.Errorf("git archive: %s", redact.URLs(strings.TrimSpace(msg)))
	}
	return xerr
}

func untar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(strings.TrimSuffix(h.Name, "/"))
		if h.Typeflag == tar.TypeXGlobalHeader || name == "" {
			continue
		}
		if !filepath.IsLocal(name) {
			return fmt.Errorf("the library has a file outside its directory: %q", h.Name)
		}
		path := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err == nil {
				err = os.Chmod(path, mode) // whatever the umask
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, path); err != nil {
				return err
			}
		}
	}
}
