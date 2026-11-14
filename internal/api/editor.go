package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// The pipeline editor. Git is the only place a pipeline is kept: the editor
// reads the file from a branch, a draft run runs edited text against a
// branch without saving it, and a save commits the file as the person who
// saved it. Neither the text nor anything else about an edit is kept on the
// server, except the text a draft run ran, which is kept with that run.

// maxEditorBody bounds the JSON body of a draft run or a save: a pipeline of
// pipeline.MaxFileBytes, escaped, and a little more.
const maxEditorBody = 4*pipeline.MaxFileBytes + 64<<10

// decodeEditor reads a JSON body of at most maxEditorBody bytes and checks
// that the pipeline text in it is not too large.
func decodeEditor(w http.ResponseWriter, r *http.Request, v any, text *string) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEditorBody))
	if err := dec.Decode(v); err != nil && err != io.EOF {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "the request is too large")
			return false
		}
		writeErr(w, http.StatusBadRequest, "the request body is not valid JSON: "+err.Error())
		return false
	}
	if len(*text) > pipeline.MaxFileBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the pipeline is larger than %d KiB", pipeline.MaxFileBytes>>10))
		return false
	}
	return true
}

// readPipeline returns the pipeline file at the tip of ?ref= (default: the
// project's default branch), with the commit it came from.
func (s *Server) readPipeline(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no projects are configured")
		return
	}
	f, err := s.Projects.ReadPipelineFile(r.Context(), r.PathValue("name"), r.URL.Query().Get("ref"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// startDraft runs pipeline text that is not in Git against the current tip
// of a branch: {"ref": "main", "pipeline": "..."}.
func (s *Server) startDraft(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no projects are configured")
		return
	}
	var in struct {
		Ref      string `json:"ref"`
		Pipeline string `json:"pipeline"`
	}
	if !decodeEditor(w, r, &in, &in.Pipeline) {
		return
	}
	name := r.PathValue("name")
	hash := engine.DraftHash(in.Pipeline)
	// The audit trail names the text by its hash; the text itself is kept
	// with the run, never in the trail.
	setAudit(r, "", fmt.Sprintf("ref %s, pipeline %s", in.Ref, hash))
	run, err := s.Projects.StartDraft(r.Context(), source.DraftRequest{Project: name, Ref: in.Ref, Text: in.Pipeline,
		Actor: identityFrom(r).User, Secrets: s.Drafts.For(name).Secrets})
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, run.ID, fmt.Sprintf("draft run #%d of %s at %s, pipeline %s", run.Number, nonEmpty(run.Branch, run.Tag), shortSHA(run.SHA), hash))
	writeJSON(w, http.StatusCreated, run)
}

// savePipeline commits pipeline text to the project's repository, as the
// caller: {"base_ref", "base_sha", "pipeline", "message", "branch", "force"}.
func (s *Server) savePipeline(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no projects are configured")
		return
	}
	var in struct {
		BaseRef  string `json:"base_ref"`
		BaseSHA  string `json:"base_sha"`
		Pipeline string `json:"pipeline"`
		Message  string `json:"message"`
		Branch   string `json:"branch"`
		Force    bool   `json:"force"`
	}
	if !decodeEditor(w, r, &in, &in.Pipeline) {
		return
	}
	name := r.PathValue("name")
	id := identityFrom(r)
	target := in.Branch
	if target == "" {
		target = "(a new branch)"
	}
	hash := engine.DraftHash(in.Pipeline)
	setAudit(r, target, fmt.Sprintf("from %s at %s, pipeline %s", in.BaseRef, shortSHA(in.BaseSHA), hash))
	if in.Force && !s.authorize(id, auth.AdministerProject, name) {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("only an admin of %s may save a pipeline that has problems", name))
		return
	}
	res, err := s.Projects.Save(r.Context(), source.SaveRequest{Project: name, BaseRef: in.BaseRef, BaseSHA: in.BaseSHA,
		Text: in.Pipeline, Message: in.Message, Branch: in.Branch, Force: in.Force, Now: s.Now(),
		Author: source.Author{User: id.User, Name: id.Profile.Name, Email: id.Profile.Email}})
	var pushErr *source.PushError
	switch {
	case errors.As(err, &pushErr):
		writeErr(w, http.StatusBadGateway, pushErr.Error())
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	forced := ""
	if in.Force {
		forced = ", saved with force"
	}
	setAudit(r, res.Branch, fmt.Sprintf("saved %s to %s at %s, from %s at %s, pipeline %s%s",
		res.Path, res.Branch, res.SHA, res.BaseRef, shortSHA(res.BaseSHA), hash, forced))
	writeJSON(w, http.StatusCreated, res)
}
