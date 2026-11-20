package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/settings"
)

// Configuration in the database (schema 23), under /api/v1/config: one
// resource per kind of object, the same for the UI, the CLI, the MCP
// server and a Terraform provider. Objects have stable names, a version
// (also the ETag; If-Match makes a change conditional), a history and an
// owner (managed_by: api, terraform or import; a client says which with
// X-Launchway-Manager). Reading and changing configuration needs the admin
// role on every project; the overview, which the UI reads to name the
// levels, needs any role.

// configObject is an object as the API gives it.
type configObject struct {
	engine.ConfigObject
	Deleted bool `json:"deleted"`
}

func objectView(o *engine.ConfigObject) *configObject {
	if o == nil {
		return nil
	}
	return &configObject{ConfigObject: *o, Deleted: o.Deleted()}
}

// settingsOr404 returns the store, or answers that there is none.
func (s *Server) settingsOr404(w http.ResponseWriter) *settings.Service {
	if s.Settings == nil || !s.Settings.Available() {
		writeErr(w, http.StatusNotFound, "this server does not keep its configuration in the database; it uses its configuration file")
		return nil
	}
	return s.Settings
}

// configKind reads {kind}, answering 404 for an unknown one.
func configKind(w http.ResponseWriter, r *http.Request) *settings.Kind {
	k := settings.KindByPath(r.PathValue("kind"))
	if k == nil {
		var names []string
		for _, k := range settings.Kinds() {
			names = append(names, k.Path)
		}
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no kind of configuration %q; there are %s", r.PathValue("kind"), strings.Join(names, ", ")))
	}
	return k
}

// configFail answers an error of the configuration store: a change the
// rules refuse lists every problem.
func (s *Server) configFail(w http.ResponseWriter, err error) {
	var ie *settings.InvalidError
	if errors.As(err, &ie) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "the configuration would not be valid: " + ie.Error(), "problems": ie.Problems})
		return
	}
	var na *settings.NotAppliedError
	if errors.As(err, &na) {
		// Stored, so it is not lost and not to be sent again; but not in
		// effect on the server that answered, which tries again.
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": na.Error(), "stored": true, "revision": na.Revision, "server": na.Server})
		return
	}
	if errors.Is(err, engine.ErrConflict) && strings.Contains(err.Error(), "is at version") {
		writeErr(w, http.StatusPreconditionFailed, strings.TrimPrefix(err.Error(), "conflict: "))
		return
	}
	s.fail(w, err)
}

// GET /api/v1/config: the kinds, the levels and their names, and the
// revision this server runs with.
func (s *Server) configOverview(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"available": false, "kinds": settings.Kinds(), "levels": settings.DefaultLevels}
	if s.Settings != nil && s.Settings.Available() {
		lay, err := s.Settings.Levels()
		if err != nil {
			s.fail(w, err)
			return
		}
		id := identityFrom(r)
		admin := s.authorize(id, auth.Administer, "")
		out["available"] = true
		out["levels"] = lay.Levels
		out["organisation"] = lay.Organisation
		out["admin"] = admin
		if admin {
			out["units_in_use"] = lay.UnitsInUse
			out["units"] = lay.Units
			out["status"] = s.Settings.Status()
		} else {
			// Any role holder reads the names of the levels; only the
			// projects they may see are named, and what is wrong with the
			// configuration (it can name hosts and repositories) is for
			// administrators.
			units := make([]settings.UnitView, 0, len(lay.Units))
			for _, u := range lay.Units {
				v := u
				v.Projects = []string{}
				for _, p := range u.Projects {
					if s.authorize(id, auth.Read, p) {
						v.Projects = append(v.Projects, p)
					}
				}
				units = append(units, v)
			}
			out["units_in_use"] = lay.UnitsInUse
			out["units"] = units
			out["status"] = settings.Status{Revision: s.Settings.Status().Revision}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/v1/config/{kind}[?deleted=1]
func (s *Server) listConfig(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	k := configKind(w, r)
	if k == nil {
		return
	}
	objs, err := st.List(k, r.URL.Query().Get("deleted") != "")
	if err != nil {
		s.configFail(w, err)
		return
	}
	out := make([]*configObject, 0, len(objs))
	for i := range objs {
		out = append(out, objectView(&objs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": k, "objects": out})
}

// GET /api/v1/config/{kind}/{obj}
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	k := configKind(w, r)
	if k == nil {
		return
	}
	o, err := st.Get(k, r.PathValue("obj"))
	if err != nil {
		s.configFail(w, err)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(o.Version, 10)))
	writeJSON(w, http.StatusOK, objectView(o))
}

// configBody is what a create or replace sends.
type configBody struct {
	Name    string          `json:"name"`
	Unit    string          `json:"unit"`
	Spec    json.RawMessage `json:"spec"`
	Comment string          `json:"comment"`
	// Version, if not 0, is the version the change expects (as If-Match).
	Version int64 `json:"version"`
}

// ifVersion reads If-Match ("3", "*") or the body's version: the version
// a change expects; -1 for any.
func ifVersion(r *http.Request, body int64) (int64, error) {
	h := strings.TrimSpace(r.Header.Get("If-Match"))
	switch {
	case h == "" || h == "*":
		if body > 0 {
			return body, nil
		}
		return -1, nil
	}
	v, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(h, "W/"), `"`), 10, 64)
	if err != nil || v < 1 {
		return 0, fmt.Errorf("If-Match %q is not a version such as \"3\"", h)
	}
	return v, nil
}

func manager(r *http.Request) string {
	switch m := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Launchway-Manager"))); m {
	case "":
		return engine.ManagedByAPI
	default:
		return m
	}
}

// POST /api/v1/config/{kind}: create; 409 if it exists.
func (s *Server) createConfig(w http.ResponseWriter, r *http.Request) {
	s.putConfig1(w, r, true)
}

// PUT /api/v1/config/{kind}/{obj}: create or replace, declaratively: the
// same body twice changes nothing the second time.
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	s.putConfig1(w, r, false)
}

func (s *Server) putConfig1(w http.ResponseWriter, r *http.Request, create bool) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	k := configKind(w, r)
	if k == nil {
		return
	}
	var b configBody
	if !decode(w, r, &b) {
		return
	}
	name := r.PathValue("obj")
	if create {
		name = b.Name
		if name == "" && k.Singleton != "" {
			name = k.Singleton
		}
	} else if b.Name != "" && b.Name != name {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("the body names %q but the path %q", b.Name, name))
		return
	}
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	v, err := ifVersion(r, b.Version)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if create {
		v = 0
	}
	setAudit(r, k.Name+"/"+name, "")
	res, err := st.Apply(identityFrom(r).User, []settings.Change{{Kind: k, Name: name, Action: engine.ConfigUpdate, Unit: b.Unit, Spec: b.Spec,
		IfVersion: v, ManagedBy: manager(r), Comment: b.Comment}})
	if err != nil {
		s.configFail(w, err)
		return
	}
	out := res[0]
	code := http.StatusOK
	if out.Change != nil && out.Change.Action == engine.ConfigCreate {
		code = http.StatusCreated
	}
	if out.Change != nil {
		setAudit(r, "", fmt.Sprintf("%s, version %d", out.Change.Action, out.Change.Version))
	} else {
		setAudit(r, "", "unchanged")
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(out.Object.Version, 10)))
	writeJSON(w, code, map[string]any{"object": objectView(out.Object), "changed": out.Changed, "change": out.Change})
}

// DELETE /api/v1/config/{kind}/{obj}: deleted, and restorable.
func (s *Server) deleteConfig(w http.ResponseWriter, r *http.Request) {
	s.lifecycle(w, r, engine.ConfigDelete)
}

// POST /api/v1/config/{kind}/{obj}/restore
func (s *Server) restoreConfig(w http.ResponseWriter, r *http.Request) {
	s.lifecycle(w, r, engine.ConfigRestore)
}

func (s *Server) lifecycle(w http.ResponseWriter, r *http.Request, action string) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	k := configKind(w, r)
	if k == nil {
		return
	}
	var b struct {
		Comment string `json:"comment"`
		Version int64  `json:"version"`
		// ForceInUse deletes an object that active runs depend on.
		ForceInUse bool `json:"force_in_use"`
	}
	if r.ContentLength != 0 && !decode(w, r, &b) {
		return
	}
	v, err := ifVersion(r, b.Version)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := r.PathValue("obj")
	setAudit(r, k.Name+"/"+name, "")
	res, err := st.Apply(identityFrom(r).User, []settings.Change{{Kind: k, Name: name, Action: action, IfVersion: v, ManagedBy: manager(r), Comment: b.Comment, Force: b.ForceInUse}})
	if err != nil {
		s.configFail(w, err)
		return
	}
	if len(res[0].Forced) > 0 {
		setAudit(r, "", "forced while in use: "+clip(strings.Join(res[0].Forced, "; "), 400))
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": objectView(res[0].Object), "change": res[0].Change, "forced": res[0].Forced})
}

// changeBody is one change of a batch.
type changeBody struct {
	Kind    string          `json:"kind"`
	Name    string          `json:"name"`
	Action  string          `json:"action"` // put (the default), delete or restore
	Unit    string          `json:"unit"`
	Spec    json.RawMessage `json:"spec"`
	Version int64           `json:"version"` // 0: any; -1: must not exist
	Comment string          `json:"comment"`
	// ForceInUse, for a delete: even if active runs depend on the object.
	ForceInUse bool `json:"force_in_use"`
}

func readChanges(r *http.Request, in []changeBody) ([]settings.Change, error) {
	if len(in) == 0 {
		return nil, errors.New("changes is empty")
	}
	if len(in) > 200 {
		return nil, errors.New("at most 200 changes at once")
	}
	out := make([]settings.Change, 0, len(in))
	for i, c := range in {
		k := settings.KindByPath(c.Kind)
		if k == nil {
			return nil, fmt.Errorf("changes[%d]: no kind %q", i, c.Kind)
		}
		ch := settings.Change{Kind: k, Name: c.Name, Unit: c.Unit, Spec: c.Spec, ManagedBy: manager(r), Comment: c.Comment, IfVersion: -1, Force: c.ForceInUse}
		switch c.Version {
		case 0:
		case -1:
			ch.IfVersion = 0
		default:
			ch.IfVersion = c.Version
		}
		switch c.Action {
		case "", "put":
			ch.Action = engine.ConfigUpdate
		case "delete":
			ch.Action = engine.ConfigDelete
		case "restore":
			ch.Action = engine.ConfigRestore
		default:
			return nil, fmt.Errorf("changes[%d]: action %q: put, delete or restore", i, c.Action)
		}
		out = append(out, ch)
	}
	return out, nil
}

// POST /api/v1/config/validate: checks changes as they would be applied,
// storing nothing. Wizards call it as people answer.
func (s *Server) validateConfig(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	var b struct {
		Changes []changeBody `json:"changes"`
	}
	if !decode(w, r, &b) {
		return
	}
	cs, err := readChanges(r, b.Changes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := st.DryRun(cs); err != nil {
		var ie *settings.InvalidError
		if errors.As(err, &ie) {
			writeJSON(w, http.StatusOK, map[string]any{"valid": false, "problems": ie.Problems})
			return
		}
		if errors.Is(err, engine.ErrConflict) || errors.Is(err, engine.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"valid": false, "problems": []string{strings.TrimPrefix(strings.TrimPrefix(err.Error(), "conflict: "), "not found: ")}})
			return
		}
		s.configFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "problems": []string{}})
}

// POST /api/v1/config/apply: several changes, all or none (a wizard
// creates everything it promised, or nothing).
func (s *Server) applyConfig(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	var b struct {
		Changes []changeBody `json:"changes"`
	}
	if !decode(w, r, &b) {
		return
	}
	cs, err := readChanges(r, b.Changes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var names []string
	for _, c := range cs {
		names = append(names, c.Kind.Name+"/"+c.Name)
	}
	setAudit(r, strings.Join(names, ","), "")
	res, err := st.Apply(identityFrom(r).User, cs)
	if err != nil {
		s.configFail(w, err)
		return
	}
	out := make([]map[string]any, 0, len(res))
	var forced []string
	changed := 0
	for _, x := range res {
		if x.Changed {
			changed++
		}
		out = append(out, map[string]any{"object": objectView(x.Object), "changed": x.Changed, "change": x.Change, "forced": x.Forced})
		if len(x.Forced) > 0 {
			forced = append(forced, clip(strings.Join(x.Forced, "; "), 200))
		}
	}
	if len(forced) > 0 {
		setAudit(r, "", "forced while in use: "+clip(strings.Join(forced, " | "), 400))
	}
	if len(forced) == 0 {
		setAudit(r, "", fmt.Sprintf("%d of %d changed", changed, len(res)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// GET /api/v1/config/{kind}/{obj}/history and GET /api/v1/config/history
func (s *Server) configHistory(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	q := r.URL.Query()
	f := engine.ConfigHistoryFilter{Kind: q.Get("kind"), Name: q.Get("name"), ImportID: q.Get("import")}
	if r.PathValue("kind") != "" {
		k := configKind(w, r)
		if k == nil {
			return
		}
		f.Kind, f.Name = k.Name, r.PathValue("obj")
	} else if f.Kind != "" {
		if k := settings.KindByPath(f.Kind); k != nil {
			f.Kind = k.Name
		}
	}
	f.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	h, err := st.History(f)
	if err != nil {
		s.configFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": h})
}

// GET /api/v1/config/export: every object as configuration text (YAML).
func (s *Server) exportConfig(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	objs, err := st.List(nil, false)
	if err != nil {
		s.configFail(w, err)
		return
	}
	text, err := settings.Export(objs)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="launchway-config.yaml"`)
	_, _ = w.Write(text)
}

// POST /api/v1/config/import: configuration text, planned (dry_run, the
// default) or applied. Applying with the plan's digest makes sure what is
// applied is what was reviewed.
func (s *Server) importConfig(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	var b struct {
		Text   string `json:"text"`
		Prune  bool   `json:"prune"`
		Apply  bool   `json:"apply"`
		Digest string `json:"digest"`
		Origin string `json:"origin"`
	}
	if !decode(w, r, &b) {
		return
	}
	if strings.TrimSpace(b.Text) == "" {
		writeErr(w, http.StatusBadRequest, "text is empty: send the configuration to import")
		return
	}
	if !b.Apply {
		plan, _, err := st.PlanImport([]byte(b.Text), b.Prune)
		if err != nil {
			s.configFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"plan": plan, "applied": false})
		return
	}
	origin := "api"
	if b.Origin != "" {
		origin = "api: " + clip(b.Origin, 200)
	}
	done, err := st.Import(identityFrom(r).User, []byte(b.Text), b.Prune, origin, b.Digest)
	if err != nil {
		s.configFail(w, err)
		return
	}
	setAudit(r, done.Import.ID, fmt.Sprintf("%d changes", done.Plan.Changes))
	writeJSON(w, http.StatusOK, map[string]any{"plan": done.Plan, "applied": true, "import": done.Import})
}

// GET /api/v1/config/imports
func (s *Server) configImports(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	list, err := st.Imports()
	if err != nil {
		s.configFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imports": list})
}

// GET /api/v1/config/effective/{project}: what applies to a project and
// where each part comes from.
func (s *Server) effectiveConfig(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	ef, err := st.EffectiveFor(r.PathValue("project"))
	if err != nil {
		s.configFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ef)
}
