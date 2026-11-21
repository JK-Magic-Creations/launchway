package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/archive"
)

// The archive of another server's issues and comments (internal/forge/archive):
// a read-only record that stays readable and searchable in Launchway after a
// move. Reading is the repository's readers; importing is its administrators.

func (s *Server) archiveRoutes() []route {
	const base = "/api/v1/repositories/{owner}/{name}/archive/issues"
	return []route{
		{"GET " + base, repoRead, "", s.listArchive},
		{"POST " + base, repoAdmin, "archive.import", s.importArchive},
		{"GET " + base + "/{number}", repoRead, "", s.getArchived},
	}
}

// archiveDB lets the archive start transactions on the engine's database.
type archiveDB struct{ *engine.Database }

func (a archiveDB) BeginTx(ctx context.Context) (archive.Tx, error) {
	return a.Begin(ctx)
}

func (s *Server) archive() *archive.Store { return archive.New(archiveDB{s.Engine.Database()}) }

// archiveRepo finds the repository the request names and its id.
func (s *Server) archiveRepo(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.forgeOn(w) {
		return "", false
	}
	repo, err := s.Forge.Provider.Repositories().Get(r.Context(), repoSubject(r.PathValue("owner"), r.PathValue("name")))
	if err != nil {
		s.failForge(w, err)
		return "", false
	}
	return repo.ID, true
}

func (s *Server) failArchive(w http.ResponseWriter, err error) {
	if err == archive.ErrNotFound {
		writeErr(w, http.StatusNotFound, "no such imported issue")
		return
	}
	s.failForge(w, err)
}

func (s *Server) listArchive(w http.ResponseWriter, r *http.Request) {
	id, ok := s.archiveRepo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	per, _ := strconv.Atoi(q.Get("per_page"))
	out, err := s.archive().List(r.Context(), id, archive.Query{State: q.Get("state"), Text: q.Get("q"), Page: page, PerPage: per})
	if err != nil {
		s.failArchive(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getArchived(w http.ResponseWriter, r *http.Request) {
	id, ok := s.archiveRepo(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || n <= 0 {
		writeErr(w, http.StatusBadRequest, "the issue number is a positive whole number")
		return
	}
	is, err := s.archive().Get(r.Context(), id, n)
	if err != nil {
		s.failArchive(w, err)
		return
	}
	writeJSON(w, http.StatusOK, is)
}

func (s *Server) importArchive(w http.ResponseWriter, r *http.Request) {
	id, ok := s.archiveRepo(w, r)
	if !ok {
		return
	}
	var doc archive.Document
	if !decode(w, r, &doc) {
		return
	}
	setAudit(r, repoSubject(r.PathValue("owner"), r.PathValue("name")), "")
	res, err := s.archive().Import(r.Context(), id, identityFrom(r).User, doc)
	if err != nil {
		s.failArchive(w, err)
		return
	}
	setAudit(r, "", strconv.Itoa(res.Issues)+" issues, "+strconv.Itoa(res.Comments)+" comments")
	writeJSON(w, http.StatusOK, res)
}
