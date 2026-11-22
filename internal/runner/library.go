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
	"github.com/jk-magic-creations/launchway/internal/starters"
	"github.com/jk-magic-creations/launchway/internal/stepkit"
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
		if l.URL == starters.URL {
			sys("Library %s at %s: the starters that ship with Launchway", l.Name, short(l.Commit))
			if err := addBuiltin(l, filepath.Join(ws, filepath.FromSlash(LibDir), l.Name)); err != nil {
				return fmt.Errorf("library %s: %w", l.Name, err)
			}
			continue
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
	if _, err := os.Stat(filepath.Join(ws, ".git")); err != nil && job.Source.Kind == "dir" {
		return nil // a directory that is not a Git checkout has nothing to keep them out of
	}
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
		r.libLocks = map[string]ctxLock{}
	}
	lock := r.libLocks[dir]
	if lock == nil {
		lock = newCtxLock()
		r.libLocks[dir] = lock
	}
	r.libMu.Unlock()
	if err := lock.lock(ctx); err != nil {
		return "", fmt.Errorf("waiting for the library's mirror (another step holds it): %w", err)
	}
	defer lock.unlock()

	from, env := l.URL, []string(nil)
	if l.Hosted != "" && l.Credential != "" {
		// Hosted by the server this runner talks to: read through it with the credential made for this run.
		from, env = r.hostedClone(l.Hosted, l.Credential)
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "", err
		}
		tmp, err := os.MkdirTemp(filepath.Dir(dir), ".clone-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(tmp)
		if err := gitEnv(ctx, "", env, "clone", "--mirror", "--quiet", from, tmp); err != nil {
			return "", err
		}
		_ = os.RemoveAll(dir)
		if err := os.Rename(tmp, dir); err != nil {
			return "", err
		}
	}
	if env != nil {
		if err := git(ctx, dir, "remote", "set-url", "origin", from); err != nil {
			return "", err
		}
	}
	has := func() bool { return git(ctx, dir, "cat-file", "-e", l.Commit+"^{commit}") == nil }
	if has() {
		return dir, nil
	}
	if err := gitEnv(ctx, dir, env, "remote", "update", "--prune"); err != nil {
		return "", err
	}
	if has() {
		return dir, nil
	}
	// A commit no branch or tag reaches any more, which some hosts still
	// hand out when asked by its ID.
	_ = gitEnv(ctx, dir, env, "fetch", "--quiet", "origin", l.Commit)
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
		// Nothing is created below a link an earlier entry made: a link
		// to another place would carry the file out of the directory.
		if err := stepkit.NoLinkAbove(dest, name); err != nil {
			return fmt.Errorf("the library has %q below a link: %w", h.Name, err)
		}
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
			// A link stays inside the directory: it is relative, and
			// where it points, from the folder it is in, is local too.
			if filepath.IsAbs(h.Linkname) || !filepath.IsLocal(filepath.Join(filepath.Dir(name), filepath.FromSlash(h.Linkname))) {
				return fmt.Errorf("the library has a link that points outside its directory: %q", h.Name)
			}
			if err := os.Symlink(h.Linkname, path); err != nil {
				return err
			}
		}
	}
}

// addBuiltin writes the starters this runner carries, if they are the ones
// the server planned the run with. A runner of another release holds other
// scripts, and running them under the server's templates would be running
// something nobody reviewed together; it says so and refuses.
func addBuiltin(l engine.LibrarySource, dest string) error {
	if have := starters.Commit(); l.Commit != have {
		return fmt.Errorf("the server planned this run with the starters of its own release (%s) and this runner carries other ones (%s): "+
			"update the runner to the server's release", short(l.Commit), short(have))
	}
	// Steps of a run in a directory (launchway run) each ask; write once.
	builtinMu.Lock()
	defer builtinMu.Unlock()
	marker := filepath.Join(dest, ".starters-commit")
	if have, err := os.ReadFile(marker); err == nil && string(have) == l.Commit {
		return nil
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	for _, name := range starters.List() {
		data, err := starters.File(name)
		if err != nil {
			return err
		}
		path := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
			return err
		}
		mode := fileMode
		if starters.IsExecutable(name) {
			mode = execMode // scripts are run by the step, so they are executable, as Git records them
		}
		if err := writeLibFile(path, data, mode); err != nil {
			return err
		}
	}
	return writeLibFile(marker, []byte(l.Commit), fileMode)
}

// The modes of the files of a library in a workspace the steps' containers
// read, whatever user they run as (see untar).
var (
	dirMode  = os.FileMode(0o755)
	fileMode = os.FileMode(0o644)
	execMode = os.FileMode(0o755)
)

// writeLibFile writes a file with exactly the mode, whatever the umask.
func writeLibFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(path, mode)
	}
	return err
}

var builtinMu sync.Mutex
