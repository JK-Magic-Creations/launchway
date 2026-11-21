package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Build caches (schema 25). Runners look entries up, send them in chunks
// and restore them through the runner API, always bound to a live lease of
// the calling runner; the request carries no scope, because the server
// works it out from the lease's run (see engine/cache.go). People and
// agents list a project's entries and delete them with the rights the
// project's other operations need, and administrators change the policy.

// cacheRoutes are the routes of build caches. Runners look up, send and
// restore entries under a lease; the server decides the scope. People and
// agents list and delete a project's entries; an administrator changes the
// policy.
func (s *Server) cacheRoutes() []route {
	return []route{
		{"POST /api/runner/v1/caches/lookup", runnerOnly, "", s.cacheLookup},
		{"POST /api/runner/v1/caches/begin", runnerOnly, "", s.once(s.cacheBegin)},
		{"PUT /api/runner/v1/caches/chunk", runnerOnly, "", s.cacheChunk},
		{"POST /api/runner/v1/caches/commit", runnerOnly, "", s.once(s.cacheCommit)},
		{"POST /api/runner/v1/caches/download", runnerOnly, "", s.cacheDownload},
		{"POST /api/runner/v1/caches/discard", runnerOnly, "", s.cacheDiscard},
		{"GET /api/v1/projects/{name}/caches", projectRead, "", s.listCaches},
		{"DELETE /api/v1/projects/{name}/caches/{id}", projectOperate, "cache.delete", s.deleteCache},
		{"DELETE /api/v1/projects/{name}/caches", projectOperate, "cache.delete", s.deleteCachesNamed},
		{"GET /api/v1/cache-policy", signedIn, "", s.getCachePolicy},
		{"PUT /api/v1/cache-policy", administer, "cache_policy.set", s.setCachePolicy},
		{"GET /api/v1/projects/{name}/cache-policy", projectRead, "", s.getProjectCachePolicy},
		{"PUT /api/v1/projects/{name}/cache-policy", administer, "cache_policy.set_project", s.setProjectCachePolicy},
		{"DELETE /api/v1/projects/{name}/cache-policy", administer, "cache_policy.clear_project", s.clearProjectCachePolicy},
	}
}

// ---- runner API ----

func (s *Server) cacheLookup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LeaseID string `json:"lease_id"`
		engine.CacheLookup
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.leaseAllowed(w, r, in.LeaseID) {
		return
	}
	out, err := s.Engine.LookupCache(in.LeaseID, in.CacheLookup)
	if err != nil {
		s.failArtifact(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) cacheBegin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LeaseID string `json:"lease_id"`
		engine.BeginCache
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.leaseAllowed(w, r, in.LeaseID) {
		return
	}
	out, err := s.Engine.BeginCache(r.Context(), in.LeaseID, in.BeginCache)
	if err != nil {
		s.failArtifact(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// cacheChunk takes one chunk of an entry: the body is the bytes, the query
// says which lease and entry, the offset and the chunk's SHA-256. It is its
// own route rather than the artifacts' because an artifact's chunk names a
// file of an artifact and checks the artifact's rows; an entry is one
// archive with its own rows and policy.
func (s *Server) cacheChunk(w http.ResponseWriter, r *http.Request) {
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
		writeErr(w, http.StatusBadRequest, "reading the chunk: "+err.Error())
		return
	}
	if !s.leaseAllowed(w, r, q.Get("lease_id")) {
		return
	}
	got, err := s.Engine.PutCacheChunk(r.Context(), q.Get("lease_id"), q.Get("cache_id"), offset, q.Get("sha256"), data)
	if err != nil {
		s.failArtifact(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"received": got})
}

func (s *Server) cacheCommit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LeaseID string `json:"lease_id"`
		CacheID string `json:"cache_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.leaseAllowed(w, r, in.LeaseID) {
		return
	}
	out, err := s.Engine.CommitCache(r.Context(), in.LeaseID, in.CacheID)
	if err != nil {
		s.failArtifact(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// cacheDownload streams an entry the lease's step may restore, from an
// offset, so a broken download carries on where it was.
func (s *Server) cacheDownload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LeaseID string `json:"lease_id"`
		CacheID string `json:"cache_id"`
		Offset  int64  `json:"offset"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.leaseAllowed(w, r, in.LeaseID) {
		return
	}
	body, size, sum, err := s.Engine.OpenCache(r.Context(), in.LeaseID, in.CacheID, in.Offset)
	if err != nil {
		s.failArtifact(w, err)
		return
	}
	defer body.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(size-in.Offset, 10))
	h.Set("X-Launchway-SHA256", sum)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

// cacheDiscard ends an entry the runner proved damaged.
func (s *Server) cacheDiscard(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LeaseID string `json:"lease_id"`
		CacheID string `json:"cache_id"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.leaseAllowed(w, r, in.LeaseID) {
		return
	}
	if err := s.Engine.DiscardCache(in.LeaseID, in.CacheID, in.Reason); err != nil {
		s.failArtifact(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ended": true})
}

// ---- entries ----

func (s *Server) listCaches(w http.ResponseWriter, r *http.Request) {
	out, err := s.Engine.Caches(r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteCache(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	setAudit(r, id, "")
	out, err := s.Engine.DeleteCaches(r.PathValue("name"), id, "", identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, id, fmt.Sprintf("%d entry, %s", out.Ended, sizeText(out.Bytes)))
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteCachesNamed(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeErr(w, http.StatusBadRequest, "say which cache to delete: ?name=pip (or delete one entry by its id)")
		return
	}
	setAudit(r, name, "")
	out, err := s.Engine.DeleteCaches(r.PathValue("name"), "", name, identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, name, fmt.Sprintf("%d entries, %s", out.Ended, sizeText(out.Bytes)))
	writeJSON(w, http.StatusOK, out)
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// ---- policy ----

// cachePolicyIn is a change to a policy; what it leaves out keeps its
// current value.
type cachePolicyIn struct {
	Enabled       *bool    `json:"enabled"`
	MaxProjectGiB *float64 `json:"max_project_gib"`
	MaxEntryGiB   *float64 `json:"max_entry_gib"`
	IdleDays      *int     `json:"idle_days"`
}

func (in cachePolicyIn) over(cur engine.CachePolicy) engine.CachePolicy {
	if in.Enabled != nil {
		cur.Enabled = *in.Enabled
	}
	if in.MaxProjectGiB != nil {
		cur.MaxProjectGiB = *in.MaxProjectGiB
	}
	if in.MaxEntryGiB != nil {
		cur.MaxEntryGiB = *in.MaxEntryGiB
	}
	if in.IdleDays != nil {
		cur.IdleDays = *in.IdleDays
	}
	return cur
}

func policyText(v engine.CachePolicyView) string {
	return fmt.Sprintf("enabled=%v max_project_gib=%g max_entry_gib=%g idle_days=%d (%s)", v.Enabled, v.MaxProjectGiB, v.MaxEntryGiB, v.IdleDays, v.Source)
}

func (s *Server) getCachePolicy(w http.ResponseWriter, r *http.Request) {
	v, err := s.Engine.CachePolicyInstallation()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) setCachePolicy(w http.ResponseWriter, r *http.Request) {
	var in cachePolicyIn
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, "installation", "")
	cur, err := s.Engine.CachePolicyInstallation()
	if err != nil {
		s.fail(w, err)
		return
	}
	before, after, err := s.Engine.SetCachePolicy("", in.over(cur.CachePolicy), identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, "installation", clip(policyText(before)+" -> "+policyText(after), 600))
	writeJSON(w, http.StatusOK, after)
}

func (s *Server) getProjectCachePolicy(w http.ResponseWriter, r *http.Request) {
	v, err := s.Engine.CachePolicyFor(r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) setProjectCachePolicy(w http.ResponseWriter, r *http.Request) {
	var in cachePolicyIn
	if !decode(w, r, &in) {
		return
	}
	project := r.PathValue("name")
	setAudit(r, project, "")
	if s.Projects != nil {
		if _, err := s.Projects.Get(project); err != nil {
			s.fail(w, err)
			return
		}
	}
	cur, err := s.Engine.CachePolicyFor(project)
	if err != nil {
		s.fail(w, err)
		return
	}
	before, after, err := s.Engine.SetCachePolicy(project, in.over(cur.CachePolicy), identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, project, clip(policyText(before)+" -> "+policyText(after), 600))
	writeJSON(w, http.StatusOK, after)
}

func (s *Server) clearProjectCachePolicy(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("name")
	setAudit(r, project, "")
	before, after, err := s.Engine.ClearCachePolicy(project, identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, project, clip(policyText(before)+" -> "+policyText(after), 600))
	writeJSON(w, http.StatusOK, after)
}
