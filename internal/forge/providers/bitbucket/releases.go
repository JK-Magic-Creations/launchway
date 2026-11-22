package bitbucket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

type releases struct{ p *Provider }

// CreateTag: GET /repositories/{w}/{r}/refs/tags/{name}, then POST
// .../refs/tags with name, target.hash and message (scope repository:write).
// A tag that exists is accepted when it already stands for the same commit.
func (r releases) CreateTag(ctx context.Context, repo forge.Repository, t forge.TagSpec) (forge.Ref, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return forge.Ref{}, err
	}
	var have struct {
		Target struct {
			Hash string `json:"hash"`
		} `json:"target"`
	}
	st, err := r.p.c.Call(ctx, http.MethodGet, rp+"/refs/tags/"+restkit.EscPath(t.Name), nil, &have, http.StatusNotFound)
	if err != nil {
		return forge.Ref{}, err
	}
	if st != http.StatusNotFound {
		if have.Target.Hash != t.Commit {
			return forge.Ref{}, fmt.Errorf("%w: the tag %s already exists on the host and stands for another commit", forge.ErrExists, t.Name)
		}
		return forge.Ref{Name: "refs/tags/" + t.Name, Commit: have.Target.Hash}, nil
	}
	if _, err := r.p.c.Call(ctx, http.MethodPost, rp+"/refs/tags", map[string]any{"name": t.Name, "target": map[string]any{"hash": t.Commit}, "message": t.Message}, nil); err != nil {
		return forge.Ref{}, err
	}
	return forge.Ref{Name: "refs/tags/" + t.Name, Commit: t.Commit}, nil
}

// PublishRelease: Bitbucket has no release object; see Capabilities.
func (r releases) PublishRelease(context.Context, forge.Repository, forge.ReleaseSpec) (string, error) {
	return "", r.p.Caps.Err(forge.CapReleases)
}

// CommitFile writes one file with POST /repositories/{w}/{r}/src as
// multipart/form-data: a form field named for the file's path holds its bytes,
// beside the fields message, branch and parents (scope repository:write). A
// branch that exists gets the commit on its tip; a new branch is made by
// naming the commit to build on as parents, which is the head of the branch it
// is cut from (GET .../refs/branches/{name}). The commit made is the branch's
// head afterwards.
func (r releases) CommitFile(ctx context.Context, repo forge.Repository, ch forge.FileChange) (string, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return "", err
	}
	if ch.Path == "" || strings.HasPrefix(ch.Path, "/") || strings.Contains(ch.Path, "..") || ch.Branch == "" {
		return "", fmt.Errorf("%w: a file change names a branch and a path inside the repository", forge.ErrInvalid)
	}
	c := r.p.c
	branch, parent := ch.Branch, ""
	if ch.NewBranch != "" {
		branch = ch.NewBranch
		var have struct {
			Target struct {
				Hash string `json:"hash"`
			} `json:"target"`
		}
		st, err := c.Call(ctx, http.MethodGet, rp+"/refs/branches/"+restkit.EscPath(ch.NewBranch), nil, &have, http.StatusNotFound)
		if err != nil {
			return "", err
		}
		if st == http.StatusNotFound { // build the new branch on the head of the branch it is cut from
			var base struct {
				Target struct {
					Hash string `json:"hash"`
				} `json:"target"`
			}
			if _, err := c.Get(ctx, rp+"/refs/branches/"+restkit.EscPath(ch.Branch), &base); err != nil {
				return "", err
			}
			parent = base.Target.Hash
		}
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("message", ch.Message)
	_ = mw.WriteField("branch", branch)
	if parent != "" {
		_ = mw.WriteField("parents", parent)
	}
	fw, err := mw.CreateFormFile("/"+ch.Path, ch.Path)
	if err != nil {
		return "", err
	}
	_, _ = fw.Write(ch.Content)
	_ = mw.Close()
	a, err := c.Do(ctx, http.MethodPost, c.URL(rp+"/src"), bytes.NewReader(buf.Bytes()), int64(buf.Len()), mw.FormDataContentType())
	if err != nil {
		return "", err
	}
	if err := c.Finish(a, nil, nil); err != nil {
		return "", err
	}
	var head struct {
		Target struct {
			Hash string `json:"hash"`
		} `json:"target"`
	}
	if _, err := c.Get(ctx, rp+"/refs/branches/"+restkit.EscPath(branch), &head); err != nil {
		return "", err
	}
	if head.Target.Hash == "" {
		return "", errors.New("the host's answer names no commit")
	}
	return head.Target.Hash, nil
}

// UpsertSummary posts the summary as a comment on the pull request: POST
// /repositories/{w}/{r}/pullrequests/{id}/comments with content.raw, and edits
// it with PUT .../comments/{comment_id} (scope pullrequest). A comment that
// was deleted on the host (404) is posted again.
func (r releases) UpsertSummary(ctx context.Context, repo forge.Repository, number int, key, body string) (string, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return "", err
	}
	comments := rp + "/pullrequests/" + strconv.Itoa(number) + "/comments"
	in := map[string]any{"content": map[string]any{"raw": body}}
	var out struct {
		ID int64 `json:"id"`
	}
	if key != "" {
		if _, err := strconv.ParseInt(key, 10, 64); err != nil {
			return "", fmt.Errorf("%w: bad comment key", forge.ErrInvalid)
		}
		st, err := r.p.c.Call(ctx, http.MethodPut, comments+"/"+key, in, &out, http.StatusNotFound)
		if err != nil {
			return "", err
		}
		if st != http.StatusNotFound {
			return key, nil
		}
	}
	if _, err := r.p.c.Call(ctx, http.MethodPost, comments, in, &out); err != nil {
		return "", err
	}
	return strconv.FormatInt(out.ID, 10), nil
}
