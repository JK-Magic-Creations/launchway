package gitea

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

// ErrTagMissing is wrapped when a release is asked for a tag the host lacks.
var ErrTagMissing = errors.New("the host does not have the tag")

type releases struct{ p *Provider }

type apiTag struct {
	ID     string `json:"id"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// CreateTag: GET /repos/{o}/{r}/tags/{tag} (repoGetTag), then POST
// /repos/{o}/{r}/tags with tag_name, target and message (repoCreateTag; with a
// message the tag is annotated). A tag that exists is accepted when it already
// stands for the same commit. Gitea answers a created tag with 200, Forgejo
// with 201.
func (r releases) CreateTag(ctx context.Context, repo forge.Repository, t forge.TagSpec) (forge.Ref, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return forge.Ref{}, err
	}
	var have apiTag
	st, err := r.p.c.Call(ctx, http.MethodGet, rp+"/tags/"+restkit.EscPath(t.Name), nil, &have, http.StatusNotFound)
	if err != nil {
		return forge.Ref{}, err
	}
	if st != http.StatusNotFound {
		if have.Commit.SHA != t.Commit {
			return forge.Ref{}, fmt.Errorf("%w: the tag %s already exists on the host and stands for another commit", forge.ErrExists, t.Name)
		}
		return forge.Ref{Name: "refs/tags/" + t.Name, Commit: have.Commit.SHA}, nil
	}
	var made apiTag
	if _, err := r.p.c.Call(ctx, http.MethodPost, rp+"/tags", map[string]any{"tag_name": t.Name, "target": t.Commit, "message": t.Message}, &made); err != nil {
		return forge.Ref{}, err
	}
	return forge.Ref{Name: "refs/tags/" + t.Name, Commit: t.Commit}, nil
}

type apiRelease struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// PublishRelease finds the release of the tag (GET /repos/{o}/{r}/releases/tags/{tag}),
// creates it (POST .../releases: tag_name, name, body, draft, prerelease) or
// updates it (PATCH .../releases/{id}), and uploads each file the release lacks
// (POST .../releases/{id}/assets?name=, the body being the raw file: the
// operation consumes application/octet-stream; a file of the same name and
// another size is deleted first with DELETE .../assets/{attachment_id}).
func (r releases) PublishRelease(ctx context.Context, repo forge.Repository, rel forge.ReleaseSpec) (string, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return "", err
	}
	c := r.p.c
	// The tag must exist: a release of a missing tag would make the host tag the default branch.
	st, err := c.Call(ctx, http.MethodGet, rp+"/tags/"+restkit.EscPath(rel.Tag), nil, nil, http.StatusNotFound)
	if err != nil {
		return "", err
	}
	if st == http.StatusNotFound {
		return "", fmt.Errorf("%w: %s is not there yet, or the credential cannot see the repository", ErrTagMissing, rel.Tag)
	}
	var made apiRelease
	st, err = c.Call(ctx, http.MethodGet, rp+"/releases/tags/"+restkit.EscPath(rel.Tag), nil, &made, http.StatusNotFound)
	if err != nil {
		return "", err
	}
	name := rel.Title
	if name == "" {
		name = rel.Tag
	}
	body := map[string]any{"tag_name": rel.Tag, "name": name, "body": rel.Notes, "draft": false, "prerelease": rel.Prerelease}
	if st == http.StatusNotFound {
		if _, err := c.Call(ctx, http.MethodPost, rp+"/releases", body, &made); err != nil {
			return "", err
		}
	} else if _, err := c.Call(ctx, http.MethodPatch, rp+"/releases/"+strconv.FormatInt(made.ID, 10), body, &made); err != nil {
		return "", err
	}
	have, haveSize := map[string]int64{}, map[string]int64{}
	for _, a := range made.Assets {
		have[a.Name], haveSize[a.Name] = a.ID, a.Size
	}
	assets := rp + "/releases/" + strconv.FormatInt(made.ID, 10) + "/assets"
	for _, f := range rel.Files {
		if id, ok := have[f.Name]; ok {
			if haveSize[f.Name] == f.Size {
				continue // uploaded by an earlier try
			}
			if _, err := c.Call(ctx, http.MethodDelete, assets+"/"+strconv.FormatInt(id, 10), nil, nil); err != nil {
				return "", err
			}
		}
		rc, err := f.Open(ctx)
		if err != nil {
			return "", fmt.Errorf("opening %s from the release: %v", f.Name, err)
		}
		a, err := c.Do(ctx, http.MethodPost, c.URL(assets+"?name="+url.QueryEscape(f.Name)), rc, f.Size, "application/octet-stream")
		rc.Close()
		if err != nil {
			return "", fmt.Errorf("uploading %s: %v", f.Name, err)
		}
		if err := c.Finish(a, nil, nil); err != nil {
			return "", fmt.Errorf("uploading %s: %v", f.Name, err)
		}
	}
	return made.HTMLURL, nil
}

// CommitFile writes one file with POST (a new file) or PUT (an existing one,
// with the sha of what it replaces) /repos/{o}/{r}/contents/{filepath}:
// content (base64), message, branch and new_branch (repoCreateFile,
// repoUpdateFile). Whether the file exists, and its sha, come from GET
// /repos/{o}/{r}/contents/{filepath}?ref= (repoGetContents). The new commit is
// in the answer's commit.sha.
func (r releases) CommitFile(ctx context.Context, repo forge.Repository, ch forge.FileChange) (string, error) {
	rp, err := r.p.repoPath(repo)
	if err != nil {
		return "", err
	}
	if ch.Path == "" || strings.HasPrefix(ch.Path, "/") || strings.Contains(ch.Path, "..") || ch.Branch == "" {
		return "", fmt.Errorf("%w: a file change names a branch and a path inside the repository", forge.ErrInvalid)
	}
	c := r.p.c
	branch, newBranch, readRef := ch.Branch, ch.NewBranch, ch.Branch
	if newBranch != "" {
		// GET /repos/{o}/{r}/branches/{branch} (repoGetBranch): a retry writes to the branch that exists.
		st, err := c.Call(ctx, http.MethodGet, rp+"/branches/"+restkit.EscPath(newBranch), nil, nil, http.StatusNotFound)
		if err != nil {
			return "", err
		}
		if st != http.StatusNotFound {
			branch, newBranch, readRef = newBranch, "", newBranch
		}
	}
	file := rp + "/contents/" + restkit.EscPath(ch.Path)
	var cur struct {
		SHA string `json:"sha"`
	}
	st, err := c.Call(ctx, http.MethodGet, file+"?ref="+url.QueryEscape(readRef), nil, &cur, http.StatusNotFound)
	if err != nil {
		return "", err
	}
	body := map[string]any{"message": ch.Message, "content": base64.StdEncoding.EncodeToString(ch.Content), "branch": branch}
	if newBranch != "" {
		body["new_branch"] = newBranch
	}
	method := http.MethodPost
	if st != http.StatusNotFound {
		method, body["sha"] = http.MethodPut, cur.SHA
	}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if _, err := c.Call(ctx, method, file, body, &out); err != nil {
		return "", err
	}
	if out.Commit.SHA == "" {
		return "", errors.New("the host's answer names no commit")
	}
	return out.Commit.SHA, nil
}

// UpsertSummary posts the summary as a comment on the pull request (a pull
// request is an issue to the comment API): POST /repos/{o}/{r}/issues/{index}/comments
// (issueCreateComment) and PATCH /repos/{o}/{r}/issues/comments/{id}
// (issueEditComment), each with a body. A comment deleted on the host (404) is posted again.
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
