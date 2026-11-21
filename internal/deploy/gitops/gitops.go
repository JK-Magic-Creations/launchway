// Package gitops commits deployments to a GitOps repository and reverts them.
//
// Every change is a single commit carrying the trailer
// "Launchway-Deployment: <deployment>", so that an action retried after a
// crash, possibly on another machine with a fresh clone, finds the commit it
// already made instead of making a second one. Nothing is ever forced: a push
// the server rejects because the branch moved is made again on the new tip.
//
// Git runs as a separate process. Credentials reach it through the
// environment only, never on a command line or in a file, and every message
// is cleaned of them before it is logged or returned.
package gitops

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// Auth is HTTP basic authentication for the repository. It is given to git
// through the environment of each git command (GIT_CONFIG_COUNT/KEY/VALUE
// with http.extraHeader), never on a command line or in a config file.
type Auth struct{ Username, Password string }

type Author struct{ Name, Email string }

// Repo is a working clone of one branch of a repository. It is not safe for
// concurrent use; the caller serialises.
type Repo struct {
	URL    string // http(s)://, file://, or a local path
	Branch string
	Dir    string // where the clone is kept; created when missing, reused (and repaired or re-cloned if broken) when present
	Auth   *Auth  // optional
	Author Author // used for both author and committer
	// Log receives progress lines for the step's log, such as
	// "Committed 3f2a9c1 to main: Deploy webapp 1.4.2 to prod". Never
	// credentials. May be nil.
	Log func(format string, args ...any)
	// MaxPushTries bounds pushes rejected because the branch moved; default
	// 12. Between tries the runner waits a little, longer each time and with
	// some chance in it, so that several writers of one branch (other
	// pipelines, other deploy runners) do not collide again and again.
	MaxPushTries int
	// Wait is how long to wait before try number try (2 and up); default
	// 40 ms times try, plus up to as much again at random, at most 1 s.
	Wait func(try int) time.Duration
	// HistoryDepth is how many commits of the branch are searched for an
	// earlier commit of the same deployment; default 200.
	HistoryDepth int

	// WorkBranch, when set, is the branch a change is pushed to instead of
	// Branch: the change is made on the tip of Branch and a pull request
	// brings it there. A change already on the work branch (an earlier try
	// pushed it) is found by its trailer and not made again.
	WorkBranch string

	// beforePush, when set, is called just before each push. Tests use it to
	// move the branch on the server at the worst moment.
	beforePush func()
}

// Edit returns a file's new contents. path is relative to the repository
// root, with forward slashes; data is the current contents (nil when the
// file does not exist, and the edit should then usually fail).
type Edit func(path string, data []byte) ([]byte, error)

type FileEdit struct {
	Path string
	Edit Edit
}

// ChangeSet is one deployment's change: one commit touching every file.
type ChangeSet struct {
	Deployment string     // e.g. dep_abc; written as the trailer "Launchway-Deployment: dep_abc"
	Message    string     // subject, blank line, body; the trailer is added
	Files      []FileEdit // applied in order; the same path may appear twice
}

type ApplyResult struct {
	// Commit is the commit that carries the change: the one made now, an
	// earlier one of the same deployment found in the branch's history, or,
	// when the files already held these values, the branch's tip.
	Commit   string
	Changed  bool     // the branch has a commit of this deployment (made now or earlier)
	Existing bool     // that commit was found, not made now; nothing was pushed
	Files    []string // files the commit changes (made now only)
	// OnWork says the commit is on the work branch, not yet on the branch.
	OnWork bool
}

type RevertResult struct {
	Commit   string // the revert commit, made now or found
	Existing bool   // found by its trailer; nothing pushed
	OnWork   bool   // the commit is on the work branch, not yet on the branch
}

// ConflictError means the revert does not apply to the branch's tip (later
// commits changed the same lines). The clone is reset; the branch on the
// server is untouched.
type ConflictError struct {
	Files []string
	// Deployment and Commit, for a series, name the item whose revert does
	// not apply.
	Deployment string
	Commit     string
}

func (e *ConflictError) Error() string {
	if len(e.Files) == 0 {
		return "the revert conflicts with later changes on the branch"
	}
	return "the revert conflicts with later changes to " + strings.Join(e.Files, ", ")
}

// Trailer keys.
const (
	deploymentKey = "Launchway-Deployment: "
	revertKey     = "Launchway-Revert: "
)

// errMoved means the push was rejected because the branch moved on the
// server since it was fetched.
var errMoved = errors.New("the branch moved on the server")

// Apply fetches the branch, and unless its last HistoryDepth commits already
// hold a commit with this deployment's trailer, applies the edits to the
// fresh tip, commits them if anything changed, and pushes. A push rejected
// because the branch moved fetches again and re-applies the edits on the new
// tip, up to MaxPushTries times. It never forces.
func (r *Repo) Apply(ctx context.Context, cs ChangeSet) (ApplyResult, error) {
	if err := r.check(); err != nil {
		return ApplyResult{}, err
	}
	if err := checkName("deployment", cs.Deployment); err != nil {
		return ApplyResult{}, err
	}
	if err := r.checkAuthor(); err != nil {
		return ApplyResult{}, err
	}
	if len(cs.Files) == 0 {
		return ApplyResult{}, errors.New("the change names no files")
	}
	tries := r.tries()
	for try := 1; ; try++ {
		tip, err := r.refresh(ctx)
		if err != nil {
			return ApplyResult{}, err
		}
		found, err := r.find(ctx, deploymentKey+cs.Deployment, true)
		if err != nil {
			return ApplyResult{}, err
		}
		if found != "" {
			r.logf("Deployment %s is already in %s as %s; nothing to push", cs.Deployment, r.Branch, short(found))
			return ApplyResult{Commit: found, Changed: true, Existing: true}, nil
		}
		if w, err := r.existingWork(ctx, deploymentKey+cs.Deployment); err != nil {
			return ApplyResult{}, err
		} else if w != "" {
			r.logf("Deployment %s is already on %s as %s; nothing to push", cs.Deployment, r.WorkBranch, short(w))
			return ApplyResult{Commit: w, Changed: true, Existing: true, OnWork: true}, nil
		}
		files, err := r.applyEdits(cs.Files)
		if err != nil {
			r.discard(ctx)
			return ApplyResult{}, err
		}
		if len(files) > 0 {
			if _, err := r.git(ctx, nil, append([]string{"add", "--"}, files...)...); err != nil {
				r.discard(ctx)
				return ApplyResult{}, err
			}
			staged, err := r.hasStaged(ctx)
			if err != nil {
				r.discard(ctx)
				return ApplyResult{}, err
			}
			if !staged {
				files = nil
			}
		}
		if len(files) == 0 {
			r.logf("The files in %s already hold these values; nothing to commit", r.Branch)
			return ApplyResult{Commit: tip}, nil
		}
		msg := buildMessage(cs.Message, "", deploymentKey+cs.Deployment)
		commit, err := r.commit(ctx, msg, false)
		if err != nil {
			r.discard(ctx)
			return ApplyResult{}, err
		}
		err = r.push(ctx)
		if errors.Is(err, errMoved) && try < tries {
			r.logf("%s moved on the server (another pipeline wrote to it); fetching and applying only this change again on top (try %d of %d)", r.Branch, try+1, tries)
			if err := r.sleep(ctx, try+1); err != nil {
				return ApplyResult{}, err
			}
			continue
		}
		if errors.Is(err, errMoved) {
			return ApplyResult{}, fmt.Errorf("%s kept moving on the server: other pipelines were writing to it all the time; gave up after %d tries to push this change on top of theirs. "+
				"Nothing was changed. Run the deploy step again; if it keeps happening, fewer pipelines should write to this repository at once", r.Branch, tries)
		}
		if err != nil {
			return ApplyResult{}, err
		}
		r.logf("Committed %s to %s: %s", short(commit), r.Branch, subject(cs.Message))
		return ApplyResult{Commit: commit, Changed: true, Files: files}, nil
	}
}

// Find returns the commit of the branch's recent history (HistoryDepth) that
// carries this deployment's change: the newest commit with the trailer
// "Launchway-Deployment: <deployment>" and without a "Launchway-Revert:"
// trailer (the revert commit carries the deployment trailer too, so it is
// never returned). "" when there is none. It fetches first.
func (r *Repo) Find(ctx context.Context, deployment string) (string, error) {
	if err := r.check(); err != nil {
		return "", err
	}
	if err := checkName("deployment", deployment); err != nil {
		return "", err
	}
	if err := r.fetch(ctx); err != nil {
		return "", err
	}
	return r.find(ctx, deploymentKey+deployment, true)
}

// Contains reports whether revision includes commit (commit is revision or
// one of its ancestors). It fetches the branch first if revision is not
// known locally. An unknown revision after fetching is an error.
func (r *Repo) Contains(ctx context.Context, commit, revision string) (bool, error) {
	if err := r.check(); err != nil {
		return false, err
	}
	if err := checkRevision(commit); err != nil {
		return false, err
	}
	if err := checkRevision(revision); err != nil {
		return false, err
	}
	fetched := false
	if revision == r.Branch {
		// The branch's name means the branch as it is on the server now.
		if err := r.fetch(ctx); err != nil {
			return false, err
		}
		fetched = true
		revision = r.remoteRef()
	} else if _, err := r.open(ctx); err != nil {
		return false, err
	}
	rev, ok, err := r.resolve(ctx, revision)
	if err != nil {
		return false, err
	}
	if !ok && !fetched {
		if err := r.fetch(ctx); err != nil {
			return false, err
		}
		fetched = true
		if rev, ok, err = r.resolve(ctx, revision); err != nil {
			return false, err
		}
	}
	if !ok {
		return false, fmt.Errorf("revision %s is not in the repository", revision)
	}
	c, ok, err := r.resolve(ctx, commit)
	if err != nil {
		return false, err
	}
	if !ok && !fetched {
		if err := r.fetch(ctx); err != nil {
			return false, err
		}
		if c, ok, err = r.resolve(ctx, commit); err != nil {
			return false, err
		}
	}
	if !ok {
		// The clone holds every ancestor of revision, so a commit it does
		// not have is not one of them.
		return false, nil
	}
	return r.isAncestor(ctx, c, rev)
}

// Tip fetches and returns the branch's current commit.
func (r *Repo) Tip(ctx context.Context) (string, error) {
	if err := r.check(); err != nil {
		return "", err
	}
	if err := r.fetch(ctx); err != nil {
		return "", err
	}
	return r.tip(ctx)
}

// --- Preparing the clone ---

// refresh fetches the branch and puts the clone at its tip, with nothing
// left over from an earlier attempt. A clone that cannot be brought there is
// replaced once.
func (r *Repo) refresh(ctx context.Context) (string, error) {
	do := func() (string, error) {
		if err := r.fetchOnce(ctx); err != nil {
			return "", err
		}
		tip, err := r.tip(ctx)
		if err != nil {
			return "", err
		}
		return tip, r.resetTo(ctx, tip)
	}
	reused, err := r.open(ctx)
	if err != nil {
		return "", err
	}
	tip, err := do()
	if err != nil && reused && ctx.Err() == nil {
		r.logf("Could not use the existing clone (%v); cloning again", err)
		if err := r.recreate(ctx); err != nil {
			return "", err
		}
		tip, err = do()
	}
	return tip, err
}

// fetch fetches the branch without touching the working tree, replacing the
// clone once if it cannot.
func (r *Repo) fetch(ctx context.Context) error {
	reused, err := r.open(ctx)
	if err != nil {
		return err
	}
	err = r.fetchOnce(ctx)
	if err != nil && reused && ctx.Err() == nil {
		r.logf("Could not fetch into the existing clone (%v); cloning again", err)
		if err := r.recreate(ctx); err != nil {
			return err
		}
		err = r.fetchOnce(ctx)
	}
	return err
}

func (r *Repo) fetchOnce(ctx context.Context) error {
	_, err := r.git(ctx, nil, "fetch", "--quiet", "--prune", "--no-tags", "origin",
		"+refs/heads/"+r.Branch+":"+r.remoteRef())
	return err
}

// resetTo puts the clone on a detached HEAD at commit, with a clean index
// and working tree.
func (r *Repo) resetTo(ctx context.Context, commit string) error {
	gitDir := filepath.Join(r.dir(), ".git")
	_ = os.Remove(filepath.Join(gitDir, "index.lock"))
	_ = os.RemoveAll(filepath.Join(gitDir, "sequencer"))
	// An earlier attempt may have stopped in the middle of a revert; a hard
	// reset clears that. It fails harmlessly on a clone without a HEAD yet.
	r.git(ctx, nil, "reset", "--quiet", "--hard")
	if _, err := r.git(ctx, nil, "checkout", "--quiet", "--force", "--detach", commit); err != nil {
		return err
	}
	if _, err := r.git(ctx, nil, "reset", "--quiet", "--hard", commit); err != nil {
		return err
	}
	_, err := r.git(ctx, nil, "clean", "--quiet", "-ffdx")
	return err
}

// discard throws away whatever an attempt left in the clone.
func (r *Repo) discard(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	if tip, err := r.tip(ctx); err == nil {
		r.resetTo(ctx, tip)
	}
}

// open makes sure Dir holds a clone of URL to work in, and reports whether
// it was there already. Anything else found in Dir is removed.
func (r *Repo) open(ctx context.Context) (reused bool, err error) {
	dir := r.dir()
	st, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, r.create(ctx)
	case err != nil:
		return false, err
	case !st.IsDir():
		r.logf("%s is not a directory; replacing it with a clone", dir)
		return false, r.recreate(ctx)
	}
	if why := r.usable(ctx); why != "" {
		r.logf("%s %s; cloning again", dir, why)
		return false, r.recreate(ctx)
	}
	return true, nil
}

// usable says what is wrong with the clone in Dir, or "" if nothing.
func (r *Repo) usable(ctx context.Context) string {
	top, err := r.git(ctx, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "is not a Git clone"
	}
	want, err1 := filepath.EvalSymlinks(r.dir())
	got, err2 := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err1 != nil || err2 != nil || want != got {
		return "is not a Git clone"
	}
	remote, err := r.git(ctx, nil, "config", "--get", "remote.origin.url")
	if err != nil {
		return "has no remote"
	}
	if strings.TrimSpace(remote) != r.url() {
		return "is a clone of another repository"
	}
	return ""
}

func (r *Repo) recreate(ctx context.Context) error {
	if err := os.RemoveAll(r.dir()); err != nil {
		return fmt.Errorf("removing the old clone: %w", err)
	}
	return r.create(ctx)
}

// create makes an empty repository in Dir with URL as its remote; fetching
// fills it.
func (r *Repo) create(ctx context.Context) error {
	if err := os.MkdirAll(r.dir(), 0o755); err != nil {
		return err
	}
	if _, err := r.git(ctx, nil, "init", "--quiet"); err != nil {
		return err
	}
	_, err := r.git(ctx, nil, "remote", "add", "origin", "--", r.url())
	return err
}

// --- Editing and committing ---

// applyEdits runs the edits in order on the working tree and returns the
// files whose contents end up different from the tip's, in the order they
// were first named.
func (r *Repo) applyEdits(edits []FileEdit) ([]string, error) {
	type original struct {
		data   []byte
		exists bool
	}
	originals := map[string]original{}
	var order []string
	for _, fe := range edits {
		p, err := cleanPath(fe.Path)
		if err != nil {
			return nil, err
		}
		if fe.Edit == nil {
			return nil, fmt.Errorf("%s: no edit given", p)
		}
		full, err := r.filePath(p)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(full)
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if _, seen := originals[p]; !seen {
			originals[p] = original{data, exists}
			order = append(order, p)
		}
		out, err := fe.Edit(p, data)
		if err != nil {
			return nil, fmt.Errorf("editing %s: %w", p, err)
		}
		if exists && bytes.Equal(out, data) {
			continue
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(full); err == nil {
			mode = st.Mode().Perm()
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if err := os.WriteFile(full, out, mode); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	var changed []string
	for _, p := range order {
		full, _ := r.filePath(p)
		now, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		o := originals[p]
		if !o.exists || !bytes.Equal(now, o.data) {
			changed = append(changed, p)
		}
	}
	return changed, nil
}

// filePath returns where a repository path lives on disk, refusing paths
// that pass through a symbolic link, which could lead out of the clone.
func (r *Repo) filePath(p string) (string, error) {
	cur := r.dir()
	for _, part := range strings.Split(p, "/") {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%s: %w", p, err)
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s: goes through a symbolic link; not editing it", p)
		}
	}
	return filepath.Join(r.dir(), filepath.FromSlash(p)), nil
}

// cleanPath checks a path given by an edit and returns it in its plain form.
func cleanPath(p string) (string, error) {
	if p == "" || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%q is not a usable file path", p)
	}
	c := path.Clean(p)
	if path.IsAbs(c) || c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%q is not a path inside the repository", p)
	}
	if first, _, _ := strings.Cut(c, "/"); strings.EqualFold(first, ".git") {
		return "", fmt.Errorf("%q is inside .git", p)
	}
	return c, nil
}

func (r *Repo) hasStaged(ctx context.Context) (bool, error) {
	code, _, err := r.gitCode(ctx, nil, "diff", "--cached", "--quiet")
	if err != nil {
		return false, err
	}
	return code == 1, nil
}

// commit commits what is staged with exactly msg and returns the commit.
func (r *Repo) commit(ctx context.Context, msg string, allowEmpty bool) (string, error) {
	args := []string{"commit", "--quiet", "--no-verify", "--cleanup=verbatim", "-F", "-"}
	if allowEmpty {
		args = append(args, "--allow-empty")
	}
	if _, err := r.git(ctx, []byte(msg), args...); err != nil {
		return "", err
	}
	return r.revParse(ctx, "HEAD")
}

// push pushes HEAD to the branch, never forcing. It returns errMoved when
// the server refused because the branch moved since it was fetched.
func (r *Repo) push(ctx context.Context) error {
	if r.beforePush != nil {
		r.beforePush()
	}
	code, out, err := r.gitCode(ctx, nil, "push", "--quiet", "origin", "HEAD:refs/heads/"+cmpOr(r.WorkBranch, r.Branch))
	if err != nil {
		return err
	}
	if code == 0 {
		return nil
	}
	for _, s := range []string{"(fetch first)", "(non-fast-forward)", "(stale info)", "cannot lock ref", "incorrect old value"} {
		if strings.Contains(out, s) {
			return errMoved
		}
	}
	return fmt.Errorf("git push: %s", r.clean(out))
}

// buildMessage builds a commit message: msg, an extra line for its body if any,
// then the trailers after a blank line.
func buildMessage(msg, bodyLine, trailers string) string {
	msg = strings.TrimRight(strings.ReplaceAll(msg, "\r\n", "\n"), "\n \t")
	var b strings.Builder
	b.WriteString(msg)
	if bodyLine != "" {
		b.WriteString("\n\n" + bodyLine)
	}
	b.WriteString("\n\n" + trailers + "\n")
	return b.String()
}

func subject(msg string) string {
	s, _, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	return strings.TrimSpace(s)
}

// --- Reading history ---

// find returns the newest of the branch's last HistoryDepth commits that has
// line, a whole line of its message; with noRevert, commits that carry a
// revert trailer are passed over.
func (r *Repo) find(ctx context.Context, line string, noRevert bool) (string, error) {
	out, err := r.git(ctx, nil, "log", "-z", "--format=%H%x00%B", fmt.Sprintf("--max-count=%d", r.depth()), r.remoteRef(), "--")
	if err != nil {
		return "", err
	}
	parts := strings.Split(out, "\x00")
	for i := 0; i+1 < len(parts); i += 2 {
		hash, body := strings.TrimSpace(parts[i]), parts[i+1]
		has, reverts := false, false
		for _, l := range strings.Split(body, "\n") {
			l = strings.TrimRight(l, " \t\r")
			if l == line {
				has = true
			}
			if strings.HasPrefix(l, revertKey) {
				reverts = true
			}
		}
		if has && !(noRevert && reverts) {
			return hash, nil
		}
	}
	return "", nil
}

func (r *Repo) tip(ctx context.Context) (string, error) {
	return r.revParse(ctx, r.remoteRef())
}

func (r *Repo) revParse(ctx context.Context, rev string) (string, error) {
	out, err := r.git(ctx, nil, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("finding %s: %w", rev, err)
	}
	return strings.TrimSpace(out), nil
}

// resolve returns the commit rev names, and false if the clone has no such
// commit.
func (r *Repo) resolve(ctx context.Context, rev string) (string, bool, error) {
	code, out, err := r.gitCode(ctx, nil, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", false, err
	}
	if code != 0 {
		return "", false, nil
	}
	return strings.TrimSpace(out), true, nil
}

func (r *Repo) isAncestor(ctx context.Context, commit, of string) (bool, error) {
	code, out, err := r.gitCode(ctx, nil, "merge-base", "--is-ancestor", commit, of)
	switch {
	case err != nil:
		return false, err
	case code == 0:
		return true, nil
	case code == 1:
		return false, nil
	}
	return false, fmt.Errorf("git merge-base: %s", r.clean(out))
}

// --- Running git ---

// git runs a git command in Dir and returns its standard output. A failure
// is an error carrying git's messages, cleaned of credentials.
func (r *Repo) git(ctx context.Context, stdin []byte, args ...string) (string, error) {
	code, out, err := r.gitCode(ctx, stdin, args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git %s: %s", args[0], r.clean(out))
	}
	return out, nil
}

// gitCode runs a git command in Dir. It returns the exit code and, when that
// is 0, standard output, otherwise standard error and output together. err
// is set only when git could not be run or ctx ended.
func (r *Repo) gitCode(ctx context.Context, stdin []byte, args ...string) (int, string, error) {
	spec := gitexec.Spec{Dir: r.dir(), Args: append(r.configArgs(), args...), Env: r.env(), Inherit: gitexec.Remote,
		Secrets: r.secrets()}
	if stdin != nil {
		spec.Stdin = bytes.NewReader(stdin)
	}
	res, err := gitexec.Run(ctx, spec)
	if ctx.Err() != nil {
		return -1, "", fmt.Errorf("git %s: %w", args[0], ctx.Err())
	}
	var exit *gitexec.ExitError
	if errors.As(err, &exit) {
		return exit.Code, exit.Stderr + exit.Stdout, nil
	}
	if err != nil {
		return -1, "", fmt.Errorf("git %s: %s", args[0], r.clean(err.Error()))
	}
	return 0, res.Stdout, nil
}

// secrets are the values git's messages must never show.
func (r *Repo) secrets() []string {
	if r.Auth == nil {
		return nil
	}
	return []string{r.Auth.Password, strings.TrimPrefix(r.authHeader(), "Authorization: Basic ")}
}

// configArgs are settings given to every git command, so that the user's
// own configuration cannot sign, run hooks, store credentials or change
// line endings.
func (r *Repo) configArgs() []string {
	return []string{
		"-c", "user.name=" + r.Author.Name,
		"-c", "user.email=" + r.Author.Email,
		"-c", "commit.gpgsign=false",
		"-c", "core.autocrlf=false",
		"-c", "core.hooksPath=/dev/null",
		"-c", "credential.helper=",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		"-c", "advice.detachedHead=false",
	}
}

// env is what this repository adds to git's environment: literal
// pathspecs, and the credentials as configuration that exists only in memory.
// The runner supplies everything else, and never lets a GIT_ variable of the
// server's own through.
func (r *Repo) env() []string {
	env := []string{"GIT_LITERAL_PATHSPECS=1", "GIT_CONFIG_COUNT=0"}
	if h := r.authHeader(); h != "" {
		env = []string{"GIT_LITERAL_PATHSPECS=1", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=" + h}
	}
	return env
}

func (r *Repo) authHeader() string {
	if r.Auth == nil {
		return ""
	}
	return "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(r.Auth.Username+":"+r.Auth.Password))
}

// clean trims git's output and removes any credentials from it.
func (r *Repo) clean(s string) string {
	s = redact.URLs(strings.TrimSpace(s))
	if r.Auth != nil {
		token := strings.TrimPrefix(r.authHeader(), "Authorization: Basic ")
		s = strings.ReplaceAll(s, token, "***")
		if r.Auth.Password != "" {
			s = strings.ReplaceAll(s, r.Auth.Password, "***")
		}
	}
	return s
}

// logf writes a progress line, cleaned of credentials.
func (r *Repo) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log("%s", r.clean(fmt.Sprintf(format, args...)))
	}
}

// --- Settings ---

func (r *Repo) check() error {
	if r.URL == "" {
		return errors.New("no repository URL given")
	}
	if strings.HasPrefix(r.URL, "-") {
		return fmt.Errorf("%q is not a repository URL", r.URL)
	}
	if strings.Contains(r.URL, "://") {
		u, err := url.Parse(r.URL)
		if err != nil {
			return fmt.Errorf("the repository URL %s cannot be read", redact.URLs(r.URL))
		}
		if u.User != nil {
			return fmt.Errorf("the repository URL %s carries credentials; give them as Auth instead", redact.URLs(r.URL))
		}
	}
	if r.Dir == "" {
		return errors.New("no directory given for the clone")
	}
	if r.Branch == "" || strings.HasPrefix(r.Branch, "-") || strings.ContainsAny(r.Branch, " ~^:?*[\\\x00\x7f") ||
		strings.Contains(r.Branch, "..") || strings.Contains(r.Branch, "@{") || strings.HasSuffix(r.Branch, ".lock") ||
		strings.HasPrefix(r.Branch, "/") || strings.HasSuffix(r.Branch, "/") || strings.HasSuffix(r.Branch, ".") {
		return fmt.Errorf("%q is not a usable branch name", r.Branch)
	}
	for _, c := range r.Branch {
		if c < 0x20 {
			return fmt.Errorf("%q is not a usable branch name", r.Branch)
		}
	}
	return nil
}

func (r *Repo) checkAuthor() error {
	if strings.TrimSpace(r.Author.Name) == "" || strings.TrimSpace(r.Author.Email) == "" {
		return errors.New("the commit author needs a name and an email address")
	}
	if strings.ContainsAny(r.Author.Name+r.Author.Email, "\n\r<>") {
		return errors.New("the commit author's name and email address may not contain line breaks or angle brackets")
	}
	return nil
}

func checkName(what, s string) error {
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return fmt.Errorf("%q is not a usable %s", s, what)
	}
	return nil
}

func checkRevision(s string) error {
	if s == "" || strings.HasPrefix(s, "-") || strings.ContainsAny(s, " \t\r\n\x00") {
		return fmt.Errorf("%q is not a usable revision", s)
	}
	return nil
}

func (r *Repo) dir() string {
	d, err := filepath.Abs(r.Dir)
	if err != nil {
		return filepath.Clean(r.Dir)
	}
	return d
}

// url is URL as stored as the clone's remote: a relative local path is made
// absolute, since git would otherwise read it relative to the clone.
func (r *Repo) url() string {
	u := r.URL
	if strings.Contains(u, "://") || filepath.IsAbs(u) {
		return u
	}
	if i := strings.IndexByte(u, ':'); i >= 0 && !strings.Contains(u[:i], "/") {
		return u // host:path
	}
	if abs, err := filepath.Abs(u); err == nil {
		return abs
	}
	return u
}

func (r *Repo) remoteRef() string { return "refs/remotes/origin/" + r.Branch }

func (r *Repo) tries() int {
	if r.MaxPushTries > 0 {
		return r.MaxPushTries
	}
	return 12
}

// sleep waits before a push is tried again.
func (r *Repo) sleep(ctx context.Context, try int) error {
	d := time.Duration(try) * 40 * time.Millisecond
	d += rand.N(d + 1) // #nosec G404 -- spreading retries, not security
	d = min(d, time.Second)
	if r.Wait != nil {
		d = r.Wait(try)
	}
	if d <= 0 {
		return ctx.Err()
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Repo) depth() int {
	if r.HistoryDepth > 0 {
		return r.HistoryDepth
	}
	return 200
}

func splitNUL(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
