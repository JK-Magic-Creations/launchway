package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// NPMRegistry reads package documents of an npm registry.
type NPMRegistry struct {
	F       *Fetcher
	BaseURL string // empty: https://registry.npmjs.org, unless offline
}

// Ecosystem is "npm".
func (*NPMRegistry) Ecosystem() string { return "npm" }

type npmDoc struct {
	Versions map[string]struct {
		Deprecated json.RawMessage `json:"deprecated"`
		Repository json.RawMessage `json:"repository"`
	} `json:"versions"`
	Time map[string]time.Time `json:"time"`
	Repo json.RawMessage      `json:"repository"`
}

func (n *NPMRegistry) doc(ctx context.Context, name string) (npmDoc, error) {
	base := strings.TrimRight(n.BaseURL, "/")
	if base == "" {
		if n.F.Offline {
			return npmDoc{}, ErrOffline
		}
		base = "https://registry.npmjs.org"
	}
	b, err := n.F.Get(ctx, base+"/"+url.PathEscape(name), nil)
	if err != nil {
		return npmDoc{}, err
	}
	var d npmDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return npmDoc{}, fmt.Errorf("the registry's document for %s is not JSON: %w", name, err)
	}
	return d, nil
}

// Versions lists every published version with its time; deprecated versions
// are marked.
func (n *NPMRegistry) Versions(ctx context.Context, name string) ([]Release, error) {
	d, err := n.doc(ctx, name)
	if err != nil {
		return nil, err
	}
	var out []Release
	for v, e := range d.Versions {
		r := Release{Version: v, Time: d.Time[v]}
		if s := string(e.Deprecated); s != "" && s != "false" && s != `""` {
			r.Deprecated = strings.Trim(s, `"`)
		}
		out = append(out, r)
	}
	return out, nil
}

// Detail returns one version (the document is cached, so this is cheap).
func (n *NPMRegistry) Detail(ctx context.Context, name, ver string) (Release, error) {
	d, err := n.doc(ctx, name)
	if err != nil {
		return Release{}, err
	}
	if _, ok := d.Versions[ver]; !ok {
		return Release{}, ErrNotFound
	}
	r := Release{Version: ver, Time: d.Time[ver]}
	r.NotesURL = "https://www.npmjs.com/package/" + name + "/v/" + ver
	return r, nil
}
