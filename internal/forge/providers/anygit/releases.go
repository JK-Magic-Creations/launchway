package anygit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type releases struct{ p *Provider }

var (
	tagNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,200}$`)
	branchRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,200}$`)
	commitRE  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func validName(re *regexp.Regexp, name string) bool {
	return re.MatchString(name) && !strings.Contains(name, "..") && !strings.Contains(name, "//") && !strings.HasSuffix(name, ".lock") &&
		!strings.HasSuffix(name, "/") && !strings.HasSuffix(name, ".")
}

// remoteTag asks the server what a tag points to: git ls-remote of the exact tag
// and its peeled form (the line "<id> refs/tags/<name>^{}" names the commit of
// an annotated tag).
func (r releases) remoteTag(ctx context.Context, path, dir, name string) (commit string, found bool, err error) {
	refs, _, err := r.p.lsRemote(ctx, path, dir, false, "refs/tags/"+name, "refs/tags/"+name+"^{}")
	if err != nil {
		return "", false, err
	}
	for _, ref := range refs {
		switch ref.Name {
		case "refs/tags/" + name + "^{}":
			return ref.Commit, true, nil // the commit an annotated tag points to wins
		case "refs/tags/" + name:
			commit, found = ref.Commit, true
		}
	}
	return commit, found, nil
}

// CreateTag makes an annotated tag with git and pushes that one tag: an empty
// repository in a scratch directory, a fetch of the branches (the commit must be
// there to be tagged), `git tag -a` and `git push` of the tag. A tag that
// already points at that commit is accepted; one that points elsewhere is
// refused. The account needs write (push) permission on the repository.
func (r releases) CreateTag(ctx context.Context, repo forge.Repository, t forge.TagSpec) (forge.Ref, error) {
	u, err := r.p.gitURL(repo.Path)
	if err != nil {
		return forge.Ref{}, err
	}
	if !validName(tagNameRE, t.Name) || !commitRE.MatchString(t.Commit) {
		return forge.Ref{}, fmt.Errorf("%w: a tag is a plain name and a full commit id", forge.ErrInvalid)
	}
	dir, err := scratch()
	if err != nil {
		return forge.Ref{}, err
	}
	defer os.RemoveAll(dir)
	want := forge.Ref{Name: "refs/tags/" + t.Name, Commit: t.Commit}
	have, found, err := r.remoteTag(ctx, repo.Path, dir, t.Name)
	if err != nil {
		return forge.Ref{}, err
	}
	if found {
		if have != t.Commit {
			return forge.Ref{}, fmt.Errorf("%w: the tag %s already exists on the server and stands for another commit", forge.ErrExists, t.Name)
		}
		return want, nil
	}
	for _, s := range [][]string{
		{"init", "--bare", "--quiet", "."},
		{"fetch", "--no-tags", "--quiet", u.String(), "+refs/heads/*:refs/heads/*"},
	} {
		if _, err := r.p.git(ctx, repo.Path, dir, s...); err != nil {
			return forge.Ref{}, err
		}
	}
	if kind, err := r.p.git(ctx, repo.Path, dir, "cat-file", "-t", t.Commit); err != nil || strings.TrimSpace(kind) != "commit" {
		return forge.Ref{}, fmt.Errorf("%w: the commit %s is not on any branch of the server", forge.ErrNotFound, t.Commit)
	}
	if _, err := r.p.git(ctx, repo.Path, dir, "tag", "-a", "-m", t.Message, t.Name, t.Commit); err != nil {
		return forge.Ref{}, err
	}
	if _, err := r.p.git(ctx, repo.Path, dir, "push", "--quiet", u.String(), "refs/tags/"+t.Name+":refs/tags/"+t.Name); err != nil {
		return forge.Ref{}, err
	}
	return want, nil
}

// PublishRelease: a plain Git server has no release object; see Capabilities.
func (r releases) PublishRelease(context.Context, forge.Repository, forge.ReleaseSpec) (string, error) {
	return "", r.p.Caps.Err(forge.CapReleases)
}

// UpsertSummary: no pull request to comment on; see Capabilities.
func (r releases) UpsertSummary(context.Context, forge.Repository, int, string, string) (string, error) {
	return "", r.p.Caps.Err(forge.CapProposalNote)
}

// maxFile is the largest file a change writes.
const maxFile = 16 << 20

// CommitFile writes one file and pushes it: a shallow fetch of the branch (the
// new branch when it already exists, else the base branch) into a scratch
// repository, the file written under it (no path may leave the working tree or
// pass through a link), one commit by the Launchway identity, and a push of that
// commit, never forced, to the new branch or to the branch itself. The same
// content again is not an error: the branch's head is returned. A branch the
// server protects refuses the push, and the server's own message comes back.
func (r releases) CommitFile(ctx context.Context, repo forge.Repository, ch forge.FileChange) (string, error) {
	u, err := r.p.gitURL(repo.Path)
	if err != nil {
		return "", err
	}
	rel, err := cleanPath(ch.Path)
	if err != nil || !validName(branchRE, ch.Branch) || (ch.NewBranch != "" && !validName(branchRE, ch.NewBranch)) {
		return "", fmt.Errorf("%w: a file change names a branch and a path inside the repository", forge.ErrInvalid)
	}
	if len(ch.Content) > maxFile {
		return "", fmt.Errorf("%w: a file is at most %d MB here", forge.ErrInvalid, maxFile>>20)
	}
	dir, err := scratch()
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	target, from := ch.Branch, ch.Branch
	if ch.NewBranch != "" {
		target = ch.NewBranch
		refs, _, err := r.p.lsRemote(ctx, repo.Path, dir, false, "refs/heads/"+ch.NewBranch)
		if err != nil {
			return "", err
		}
		if len(refs) > 0 {
			from = ch.NewBranch
		}
	}
	for _, s := range [][]string{
		{"init", "--quiet", "."},
		{"fetch", "--depth", "1", "--no-tags", "--quiet", u.String(), "+refs/heads/" + from + ":refs/remotes/origin/" + from},
		{"checkout", "--quiet", "-B", "work", "refs/remotes/origin/" + from},
	} {
		if _, err := r.p.git(ctx, repo.Path, dir, s...); err != nil {
			return "", err
		}
	}
	if err := writeInside(dir, rel, ch.Content); err != nil {
		return "", err
	}
	if _, err := r.p.git(ctx, repo.Path, dir, "add", "--", rel); err != nil {
		return "", err
	}
	if st, err := r.p.git(ctx, repo.Path, dir, "status", "--porcelain", "--", rel); err != nil {
		return "", err
	} else if strings.TrimSpace(st) == "" {
		return r.head(ctx, repo.Path, dir)
	}
	if _, err := r.p.git(ctx, repo.Path, dir, "commit", "--quiet", "-m", ch.Message); err != nil {
		return "", err
	}
	id, err := r.head(ctx, repo.Path, dir)
	if err != nil {
		return "", err
	}
	if _, err := r.p.git(ctx, repo.Path, dir, "push", "--quiet", u.String(), "HEAD:refs/heads/"+target); err != nil {
		return "", err
	}
	return id, nil
}

func (r releases) head(ctx context.Context, path, dir string) (string, error) {
	out, err := r.p.git(ctx, path, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// cleanPath makes a path inside the repository: relative, no "..", nothing of
// Git's own directory.
func cleanPath(p string) (string, error) {
	c := filepath.ToSlash(filepath.Clean(p))
	if p == "" || strings.HasPrefix(c, "/") || c == "." || c == ".." || strings.HasPrefix(c, "../") || strings.ContainsAny(p, "\x00\\") {
		return "", errors.New("not a path inside the repository")
	}
	for _, seg := range strings.Split(c, "/") {
		if strings.EqualFold(seg, ".git") {
			return "", errors.New("not a path inside the repository")
		}
	}
	return c, nil
}

// writeInside writes the file below dir, refusing a path whose parent is a link
// (a repository from outside may hold one that points out of the working tree).
func writeInside(dir, rel string, content []byte) error {
	cur := dir
	parts := strings.Split(rel, "/")
	for i, seg := range parts {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if i < len(parts)-1 {
				if err := os.Mkdir(cur, 0o750); err != nil {
					return err
				}
			}
		case err != nil:
			return err
		case fi.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%w: %s is a link in the repository; Launchway does not write through links", forge.ErrInvalid, strings.Join(parts[:i+1], "/"))
		case i < len(parts)-1 && !fi.IsDir():
			return fmt.Errorf("%w: %s is a file, not a directory", forge.ErrInvalid, strings.Join(parts[:i+1], "/"))
		case i == len(parts)-1 && fi.IsDir():
			return fmt.Errorf("%w: %s is a directory", forge.ErrInvalid, rel)
		}
	}
	return os.WriteFile(cur, content, 0o644) //nolint:gosec // G306: a file of a repository, read by git; it is not a secret
}
