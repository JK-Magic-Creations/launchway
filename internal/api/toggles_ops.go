package api

import (
	"net/http"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/toggles"
	"github.com/jk-magic-creations/launchway/internal/togglestore"
)

// Segments, safeguards, keys and the file form of a project's toggles.

func (s *Server) listToggleSegments(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	list, err := svc.Segments(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"segments": list})
}

type segmentBody struct {
	Level  string `json:"level"`
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
	toggles.Segment
}

func (s *Server) setToggleSegment(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	var in segmentBody
	if !decode(w, r, &in) {
		return
	}
	project := r.PathValue("name")
	where, err := whereOf(in.Level, in.Scope, project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if code, msg := s.mayEditLevel(r, where, project); code != 0 {
		writeErr(w, code, msg)
		return
	}
	setAudit(r, project+"/"+r.PathValue("segment"), clip(in.Reason, 300))
	if err := svc.SaveSegment(r.Context(), s.toggleActor(r), where, r.PathValue("segment"), in.Segment, in.Reason); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": r.PathValue("segment")})
}

func (s *Server) deleteToggleSegment(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	q := r.URL.Query()
	project := r.PathValue("name")
	where, err := whereOf(q.Get("level"), q.Get("scope"), project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if code, msg := s.mayEditLevel(r, where, project); code != 0 {
		writeErr(w, code, msg)
		return
	}
	setAudit(r, project+"/"+r.PathValue("segment"), clip(q.Get("reason"), 300))
	if err := svc.DeleteSegment(r.Context(), s.toggleActor(r), where, r.PathValue("segment"), q.Get("reason")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) getToggleSafeguards(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	project := r.PathValue("name")
	out := map[string]toggles.Resolved{}
	envs := s.projectEnvs(project)
	if e := r.URL.Query().Get("environment"); e != "" {
		envs = []string{e}
	}
	for _, env := range envs {
		g, err := svc.Safeguards(r.Context(), togglestore.ProjectWhere(project), env)
		if err != nil {
			s.fail(w, err)
			return
		}
		out[env] = g
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "environments": out,
		"note": "On by default. A level may turn a safeguard down with a reason, which is recorded; a level above may lock one, and that is the only way a team loses the choice."})
}

type safeguardBody struct {
	Level       string `json:"level"`
	Scope       string `json:"scope"`
	Environment string `json:"environment"`
	Reason      string `json:"reason"`
	toggles.Setting
}

func (s *Server) setToggleSafeguards(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	var in safeguardBody
	if !decode(w, r, &in) {
		return
	}
	project := r.PathValue("name")
	where, err := whereOf(in.Level, in.Scope, project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if where.Level == togglestore.LevelProject {
		if where.Scope != project {
			writeErr(w, http.StatusNotFound, "not found: {project} "+where.Scope)
			return
		}
		if !s.authorize(identityFrom(r), auth.AdministerProject, project) {
			writeErr(w, http.StatusForbidden, "you need the admin role on "+project+" to change its toggle safeguards")
			return
		}
	} else if code, msg := s.mayChangeLevel(identityFrom(r), settings.Level(where.Level), where.Scope, true); code != 0 {
		writeErr(w, code, msg)
		return
	}
	setAudit(r, where.String()+" "+in.Environment, clip(in.Reason, 300))
	res, err := svc.SetSafeguards(r.Context(), s.toggleActor(r), where, in.Environment, in.Setting, in.Reason)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- keys ----

func (s *Server) listToggleKeys(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	keys, err := svc.Keys(r.Context(), r.PathValue("name"), r.URL.Query().Get("environment"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "note": "A key's secret is shown once, when it is made, and never again."})
}

func (s *Server) mayManageKeys(w http.ResponseWriter, r *http.Request) bool {
	project := r.PathValue("name")
	if !s.authorize(identityFrom(r), auth.AdministerProject, project) {
		writeErr(w, http.StatusForbidden, "you need the admin role on "+project+" to make, rotate or revoke SDK keys")
		return false
	}
	return true
}

func (s *Server) createToggleKey(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil || !s.mayManageKeys(w, r) {
		return
	}
	var in struct {
		Environment string   `json:"environment"`
		Kind        string   `json:"kind"`
		Name        string   `json:"name"`
		Origins     []string `json:"origins"`
	}
	if !decode(w, r, &in) {
		return
	}
	project := r.PathValue("name")
	if msg := s.envRefusal(identityFrom(r), project, in.Environment); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	setAudit(r, project+"/"+in.Environment, in.Kind+" key "+clip(in.Name, 100))
	full, info, err := svc.NewKey(r.Context(), s.toggleActor(r), project, in.Environment, in.Kind, in.Name, in.Origins)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"key": full, "info": info, "note": "Keep it now: this is the only time it is shown."})
}

func (s *Server) rotateToggleKey(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil || !s.mayManageKeys(w, r) {
		return
	}
	var in struct {
		OverlapHours int `json:"overlap_hours"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, r.PathValue("name")+" key "+r.PathValue("id"), "")
	full, info, err := svc.RotateKey(r.Context(), s.toggleActor(r), r.PathValue("name"), r.PathValue("id"), time.Duration(in.OverlapHours)*time.Hour)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"key": full, "info": info, "note": "The old key keeps working until its expiry, so that deployments can move over; revoke it sooner if it leaked."})
}

func (s *Server) revokeToggleKey(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil || !s.mayManageKeys(w, r) {
		return
	}
	setAudit(r, r.PathValue("name")+" key "+r.PathValue("id"), "")
	if err := svc.RevokeKey(r.Context(), s.toggleActor(r), r.PathValue("name"), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

// ---- the file ----

func (s *Server) exportToggles(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	f, err := svc.Export(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	text, err := f.YAML()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"file": string(text), "path": ".launchway/toggles.yaml"})
}

type fileBody struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
	// Origin says where the file came from ("config:<commit>").
	Origin string `json:"origin"`
}

func (s *Server) planToggles(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	var in fileBody
	if !decode(w, r, &in) {
		return
	}
	f, err := toggles.ParseFile([]byte(in.File))
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	drift, err := svc.Plan(r.Context(), r.PathValue("name"), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"drift": drift, "in_sync": len(drift) == 0,
		"note": "Nothing was changed. Apply makes the live state match the file; it never removes what the file leaves out."})
}

func (s *Server) applyToggles(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	var in fileBody
	if !decode(w, r, &in) {
		return
	}
	f, err := toggles.ParseFile([]byte(in.File))
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	project := r.PathValue("name")
	// Each environment the file sets needs the operator role there.
	for _, k := range f.Keys() {
		for env := range f.Toggles[k].Environments {
			if msg := s.envRefusal(identityFrom(r), project, env); msg != "" {
				writeErr(w, http.StatusForbidden, msg)
				return
			}
		}
	}
	setAudit(r, project, clip(in.Reason, 300))
	res, err := svc.Apply(r.Context(), s.toggleActor(r), project, f, in.Reason, clip(in.Origin, 100))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
