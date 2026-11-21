package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// HostTags lists the tags of an action's repository on the code host the
// workflow files name (its REST API: /repos/{owner}/{repo}/tags).
type HostTags struct {
	F       *Fetcher
	BaseURL string // empty: https://api.github.com, unless offline
}

// Ecosystem is "workflow".
func (*HostTags) Ecosystem() string { return "workflow" }

func (h *HostTags) base() (string, error) {
	switch {
	case h.BaseURL != "":
		return strings.TrimRight(h.BaseURL, "/"), nil
	case h.F.Offline:
		return "", ErrOffline
	}
	return "https://api.github.com", nil
}

type tagRow struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// Versions lists tags (the first 100) with the commit each points at.
func (h *HostTags) Versions(ctx context.Context, repo string) ([]Release, error) {
	base, err := h.base()
	if err != nil {
		return nil, err
	}
	b, err := h.F.Get(ctx, base+"/repos/"+repo+"/tags?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	var rows []tagRow
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("the host's tag list for %s is not JSON: %w", repo, err)
	}
	var out []Release
	for _, r := range rows {
		out = append(out, Release{Version: r.Name, Digest: r.Commit.SHA})
	}
	return out, nil
}

// Detail adds the commit's date and a release-notes link.
func (h *HostTags) Detail(ctx context.Context, repo, tag string) (Release, error) {
	all, err := h.Versions(ctx, repo)
	if err != nil {
		return Release{}, err
	}
	for _, r := range all {
		if r.Version != tag {
			continue
		}
		base, _ := h.base()
		if b, err := h.F.Get(ctx, base+"/repos/"+repo+"/commits/"+r.Digest, nil); err == nil {
			var c struct {
				Commit struct {
					Committer struct {
						Date time.Time `json:"date"`
					} `json:"committer"`
				} `json:"commit"`
			}
			if json.Unmarshal(b, &c) == nil {
				r.Time = c.Commit.Committer.Date
			}
		}
		r.NotesURL = "https://github.com/" + repo + "/releases/tag/" + tag
		return r, nil
	}
	return Release{}, ErrNotFound
}
