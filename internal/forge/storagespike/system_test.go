package storagespike

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// PushOutcome is what a push came to, as the client saw it.
type PushOutcome struct {
	// Acked: git push exited 0, so the server said every ref was updated.
	// This is the only definition of "acknowledged" the properties use.
	Acked    bool
	Stale    bool // the server refused because the branch moved
	Output   string
	Duration time.Duration
}

// Server is one server process-set of a candidate.
type Server interface {
	Name() string
	// Push pushes refspecs from a client working repository, with a fault
	// injected into the git processes of this one push.
	Push(ctx context.Context, client string, f Fault, refspecs ...string) (PushOutcome, error)
	// Clone makes a mirror clone of what this server serves now (after
	// making itself current), into dest.
	Clone(ctx context.Context, dest string) error
	// WipeLocal deletes the local, disposable state of this server (candidate
	// B's cache). For candidate A there is none: it returns ErrNoCache.
	WipeLocal() error
}

// ErrNoCache is returned by WipeLocal when the server's disk is the data.
var ErrNoCache = errors.New("this candidate has no disposable cache: the volume is the data")

// System is a candidate with its shared infrastructure and N servers.
type System interface {
	Name() string
	NewServer(name string) (Server, error)
	// Authoritative returns the refs of record: the database for B, the
	// volume for A.
	Authoritative(ctx context.Context) (map[string]string, error)
	// LoseNode simulates the loss of the node that holds this candidate's
	// local state, followed by a restart on an empty disk and a restore from
	// whatever off-node copy exists.
	LoseNode(ctx context.Context) error
	// Maintain runs repacking and garbage collection.
	Maintain(ctx context.Context) error
	// Close releases the shared infrastructure.
	Close() error
}

// ---------------------------------------------------------------- clients

// Client is a working repository the tests push from.
type Client struct{ Dir string }

var gitIdentity = []string{"GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=dana@example.com", "GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=dana@example.com"}

// NewClient makes an empty working repository on branch main.
func NewClient(ctx context.Context, dir string) (*Client, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	c := &Client{Dir: dir}
	if _, err := c.git(ctx, "init", "-q", "-b", "main"); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) git(ctx context.Context, args ...string) (string, error) {
	res, err := gitexec.Run(ctx, gitexec.Spec{Dir: c.Dir, Args: args, Env: gitIdentity, Timeout: 30 * time.Minute})
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return res.Stdout, fmt.Errorf("git %s: %s", args[0], ee.Output())
		}
		return res.Stdout, err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// Commit adds a file of random content of the given size (and a text file,
// so the change is real) and commits it on the current branch. Random content
// does not compress, so the pack is about as big as the data.
func (c *Client) Commit(ctx context.Context, name string, size int, seed int64) (string, error) {
	rng := rand.New(rand.NewSource(seed))
	buf := make([]byte, size)
	rng.Read(buf)
	if err := os.WriteFile(filepath.Join(c.Dir, name), buf, 0o644); err != nil {
		return "", err
	}
	if _, err := c.git(ctx, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := c.git(ctx, "commit", "-q", "-m", fmt.Sprintf("%s %d", name, seed)); err != nil {
		return "", err
	}
	return c.git(ctx, "rev-parse", "HEAD")
}

// Head is the current commit.
func (c *Client) Head(ctx context.Context) (string, error) { return c.git(ctx, "rev-parse", "HEAD") }

// Branch makes and switches to a branch at a commit.
func (c *Client) Branch(ctx context.Context, name, at string) error {
	_, err := c.git(ctx, "checkout", "-q", "-B", name, at)
	return err
}

// Reset moves the current branch to a commit (the next commit then forks from
// there).
func (c *Client) Reset(ctx context.Context, to string) error {
	_, err := c.git(ctx, "reset", "-q", "--hard", to)
	return err
}

// Pull makes the client's branch what the server has.
func (c *Client) Fetch(ctx context.Context, from, ref string) error {
	_, err := c.git(ctx, "fetch", "-q", from, ref)
	return err
}

// Merge is not needed; clients only fast-forward.

// IsAncestor reports whether a is an ancestor of (or equal to) b in the
// repository at dir.
func IsAncestor(ctx context.Context, dir, a, b string) bool {
	_, err := gitexec.Run(ctx, gitexec.Spec{GitDir: dir, Args: []string{"merge-base", "--is-ancestor", a, b}})
	return err == nil
}

// ---------------------------------------------------------------- checks

// Fingerprint identifies what a repository holds: its refs and the SHA-256 of
// the list of every object reachable from them. Two repositories with the
// same fingerprint hold the same history, byte for byte as git sees it.
func Fingerprint(ctx context.Context, dir string) (string, error) {
	refs, err := git(ctx, dir, "", "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return "", err
	}
	objs, err := git(ctx, dir, "", "rev-list", "--all", "--objects")
	if err != nil && strings.TrimSpace(refs) != "" {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(objs), "\n")
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.TrimSpace(refs) + "\n--\n" + strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

// Fsck runs git fsck --full on a repository.
func Fsck(ctx context.Context, dir string) error {
	_, err := git(ctx, dir, "", "fsck", "--full", "--strict", "--no-dangling")
	return err
}

// RefsOf lists a repository's refs.
func RefsOf(ctx context.Context, dir string) (map[string]string, error) {
	out, err := git(ctx, dir, "", "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if n, t, ok := strings.Cut(line, " "); ok {
			m[n] = t
		}
	}
	return m, nil
}

// DirSize is the number of bytes under a directory.
func DirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// cloneMirror makes a mirror clone of a repository through the upload-pack
// transport (not a local hard-link copy).
func cloneMirror(ctx context.Context, src, dest string) error {
	_, err := gitexec.Run(ctx, gitexec.Spec{Args: []string{"clone", "-q", "--mirror", "--no-local", "file://" + src, dest}, Timeout: 30 * time.Minute})
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("clone: %s", ee.Output())
		}
	}
	return err
}

// copyTree copies a directory tree (cp -a), preserving what git needs.
func copyTree(src, dest string) error {
	if out, err := exec.Command("cp", "-a", src, dest).CombinedOutput(); err != nil { //nolint:gosec // the spike's own temp directories
		return fmt.Errorf("cp: %v: %s", err, out)
	}
	return nil
}

// runPush runs the client's git push to a server's repository through the
// wrapper, with a fault in the environment of this one push.
func runPush(ctx context.Context, cfgPath, wrapper, repo, client string, f Fault, refspecs []string) (PushOutcome, error) {
	args := append([]string{"push", "--atomic", "--receive-pack=" + wrapper, repo}, refspecs...)
	env := append([]string{CfgEnv + "=" + cfgPath}, f.Env()...)
	env = append(env, gitIdentity...)
	start := time.Now()
	res, err := gitexec.Run(ctx, gitexec.Spec{Dir: client, Args: args, Env: env, Timeout: 30 * time.Minute})
	out := PushOutcome{Duration: time.Since(start)}
	var ee *gitexec.ExitError
	switch {
	case err == nil:
		out.Acked = true
		out.Output = res.Stderr
	case errors.As(err, &ee):
		out.Output = ee.Output()
	default:
		// The client's git was killed with the machine, or timed out: not acknowledged.
		out.Output = err.Error()
	}
	out.Stale = strings.Contains(out.Output, "the branch moved") || strings.Contains(out.Output, "cannot lock ref") || strings.Contains(out.Output, "failed to update ref")
	return out, nil
}

func writeRandom(path string, size int, seed int64) error {
	buf := make([]byte, size)
	rand.New(rand.NewSource(seed)).Read(buf)
	return os.WriteFile(path, buf, 0o644)
}

// WipeLocalOrNil wipes a server's local state when it has any.
func WipeLocalOrNil(s Server) (bool, error) {
	if err := s.WipeLocal(); err != nil {
		if errors.Is(err, ErrNoCache) {
			return false, ErrNoCache
		}
		return false, err
	}
	return true, nil
}
