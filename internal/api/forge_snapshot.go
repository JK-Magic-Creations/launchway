package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge/files"
)

// A snapshot of a ref of a hosted repository, as a tar.gz or a zip:
//
//	GET /api/v1/repositories/{owner}/{name}/download?ref=REF&format=tar.gz|zip
//
// It takes the repository's read permission, is refused before the first byte when the files add up to more than the
// installation's limit (hosting settings), is streamed (nothing is held in memory or on disk) and is in the
// Logbook with the repository, the ref, the commit and the size. It is rate limited like the other expensive reads.

var archiveBuckets = &bucket{last: map[string]time.Time{}, n: map[string]float64{}}

func (s *Server) archiveDownloadRoutes() []route {
	return []route{{"GET /api/v1/repositories/{owner}/{name}/download", repoRead, "repository.archive", s.downloadSnapshot}}
}

var unsafeNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (s *Server) downloadSnapshot(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	if !s.limitRead(w, r, archiveBuckets, 6, 6, "downloading snapshots") {
		return
	}
	ref, format := defaultRef(r, repo), r.URL.Query().Get("format")
	if format == "" {
		format = "tar.gz"
	}
	name := strings.Trim(unsafeNameRe.ReplaceAllString(repo.Name+"-"+ref, "-"), "-.")
	limit := int64(s.hostingSettings().ArchiveMaxMiB) << 20
	sent := false
	err := s.filesService().Archive(r.Context(), repo.ID, ref, format, name, limit, func(i files.ArchiveInfo) (io.Writer, error) {
		sent = true
		h := w.Header()
		ct := "application/gzip"
		if format == "zip" {
			ct = "application/zip"
		}
		h.Set("Content-Type", ct)
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": asciiName(name + "." + format)}))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "private, no-store")
		h.Set("X-Launchway-Commit", i.Commit)
		setAudit(r, repo.Path, fmt.Sprintf("%s of %s at %s: %d files, %d bytes", format, ref, i.Commit[:min(12, len(i.Commit))], i.Files, i.Bytes))
		return w, nil
	})
	if err != nil && !sent {
		h := w.Header()
		h.Set("Content-Type", "application/json")
		if errors.Is(err, files.ErrTooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, strings.TrimPrefix(err.Error(), "too large: "))
			return
		}
		s.failFiles(w, err)
	}
}

// The description a pull request starts with, from the repository (internal/forge/files/template.go):
//
//	GET /api/v1/repositories/{owner}/{name}/pull-template?ref=TARGET
//
// It reads the target branch (default: the default branch) and answers with the file's path and text, or an empty
// template when the repository has none, so a page or a script needs no second answer for that case.

func (s *Server) pullTemplateRoutes() []route {
	return []route{{"GET /api/v1/repositories/{owner}/{name}/pull-template", repoRead, "", s.getPullTemplate}}
}

func (s *Server) getPullTemplate(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	t, err := s.filesService().PullTemplate(r.Context(), repo.ID, defaultRef(r, repo))
	if err != nil {
		s.failFiles(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}
