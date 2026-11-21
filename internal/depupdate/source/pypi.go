package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PyPI reads PyPI's JSON API (or a mirror speaking it).
type PyPI struct {
	F       *Fetcher
	BaseURL string // empty: https://pypi.org, unless offline
}

// Ecosystem is "python".
func (*PyPI) Ecosystem() string { return "python" }

type pypiDoc struct {
	Info struct {
		ProjectURLs map[string]string `json:"project_urls"`
	} `json:"info"`
	Releases map[string][]struct {
		Time   string `json:"upload_time_iso_8601"`
		Yanked bool   `json:"yanked"`
	} `json:"releases"`
}

func (p *PyPI) doc(ctx context.Context, name string) (pypiDoc, error) {
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		if p.F.Offline {
			return pypiDoc{}, ErrOffline
		}
		base = "https://pypi.org"
	}
	b, err := p.F.Get(ctx, base+"/pypi/"+name+"/json", nil)
	if err != nil {
		return pypiDoc{}, err
	}
	var d pypiDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return pypiDoc{}, fmt.Errorf("the index's answer for %s is not JSON: %w", name, err)
	}
	return d, nil
}

// Versions lists releases; a release whose files are all yanked is yanked,
// and one with no files is skipped.
func (p *PyPI) Versions(ctx context.Context, name string) ([]Release, error) {
	d, err := p.doc(ctx, name)
	if err != nil {
		return nil, err
	}
	var out []Release
	for v, files := range d.Releases {
		if len(files) == 0 {
			continue
		}
		r := Release{Version: v, Yanked: true}
		for _, f := range files {
			if !f.Yanked {
				r.Yanked = false
			}
			if t, err := time.Parse(time.RFC3339Nano, f.Time); err == nil && (r.Time.IsZero() || t.Before(r.Time)) {
				r.Time = t
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// Detail returns one release.
func (p *PyPI) Detail(ctx context.Context, name, ver string) (Release, error) {
	all, err := p.Versions(ctx, name)
	if err != nil {
		return Release{}, err
	}
	for _, r := range all {
		if r.Version == ver {
			r.NotesURL = "https://pypi.org/project/" + name + "/" + ver + "/"
			return r, nil
		}
	}
	return Release{}, ErrNotFound
}
