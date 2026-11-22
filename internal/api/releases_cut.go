package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Cutting a release in one request, and serving its files.

// releaseHalfDone answers a request that made the draft and then could not go on: the draft stands, and the answer
// says so and what to do.
func (s *Server) releaseHalfDone(w http.ResponseWriter, rel *engine.Release, what string, err error) {
	msg := fmt.Sprintf("The draft %s %s was made, but %s: %s. It is still a draft: fix that and publish it from its page, or delete it.", rel.Project, rel.Version, what, errText(err))
	writeErr(w, http.StatusConflict, msg)
}

// attachCargoOf copies the files a Sortie kept, and its image digests, into a draft. With no Sortie named, the latest
// successful one at the release's commit is used; none at that commit is an error that says so.
func (s *Server) attachCargoOf(ctx context.Context, rel *engine.Release, runID, actor string) error {
	if runID == "" {
		runs, err := s.Engine.ListRuns(engine.RunFilter{Project: rel.Project, Status: string(engine.RunSucceeded), Limit: 200})
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.SHA == rel.SHA {
				runID = run.ID
				break
			}
		}
		if runID == "" {
			return fmt.Errorf("no Sortie of %s succeeded at commit %s, so there is nothing built to attach: run the pipeline on that commit first", rel.Project, rel.SHA[:8])
		}
	}
	arts, err := s.Engine.RunArtifacts(runID)
	if err != nil {
		return err
	}
	for _, a := range arts.Artifacts {
		if a.Status != "kept" || a.Reused {
			continue
		}
		for _, f := range a.Files {
			if _, err := s.Engine.AddReleaseCargo(ctx, rel.ID, runID, a.ID, f.Path, "", actor); err != nil {
				return err
			}
		}
	}
	_, err = s.Engine.AddReleaseRunOutputs(rel.ID, runID, actor)
	return err
}

// buildAt says what the latest successful Sortie at a commit kept, so a person cutting a release sees which files it
// will carry. Nil when no Sortie succeeded there.
func (s *Server) buildAt(project, sha string) map[string]any {
	runs, err := s.Engine.ListRuns(engine.RunFilter{Project: project, Status: string(engine.RunSucceeded), Limit: 200})
	if err != nil {
		return nil
	}
	for _, run := range runs {
		if run.SHA != sha {
			continue
		}
		files := []map[string]any{}
		if arts, err := s.Engine.RunArtifacts(run.ID); err == nil {
			for _, a := range arts.Artifacts {
				if a.Status != "kept" || a.Reused {
					continue
				}
				for _, f := range a.Files {
					files = append(files, map[string]any{"name": path.Base(f.Path), "path": f.Path, "size": f.Size, "sha256": f.SHA256})
				}
			}
		}
		return map[string]any{"run_id": run.ID, "run_number": run.Number, "files": files}
	}
	return nil
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, rel *engine.Release) {
	name := r.PathValue("asset")
	rc, a, err := s.Engine.OpenReleaseAsset(r.Context(), rel.ID, name)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Every download is recorded; the later ranges of a resumed one say so.
	detail := "download " + a.Name + " (" + strconv.FormatInt(a.Size, 10) + " bytes)"
	if h := r.Header.Get("Range"); h != "" && !strings.HasPrefix(h, "bytes=0-") {
		detail += ", continued"
	}
	setAudit(r, releaseTarget(rel), detail)
	downloadHeaders(w.Header(), a.Name)
	w.Header().Set("Content-Type", assetContentType(a.Name))
	w.Header().Set("X-Launchway-SHA256", a.SHA256)
	w.Header().Set("ETag", `"sha256-`+a.SHA256+`"`)
	// The file is read from the store as it is sent, never held in memory. ServeContent answers Range and If-Range,
	// so a phone resumes a broken download; it sets Content-Length itself.
	rs := &artifactSeeker{ctx: r.Context(), size: a.Size, body: rc, open: func(ctx context.Context, off int64) (io.ReadCloser, error) {
		g, _, err := s.Engine.OpenReleaseAssetAt(ctx, rel.ID, name, off)
		return g, err
	}}
	defer rs.Close()
	if r.Method != http.MethodGet {
		http.ServeContent(w, r, "", time.Time{}, rs)
		return
	}
	cw := &countingWriter{ResponseWriter: w, size: a.Size}
	http.ServeContent(cw, r, "", time.Time{}, rs)
	if cw.complete() && r.Context().Err() == nil {
		if err := s.Engine.CountReleaseDownload(rel.ID, a.Name); err != nil {
			s.Log.Warn("a release download could not be counted", "release", rel.ID, "file", a.Name, "err", err)
		}
	}
}

// assetContentType is the type a file is served as. Files are always downloads (never shown as pages), so an
// unknown or active type (a page, a script) is a plain byte stream; an Android package is named as one, so a phone
// offers to install it.
func assetContentType(name string) string {
	n := strings.ToLower(name)
	for suffix, t := range map[string]string{
		".apk": "application/vnd.android.package-archive", ".aab": "application/octet-stream",
		".zip": "application/zip", ".tar.gz": "application/gzip", ".tgz": "application/gzip", ".gz": "application/gzip",
		".tar": "application/x-tar", ".xz": "application/x-xz", ".bz2": "application/x-bzip2", ".7z": "application/x-7z-compressed",
		".jar": "application/java-archive", ".pdf": "application/pdf", ".json": "application/json",
		".txt": "text/plain; charset=utf-8", ".sig": "text/plain; charset=utf-8", ".sha256": "text/plain; charset=utf-8",
		".deb": "application/vnd.debian.binary-package", ".rpm": "application/x-rpm", ".dmg": "application/x-apple-diskimage",
		".msi": "application/x-msi", ".whl": "application/zip", ".ipa": "application/octet-stream",
	} {
		if strings.HasSuffix(n, suffix) {
			return t
		}
	}
	if strings.HasSuffix(n, "sha256sums") {
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}
