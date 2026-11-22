package bitbucketdc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

type releases struct{ p *Provider }

// CreateTag: GET .../tags/{name} (REPO_READ), then POST
// /git/latest/projects/{k}/repos/{s}/tags (an effective REPO_WRITE) with name,
// startPoint (a ref or a commit), message and type ANNOTATED. A tag that exists
// is accepted when it already stands for the same commit.
func (r releases) CreateTag(ctx context.Context, repo forge.Repository, t forge.TagSpec) (forge.Ref, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return forge.Ref{}, err
	}
	var have apiRef
	st, err := r.p.c.Call(ctx, http.MethodGet, rp+"/tags/"+restkit.EscPath(t.Name), nil, &have, http.StatusNotFound)
	if err != nil {
		return forge.Ref{}, err
	}
	if st != http.StatusNotFound {
		if have.LatestCommit != t.Commit {
			return forge.Ref{}, fmt.Errorf("%w: the tag %s already exists on the host and stands for another commit", forge.ErrExists, t.Name)
		}
		return forge.Ref{Name: "refs/tags/" + t.Name, Commit: have.LatestCommit}, nil
	}
	body := map[string]any{"name": t.Name, "startPoint": t.Commit, "message": t.Message, "type": "ANNOTATED"}
	if _, err := r.p.c.Call(ctx, http.MethodPost, "/git/latest"+strings.TrimPrefix(rp, "/api/latest")+"/tags", body, nil); err != nil {
		return forge.Ref{}, err
	}
	return forge.Ref{Name: "refs/tags/" + t.Name, Commit: t.Commit}, nil
}

// PublishRelease: Bitbucket has no release object; see Capabilities.
func (r releases) PublishRelease(context.Context, forge.Repository, forge.ReleaseSpec) (string, error) {
	return "", r.p.Caps.Err(forge.CapReleases)
}

// branchExists: GET .../branches?filterText=NAME (REPO_READ), the exact id kept.
func (r releases) branchExists(ctx context.Context, rp, name string) (bool, error) {
	var in page[apiRef]
	if _, err := r.p.c.Get(ctx, rp+"/branches?limit=100&filterText="+url.QueryEscape(name), &in); err != nil {
		return false, err
	}
	for _, b := range in.Values {
		if b.ID == "refs/heads/"+name {
			return true, nil
		}
	}
	return false, nil
}

// lastChange: the commit that last changed the file on the branch, GET
// .../commits?path=PATH&until=BRANCH&limit=1 (REPO_READ); "" when the branch has
// no such file yet (a new file takes no sourceCommitId).
func (r releases) lastChange(ctx context.Context, rp, branch, path string) (string, error) {
	var in page[struct {
		ID string `json:"id"`
	}]
	if _, err := r.p.c.Get(ctx, rp+"/commits?limit=1&path="+url.QueryEscape(path)+"&until="+url.QueryEscape(branch), &in); err != nil {
		return "", err
	}
	if len(in.Values) == 0 {
		return "", nil
	}
	return in.Values[0].ID, nil
}

// CommitFile: PUT .../browse/{path} ("Edit file", multipart/form-data; the
// reference's example is a curl -F request with the form fields content, message,
// branch, sourceBranch and sourceCommitId; REPO_WRITE). The file's bytes are
// the part named content; branch is the branch to commit on; sourceBranch
// names the branch a new one starts from; sourceCommitId is the commit that last
// changed the file (omitted for a new file; the host refuses with 409 a file that
// changed since). The multipart resources carry the host's XSRF check, passed with
// the header X-Atlassian-Token: no-check as the reference's multipart resources say.
// The commit's id is in the answer.
func (r releases) CommitFile(ctx context.Context, repo forge.Repository, ch forge.FileChange) (string, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return "", err
	}
	if ch.Path == "" || strings.HasPrefix(ch.Path, "/") || strings.Contains(ch.Path, "..") || ch.Branch == "" {
		return "", fmt.Errorf("%w: a file change names a branch and a path inside the repository", forge.ErrInvalid)
	}
	branch, source := ch.Branch, ""
	if ch.NewBranch != "" {
		branch = ch.NewBranch
		ok, err := r.branchExists(ctx, rp, ch.NewBranch)
		if err != nil {
			return "", err
		}
		if !ok {
			source = ch.Branch
		}
	}
	basis := branch
	if source != "" {
		basis = source
	}
	last, err := r.lastChange(ctx, rp, basis, ch.Path)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("message", ch.Message)
	_ = mw.WriteField("branch", branch)
	if source != "" {
		_ = mw.WriteField("sourceBranch", source)
	}
	if last != "" {
		_ = mw.WriteField("sourceCommitId", last)
	}
	fw, err := mw.CreateFormFile("content", ch.Path[strings.LastIndex(ch.Path, "/")+1:])
	if err != nil {
		return "", err
	}
	_, _ = fw.Write(ch.Content)
	_ = mw.Close()
	c := r.p.c
	a, err := c.DoWith(ctx, http.MethodPut, c.URL(rp+"/browse/"+restkit.EscPath(ch.Path)), bytes.NewReader(buf.Bytes()), int64(buf.Len()), mw.FormDataContentType(),
		http.Header{"X-Atlassian-Token": {"no-check"}})
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.Finish(a, &out, nil); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("the host's answer names no commit")
	}
	return out.ID, nil
}

// UpsertSummary posts the summary as a comment on the pull request: POST
// .../pull-requests/{id}/comments with text (REPO_READ), and edits it with PUT
// .../comments/{commentId} with text and the comment's current version (which
// GET .../comments/{commentId} gives); only the comment's author may edit its
// text, which the connection is. A comment that was deleted on the host (404)
// is posted again.
func (r releases) UpsertSummary(ctx context.Context, repo forge.Repository, number int, key, body string) (string, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return "", err
	}
	comments := rp + "/pull-requests/" + strconv.Itoa(number) + "/comments"
	var cur struct {
		ID      int64 `json:"id"`
		Version int   `json:"version"`
	}
	if key != "" {
		if _, err := strconv.ParseInt(key, 10, 64); err != nil {
			return "", fmt.Errorf("%w: bad comment key", forge.ErrInvalid)
		}
		st, err := r.p.c.Call(ctx, http.MethodGet, comments+"/"+key, nil, &cur, http.StatusNotFound)
		if err != nil {
			return "", err
		}
		if st != http.StatusNotFound {
			st, err = r.p.c.Call(ctx, http.MethodPut, comments+"/"+key, map[string]any{"text": body, "version": cur.Version}, nil, http.StatusNotFound)
			if err != nil {
				return "", err
			}
			if st != http.StatusNotFound {
				return key, nil
			}
		}
	}
	if _, err := r.p.c.Call(ctx, http.MethodPost, comments, map[string]any{"text": body}, &cur); err != nil {
		return "", err
	}
	return strconv.FormatInt(cur.ID, 10), nil
}
