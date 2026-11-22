package azuredevops

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

type releases struct{ p *Provider }

// tagRef finds the exact ref refs/tags/{name} among the refs the host
// lists for the filter "tags/{name}" (a filter is "starts with").
func (r releases) tagRef(ctx context.Context, rp, name string) (apiRef, bool, error) {
	rs, err := r.p.refs(ctx, rp, "tags/"+name)
	if err != nil {
		return apiRef{}, false, err
	}
	for _, x := range rs {
		if x.Name == "refs/tags/"+name {
			return x, true, nil
		}
	}
	return apiRef{}, false, nil
}

// CreateTag makes an annotated tag: POST .../annotatedtags with name,
// message and taggedObject.objectId (scope vso.code_manage;
// https://learn.microsoft.com/en-us/rest/api/azure/devops/git/annotated-tags/create).
// A tag that exists is accepted when it already stands for the same commit (an
// annotated tag's peeled commit, as refs/list gives it with peelTags). The
// reference does not say the call also creates the ref refs/tags/{name}, so the
// ref is read afterwards and, when it is not there, created with POST .../refs
// (scope vso.code_write; refs/update-refs, oldObjectId all zeros) pointing at the
// tag object the call returned.
func (r releases) CreateTag(ctx context.Context, repo forge.Repository, t forge.TagSpec) (forge.Ref, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return forge.Ref{}, err
	}
	want := forge.Ref{Name: "refs/tags/" + t.Name, Commit: t.Commit}
	have, ok, err := r.tagRef(ctx, rp, t.Name)
	if err != nil {
		return forge.Ref{}, err
	}
	if ok {
		if have.commit() != t.Commit {
			return forge.Ref{}, fmt.Errorf("%w: the tag %s already exists on the host and stands for another commit", forge.ErrExists, t.Name)
		}
		return want, nil
	}
	var made struct {
		ObjectID string `json:"objectId"`
	}
	body := map[string]any{"name": t.Name, "message": t.Message, "taggedObject": map[string]any{"objectId": t.Commit}}
	if _, err := r.p.c.Call(ctx, http.MethodPost, rp+"/annotatedtags"+qs(), body, &made); err != nil {
		return forge.Ref{}, err
	}
	if _, ok, err := r.tagRef(ctx, rp, t.Name); err != nil || ok {
		return want, err
	}
	if made.ObjectID == "" {
		return forge.Ref{}, errors.New("the host made no ref for the tag and its answer names no tag object")
	}
	var up page[struct {
		Success      bool   `json:"success"`
		UpdateStatus string `json:"updateStatus"`
	}]
	upd := []map[string]any{{"name": want.Name, "oldObjectId": zeros, "newObjectId": made.ObjectID}}
	if _, err := r.p.c.Call(ctx, http.MethodPost, rp+"/refs"+qs(), upd, &up); err != nil {
		return forge.Ref{}, err
	}
	if len(up.Value) != 1 || !up.Value[0].Success {
		return forge.Ref{}, errors.New("the host would not create the ref for the tag")
	}
	return want, nil
}

// PublishRelease: Azure DevOps Git has no release object; see Capabilities.
func (r releases) PublishRelease(context.Context, forge.Repository, forge.ReleaseSpec) (string, error) {
	return "", r.p.Caps.Err(forge.CapReleases)
}

// tip is the commit a branch points at: the exact ref among those starting with heads/{name}.
func (p *Provider) tip(ctx context.Context, rp, branch string) (string, bool, error) {
	rs, err := p.refs(ctx, rp, "heads/"+branch)
	if err != nil {
		return "", false, err
	}
	for _, x := range rs {
		if x.Name == "refs/heads/"+branch {
			return x.ObjectID, true, nil
		}
	}
	return "", false, nil
}

// exists says whether a file is on a branch: GET .../items?path=...&versionDescriptor.version={branch}
// &versionDescriptor.versionType=branch (scope vso.code); a 404 is "no".
func (p *Provider) exists(ctx context.Context, rp, branch, path string) (bool, error) {
	st, err := p.c.Call(ctx, http.MethodGet, rp+"/items"+qs("path", "/"+path, "versionDescriptor.version", branch, "versionDescriptor.versionType", "branch"), nil, nil, http.StatusNotFound)
	return st != http.StatusNotFound, err
}

// CommitFile writes one file with POST .../pushes (scope vso.code_write;
// https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pushes/create): a
// refUpdate names the branch and the commit it stands on (oldObjectId), a commit
// has a comment and a change of type add or edit with the file's content as
// base64. The reference's example "Update a file in a new branch" names a
// branch that does not exist with the commit to build it on, which is how a new
// branch is cut in the same call. Whether the file exists decides add or edit
// (GET .../items).
func (r releases) CommitFile(ctx context.Context, repo forge.Repository, ch forge.FileChange) (string, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return "", err
	}
	if ch.Path == "" || strings.HasPrefix(ch.Path, "/") || strings.Contains(ch.Path, "..") || ch.Branch == "" {
		return "", fmt.Errorf("%w: a file change names a branch and a path inside the repository", forge.ErrInvalid)
	}
	branch, from := ch.Branch, ch.Branch
	if ch.NewBranch != "" {
		branch = ch.NewBranch
		if _, have, err := r.p.tip(ctx, rp, ch.NewBranch); err != nil {
			return "", err
		} else if have {
			from = ch.NewBranch
		}
	}
	oldID, ok, err := r.p.tip(ctx, rp, from)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: the branch %s", forge.ErrNotFound, from)
	}
	there, err := r.p.exists(ctx, rp, from, ch.Path)
	if err != nil {
		return "", err
	}
	kind := map[bool]string{true: "edit", false: "add"}[there]
	body := map[string]any{
		"refUpdates": []map[string]any{{"name": "refs/heads/" + branch, "oldObjectId": oldID}},
		"commits": []map[string]any{{"comment": ch.Message, "changes": []map[string]any{{"changeType": kind, "item": map[string]any{"path": "/" + ch.Path},
			"newContent": map[string]any{"content": base64.StdEncoding.EncodeToString(ch.Content), "contentType": "base64Encoded"}}}}},
	}
	var out struct {
		Commits []struct {
			CommitID string `json:"commitId"`
		} `json:"commits"`
	}
	if _, err := r.p.c.Call(ctx, http.MethodPost, rp+"/pushes"+qs(), body, &out); err != nil {
		return "", err
	}
	if len(out.Commits) == 0 || out.Commits[0].CommitID == "" {
		return "", errors.New("the host's answer names no commit")
	}
	return out.Commits[0].CommitID, nil
}

// UpsertSummary posts the summary as one comment thread of the pull request:
// POST .../pullRequests/{id}/threads with comments [{parentCommentId 0, content,
// commentType 1}] and status 1 (scopes vso.code_write or vso.threads_full;
// https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create),
// and edits the comment in place with PATCH .../threads/{threadId}/comments/{commentId}
// and its content (pull-request-thread-comments/update). The key is
// "{threadId}:{commentId}". A comment that was deleted on the host (404) is
// posted again.
func (r releases) UpsertSummary(ctx context.Context, repo forge.Repository, number int, key, body string) (string, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return "", err
	}
	threads := rp + "/pullRequests/" + strconv.Itoa(number) + "/threads"
	if key != "" {
		t, c, ok := strings.Cut(key, ":")
		_, e1 := strconv.Atoi(t)
		_, e2 := strconv.Atoi(c)
		if !ok || e1 != nil || e2 != nil {
			return "", fmt.Errorf("%w: bad comment key", forge.ErrInvalid)
		}
		st, err := r.p.c.Call(ctx, http.MethodPatch, threads+"/"+t+"/comments/"+c+qs(), map[string]any{"content": body}, nil, http.StatusNotFound)
		if err != nil {
			return "", err
		}
		if st != http.StatusNotFound {
			return key, nil
		}
	}
	in := map[string]any{"comments": []map[string]any{{"parentCommentId": 0, "content": body, "commentType": 1}}, "status": 1}
	var out struct {
		ID       int `json:"id"`
		Comments []struct {
			ID int `json:"id"`
		} `json:"comments"`
	}
	if _, err := r.p.c.Call(ctx, http.MethodPost, threads+qs(), in, &out); err != nil {
		return "", err
	}
	if out.ID == 0 || len(out.Comments) == 0 {
		return "", errors.New("the host's answer names no thread or comment")
	}
	return strconv.Itoa(out.ID) + ":" + strconv.Itoa(out.Comments[0].ID), nil
}
