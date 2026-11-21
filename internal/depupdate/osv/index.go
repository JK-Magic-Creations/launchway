package osv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
)

// DirIndex reads an OSV database mirror from a directory: every *.json file
// below it is one record. It is the offline source.
type DirIndex struct {
	mu     sync.Mutex
	byPkg  map[string][]Vuln
	loaded bool
	FS     fs.FS
	// Skipped counts files that were not valid records.
	Skipped int
}

func key(eco, name string) string {
	if eco == "PyPI" {
		name = strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(name))
	}
	return eco + "|" + name
}

func (d *DirIndex) load() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loaded {
		return nil
	}
	d.byPkg = map[string][]Vuln{}
	err := fs.WalkDir(d.FS, ".", func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		info, err := e.Info()
		if err != nil || info.Size() > 8<<20 {
			d.Skipped++
			return nil
		}
		b, err := fs.ReadFile(d.FS, p)
		if err != nil {
			d.Skipped++
			return nil
		}
		v, err := Raw(b)
		if err != nil || v.ID == "" {
			d.Skipped++
			return nil
		}
		done := map[string]bool{}
		for _, a := range v.Affected {
			k := key(a.Package.Ecosystem, a.Package.Name)
			if !done[k] {
				d.byPkg[k] = append(d.byPkg[k], v)
				done[k] = true
			}
		}
		return nil
	})
	d.loaded = true
	return err
}

// Lookup returns every record of the package.
func (d *DirIndex) Lookup(_ context.Context, q Query) ([]Vuln, error) {
	if err := d.load(); err != nil {
		return nil, err
	}
	return d.byPkg[key(q.Ecosystem, q.Name)], nil
}

// API asks an OSV API ("https://api.osv.dev" or an installation's own).
type API struct {
	Client  *http.Client
	BaseURL string
	mu      sync.Mutex
	full    map[string]Vuln
}

// Lookup asks for the vulnerabilities of one package version, then reads
// each record in full.
func (a *API) Lookup(ctx context.Context, q Query) ([]Vuln, error) {
	body, _ := json.Marshal(map[string]any{"version": q.Version, "package": map[string]string{"name": q.Name, "ecosystem": q.Ecosystem}})
	var res struct {
		Vulns []struct {
			ID string `json:"id"`
		} `json:"vulns"`
	}
	if err := a.do(ctx, http.MethodPost, "/v1/query", body, &res); err != nil {
		return nil, err
	}
	var out []Vuln
	for _, r := range res.Vulns {
		a.mu.Lock()
		v, ok := a.full[r.ID]
		a.mu.Unlock()
		if !ok {
			if err := a.do(ctx, http.MethodGet, "/v1/vulns/"+r.ID, nil, &v); err != nil {
				return nil, err
			}
			a.mu.Lock()
			if a.full == nil {
				a.full = map[string]Vuln{}
			}
			a.full[r.ID] = v
			a.mu.Unlock()
		}
		out = append(out, v)
	}
	return out, nil
}

func (a *API) do(ctx context.Context, method, path string, body []byte, into any) error {
	c := a.Client
	if c == nil {
		c = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the OSV service answered %s for %s", resp.Status, path)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(into)
}
