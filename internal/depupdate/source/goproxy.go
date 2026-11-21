package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/depupdate/version"
)

// GoProxy speaks the Go module proxy protocol (GOPROXY).
type GoProxy struct {
	F       *Fetcher
	BaseURL string // empty: https://proxy.golang.org, unless the fetcher is offline
}

// Ecosystem is "go".
func (*GoProxy) Ecosystem() string { return "go" }

func (g *GoProxy) base() (string, error) {
	switch {
	case g.BaseURL != "":
		return strings.TrimRight(g.BaseURL, "/"), nil
	case g.F.Offline:
		return "", ErrOffline
	}
	return "https://proxy.golang.org", nil
}

// escape applies the proxy's case encoding: capitals become "!" and the
// lower-case letter.
func escape(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + 32)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (g *GoProxy) list(ctx context.Context, base, module string) ([]string, error) {
	b, err := g.F.Get(ctx, base+"/"+escape(module)+"/@v/list", nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// MajorSplit separates "x/y/v3" into ("x/y", 3); a path without a major
// suffix is major 1 (0 and 1 share the path).
func MajorSplit(module string) (string, int) {
	if i := strings.LastIndex(module, "/v"); i > 0 {
		if n, err := strconv.Atoi(module[i+2:]); err == nil && n >= 2 {
			return module[:i], n
		}
	}
	return module, 1
}

// Versions lists the versions of the module's own path and probes the next
// three major version paths: Go requires a new module path from v2 on
// (x/y/v2), so a newer major version is a different module. Those releases
// carry Module with the new path.
func (g *GoProxy) Versions(ctx context.Context, module string) ([]Release, error) {
	base, err := g.base()
	if err != nil {
		return nil, err
	}
	vs, err := g.list(ctx, base, module)
	if err != nil {
		return nil, err
	}
	var out []Release
	for _, v := range vs {
		out = append(out, Release{Version: v})
	}
	root, major := MajorSplit(module)
	for n := major + 1; n <= major+3; n++ {
		next := root + "/v" + strconv.Itoa(n)
		more, err := g.list(ctx, base, next)
		if err != nil || len(more) == 0 {
			continue
		}
		for _, v := range more {
			out = append(out, Release{Version: v, Module: next})
		}
	}
	return out, nil
}

// Detail reads the .info of a version for its time.
func (g *GoProxy) Detail(ctx context.Context, module, ver string) (Release, error) {
	base, err := g.base()
	if err != nil {
		return Release{}, err
	}
	if root, major := MajorSplit(module); major == 1 {
		// the module may be the v1 path while the version is v2+: use the path of the version
		if v, ok := version.ParseSemver(ver); ok && v.Major >= 2 {
			module = root + "/v" + strconv.FormatUint(v.Major, 10)
		}
	}
	b, err := g.F.Get(ctx, base+"/"+escape(module)+"/@v/"+escape(ver)+".info", nil)
	if err != nil {
		return Release{}, err
	}
	var info struct {
		Version string
		Time    time.Time
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return Release{}, fmt.Errorf("the proxy's answer for %s@%s is not JSON: %w", module, ver, err)
	}
	r := Release{Version: ver, Time: info.Time}
	if strings.HasPrefix(module, "github.com/") {
		parts := strings.Split(module, "/")
		if len(parts) >= 3 {
			r.NotesURL = "https://" + strings.Join(parts[:3], "/") + "/releases/tag/" + ver
		}
	}
	return r, nil
}
