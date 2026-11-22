package api

// Keeping code on another Git host: connections to it, the Flights attached
// to a repository there, and the webhook it calls. See
// docs/guides/outside-host.md. Every route answers 404 feature_off when the
// server has no hosts service (Server.Hosts nil).

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
)

func (s *Server) outsideHostRoutes() []route {
	return []route{
		{"GET /api/v1/outside-hosts", administer, "", s.listOutsideHosts},
		{"POST /api/v1/outside-hosts", administer, "outside_host.create", s.createOutsideHost},
		{"DELETE /api/v1/outside-hosts/{name}", administer, "outside_host.delete", s.deleteOutsideHost},
		{"POST /api/v1/outside-hosts/{name}/test", administer, "outside_host.test", s.testOutsideHost},
		{"GET /api/v1/outside-hosts/{name}/repositories", administer, "", s.outsideHostRepositories},
		// The host proves itself with the connection's webhook secret, not a session; the handler checks and audits.
		{"POST /api/v1/outside-hosts/{name}/webhook", public, "", s.outsideHostWebhook},
		{"GET /api/v1/projects/{name}/outside", projectRead, "", s.getOutsideFlight},
		{"PUT /api/v1/projects/{name}/outside", administer, "project.outside_attach", s.putOutsideFlight},
		{"DELETE /api/v1/projects/{name}/outside", administer, "project.outside_detach", s.deleteOutsideFlight},
	}
}

func (s *Server) hostsOn(w http.ResponseWriter) bool {
	if s.Hosts == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "feature_off: connections to outside Git hosts are not set up on this server", "code": "feature_off"})
		return false
	}
	return true
}

func (s *Server) failHosts(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, forge.ErrNotFound):
		writeErr(w, http.StatusNotFound, strings.TrimPrefix(err.Error(), "not found: "))
	case errors.Is(err, forge.ErrExists):
		writeErr(w, http.StatusConflict, strings.TrimPrefix(err.Error(), "already exists: "))
	case errors.Is(err, forge.ErrInvalid), errors.Is(err, hosts.ErrProtected):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid: "))
	default:
		if u, ok := forge.IsUnsupported(err); ok {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": u.Error(), "code": "unsupported", "capability": u.Capability,
				"reason": u.Reason, "workaround": u.Workaround})
			return
		}
		// A refusal from the host is shown in the host's words with the credential already removed.
		writeErr(w, http.StatusBadGateway, err.Error())
	}
}

func (s *Server) listOutsideHosts(w http.ResponseWriter, r *http.Request) {
	if !s.hostsOn(w) {
		return
	}
	vs, err := s.Hosts.List(r.Context())
	if err != nil {
		s.failHosts(w, err)
		return
	}
	writeJSON(w, http.StatusOK, forge.Page[hosts.View]{Items: vs})
}

func (s *Server) createOutsideHost(w http.ResponseWriter, r *http.Request) {
	if !s.hostsOn(w) {
		return
	}
	var in hosts.AddInput
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, in.Name, "")
	v, err := s.Hosts.Add(r.Context(), in, identityFrom(r).User)
	if err != nil {
		s.failHosts(w, err)
		return
	}
	setAudit(r, v.Name, v.Kind+" "+v.BaseURL+auditCredential(v.Credential)+" owners "+strings.Join(v.Owners, ","))
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) deleteOutsideHost(w http.ResponseWriter, r *http.Request) {
	if !s.hostsOn(w) {
		return
	}
	setAudit(r, r.PathValue("name"), "")
	if _, err := s.Hosts.Remove(r.Context(), r.PathValue("name")); err != nil {
		s.failHosts(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testOutsideHost(w http.ResponseWriter, r *http.Request) {
	if !s.hostsOn(w) {
		return
	}
	setAudit(r, r.PathValue("name"), "")
	res, err := s.Hosts.Test(r.Context(), r.PathValue("name"))
	if err != nil {
		s.failHosts(w, err)
		return
	}
	lack := 0
	for _, c := range res.Report.Checks {
		if !c.OK {
			lack++
		}
	}
	setAudit(r, r.PathValue("name"), "account "+res.Report.Account+", lacking "+strconv.Itoa(lack))
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) outsideHostRepositories(w http.ResponseWriter, r *http.Request) {
	if !s.hostsOn(w) {
		return
	}
	page, err := s.Hosts.Repositories(r.Context(), r.PathValue("name"), r.URL.Query().Get("cursor"), r.URL.Query().Get("q"))
	if err != nil {
		s.failHosts(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) getOutsideFlight(w http.ResponseWriter, r *http.Request) {
	if !s.hostsOn(w) {
		return
	}
	v, err := s.Hosts.View(r.Context(), r.PathValue("name"))
	if err != nil {
		s.failHosts(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) putOutsideFlight(w http.ResponseWriter, r *http.Request) {
	if !s.hostsOn(w) {
		return
	}
	var in struct {
		Connection     string `json:"connection"`
		Repository     string `json:"repository"`
		ReportStatuses *bool  `json:"report_statuses"`
		ReportComment  *bool  `json:"report_comment"`
	}
	if !decode(w, r, &in) {
		return
	}
	name := r.PathValue("name")
	setAudit(r, name, "")
	if in.Connection != "" || in.Repository != "" {
		if _, err := s.Hosts.Attach(r.Context(), name, in.Connection, in.Repository); err != nil {
			s.failHosts(w, err)
			return
		}
	}
	if _, err := s.Hosts.SetReports(r.Context(), name, in.ReportStatuses, in.ReportComment); err != nil {
		s.failHosts(w, err)
		return
	}
	v, err := s.Hosts.View(r.Context(), name)
	if err != nil {
		s.failHosts(w, err)
		return
	}
	setAudit(r, name, "repository "+v.Connection+":"+v.Repository)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteOutsideFlight(w http.ResponseWriter, r *http.Request) {
	if !s.hostsOn(w) {
		return
	}
	setAudit(r, r.PathValue("name"), "")
	if err := s.Hosts.Detach(r.Context(), r.PathValue("name")); err != nil {
		s.failHosts(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// outsideHostWebhook is the host's call. It proves itself with the
// connection's secret (HMAC of the body, compared in constant time); a
// delivery ID seen before is answered and ignored; an unknown connection, a
// connection without a secret and a wrong signature all get the same 404. A
// verified push, tag push or pull request event makes the Flights attached to
// the repository poll at once: the poller stays the source of truth, and a
// pull request from a fork is run by the poller under the restricted
// pull-request rule. Failed calls count against the caller's address.
func (s *Server) outsideHostWebhook(w http.ResponseWriter, r *http.Request) {
	if s.tooManyFailures(w, r) {
		return
	}
	if s.Hosts == nil {
		hookNotFound(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGitHookBody)
	name := r.PathValue("name")
	d, err := s.Hosts.Webhook(r.Context(), name, r)
	if errors.Is(err, forge.ErrRateLimited) {
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "too many webhook calls for this connection; try again in a minute")
		return
	}
	if err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			writeErr(w, http.StatusRequestEntityTooLarge, "the body is larger than this server accepts")
			return
		}
		s.hookDenied(r, "webhook.outside", "outside:"+name, "a webhook call that could not prove itself")
		hookNotFound(w)
		return
	}
	if !d.Fresh {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "already received"})
		return
	}
	if len(d.Events) == 0 || len(d.Flights) == 0 {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
		return
	}
	for _, f := range d.Flights {
		if s.Projects != nil {
			s.Projects.PollNow(f.Project)
		}
		detail := ""
		for _, e := range d.Events {
			detail += string(e.Kind) + " " + clip(e.Ref, 80)
			if e.Fork {
				detail += " (from a fork: restricted run)"
			}
			detail += "; "
		}
		s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "outside:" + name, Source: s.clientAddr(r), RequestID: reqID(r), Action: "webhook.outside",
			Project: f.Project, Target: clip(d.Events[0].DeliveryID, maxTarget), Outcome: "ok", Detail: clip(detail, 300) + "polling the Flight now"})
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "polling"})
}
