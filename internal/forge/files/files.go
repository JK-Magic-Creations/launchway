package files

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/jk-magic-creations/launchway/internal/forge/signing"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// Limits.
const (
	MaxFileBytes   = 1 << 20 // a file read or written through this package
	MaxCommits     = 200
	DefaultCommits = 30
	MaxPathBytes   = 1024
	MaxMessage     = 10000
)

// Service reads and writes repositories through the hosting storage.
type Service struct {
	Git   *storage.Store
	Clock func() time.Time
	// Sign, when set, signs the commits a web edit or upload makes with the installation's key, as merges are.
	// When it cannot (there is no root key), the commit is made unsigned: an edit is never refused for that.
	Sign func(ctx context.Context, payload []byte) ([]byte, error)
}

// signed returns the commit as the installation signs it, or the commit itself when signing is off or fails.
func (s *Service) signed(ctx context.Context, g runner, sha string) string {
	if s.Sign == nil {
		return sha
	}
	raw, err := g.run(ctx, nil, "cat-file", "commit", sha)
	if err != nil {
		return sha
	}
	sig, err := s.Sign(ctx, []byte(raw))
	if err != nil {
		return sha
	}
	signed, err := signing.AddCommitSignature([]byte(raw), sig)
	if err != nil {
		return sha
	}
	out, err := g.run(ctx, signed, "hash-object", "-t", "commit", "-w", "--stdin")
	if err != nil {
		return sha
	}
	return strings.TrimSpace(out)
}

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

type runner struct {
	dir     string
	limits  gitexec.Limits
	scratch bool
	env     []string
}

func (g runner) run(ctx context.Context, stdin []byte, args ...string) (string, error) {
	sp := gitexec.Spec{GitDir: g.dir, Args: args, Timeout: 2 * time.Minute, Limits: g.limits, AllowAlternates: g.scratch, Env: g.env, MaxOutput: 8 << 20}
	if stdin != nil {
		sp.Stdin = bytes.NewReader(stdin)
	}
	res, err := gitexec.Run(ctx, sp)
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return res.Stdout, fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(ee.Stderr+" "+ee.Stdout))
		}
		return res.Stdout, fmt.Errorf("git %s: %w", args[0], err)
	}
	return res.Stdout, nil
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{forge.ErrInvalid}, a...)...)
}
func missing(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{forge.ErrNotFound}, a...)...)
}

// CleanPath checks a path inside a repository and returns it in its plain
// form: slashes only, no empty, "." or ".." parts, no control characters, no
// ".git" part. The empty path is the root.
func CleanPath(p string) (string, error) {
	if p == "" || p == "/" {
		return "", nil
	}
	if len(p) > MaxPathBytes || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00") {
		return "", invalid("that is not a path inside a repository")
	}
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	for _, part := range strings.Split(p, "/") {
		switch {
		case part == "" || part == "." || part == "..":
			return "", invalid("%q is not a path inside a repository: no empty, . or .. parts", p)
		case strings.EqualFold(part, ".git"):
			return "", invalid("a path may not contain a .git part")
		}
		for _, r := range part {
			if r < 0x20 || r == 0x7f {
				return "", invalid("a path may not contain control characters")
			}
		}
	}
	return path.Clean(p), nil
}

// checkRef refuses a ref that git could take for an option or a range.
func checkRef(ref string) error {
	if ref == "" || len(ref) > 200 || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00 ~^:?*[\\") || strings.Contains(ref, "..") {
		return invalid("%q is not a branch, tag or commit", ref)
	}
	return nil
}

// resolve turns a ref into a commit, or says there is none.
func (g runner) resolve(ctx context.Context, ref string) (string, error) {
	if err := checkRef(ref); err != nil {
		return "", err
	}
	out, err := g.run(ctx, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil || strings.TrimSpace(out) == "" {
		return "", missing("no branch, tag or commit %q", ref)
	}
	return strings.TrimSpace(out), nil
}

// Commit is one entry of a log.
type Commit struct {
	SHA       string `json:"sha"`
	Subject   string `json:"subject"`
	Message   string `json:"message"`
	Author    string `json:"author"`
	AuthorAt  string `json:"authored_at"`
	Committer string `json:"committer"`
	CommitAt  string `json:"committed_at"`
	Parents   int    `json:"parents"`
	// ParentSHAs are the commit's parents, first parent first.
	ParentSHAs []string `json:"parent_shas"`
}

// Commits lists the commits reachable from ref, newest first, optionally only
// those that changed path.
func (s *Service) Commits(ctx context.Context, repoID, ref, p string, limit int) ([]Commit, error) {
	if limit <= 0 {
		limit = DefaultCommits
	}
	limit = min(limit, MaxCommits)
	p, err := CleanPath(p)
	if err != nil {
		return nil, err
	}
	var out []Commit
	err = s.Git.WithCache(ctx, repoID, func(dir string) error {
		g := runner{dir: dir, limits: s.Git.Limits()}
		sha, err := g.resolve(ctx, ref)
		if err != nil {
			return err
		}
		args := []string{"log", "-n", strconv.Itoa(limit), "--format=%H%x1f%an%x1f%aI%x1f%cn%x1f%cI%x1f%P%x1f%B%x1e", sha}
		if p != "" {
			// The history of one file follows it through renames; that of a folder does not.
			if kind, err := g.run(ctx, nil, "cat-file", "-t", "--end-of-options", sha+":"+p); err == nil && strings.TrimSpace(kind) == "blob" {
				args = append(args[:len(args)-1], "--follow", sha)
			}
			args = append(args, "--", p)
		}
		raw, err := g.run(ctx, nil, args...)
		if err != nil {
			return err
		}
		for _, rec := range strings.Split(raw, "\x1e") {
			rec = strings.TrimPrefix(rec, "\n")
			f := strings.SplitN(rec, "\x1f", 7)
			if len(f) < 7 {
				continue
			}
			msg := strings.TrimRight(f[6], "\n")
			subject, _, _ := strings.Cut(msg, "\n")
			out = append(out, Commit{SHA: f[0], Author: f[1], AuthorAt: f[2], Committer: f[3], CommitAt: f[4], Parents: len(strings.Fields(f[5])), ParentSHAs: strings.Fields(f[5]), Subject: subject, Message: msg})
		}
		return nil
	})
	if out == nil {
		out = []Commit{}
	}
	return out, err
}

// Entry is one item of a directory.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // file, dir, symlink or submodule
	Size int64  `json:"size,omitempty"`
	SHA  string `json:"sha"`
	// Last is the newest commit that changed the entry: set only when the
	// caller asked for it (ReadWithLast); nil when it lies beyond the search
	// bound or was not asked for.
	Last *LastCommit `json:"last,omitempty"`
}

// Item is what a path is: a directory with its entries, or a file with its
// text (not for a file over the limit).
type Item struct {
	Path      string  `json:"path"`
	Type      string  `json:"type"` // dir or file
	Ref       string  `json:"ref"`
	Commit    string  `json:"commit"`
	SHA       string  `json:"sha"` // the blob's or the tree's
	Size      int64   `json:"size,omitempty"`
	Encoding  string  `json:"encoding,omitempty"` // utf8 or base64
	Content   string  `json:"content,omitempty"`
	Truncated bool    `json:"truncated,omitempty"` // a file over the limit: no content
	Entries   []Entry `json:"entries,omitempty"`
	// Mode is the file's git mode (100644, 100755, 120000, 160000).
	Mode string `json:"mode,omitempty"`
	// LastBounded says the last-commit column stopped at its search bound:
	// entries without Last were not changed within it.
	LastBounded bool `json:"last_bounded,omitempty"`
}

// Read looks up a path at a ref ("" is the root).
func (s *Service) Read(ctx context.Context, repoID, ref, p string) (Item, error) {
	p, err := CleanPath(p)
	if err != nil {
		return Item{}, err
	}
	var it Item
	err = s.Git.WithCache(ctx, repoID, func(dir string) error {
		g := runner{dir: dir, limits: s.Git.Limits()}
		commit, err := g.resolve(ctx, ref)
		if err != nil {
			return err
		}
		spec := commit + ":" + p
		if p == "" {
			spec = commit + "^{tree}"
		}
		mode := ""
		if p != "" {
			ls, err := g.run(ctx, nil, "ls-tree", "-z", "--end-of-options", commit, "--", p)
			if err != nil || strings.TrimSpace(ls) == "" {
				return missing("no %q at %s", p, ref)
			}
			mode = strings.Fields(ls)[0]
			if mode == "160000" {
				f := strings.Fields(strings.SplitN(ls, "\t", 2)[0])
				it = Item{Path: p, Ref: ref, Commit: commit, Type: "submodule", Mode: mode}
				if len(f) >= 3 {
					it.SHA = f[2]
				}
				return nil
			}
		}
		kind, err := g.run(ctx, nil, "cat-file", "-t", "--end-of-options", spec)
		if err != nil {
			return missing("no %q at %s", p, ref)
		}
		sha, _ := g.run(ctx, nil, "rev-parse", "--verify", "--end-of-options", spec)
		it = Item{Path: p, Ref: ref, Commit: commit, SHA: strings.TrimSpace(sha), Mode: mode}
		switch strings.TrimSpace(kind) {
		case "tree":
			it.Type = "dir"
			treeish := commit + ":" + p
			if p == "" {
				treeish = commit
			}
			raw, err := g.run(ctx, nil, "ls-tree", "-z", "-l", "--end-of-options", treeish)
			if err != nil {
				return err
			}
			it.Entries = parseTree(raw, p)
		case "blob":
			it.Type = "file"
			if it.Mode == "120000" {
				it.Type = "symlink"
			}
			szs, err := g.run(ctx, nil, "cat-file", "-s", "--end-of-options", spec)
			if err != nil {
				return err
			}
			it.Size, _ = strconv.ParseInt(strings.TrimSpace(szs), 10, 64)
			if it.Size > MaxFileBytes {
				it.Truncated = true
				return nil
			}
			body, err := g.run(ctx, nil, "cat-file", "blob", "--end-of-options", it.SHA)
			if err != nil {
				return err
			}
			if utf8.ValidString(body) && !strings.ContainsRune(body, 0) {
				it.Encoding, it.Content = "utf8", body
			} else {
				it.Encoding, it.Content = "base64", base64.StdEncoding.EncodeToString([]byte(body))
			}
		default:
			return invalid("%q is a %s, not a file or a directory", p, strings.TrimSpace(kind))
		}
		return nil
	})
	return it, err
}

func parseTree(raw, dir string) []Entry {
	out := []Entry{}
	for _, rec := range strings.Split(raw, "\x00") {
		meta, name, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta) // mode type sha size
		if len(f) < 4 {
			continue
		}
		e := Entry{Name: name, Path: path.Join(dir, name), SHA: f[2]}
		switch {
		case f[0] == "120000":
			e.Type = "symlink"
		case f[1] == "tree":
			e.Type = "dir"
		case f[1] == "commit":
			e.Type = "submodule"
		default:
			e.Type = "file"
		}
		if n, err := strconv.ParseInt(f[3], 10, 64); err == nil && e.Type == "file" {
			e.Size = n
		}
		out = append(out, e)
	}
	return out
}

// Write is a request to add or change one file in one commit.
type Write struct {
	Branch  string
	Path    string
	Content []byte
	Message string
	// ExpectedSHA is the blob the caller is replacing: "" means the file must not exist yet.
	ExpectedSHA string
	Actor       string
	// Delete removes the file instead of writing it; ExpectedSHA is then required.
	Delete bool
}

// Written says what a write did.
type Written struct {
	Branch string `json:"branch"`
	Path   string `json:"path"`
	Commit string `json:"commit"`
	SHA    string `json:"sha"` // the file's blob
	Parent string `json:"parent"`
}

// ErrConflict is returned when the file is not as the caller expected.
var ErrConflict = errors.New("conflict")

// Check validates what Write would refuse without touching the repository.
func (w Write) Check(branch string) (string, error) {
	p, err := CleanPath(w.Path)
	if err != nil || p == "" {
		return "", invalid("a file needs a path inside the repository")
	}
	switch {
	case w.Delete && w.ExpectedSHA == "":
		return "", invalid("deleting a file needs expected_sha, its current id")
	case len(w.Content) > MaxFileBytes:
		return "", invalid("a file written here may be at most %d bytes", MaxFileBytes)
	case strings.TrimSpace(w.Message) == "" || len(w.Message) > MaxMessage:
		return "", invalid("a commit needs a message of at most %d bytes", MaxMessage)
	case checkRef(branch) != nil || strings.HasPrefix(branch, "refs/"):
		return "", invalid("%q is not a branch name", branch)
	case w.ExpectedSHA != "" && !isSHA(w.ExpectedSHA):
		return "", invalid("expected_sha is the 40-character id of the file being replaced")
	}
	return p, nil
}

func isSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Put makes one commit on a branch that adds the file (ExpectedSHA empty: it
// must not exist) or replaces it (ExpectedSHA is the blob being replaced). The
// authorization and the branch rules are the caller's; the update is a
// compare-and-swap on the branch, so a push in between makes it fail, not lose
// anything.
func (s *Service) Put(ctx context.Context, repoID string, w Write, check func(ref, old, next string) error) (Written, error) {
	p, err := w.Check(w.Branch)
	if err != nil {
		return Written{}, err
	}
	var out Written
	_, err = s.Git.Mutate(ctx, repoID, func(ctx context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
		ref := "refs/heads/" + w.Branch
		tip := refs[ref]
		if tip == "" {
			return nil, missing("the branch %s does not exist", w.Branch)
		}
		g := runner{dir: scratch, limits: s.Git.Limits(), scratch: true}
		cur, curErr := g.run(ctx, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", tip+":"+p)
		cur = strings.TrimSpace(cur)
		switch {
		case curErr == nil && cur != "":
			kind, _ := g.run(ctx, nil, "cat-file", "-t", "--end-of-options", tip+":"+p)
			if strings.TrimSpace(kind) != "blob" {
				return nil, fmt.Errorf("%w: %s is a directory", ErrConflict, p)
			}
			if w.ExpectedSHA == "" {
				return nil, fmt.Errorf("%w: %s exists already; send expected_sha (its current id, %s) to replace it", ErrConflict, p, cur)
			}
			if w.ExpectedSHA != cur {
				return nil, fmt.Errorf("%w: %s is %s now, not %s: read it again", ErrConflict, p, cur, w.ExpectedSHA)
			}
		case w.ExpectedSHA != "":
			return nil, fmt.Errorf("%w: %s does not exist on %s, so there is nothing to replace", ErrConflict, p, w.Branch)
		}
		idx := filepath.Join(scratch, "lw-files-index")
		defer os.Remove(idx)
		g.env = []string{"GIT_INDEX_FILE=" + idx}
		if _, err := g.run(ctx, nil, "read-tree", tip); err != nil {
			return nil, err
		}
		var blob string
		if w.Delete {
			if _, err := g.run(ctx, []byte("0 "+strings.Repeat("0", len(tip))+"\t"+p+"\n"), "update-index", "--index-info"); err != nil {
				return nil, err
			}
		} else {
			b, err := g.run(ctx, w.Content, "hash-object", "-w", "--stdin")
			if err != nil {
				return nil, err
			}
			blob = strings.TrimSpace(b)
		}
		mode := "100644"
		if cur != "" {
			if ls, err := g.run(ctx, nil, "ls-tree", "--end-of-options", tip, "--", p); err == nil {
				if f := strings.Fields(ls); len(f) > 0 && (f[0] == "100755" || f[0] == "100644") {
					mode = f[0]
				}
			}
		}
		if !w.Delete {
			if _, err := g.run(ctx, nil, "update-index", "--add", "--cacheinfo", mode+","+blob+","+p); err != nil {
				return nil, fmt.Errorf("%w: %s cannot be placed there (a file stands where a directory is needed, or the reverse)", ErrConflict, p)
			}
		}
		tree, err := g.run(ctx, nil, "write-tree")
		if err != nil {
			return nil, err
		}
		g.env = identEnv(w.Actor, s.now())
		msg := strings.TrimRight(w.Message, "\n") + "\n"
		commit, err := g.run(ctx, nil, "commit-tree", strings.TrimSpace(tree), "-p", tip, "-m", msg)
		if err != nil {
			return nil, err
		}
		commit = s.signed(ctx, g, strings.TrimSpace(commit))
		if check != nil {
			if err := check(ref, tip, commit); err != nil {
				return nil, err
			}
		}
		out = Written{Branch: w.Branch, Path: p, Commit: commit, SHA: blob, Parent: tip}
		return []storage.RefUpdate{{Name: ref, Old: tip, New: commit}}, nil
	})
	return out, err
}

func identEnv(name string, now time.Time) []string {
	if strings.TrimSpace(name) == "" {
		name = "launchway"
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '<' || r == '>' || r == 0x7f {
			return -1
		}
		return r
	}, name)
	date := fmt.Sprintf("%d +0000", now.Unix())
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + name + "@users.noreply.launchway.invalid", "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=Launchway", "GIT_COMMITTER_EMAIL=noreply@launchway.invalid", "GIT_COMMITTER_DATE=" + date,
	}
}

// IsStale says whether a write lost a race: the branch moved while the commit
// was being made, and nothing was changed.
func IsStale(err error) bool {
	var stale *storage.ErrStale
	return errors.As(err, &stale)
}
