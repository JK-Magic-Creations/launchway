package codecommit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type releases struct{ p *Provider }

var (
	tagNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,200}$`)
	commitRE  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func validTag(name string) bool {
	return tagNameRE.MatchString(name) && !strings.Contains(name, "..") && !strings.Contains(name, "//") && !strings.HasSuffix(name, ".lock") &&
		!strings.HasSuffix(name, "/") && !strings.HasSuffix(name, ".")
}

// remoteTag asks the host what a tag points to: git ls-remote of the exact tag
// and its peeled form (the line "<id> refs/tags/<name>^{}" names the commit of
// an annotated tag). Found is false when the tag is not there.
func (r releases) remoteTag(ctx context.Context, repo forge.Repository, dir, name string) (commit string, found bool, err error) {
	u, err := r.p.gitURL(repo)
	if err != nil {
		return "", false, err
	}
	out, err := r.p.git(ctx, repo, dir, false, "ls-remote", u.String(), "refs/tags/"+name, "refs/tags/"+name+"^{}")
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(out, "\n") {
		id, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		switch ref {
		case "refs/tags/" + name + "^{}":
			return id, true, nil // the commit an annotated tag points to wins
		case "refs/tags/" + name:
			commit, found = id, true
		}
	}
	return commit, found, nil
}

// CreateTag makes an annotated tag and pushes it, because CodeCommit has no API
// call that creates a tag (the API reference's list of calls, Welcome page, has
// branches, commits, files and pull requests but no tags; its "Tags" are AWS
// resource tags, not Git's). It runs the product's own git, no shell: an empty
// bare repository in a scratch directory, a fetch of the branches (the commit
// must be there to be tagged), `git tag -a` and `git push` of the one tag, with
// the credentials of the connection signed for each request (git.go). A tag
// that already points at that commit is accepted; one that points elsewhere is
// refused. It needs the IAM permissions GitPull and GitPush on the repository.
func (r releases) CreateTag(ctx context.Context, repo forge.Repository, t forge.TagSpec) (forge.Ref, error) {
	if _, err := r.p.repoName(repo); err != nil {
		return forge.Ref{}, err
	}
	if !validTag(t.Name) || !commitRE.MatchString(t.Commit) {
		return forge.Ref{}, fmt.Errorf("%w: a tag is a plain name and a full commit id", forge.ErrInvalid)
	}
	dir, err := scratch()
	if err != nil {
		return forge.Ref{}, err
	}
	defer os.RemoveAll(dir)
	want := forge.Ref{Name: "refs/tags/" + t.Name, Commit: t.Commit}
	have, found, err := r.remoteTag(ctx, repo, dir, t.Name)
	if err != nil {
		return forge.Ref{}, err
	}
	if found {
		if have != t.Commit {
			return forge.Ref{}, fmt.Errorf("%w: the tag %s already exists on the host and stands for another commit", forge.ErrExists, t.Name)
		}
		return want, nil
	}
	u, _ := r.p.gitURL(repo)
	steps := [][]string{
		{"init", "--bare", "--quiet", "."},
		{"fetch", "--no-tags", "--quiet", u.String(), "+refs/heads/*:refs/heads/*"},
	}
	for _, s := range steps {
		if _, err := r.p.git(ctx, repo, dir, false, s...); err != nil {
			return forge.Ref{}, err
		}
	}
	if kind, err := r.p.git(ctx, repo, dir, false, "cat-file", "-t", t.Commit); err != nil || strings.TrimSpace(kind) != "commit" {
		return forge.Ref{}, fmt.Errorf("%w: the commit %s is not on any branch of the host", forge.ErrNotFound, t.Commit)
	}
	if _, err := r.p.git(ctx, repo, dir, false, "tag", "-a", "-m", t.Message, t.Name, t.Commit); err != nil {
		return forge.Ref{}, err
	}
	if _, err := r.p.git(ctx, repo, dir, true, "push", "--quiet", u.String(), "refs/tags/"+t.Name+":refs/tags/"+t.Name); err != nil {
		return forge.Ref{}, err
	}
	return want, nil
}

// PublishRelease: CodeCommit has no release object; see Capabilities.
func (r releases) PublishRelease(context.Context, forge.Repository, forge.ReleaseSpec) (string, error) {
	return "", r.p.Caps.Err(forge.CapReleases)
}

// maxFile is the largest file PutFile takes: "Maximum length of 6291456"
// (https://docs.aws.amazon.com/codecommit/latest/APIReference/API_PutFile.html).
const maxFile = 6291456

// CommitFile writes one file with PutFile (permission codecommit:PutFile) on a
// branch, whose head is the parentCommitId it requires; a new branch is cut
// first with CreateBranch (codecommit:CreateBranch) from the base branch's head
// (and written to if it already exists). A file whose content is already that on
// the branch is not an error (the host's SameFileContentException): the branch's
// head is returned. An empty file cannot be written through the API.
func (r releases) CommitFile(ctx context.Context, repo forge.Repository, ch forge.FileChange) (string, error) {
	name, err := r.p.repoName(repo)
	if err != nil {
		return "", err
	}
	if ch.Path == "" || strings.HasPrefix(ch.Path, "/") || strings.Contains(ch.Path, "..") || ch.Branch == "" {
		return "", fmt.Errorf("%w: a file change names a branch and a path inside the repository", forge.ErrInvalid)
	}
	if len(ch.Content) == 0 || len(ch.Content) > maxFile {
		return "", fmt.Errorf("%w: through the API a file is 1 byte to 6 MB; larger or empty files need a Git push", forge.ErrInvalid)
	}
	branch := ch.Branch
	parent, err := r.p.tip(ctx, name, ch.Branch)
	if err != nil {
		return "", err
	}
	if ch.NewBranch != "" {
		branch = ch.NewBranch
		existing, err := r.p.tip(ctx, name, ch.NewBranch)
		switch {
		case err == nil:
			parent = existing
		case errors.Is(err, forge.ErrNotFound):
			if err := r.p.invoke(ctx, "CreateBranch", map[string]any{"repositoryName": name, "branchName": ch.NewBranch, "commitId": parent}, nil); err != nil {
				return "", err
			}
		default:
			return "", err
		}
	}
	in := map[string]any{"repositoryName": name, "branchName": branch, "parentCommitId": parent, "filePath": ch.Path, "fileMode": "NORMAL",
		"fileContent": base64.StdEncoding.EncodeToString(ch.Content), "commitMessage": ch.Message, "name": "Launchway"}
	var out struct {
		CommitID string `json:"commitId"`
	}
	if err := r.p.invoke(ctx, "PutFile", in, &out); err != nil {
		if a, ok := asAPI(err); ok && a.is("SameFileContentException") {
			return parent, nil
		}
		return "", err
	}
	if out.CommitID == "" {
		return "", errors.New("the host's answer names no commit")
	}
	return out.CommitID, nil
}

// maxComment is the comment size limit: "Comments are limited to 10,240 characters"
// (API_PostCommentForPullRequest.html, CommentContentSizeLimitExceededException).
const maxComment = 10240

// UpsertSummary posts the summary as one comment of the pull request:
// PostCommentForPullRequest with the pull request's destination head as
// beforeCommitId and its source head as afterCommitId, a general comment with
// no location (permission codecommit:PostCommentForPullRequest), and edits it in
// place with UpdateComment (codecommit:UpdateComment), which the reference says
// only the comment's author may do. The key is the comment's id. A comment that
// was deleted, or that another identity wrote (the credentials changed), is
// posted again.
func (r releases) UpsertSummary(ctx context.Context, repo forge.Repository, number int, key, body string) (string, error) {
	name, err := r.p.repoName(repo)
	if err != nil {
		return "", err
	}
	if r := []rune(body); len(r) > maxComment {
		body = string(r[:maxComment-1]) + "…"
	}
	if key != "" {
		var out struct{}
		err := r.p.invoke(ctx, "UpdateComment", map[string]any{"commentId": key, "content": body}, &out)
		if err == nil {
			return key, nil
		}
		a, ok := asAPI(err)
		if !ok || !a.is("CommentDoesNotExistException", "CommentDeletedException", "CommentNotCreatedByCallerException", "InvalidCommentIdException") {
			return "", err
		}
	}
	pr := proposals(r)
	a, err := pr.get(ctx, fmt.Sprint(number))
	if err != nil {
		return "", err
	}
	if !pr.owns(a, name) {
		return "", fmt.Errorf("%w: pull request %d is not in %s", forge.ErrNotFound, number, repo.Path)
	}
	t := a.Targets[0]
	in := map[string]any{"pullRequestId": a.ID, "repositoryName": name, "beforeCommitId": t.DestCommit, "afterCommitId": t.SrcCommit,
		"content": body, "clientRequestToken": token()}
	var out struct {
		Comment struct {
			ID string `json:"commentId"`
		} `json:"comment"`
	}
	if err := r.p.invoke(ctx, "PostCommentForPullRequest", in, &out); err != nil {
		return "", err
	}
	if out.Comment.ID == "" {
		return "", errors.New("the host's answer names no comment")
	}
	return out.Comment.ID, nil
}

var errNoVote = "Launchway reads approvals but does not vote or comment as a reviewer"

func (rv reviews) Submit(context.Context, forge.Repository, int, forge.Verdict, string) (forge.Review, error) {
	return forge.Review{}, &forge.ErrUnsupported{Capability: forge.CapReviews, Reason: errNoVote, Workaround: "Review and approve in CodeCommit"}
}
func (rv reviews) Threads(context.Context, forge.Repository, int) ([]forge.Thread, error) {
	return nil, &forge.ErrUnsupported{Capability: forge.CapReviews, Reason: errNoVote, Workaround: "Read the threads in CodeCommit"}
}
func (rv reviews) Reply(context.Context, forge.Repository, int, string, string) (forge.Thread, error) {
	return forge.Thread{}, &forge.ErrUnsupported{Capability: forge.CapReviews, Reason: errNoVote, Workaround: "Reply in CodeCommit"}
}
func (rv reviews) Resolve(context.Context, forge.Repository, int, string, bool) error {
	return &forge.ErrUnsupported{Capability: forge.CapReviews, Reason: errNoVote, Workaround: "Resolve in CodeCommit"}
}
