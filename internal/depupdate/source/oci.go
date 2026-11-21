package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// OCI reads tag lists and digests from container registries.
type OCI struct {
	F *Fetcher
	// Mirrors maps a registry host ("docker.io", "ghcr.io") to the base URL
	// to ask instead ("https://registry.example.com"). In offline mode only
	// registries with a mirror are asked.
	Mirrors map[string]string
	// Scheme is "https" unless a test registry needs "http" (for hosts in Insecure).
	Insecure map[string]bool
}

// Ecosystem is "container".
func (*OCI) Ecosystem() string { return "container" }

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// Split breaks an image name into registry host and repository path:
// "node" is docker.io/library/node, "grafana/grafana" docker.io/grafana/grafana.
func Split(name string) (host, repo string) {
	first, rest, ok := strings.Cut(name, "/")
	if !ok {
		return "docker.io", "library/" + name
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first, rest
	}
	return "docker.io", name
}

func (o *OCI) base(host string) (string, error) {
	if m := o.Mirrors[host]; m != "" {
		return strings.TrimRight(m, "/"), nil
	}
	if o.F.Offline {
		return "", ErrOffline
	}
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	scheme := "https"
	if o.Insecure[host] {
		scheme = "http"
	}
	return scheme + "://" + host, nil
}

type tagList struct {
	Tags []string `json:"tags"`
}

// Versions lists the tags of the repository (at most 20 pages of results).
func (o *OCI) Versions(ctx context.Context, name string) ([]Release, error) {
	host, repo := Split(name)
	base, err := o.base(host)
	if err != nil {
		return nil, err
	}
	var out []Release
	next := base + "/v2/" + repo + "/tags/list?n=1000"
	for page := 0; page < 20 && next != ""; page++ {
		b, err := o.F.Get(ctx, next, nil)
		if err != nil {
			return nil, err
		}
		var tl tagList
		if err := json.Unmarshal(b, &tl); err != nil {
			return nil, fmt.Errorf("the registry's tag list for %s is not JSON: %w", name, err)
		}
		for _, t := range tl.Tags {
			out = append(out, Release{Version: t})
		}
		next = ""
		if len(tl.Tags) == 1000 {
			next = base + "/v2/" + repo + "/tags/list?n=1000&last=" + tl.Tags[len(tl.Tags)-1]
		}
	}
	return out, nil
}

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Detail returns the digest of a tag and, when the image's config says so,
// when it was built.
func (o *OCI) Detail(ctx context.Context, name, tag string) (Release, error) {
	host, repo := Split(name)
	base, err := o.base(host)
	if err != nil {
		return Release{}, err
	}
	h := http.Header{"Accept": {manifestAccept}}
	murl := base + "/v2/" + repo + "/manifests/" + tag
	hd, err := o.F.Head(ctx, murl, h)
	if err != nil {
		return Release{}, err
	}
	r := Release{Version: tag, Digest: hd.Get("Docker-Content-Digest")}
	if r.Digest != "" && !digestRe.MatchString(r.Digest) && !strings.HasPrefix(r.Digest, "sha256:") {
		return Release{}, fmt.Errorf("the registry sent an unusable digest for %s:%s", name, tag)
	}
	body, err := o.F.Get(ctx, murl, h)
	if err != nil {
		return r, nil
	}
	var m struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				Arch string `json:"architecture"`
				OS   string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if json.Unmarshal(body, &m) != nil {
		return r, nil
	}
	if len(m.Manifests) > 0 {
		pick := m.Manifests[0].Digest
		for _, x := range m.Manifests {
			if x.Platform.Arch == "amd64" && x.Platform.OS == "linux" {
				pick = x.Digest
				break
			}
		}
		if body, err = o.F.Get(ctx, base+"/v2/"+repo+"/manifests/"+pick, h); err != nil {
			return r, nil
		}
		m.Config.Digest = ""
		_ = json.Unmarshal(body, &m)
	}
	if m.Config.Digest != "" {
		if cb, err := o.F.Get(ctx, base+"/v2/"+repo+"/blobs/"+m.Config.Digest, nil); err == nil {
			var c struct {
				Created time.Time `json:"created"`
			}
			if json.Unmarshal(cb, &c) == nil {
				r.Time = c.Created
			}
		}
	}
	return r, nil
}
