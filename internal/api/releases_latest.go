package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// The stable "latest" address of a project's releases, and the count of downloads of each file.
//
//	GET /api/v1/projects/{name}/releases/latest                 the newest published release that is not a pre-release
//	GET /api/v1/projects/{name}/releases/latest/assets/{asset}  one of its files, by name
//	GET /public/releases/{name}/latest                          the same page, with no sign-in, where public downloads are allowed
//	GET /public/releases/{name}/latest/{asset}                  the same file, with no sign-in
//
// The address is the word latest in the place of the version; it follows each new release. The public pair
// answers only where all three switches are on (the installation, the Flight's release settings, and the release
// itself): anywhere else, and for a latest that was not made public, it is a plain not found.

// publicRelease finds the release a public request names, "latest" included, and says whether it may be seen without
// signing in: the installation's switch and the Flight's are checked by the caller, the release's here.
func (s *Server) publicRelease(project, version string) (*engine.Release, bool) {
	var rel *engine.Release
	var err error
	if version == "latest" {
		rel, err = s.Engine.LatestRelease(project)
	} else {
		rel, err = s.Engine.FindRelease(project, version)
	}
	if err != nil || rel.State != engine.ReleasePublished || !rel.Public {
		return nil, false
	}
	return rel, true
}

// publicSwitches says whether the installation and the Flight allow public downloads at all.
func (s *Server) publicSwitches(project string) bool {
	ok, err := s.Engine.PublicReleasesAllowed()
	if err != nil || !ok {
		return false
	}
	set, err := s.Engine.ReleaseSettingsOf(project)
	return err == nil && set.Public
}

// publicReleasePage is what a person without an account may read of a public release: its version, notes and
// files with their sizes, checksums and download counts. Nothing about who made it, the Sortie, the commit's
// pipeline or the Airfields it was promoted to.
func (s *Server) publicReleasePage(w http.ResponseWriter, r *http.Request) {
	notFound := func() { writeErr(w, http.StatusNotFound, "not found") }
	project := r.PathValue("name")
	if !s.publicSwitches(project) {
		notFound()
		return
	}
	rel, ok := s.publicRelease(project, r.PathValue("version"))
	if !ok {
		notFound()
		return
	}
	type file struct {
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		SHA256    string `json:"sha256"`
		Downloads int64  `json:"downloads"`
		URL       string `json:"url"`
	}
	files := []file{}
	for _, a := range rel.Assets {
		if a.Kind != engine.AssetFile {
			continue
		}
		files = append(files, file{a.Name, a.Size, a.SHA256, a.Downloads, s.baseURL(r) + "/public/releases/" + rel.Project + "/" + rel.Version + "/" + a.Name})
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, map[string]any{"project": rel.Project, "version": rel.Version, "title": rel.Title, "notes": rel.Notes,
		"tag": rel.Tag, "prerelease": rel.Prerelease, "latest": rel.Latest, "published_at": rel.PublishedAt, "files": files})
}

// sizedWriter sees whether a download reached its end: the response was a whole file or a range that ends at the
// file's last byte, and every byte of it was written. A continuation of a broken download is counted once, when it
// reaches the end, and the broken part not at all; a HEAD, a partial range or a client that went away counts nothing.
type sizedWriter struct {
	http.ResponseWriter
	size    int64
	status  int
	length  int64
	written int64
	failed  bool
	ends    bool
}

func (c *sizedWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
		c.length, _ = strconv.ParseInt(c.Header().Get("Content-Length"), 10, 64)
		switch code {
		case http.StatusOK:
			c.ends = true
		case http.StatusPartialContent:
			// Content-Range: bytes a-b/size
			if cr := strings.TrimPrefix(c.Header().Get("Content-Range"), "bytes "); cr != "" {
				rng, _, _ := strings.Cut(cr, "/")
				_, last, _ := strings.Cut(rng, "-")
				if n, err := strconv.ParseInt(last, 10, 64); err == nil && n == c.size-1 {
					c.ends = true
				}
			}
		}
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *sizedWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.WriteHeader(http.StatusOK)
	}
	n, err := c.ResponseWriter.Write(b)
	c.written += int64(n)
	if err != nil {
		c.failed = true
	}
	return n, err
}

func (c *sizedWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// complete says whether what was sent counts as a download of the file.
func (c *sizedWriter) complete() bool {
	return !c.failed && c.ends && c.written == c.length
}
