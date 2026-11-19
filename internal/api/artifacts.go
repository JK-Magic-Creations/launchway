package api

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Artifacts, findings and SBOM components. Runners upload and download
// artifact files through the runner API; people read them through the UI
// API with the same rights as the run (viewer of its project), and an
// operator may delete one.
//
// Artifact files are whatever a step wrote: untrusted content. They are only
// ever served as downloads: Content-Type application/octet-stream,
// Content-Disposition attachment, X-Content-Type-Options nosniff and a
// Content-Security-Policy that allows nothing and sandboxes the response,
// so a browser never runs or renders one as a page of this site, whatever
// its name or content.

// failArtifact maps the errors of artifact calls that fail does not know.
func (s *Server) failArtifact(w http.ResponseWriter, err error) {
	var co *engine.ChunkOffsetError
	switch {
	case errors.As(err, &co):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "received": co.Received})
	case errors.Is(err, engine.ErrQuota):
		writeErr(w, http.StatusRequestEntityTooLarge, strings.TrimPrefix(err.Error(), engine.ErrQuota.Error()+": "))
	default:
		s.fail(w, err)
	}
}

// ---- runner API ----

func (s *Server) beginArtifact(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LeaseID  string               `json:"lease_id"`
		Artifact engine.BeginArtifact `json:"artifact"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.Engine.BeginArtifact(in.LeaseID, in.Artifact)
	if err != nil {
		s.failArtifact(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// artifactChunk takes one chunk of a file: the body is the bytes, the query
// says which lease, artifact, path and offset, and the chunk's SHA-256.
func (s *Server) artifactChunk(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, err := strconv.ParseInt(q.Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeErr(w, http.StatusBadRequest, "offset must be a whole number")
		return
	}
	if r.ContentLength > engine.ArtifactChunkBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a chunk holds at most %d bytes", engine.ArtifactChunkBytes))
		return
	}
	select {
	case chunkSlots <- struct{}{}:
		defer func() { <-chunkSlots }()
	case <-r.Context().Done():
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, engine.ArtifactChunkBytes))
	if err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a chunk holds at most %d bytes", engine.ArtifactChunkBytes))
			return
		}
		writeErr(w, http.StatusBadRequest, "reading the chunk: "+err.Error())
		return
	}
	got, err := s.Engine.PutArtifactChunk(r.Context(), q.Get("lease_id"), q.Get("artifact_id"), q.Get("path"), offset, q.Get("sha256"), data)
	if err != nil {
		s.failArtifact(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"received": got})
}

// chunkSlots bounds how many chunks one server holds in memory at once.
var chunkSlots = make(chan struct{}, 8)

func (s *Server) commitArtifact(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LeaseID    string `json:"lease_id"`
		ArtifactID string `json:"artifact_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.Engine.CommitArtifact(r.Context(), in.LeaseID, in.ArtifactID)
	if err != nil {
		s.failArtifact(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// fetchArtifact streams a file of an artifact the lease's step fetches,
// from an offset, so a broken download carries on where it was.
func (s *Server) fetchArtifact(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LeaseID    string `json:"lease_id"`
		ArtifactID string `json:"artifact_id"`
		Path       string `json:"path"`
		Offset     int64  `json:"offset"`
	}
	if !decode(w, r, &in) {
		return
	}
	f, err := s.Engine.OpenFetch(r.Context(), in.LeaseID, in.ArtifactID, in.Path, in.Offset)
	if err != nil {
		s.failArtifact(w, err)
		return
	}
	defer f.Body.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(f.Size-in.Offset, 10))
	h.Set("X-Launchway-SHA256", f.SHA256)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f.Body)
}

// ---- UI API ----

func (s *Server) runArtifacts(w http.ResponseWriter, r *http.Request) {
	out, err := s.Engine.RunArtifacts(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// downloadHeaders marks a response as a file to save, never a page.
func downloadHeaders(h http.Header, name string) {
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", attachment(name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}

// attachment is a Content-Disposition that saves a file under name: a
// plain ASCII fallback, and the name itself in RFC 5987 form.
func attachment(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == ".." || name == "/" || name == "" {
		name = "download"
	}
	var ascii strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f || r > 0x7e || r == '"' || r == '\\' || r == '%' || r == ';':
			ascii.WriteByte('_')
		default:
			ascii.WriteRune(r)
		}
	}
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	// RFC 6266 and RFC 8187: a quoted ASCII name for old clients, and the
	// real name percent-encoded as UTF-8.
	var enc strings.Builder
	for _, b := range []byte(clean) {
		if 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9' || strings.IndexByte("!#$&+-.^_`|~", b) >= 0 {
			enc.WriteByte(b)
		} else {
			fmt.Fprintf(&enc, "%%%02X", b)
		}
	}
	return `attachment; filename="` + ascii.String() + `"; filename*=UTF-8''` + enc.String()
}

// downloadArtifactFile serves one file of a kept artifact, ?path=, with
// ranges.
func (s *Server) downloadArtifactFile(w http.ResponseWriter, r *http.Request) {
	runID, artID, p := r.PathValue("id"), r.PathValue("artifact"), r.URL.Query().Get("path")
	f, err := s.Engine.OpenArtifactFile(r.Context(), runID, artID, p, 0)
	if err != nil {
		s.fail(w, err)
		return
	}
	downloadHeaders(w.Header(), f.Path)
	w.Header().Set("ETag", `"sha256-`+f.SHA256+`"`)
	w.Header().Set("X-Launchway-SHA256", f.SHA256)
	rs := &artifactSeeker{ctx: r.Context(), size: f.Size, body: f.Body, open: func(ctx context.Context, off int64) (io.ReadCloser, error) {
		g, err := s.Engine.OpenArtifactFile(ctx, runID, artID, p, off)
		if err != nil {
			return nil, err
		}
		return g.Body, nil
	}}
	defer rs.Close()
	// ServeContent answers Range and If-Range; the type is set above, so it
	// does not sniff the content.
	http.ServeContent(w, r, "", time.Time{}, rs)
}

// artifactSeeker reads a stored file as an io.ReadSeeker, opening it again
// at the offset after a seek.
type artifactSeeker struct {
	ctx  context.Context
	size int64
	pos  int64
	body io.ReadCloser // positioned at pos, or nil
	open func(ctx context.Context, off int64) (io.ReadCloser, error)
}

func (a *artifactSeeker) Read(p []byte) (int, error) {
	if a.pos >= a.size {
		return 0, io.EOF
	}
	if a.body == nil {
		b, err := a.open(a.ctx, a.pos)
		if err != nil {
			return 0, err
		}
		a.body = b
	}
	n, err := a.body.Read(p)
	a.pos += int64(n)
	return n, err
}

func (a *artifactSeeker) Seek(off int64, whence int) (int64, error) {
	var to int64
	switch whence {
	case io.SeekStart:
		to = off
	case io.SeekCurrent:
		to = a.pos + off
	case io.SeekEnd:
		to = a.size + off
	}
	if to < 0 {
		return 0, errors.New("seek before the start")
	}
	if to != a.pos && a.body != nil {
		a.body.Close()
		a.body = nil
	}
	a.pos = to
	return to, nil
}

func (a *artifactSeeker) Close() error {
	if a.body != nil {
		return a.body.Close()
	}
	return nil
}

// downloadArtifactArchive serves every file of a kept artifact as a zip
// archive, streamed.
func (s *Server) downloadArtifactArchive(w http.ResponseWriter, r *http.Request) {
	a, err := s.Engine.ArtifactForArchive(r.PathValue("id"), r.PathValue("artifact"))
	if err != nil {
		s.fail(w, err)
		return
	}
	downloadHeaders(w.Header(), a.Step+"-"+a.Name+".zip")
	w.Header().Set("Content-Type", "application/zip")
	w.WriteHeader(http.StatusOK)
	zw := zip.NewWriter(w)
	for _, f := range a.Files {
		fh := &zip.FileHeader{Name: f.Path, Method: zip.Deflate, Modified: time.Now()}
		fh.SetMode(0o644)
		if f.Mode&0o111 != 0 {
			fh.SetMode(0o755)
		}
		dst, err := zw.CreateHeader(fh)
		if err != nil {
			return
		}
		src, err := a.Open(r.Context(), f.Path)
		if err != nil {
			// The status is sent: all that can be done is to stop, which
			// leaves an archive the client sees is broken.
			s.Log.Warn("streaming an artifact archive", "artifact", r.PathValue("artifact"), "err", err)
			return
		}
		_, err = io.Copy(dst, src.Body)
		src.Body.Close()
		if err != nil {
			return
		}
	}
	_ = zw.Close()
}

// deleteArtifact deletes a kept artifact; the audit trail records who did.
func (s *Server) deleteArtifact(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	actor := s.Actor
	if id != nil {
		actor = id.User
	}
	v, err := s.Engine.DeleteArtifact(r.PathValue("id"), r.PathValue("artifact"), actor)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, r.PathValue("id"), fmt.Sprintf("artifact %s of step %s (attempt %d): %d files, %d bytes", v.Name, v.Step, v.Attempt, v.FileCount, v.Bytes))
	writeJSON(w, http.StatusOK, v)
}

// runFindings is a run's security findings: ?severity= (comma separated),
// ?tool=, ?status=new|existing|fixed, ?q=, ?step=, ?limit=, ?offset=.
func (s *Server) runFindings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fq := engine.FindingQuery{Tool: q.Get("tool"), Status: q.Get("status"), Search: q.Get("q"), Step: q.Get("step")}
	for _, sv := range strings.Split(q.Get("severity"), ",") {
		if sv = strings.TrimSpace(sv); sv == "" {
			continue
		}
		if !engine.ValidSeverity(sv) {
			writeErr(w, http.StatusBadRequest, "severity is critical, high, medium, low or unknown")
			return
		}
		fq.Severities = append(fq.Severities, sv)
	}
	switch fq.Status {
	case "", engine.FindingNew, engine.FindingExisting, engine.FindingFixed:
	default:
		writeErr(w, http.StatusBadRequest, "status is new, existing or fixed")
		return
	}
	if len(fq.Search) > 200 || len(fq.Tool) > 300 || len(fq.Step) > 300 {
		writeErr(w, http.StatusBadRequest, "the search text is longer than 200 bytes")
		return
	}
	var err error
	if fq.Limit, err = intParam(r, "limit"); err == nil {
		fq.Offset, err = intParam(r, "offset")
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.Engine.RunFindings(r.PathValue("id"), fq)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// projectSecurity is a project's findings over the last runs of a ref
// (?ref=, ?runs=, default 30).
func (s *Server) projectSecurity(w http.ResponseWriter, r *http.Request) {
	n, err := intParam(r, "runs")
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.Engine.ProjectSecurity(r.PathValue("name"), s.projectRef(r), n)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// searchComponents finds the runs whose SBOMs list a component: ?name=
// (exact, ignoring case), ?version=, ?limit=. Only projects the caller may
// see are searched.
func (s *Server) searchComponents(w http.ResponseWriter, r *http.Request) {
	only, err := s.visibleProjects(identityFrom(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	n, err := intParam(r, "limit")
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.Engine.SearchComponents(r.URL.Query().Get("name"), r.URL.Query().Get("version"), only, n)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// runImages lists what a run's image steps built: digest, tags, platforms,
// signature, provenance, SBOM and scan, for the latest attempt of each.
func (s *Server) runImages(w http.ResponseWriter, r *http.Request) {
	out, err := s.Engine.RunImages(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": out})
}

// searchImages answers "which run built this digest" (?digest=) and "what
// is the latest image of this repository" (?repository=host/path, with
// ?tag=), or lists a project's images (?project=); ?limit=. Only projects
// the caller may see are searched.
func (s *Server) searchImages(w http.ResponseWriter, r *http.Request) {
	only, err := s.visibleProjects(identityFrom(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	n, err := intParam(r, "limit")
	if err != nil {
		s.fail(w, err)
		return
	}
	q := r.URL.Query()
	hits, err := s.Engine.SearchImages(engine.ImageQuery{Digest: q.Get("digest"), Repository: q.Get("repository"), Tag: q.Get("tag"),
		Project: q.Get("project"), Limit: n}, only)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}
