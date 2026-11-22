package gitlab

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

// packageName is the generic package under which release files are stored; the
// package version is the tag.
const packageName = "launchway-release"

type releases struct{ p *Provider }

type apiTag struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

// CreateTag: GET /projects/:id/repository/tags/:tag_name, then POST
// /projects/:id/repository/tags with tag_name, ref (the commit) and message,
// which makes an annotated tag (https://docs.gitlab.com/api/tags/). A tag
// that exists is accepted when it already stands for the same commit.
func (r releases) CreateTag(ctx context.Context, repo forge.Repository, t forge.TagSpec) (forge.Ref, error) {
	pp, err := r.p.projectPath(repo)
	if err != nil {
		return forge.Ref{}, err
	}
	var have apiTag
	st, err := r.p.c.Call(ctx, http.MethodGet, pp+"/repository/tags/"+restkit.Esc(t.Name), nil, &have, http.StatusNotFound)
	if err != nil {
		return forge.Ref{}, err
	}
	if st != http.StatusNotFound {
		if have.Commit.ID != t.Commit {
			return forge.Ref{}, fmt.Errorf("%w: the tag %s already exists on the host and stands for another commit", forge.ErrExists, t.Name)
		}
		return forge.Ref{Name: "refs/tags/" + t.Name, Commit: have.Target}, nil
	}
	var made apiTag
	if _, err := r.p.c.Call(ctx, http.MethodPost, pp+"/repository/tags", map[string]any{"tag_name": t.Name, "ref": t.Commit, "message": t.Message}, &made); err != nil {
		return forge.Ref{}, err
	}
	return forge.Ref{Name: "refs/tags/" + t.Name, Commit: made.Target}, nil
}

type apiRelease struct {
	Links struct {
		Self string `json:"self"`
	} `json:"_links"`
}

type apiLink struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// PublishRelease finds the release of the tag (GET /projects/:id/releases/:tag_name),
// creates it (POST /projects/:id/releases: tag_name, name, description) or
// updates it (PUT /projects/:id/releases/:tag_name: name, description)
// (https://docs.gitlab.com/api/releases/), and attaches each file the release
// does not link yet: the file is stored with PUT
// /projects/:id/packages/generic/:package_name/:package_version/:file_name
// (https://docs.gitlab.com/user/packages/generic_packages/) and linked with
// POST /projects/:id/releases/:tag_name/assets/links (name, url, link_type
// package; https://docs.gitlab.com/api/releases/links/). The host has no
// pre-release flag in its release API, so ReleaseSpec.Prerelease is not sent.
// A file whose name is already linked is not uploaded again.
func (r releases) PublishRelease(ctx context.Context, repo forge.Repository, rel forge.ReleaseSpec) (string, error) {
	pp, err := r.p.projectPath(repo)
	if err != nil {
		return "", err
	}
	c := r.p.c
	// The tag must exist: a release of a missing tag would make the host tag its ref.
	st, err := c.Call(ctx, http.MethodGet, pp+"/repository/tags/"+restkit.Esc(rel.Tag), nil, nil, http.StatusNotFound)
	if err != nil {
		return "", err
	}
	if st == http.StatusNotFound {
		return "", fmt.Errorf("%w: %s is not there yet, or the credential cannot see the project", ErrTagMissing, rel.Tag)
	}
	var made apiRelease
	st, err = c.Call(ctx, http.MethodGet, pp+"/releases/"+restkit.Esc(rel.Tag), nil, &made, http.StatusNotFound)
	if err != nil {
		return "", err
	}
	name := rel.Title
	if name == "" {
		name = rel.Tag
	}
	body := map[string]any{"name": name, "description": rel.Notes}
	if st == http.StatusNotFound {
		body["tag_name"] = rel.Tag
		if _, err := c.Call(ctx, http.MethodPost, pp+"/releases", body, &made); err != nil {
			return "", err
		}
	} else if _, err := c.Call(ctx, http.MethodPut, pp+"/releases/"+restkit.Esc(rel.Tag), body, &made); err != nil {
		return "", err
	}
	if len(rel.Files) > 0 {
		var links []apiLink
		if _, err := c.Get(ctx, pp+"/releases/"+restkit.Esc(rel.Tag)+"/assets/links", &links); err != nil {
			return "", err
		}
		have := map[string]bool{}
		for _, l := range links {
			have[l.Name] = true
		}
		for _, f := range rel.Files {
			if have[f.Name] {
				continue
			}
			dl, err := r.upload(ctx, pp, rel.Tag, f)
			if err != nil {
				return "", err
			}
			if _, err := c.Call(ctx, http.MethodPost, pp+"/releases/"+restkit.Esc(rel.Tag)+"/assets/links",
				map[string]any{"name": f.Name, "url": dl, "link_type": "package"}, nil); err != nil {
				return "", err
			}
		}
	}
	return made.Links.Self, nil
}

// upload stores one file in the generic package registry and returns its download address.
func (r releases) upload(ctx context.Context, pp, tag string, f forge.ReleaseFile) (string, error) {
	if strings.Contains(f.Name, " ") || strings.HasPrefix(f.Name, "~") || strings.HasSuffix(f.Name, "~") || strings.HasPrefix(f.Name, "@") || strings.HasSuffix(f.Name, "@") {
		return "", fmt.Errorf("%w: the host's package registry takes file names without spaces and not starting or ending with ~ or @ (%q)", forge.ErrInvalid, f.Name)
	}
	path := pp + "/packages/generic/" + packageName + "/" + restkit.Esc(tag) + "/" + restkit.EscPath(f.Name)
	rc, err := f.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("opening %s from the release: %v", f.Name, err)
	}
	defer rc.Close()
	c := r.p.c
	a, err := c.Do(ctx, http.MethodPut, c.URL(path), rc, f.Size, "application/octet-stream")
	if err != nil {
		return "", fmt.Errorf("uploading %s: %v", f.Name, err)
	}
	if err := c.Finish(a, nil, nil); err != nil {
		return "", fmt.Errorf("uploading %s: %v", f.Name, err)
	}
	return c.URL(path), nil
}

// CommitFile writes one file with POST (a new file) or PUT (an existing one)
// /projects/:id/repository/files/:file_path: branch, content (base64),
// commit_message, and start_branch to cut a new branch from another in the same
// call (https://docs.gitlab.com/api/repository_files/). Whether the file exists is
// asked with HEAD on the same address with ref. The new commit is the branch's
// head: GET /projects/:id/repository/branches/:branch.
func (r releases) CommitFile(ctx context.Context, repo forge.Repository, ch forge.FileChange) (string, error) {
	pp, err := r.p.projectPath(repo)
	if err != nil {
		return "", err
	}
	if ch.Path == "" || strings.HasPrefix(ch.Path, "/") || strings.Contains(ch.Path, "..") || ch.Branch == "" {
		return "", fmt.Errorf("%w: a file change names a branch and a path inside the repository", forge.ErrInvalid)
	}
	c := r.p.c
	target, start, readRef := ch.Branch, "", ch.Branch
	if ch.NewBranch != "" {
		target = ch.NewBranch
		st, err := c.Call(ctx, http.MethodGet, pp+"/repository/branches/"+restkit.Esc(ch.NewBranch), nil, nil, http.StatusNotFound)
		if err != nil {
			return "", err
		}
		if st == http.StatusNotFound {
			start = ch.Branch
		} else {
			readRef = ch.NewBranch
		}
	}
	file := pp + "/repository/files/" + restkit.Esc(ch.Path)
	a, err := c.Do(ctx, http.MethodHead, c.URL(file+"?ref="+url.QueryEscape(readRef)), nil, 0, "")
	if err != nil {
		return "", err
	}
	method := http.MethodPost
	switch {
	case a.Status == http.StatusNotFound:
	case a.Status < 300:
		method = http.MethodPut
	default:
		if err := c.Finish(a, nil, nil); err != nil {
			return "", err
		}
	}
	body := map[string]any{"branch": target, "content": base64.StdEncoding.EncodeToString(ch.Content), "encoding": "base64", "commit_message": ch.Message}
	if start != "" {
		body["start_branch"] = start
	}
	if _, err := c.Call(ctx, method, file, body, nil); err != nil {
		return "", err
	}
	var br struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if _, err := c.Get(ctx, pp+"/repository/branches/"+restkit.Esc(target), &br); err != nil {
		return "", err
	}
	if br.Commit.ID == "" {
		return "", errors.New("the host's answer names no commit")
	}
	return br.Commit.ID, nil
}

// UpsertSummary posts the summary as a note on the merge request:
// POST /projects/:id/merge_requests/:iid/notes (body), and edits it in place
// with PUT /projects/:id/merge_requests/:iid/notes/:note_id (body)
// (https://docs.gitlab.com/api/notes/). A note that was deleted on the host
// (404) is posted again.
func (r releases) UpsertSummary(ctx context.Context, repo forge.Repository, number int, key, body string) (string, error) {
	pp, err := r.p.projectPath(repo)
	if err != nil {
		return "", err
	}
	notes := pp + "/merge_requests/" + strconv.Itoa(number) + "/notes"
	var out struct {
		ID int64 `json:"id"`
	}
	if key != "" {
		if _, err := strconv.ParseInt(key, 10, 64); err != nil {
			return "", fmt.Errorf("%w: bad note key", forge.ErrInvalid)
		}
		st, err := r.p.c.Call(ctx, http.MethodPut, notes+"/"+key, map[string]string{"body": body}, &out, http.StatusNotFound)
		if err != nil {
			return "", err
		}
		if st != http.StatusNotFound {
			return key, nil
		}
	}
	if _, err := r.p.c.Call(ctx, http.MethodPost, notes, map[string]string{"body": body}, &out); err != nil {
		return "", err
	}
	return strconv.FormatInt(out.ID, 10), nil
}
