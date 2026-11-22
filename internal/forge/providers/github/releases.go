package github

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

// CreateTag makes an annotated tag: POST /repos/{o}/{r}/git/tags (tag object:
// tag, message, object, type; https://docs.github.com/en/rest/git/tags#create-a-tag-object)
// and then POST /repos/{o}/{r}/git/refs with ref refs/tags/<tag> and the
// object's sha (https://docs.github.com/en/rest/git/refs#create-a-reference).
// A tag that exists is accepted when it already stands for the same commit.
func (r releases) CreateTag(ctx context.Context, repo forge.Repository, t forge.TagSpec) (forge.Ref, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return forge.Ref{}, err
	}
	// GET /repos/{o}/{r}/git/ref/{ref} (https://docs.github.com/en/rest/git/refs#get-a-reference)
	var have struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	st, err := r.p.c.Call(ctx, http.MethodGet, rp+"/git/ref/tags/"+escPath(t.Name), nil, &have, http.StatusNotFound)
	if err != nil {
		return forge.Ref{}, err
	}
	if st != http.StatusNotFound {
		target := have.Object.SHA
		if have.Object.Type == "tag" {
			var obj struct {
				Object struct {
					SHA string `json:"sha"`
				} `json:"object"`
			}
			// GET /repos/{o}/{r}/git/tags/{sha} (https://docs.github.com/en/rest/git/tags#get-a-tag)
			if _, err := r.p.c.Call(ctx, http.MethodGet, rp+"/git/tags/"+esc(have.Object.SHA), nil, &obj); err != nil {
				return forge.Ref{}, err
			}
			target = obj.Object.SHA
		}
		if target != t.Commit {
			return forge.Ref{}, fmt.Errorf("%w: the tag %s already exists on the host and stands for another commit", forge.ErrExists, t.Name)
		}
		return forge.Ref{Name: "refs/tags/" + t.Name, Commit: t.Commit}, nil
	}
	var tag struct {
		SHA string `json:"sha"`
	}
	if _, err := r.p.c.Call(ctx, http.MethodPost, rp+"/git/tags", map[string]any{
		"tag": t.Name, "message": t.Message, "object": t.Commit, "type": "commit"}, &tag); err != nil {
		return forge.Ref{}, err
	}
	if _, err := r.p.c.Call(ctx, http.MethodPost, rp+"/git/refs", map[string]any{"ref": "refs/tags/" + t.Name, "sha": tag.SHA}, nil); err != nil {
		return forge.Ref{}, err
	}
	return forge.Ref{Name: "refs/tags/" + t.Name, Commit: t.Commit}, nil
}

type apiRelease struct {
	ID        int64  `json:"id"`
	HTMLURL   string `json:"html_url"`
	UploadURL string `json:"upload_url"`
	Assets    []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// PublishRelease finds the release of the tag (GET /repos/{o}/{r}/releases/tags/{tag}),
// creates it (POST /repos/{o}/{r}/releases: tag_name, name, body, draft,
// prerelease) or updates its notes (PATCH /repos/{o}/{r}/releases/{id}), and
// uploads each file the release lacks to the upload address the host names
// (POST <upload_url>?name=; https://docs.github.com/en/rest/releases/assets#upload-a-release-asset;
// a file of the same name and another size is deleted first:
// DELETE /repos/{o}/{r}/releases/assets/{id}).
// Docs: https://docs.github.com/en/rest/releases/releases
func (r releases) PublishRelease(ctx context.Context, repo forge.Repository, rel forge.ReleaseSpec) (string, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return "", err
	}
	c := r.p.c
	// The tag must exist: asking for a release of a missing tag makes the host
	// create the tag from its default branch.
	st, err := c.Call(ctx, http.MethodGet, rp+"/git/ref/tags/"+escPath(rel.Tag), nil, nil, http.StatusNotFound)
	if err != nil {
		return "", err
	}
	if st == http.StatusNotFound {
		return "", fmt.Errorf("%w: %s is not there yet, or the credential cannot see the repository", ErrTagMissing, rel.Tag)
	}
	var made apiRelease
	st, err = c.Call(ctx, http.MethodGet, rp+"/releases/tags/"+escPath(rel.Tag), nil, &made, http.StatusNotFound)
	if err != nil {
		return "", err
	}
	body := map[string]any{"tag_name": rel.Tag, "name": rel.Title, "body": rel.Notes, "draft": false, "prerelease": rel.Prerelease}
	if st == http.StatusNotFound {
		if _, err := c.Call(ctx, http.MethodPost, rp+"/releases", body, &made); err != nil {
			return "", err
		}
	} else if _, err := c.Call(ctx, http.MethodPatch, rp+"/releases/"+strconv.FormatInt(made.ID, 10), body, &made); err != nil {
		return "", err
	}
	have := map[string]int64{}
	haveSize := map[string]int64{}
	for _, a := range made.Assets {
		have[a.Name], haveSize[a.Name] = a.ID, a.Size
	}
	for _, f := range rel.Files {
		if id, ok := have[f.Name]; ok {
			if haveSize[f.Name] == f.Size {
				continue // uploaded by an earlier try
			}
			if _, err := c.Call(ctx, http.MethodDelete, rp+"/releases/assets/"+strconv.FormatInt(id, 10), nil, nil); err != nil {
				return "", err
			}
		}
		got, err := c.Upload(ctx, made.UploadURL, f.Name, f)
		if err != nil {
			return "", err
		}
		if got != 0 && got != f.Size {
			return "", fmt.Errorf("uploading %s: the host kept %d bytes, not %d", f.Name, got, f.Size)
		}
	}
	return made.HTMLURL, nil
}

// CommitFile writes one file with PUT /repos/{o}/{r}/contents/{path}: message,
// content (base64), sha of the file replaced, branch
// (https://docs.github.com/en/rest/repos/contents#create-or-update-file-contents).
// With NewBranch the branch is first created from Branch's head: GET
// /repos/{o}/{r}/git/ref/heads/{branch} and POST /repos/{o}/{r}/git/refs.
// The sha of an existing file comes from GET /repos/{o}/{r}/contents/{path}?ref=.
func (r releases) CommitFile(ctx context.Context, repo forge.Repository, ch forge.FileChange) (string, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return "", err
	}
	if ch.Path == "" || strings.HasPrefix(ch.Path, "/") || strings.Contains(ch.Path, "..") || ch.Branch == "" {
		return "", fmt.Errorf("%w: a file change names a branch and a path inside the repository", forge.ErrInvalid)
	}
	c := r.p.c
	target := ch.Branch
	if ch.NewBranch != "" {
		var ref struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		if _, err := c.Call(ctx, http.MethodGet, rp+"/git/ref/heads/"+escPath(ch.Branch), nil, &ref); err != nil {
			return "", err
		}
		if _, err := c.Call(ctx, http.MethodPost, rp+"/git/refs", map[string]any{"ref": "refs/heads/" + ch.NewBranch, "sha": ref.Object.SHA}, nil); err != nil {
			return "", err
		}
		target = ch.NewBranch
	}
	var cur struct {
		SHA string `json:"sha"`
	}
	st, err := c.Call(ctx, http.MethodGet, rp+"/contents/"+escPath(ch.Path)+"?ref="+esc(target), nil, &cur, http.StatusNotFound)
	if err != nil {
		return "", err
	}
	body := map[string]any{"message": ch.Message, "content": base64.StdEncoding.EncodeToString(ch.Content), "branch": target}
	if st != http.StatusNotFound {
		body["sha"] = cur.SHA
	}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if _, err := c.Call(ctx, http.MethodPut, rp+"/contents/"+escPath(ch.Path), body, &out); err != nil {
		return "", err
	}
	if out.Commit.SHA == "" {
		return "", errors.New("the host's answer names no commit")
	}
	return out.Commit.SHA, nil
}

// UpsertSummary posts the summary as a comment on the pull request:
// POST /repos/{o}/{r}/issues/{n}/comments, and edits it in place with
// PATCH /repos/{o}/{r}/issues/comments/{id}
// (https://docs.github.com/en/rest/issues/comments). A comment that was
// deleted on the host (404) is posted again.
func (r releases) UpsertSummary(ctx context.Context, repo forge.Repository, number int, key, body string) (string, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return "", err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if key != "" {
		if _, err := strconv.ParseInt(key, 10, 64); err != nil {
			return "", fmt.Errorf("%w: bad comment key", forge.ErrInvalid)
		}
		st, err := r.p.c.Call(ctx, http.MethodPatch, rp+"/issues/comments/"+key, map[string]string{"body": body}, &out, http.StatusNotFound)
		if err != nil {
			return "", err
		}
		if st != http.StatusNotFound {
			return key, nil
		}
	}
	if _, err := r.p.c.Call(ctx, http.MethodPost, rp+"/issues/"+strconv.Itoa(number)+"/comments", map[string]string{"body": body}, &out); err != nil {
		return "", err
	}
	return strconv.FormatInt(out.ID, 10), nil
}
