package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
)

// The trash of repositories: deleted repositories, who deleted them and when,
// restoring one (under another name when its address was taken), and removing
// one for good (an administrator, with the address typed to confirm). The
// background worker removes what has waited out the retention (hosting setting
// trash_retention_days) with the same code.

// TrashService is the trash, as hosting provides it.
type TrashService interface {
	NoteDeleted(ctx context.Context, id, by string) error
	TrashList(ctx context.Context) ([]forge.TrashedRepository, error)
	TrashGet(ctx context.Context, id string) (forge.TrashedRepository, error)
	TrashRestore(ctx context.Context, id, owner, name string) (forge.Repository, error)
	TrashPurge(ctx context.Context, id string) (forge.PurgeSummary, error)
}

func (s *Server) trashRoutes() []route {
	return []route{
		{"GET /api/v1/repository-trash", signedIn, "", s.listTrash},
		{"POST /api/v1/repository-trash/{id}/restore", signedIn, "repository.restore", s.restoreTrash},
		{"DELETE /api/v1/repository-trash/{id}", administer, "repository.purge", s.purgeTrash},
	}
}

// mayManageTrashed says whether the caller administers a deleted repository: an
// admin of the repository's address (its owner/name bindings still apply) or of
// everything. A token limited to repositories never does.
func (s *Server) mayManageTrashed(id *identity, t forge.TrashedRepository) bool {
	if id.repoLimited() {
		return false
	}
	return s.authorize(id, auth.AdministerProject, t.Path)
}

func (s *Server) trashOn(w http.ResponseWriter) bool {
	if s.Forge == nil || s.Forge.Trash == nil {
		writeFeatureOff(w)
		return false
	}
	return true
}

func (s *Server) listTrash(w http.ResponseWriter, r *http.Request) {
	if !s.trashOn(w) {
		return
	}
	all, err := s.Forge.Trash.TrashList(r.Context())
	if err != nil {
		s.failForge(w, err)
		return
	}
	id := identityFrom(r)
	out := make([]forge.TrashedRepository, 0, len(all))
	for _, t := range all {
		if s.mayManageTrashed(id, t) {
			out = append(out, t)
		}
	}
	writeJSON(w, http.StatusOK, forge.Page[forge.TrashedRepository]{Items: out})
}

func (s *Server) restoreTrash(w http.ResponseWriter, r *http.Request) {
	if !s.trashOn(w) {
		return
	}
	var in struct {
		Owner string `json:"owner"`
		Name  string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx, id := r.Context(), identityFrom(r)
	t, err := s.Forge.Trash.TrashGet(ctx, r.PathValue("id"))
	if err != nil || !s.mayManageTrashed(id, t) {
		writeErr(w, http.StatusNotFound, "no such deleted repository")
		return
	}
	if in.Owner != "" && in.Owner != t.Home && !s.authorize(id, auth.Administer, "") {
		writeErr(w, http.StatusForbidden, "only admins of every Flight may restore a repository under another owner")
		return
	}
	setAudit(r, t.Path, "restore from the trash")
	got, err := s.Forge.Trash.TrashRestore(ctx, t.ID, in.Owner, in.Name)
	if err != nil {
		if strings.Contains(err.Error(), "exists") {
			writeErr(w, http.StatusConflict, "another repository has the address "+t.Path+" now: restore this one under another name")
			return
		}
		s.failForge(w, err)
		return
	}
	setAudit(r, got.Path, "restored from the trash (it was "+t.Path+")")
	s.emitRepository(ctx, id.User, got, "restored", nil)
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) purgeTrash(w http.ResponseWriter, r *http.Request) {
	if !s.trashOn(w) {
		return
	}
	var in struct {
		Confirm string `json:"confirm"`
	}
	if !decode(w, r, &in) {
		return
	}
	t, err := s.Forge.Trash.TrashGet(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such deleted repository")
		return
	}
	if in.Confirm != t.Path {
		writeErr(w, http.StatusBadRequest, "type the address of the repository ("+t.Path+") in confirm to remove it for good; nothing was removed")
		return
	}
	setAudit(r, t.Path, "removing for good")
	sum, err := s.Forge.Trash.TrashPurge(r.Context(), t.ID)
	if err != nil {
		s.failForge(w, err)
		return
	}
	setAudit(r, t.Path, "removed for good by an administrator: "+purgeDetail(sum))
	writeJSON(w, http.StatusOK, sum)
}

func purgeDetail(sum forge.PurgeSummary) string {
	return strings.TrimSpace(strings.Join([]string{
		strconv.Itoa(sum.Packs) + " packs", strconv.Itoa(sum.BlobsDeleted) + " blobs deleted", strconv.Itoa(sum.BlobsKept) + " kept because another repository names them",
	}, ", "))
}
