package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/settings"
)

// Squadrons (stored as units) and Flights (stored as projects): the folder a person makes with a name only, its page,
// adding a Flight to it from a repository, moving Flights between Squadrons with a preview, and organizing Flights
// that sit in no Squadron. Nothing here adds a table or a column: every change is a configuration object change
// (kinds unit and project), so it has history, an audit entry and can be undone.
// The route words are the permanent names (unit, project); the pages say Squadron and Flight.

const (
	maxCandidates = 100
	opsBudget     = 4 * time.Second
)

func (s *Server) squadronRoutes() []route {
	return []route{
		{"GET /api/v1/units", signedIn, "", s.unitsList},
		{"POST /api/v1/units", administer, "unit.create", s.unitCreate},
		{"GET /api/v1/units/{name}", signedIn, "", s.unitShow},
		{"POST /api/v1/units/{name}/projects", administer, "project.create_in_unit", s.unitAddProject},
		{"GET /api/v1/project-candidates", administer, "", s.projectCandidates},
		{"POST /api/v1/unit-moves/preview", administer, "project.move_preview", s.unitMovePreview},
		{"POST /api/v1/unit-moves", administer, "project.move", s.unitMove},
		{"GET /api/v1/unit-organize", administer, "", s.unitOrganizeProposal},
		{"POST /api/v1/unit-organize", administer, "unit.organize", s.unitOrganize},
		{"POST /api/v1/unit-organize/undo", administer, "unit.organize_undo", s.unitOrganizeUndo},
	}
}

// squadronSummary is one Squadron in the list.
type squadronSummary struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	Flights     int    `json:"flights"`
	Needs       int    `json:"needs"`
	Failing     int    `json:"failing"`
	Running     int    `json:"running"`
}

func (s *Server) mayManage(r *http.Request) bool {
	return s.authorize(identityFrom(r), auth.Administer, "")
}

const manageWhy = "Making and changing Squadrons is for people who administer every Flight today. Ask one of them, or ask for the admin role."

// flightRow is one Flight on a Squadron page.
type flightRow struct {
	homeProject
	Pipelines int `json:"pipelines"`
}

// squadronData computes the Flights the caller may see, grouped by Squadron, with what each needs.
func (s *Server) squadronData(r *http.Request) (map[string][]flightRow, *settings.Layout, []engine.AttentionItem, error) {
	id := identityFrom(r)
	only, err := s.visibleProjects(id)
	if err != nil {
		return nil, nil, nil, err
	}
	res, runs, err := s.attentionFor(id, only)
	if err != nil {
		return nil, nil, nil, err
	}
	prefs := s.prefsOf(id.User)
	open, _ := dismissedSplit(res.Needs, prefs, s.Now().UnixMilli())
	in := homeInputs{defaults: map[string]string{}, needsBy: map[string]int{}, fav: map[string]bool{}, view: "all"}
	allowed := map[string]bool{}
	for _, n := range only {
		allowed[n] = true
	}
	for _, p := range s.briefs() {
		if only == nil || allowed[p.Name] {
			in.defaults[p.Name] = p.DefaultBranch
			in.names = append(in.names, p.Name)
		}
	}
	sort.Strings(in.names)
	in.by = aggregateRuns(runs, in.defaults, id.User)
	for _, n := range open {
		in.needsBy[n.Project]++
	}
	var lay *settings.Layout
	unit := map[string]string{}
	if s.Settings != nil {
		if l, err := s.Settings.Levels(); err == nil && l != nil {
			lay = l
			for p, u := range l.ProjectUnit {
				unit[p] = u
			}
		}
	}
	by := map[string][]flightRow{}
	for _, n := range in.names {
		hp := homeProject{Name: n, Needs: in.needsBy[n], History: []string{}}
		if a := in.by[n]; a != nil {
			hp.Running = a.running
			hp.History = append(hp.History, a.history...)
			if a.last != nil {
				hp.Last, hp.LastID, hp.LastNo, hp.LastAt = string(a.last.Status), a.last.ID, a.last.Number, nz64(a.last.FinishedAt, a.last.CreatedAt)
			}
		}
		by[unit[n]] = append(by[unit[n]], flightRow{homeProject: hp, Pipelines: 1})
	}
	return by, lay, open, nil
}

// GET /api/v1/units: the Squadrons, with how many Flights each holds, and the Flights that sit in none.
func (s *Server) unitsList(w http.ResponseWriter, r *http.Request) {
	by, lay, _, err := s.squadronData(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := []squadronSummary{}
	if lay != nil {
		for _, u := range lay.Units {
			sm := squadronSummary{Name: u.Name, DisplayName: nzs(u.DisplayName, u.Name), Description: u.Description}
			for _, f := range by[u.Name] {
				sm.Flights++
				sm.Needs += f.Needs
				sm.Running += f.Running
				if f.Last == "failed" {
					sm.Failing++
				}
			}
			out = append(out, sm)
		}
	}
	unfiled := by[""]
	if unfiled == nil {
		unfiled = []flightRow{}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].DisplayName) < strings.ToLower(out[j].DisplayName) })
	resp := map[string]any{"squadrons": out, "unfiled": unfiled, "may_manage": s.mayManage(r)}
	if !s.mayManage(r) {
		resp["why"] = manageWhy
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /api/v1/units: make a Squadron with a name (and, if given, a display name and a description).
func (s *Server) unitCreate(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	var in struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	setAudit(r, "unit/"+in.Name, "")
	if p := settings.UnitNameProblem(in.Name); p != "" {
		writeErr(w, http.StatusBadRequest, p)
		return
	}
	if len(in.DisplayName) > 100 || len(in.Description) > 1000 {
		writeErr(w, http.StatusBadRequest, "A display name is at most 100 characters and a description at most 1000.")
		return
	}
	if lay, err := st.Levels(); err == nil {
		for _, u := range lay.Units {
			if strings.EqualFold(u.Name, in.Name) {
				writeJSON(w, http.StatusConflict, map[string]any{"error": fmt.Sprintf("A Squadron called %s already exists.", u.Name), "next": "Choose another name, or open the one that exists."})
				return
			}
		}
	}
	spec, _ := json.Marshal(settings.Unit{DisplayName: in.DisplayName, Description: in.Description})
	res, err := st.Apply(identityFrom(r).User, []settings.Change{{Kind: unitKind(), Name: in.Name, Action: engine.ConfigUpdate, Spec: spec, IfVersion: 0, ManagedBy: engine.ManagedByAPI, Comment: "made with a name"}})
	if err != nil {
		s.configFail(w, err)
		return
	}
	setAudit(r, "", "made")
	writeJSON(w, http.StatusCreated, map[string]any{"name": in.Name, "display_name": nzs(in.DisplayName, in.Name), "description": in.Description, "object": objectView(res[0].Object)})
}

func unitKind() *settings.Kind    { return settings.KindByPath("units") }
func projectKind() *settings.Kind { return settings.KindByPath("projects") }

type personRole struct {
	Who  string `json:"who"`
	Role string `json:"role"`
}

type settingCount struct {
	Kind  string `json:"kind"`
	Path  string `json:"path"`
	Title string `json:"title"`
	Count int    `json:"count"`
}

type operation struct {
	Flight string `json:"flight"`
	// Pipeline is the pipeline's name; today a Flight has one.
	Pipeline string `json:"pipeline"`
	Last     string `json:"last,omitempty"`
	LastID   string `json:"last_id,omitempty"`
	LastNo   int    `json:"last_number,omitempty"`
	LastAt   int64  `json:"last_at,omitempty"`
}

// GET /api/v1/units/{name}: one Squadron: its Flights with their latest result and what needs attention, its people and
// shared settings (for those who may see them), and its Operations: pipelines nothing starts but a person.
func (s *Server) unitShow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	by, lay, open, err := s.squadronData(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var u *settings.UnitView
	if lay != nil {
		for i := range lay.Units {
			if lay.Units[i].Name == name {
				u = &lay.Units[i]
			}
		}
	}
	if u == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("There is no Squadron called %s.", name))
		return
	}
	flights := by[name]
	if flights == nil {
		flights = []flightRow{}
	}
	mine := map[string]bool{}
	for _, f := range flights {
		mine[f.Name] = true
	}
	needs := []engine.AttentionItem{}
	for _, n := range open {
		if mine[n.Project] {
			needs = append(needs, n)
		}
	}
	resp := map[string]any{"name": u.Name, "display_name": nzs(u.DisplayName, u.Name), "description": u.Description, "flights": flights, "needs": needs,
		"may_manage": s.mayManage(r), "people": []personRole{}, "settings": []settingCount{}, "operations": []operation{}, "operations_complete": true}
	if !s.mayManage(r) {
		resp["why"] = manageWhy
	}
	if st := s.Settings; st != nil && st.Available() && s.mayManage(r) {
		people, counts := s.squadronSharing(st, name)
		resp["people"], resp["settings"] = people, counts
	}
	ops, complete := s.squadronOperations(r.Context(), flights)
	resp["operations"], resp["operations_complete"] = ops, complete
	writeJSON(w, http.StatusOK, resp)
}

// squadronSharing lists who holds a role at the Squadron and how many of each shared setting it defines.
func (s *Server) squadronSharing(st *settings.Service, unit string) ([]personRole, []settingCount) {
	people, counts := []personRole{}, []settingCount{}
	for _, k := range settings.Kinds() {
		if !k.Levels || k.Name == "project" {
			continue
		}
		objs, err := st.List(k, false)
		if err != nil {
			continue
		}
		n := 0
		for _, o := range objs {
			if o.Unit != unit {
				continue
			}
			n++
			if k.Name == "role-binding" {
				var b config.AccessBinding
				if json.Unmarshal(o.Spec, &b) == nil {
					who := b.User
					if who == "" {
						who = b.Group
						if who != "" {
							who = "group " + who
						}
					}
					if who != "" {
						people = append(people, personRole{Who: who, Role: b.Role})
					}
				}
			}
		}
		if n > 0 {
			counts = append(counts, settingCount{Kind: k.Name, Path: k.Path, Title: k.Title, Count: n})
		}
	}
	return people, counts
}

// squadronOperations reads each Flight's pipeline and lists those no trigger starts: they are started by hand.
func (s *Server) squadronOperations(ctx context.Context, flights []flightRow) ([]operation, bool) {
	out := []operation{}
	if s.Projects == nil {
		return out, true
	}
	ctx, cancel := context.WithTimeout(ctx, opsBudget)
	defer cancel()
	for _, f := range flights {
		if ctx.Err() != nil {
			return out, false
		}
		file, err := s.Projects.ReadPipelineFile(ctx, f.Name, "")
		if err != nil || !file.Exists && !file.Configured {
			continue
		}
		p, err := pipeline.Parse([]byte(file.Text))
		if err != nil {
			continue
		}
		on := p.On
		if on.Push != nil || on.PullRequest != nil || len(on.Schedule) > 0 || len(on.Webhook) > 0 {
			continue
		}
		out = append(out, operation{Flight: f.Name, Pipeline: p.Name, Last: f.Last, LastID: f.LastID, LastNo: f.LastNo, LastAt: f.LastAt})
	}
	return out, true
}

// ---- adding a Flight ----

type candidate struct {
	Path          string `json:"path"`
	Source        string `json:"source"` // hosted or the connection's name
	DefaultBranch string `json:"default_branch,omitempty"`
	Visibility    string `json:"visibility,omitempty"`
	Address       string `json:"address"`
	// Flight is the Flight that already uses this repository, if any.
	Flight string `json:"flight,omitempty"`
}

// projectsByAddress maps a repository address, without a trailing .git, to the Flights using it.
func (s *Server) flightsByAddress() map[string]string {
	out := map[string]string{}
	for _, p := range s.projects() {
		out[normAddress(p.URL)] = p.Name
	}
	return out
}

func normAddress(a string) string {
	return strings.TrimSuffix(strings.TrimRight(strings.ToLower(strings.TrimSpace(a)), "/"), ".git")
}

// GET /api/v1/project-candidates?q=&connection=: repositories a Flight could be made from: first the hosted
// repositories that have no Flight yet, then the ones that have, and, when a connection is named, what it can see.
func (s *Server) projectCandidates(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	used := s.flightsByAddress()
	resp := map[string]any{"hosted": []candidate{}, "connections": []string{}, "connection_items": []candidate{}}
	var fresh, taken []candidate
	if s.Forge != nil && s.Forge.Provider != nil {
		page, err := s.Forge.Provider.Repositories().List(r.Context(), "")
		if err != nil {
			s.failForge(w, err)
			return
		}
		id := identityFrom(r)
		for _, x := range page.Items {
			if !s.authorize(id, auth.Read, x.Path) || x.Archived {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(x.Path), q) {
				continue
			}
			addr := nzs(x.Clone.HTTPS, x.Clone.SSH)
			c := candidate{Path: x.Path, Source: "hosted", DefaultBranch: x.DefaultBranch, Visibility: string(x.Visibility), Address: addr, Flight: used[normAddress(addr)]}
			if c.Flight == "" {
				fresh = append(fresh, c)
			} else {
				taken = append(taken, c)
			}
		}
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].Path < fresh[j].Path })
	sort.Slice(taken, func(i, j int) bool { return taken[i].Path < taken[j].Path })
	hosted := append(fresh, taken...)
	if len(hosted) > maxCandidates {
		hosted = hosted[:maxCandidates]
	}
	if hosted != nil {
		resp["hosted"] = hosted
	}
	resp["hosted_without_flight"] = len(fresh)
	if ms, ok := s.mirrorsOrNil(); ok {
		if cs, err := ms.Connections(r.Context()); err == nil {
			names := []string{}
			for _, c := range cs {
				names = append(names, c.Name)
			}
			resp["connections"] = names
			if want := r.URL.Query().Get("connection"); want != "" {
				items, err := s.connectionCandidates(r.Context(), ms, want, q, used, identityFrom(r).User)
				if err != nil {
					resp["connection_error"] = "The connection " + want + " could not be read: " + err.Error()
				} else {
					resp["connection_items"] = items
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) mirrorsOrNil() (*mirror.Service, bool) {
	if s.Forge == nil || s.Forge.Mirrors == nil {
		return nil, false
	}
	return s.Forge.Mirrors, true
}

// connectionCandidates lists what a connection can see, without importing anything (a dry run).
func (s *Server) connectionCandidates(ctx context.Context, ms *mirror.Service, conn, q string, used map[string]string, user string) ([]candidate, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := ms.Import(ctx, mirror.ImportRequest{Connection: conn, DryRun: true}, user)
	if err != nil {
		return nil, err
	}
	out := []candidate{}
	for _, it := range res.Items {
		if q != "" && !strings.Contains(strings.ToLower(it.Repository), q) {
			continue
		}
		out = append(out, candidate{Path: it.Repository, Source: conn, Address: it.Repository, Flight: used[normAddress(it.Repository)]})
		if len(out) >= maxCandidates {
			break
		}
	}
	return out, nil
}

// pipelineProposal says what the repository holds: the pipeline file it finds, or a starter.
type pipelineProposal struct {
	State   string `json:"state"` // found or starter
	Path    string `json:"path,omitempty"`
	Starter string `json:"starter,omitempty"`
	Note    string `json:"note"`
}

// POST /api/v1/units/{name}/projects: make a Flight in the Squadron from a repository. A hosted repository is named by
// its path (`repository`); another one by its address (`repo`). No connection or credential is asked for here.
func (s *Server) unitAddProject(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	unit := r.PathValue("name")
	var in struct {
		Repository string `json:"repository"`
		Repo       string `json:"repo"`
		Name       string `json:"name"`
		Branch     string `json:"branch"`
	}
	if !decode(w, r, &in) {
		return
	}
	lay, err := st.Levels()
	if err != nil {
		s.fail(w, err)
		return
	}
	found := false
	for _, u := range lay.Units {
		found = found || u.Name == unit
	}
	if !found {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("There is no Squadron called %s.", unit))
		return
	}
	addr, branch := strings.TrimSpace(in.Repo), strings.TrimSpace(in.Branch)
	if in.Repository != "" {
		if s.Forge == nil || s.Forge.Provider == nil {
			writeErr(w, http.StatusBadRequest, "This server does not host repositories, so give the address of the repository (repo).")
			return
		}
		page, err := s.Forge.Provider.Repositories().List(r.Context(), "")
		if err != nil {
			s.failForge(w, err)
			return
		}
		for _, x := range page.Items {
			if x.Path == in.Repository && s.authorize(identityFrom(r), auth.Read, x.Path) {
				addr = nzs(x.Clone.HTTPS, x.Clone.SSH)
				if branch == "" {
					branch = x.DefaultBranch
				}
			}
		}
		if addr == "" {
			writeErr(w, http.StatusNotFound, fmt.Sprintf("There is no hosted repository %s that you may read.", in.Repository))
			return
		}
	}
	if addr == "" {
		writeErr(w, http.StatusBadRequest, "Choose a repository.")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = flightNameFrom(nzs(in.Repository, addr))
	}
	if !settings.ValidName(name) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("%q cannot name a Flight: use letters, digits, '.', '_' and '-', starting with a letter or a digit.", name))
		return
	}
	if other := s.flightsByAddress()[normAddress(addr)]; other != "" {
		// Allowed (a monorepo has several), but said.
		setAudit(r, "", "the repository is also used by "+other)
	}
	spec := map[string]any{"repo": addr}
	if branch != "" {
		spec["branch"] = branch
	}
	raw, _ := json.Marshal(spec)
	setAudit(r, "project/"+name, "in Squadron "+unit)
	res, err := st.Apply(identityFrom(r).User, []settings.Change{{Kind: projectKind(), Name: name, Action: engine.ConfigUpdate, Unit: unit, Spec: raw, IfVersion: 0, ManagedBy: engine.ManagedByAPI, Comment: "added to the Squadron " + unit}})
	if err != nil {
		var ie *settings.InvalidError
		if strings.Contains(err.Error(), "version") || strings.Contains(err.Error(), "exists") {
			writeJSON(w, http.StatusConflict, map[string]any{"error": fmt.Sprintf("A Flight called %s already exists.", name), "next": "Choose another name."})
			return
		}
		_ = ie
		s.configFail(w, err)
		return
	}
	prop := pipelineProposal{State: "starter", Starter: pipeline.Starter(name), Note: "The repository has no pipeline file yet (or it cannot be read until the Flight exists). Open the pipeline editor to commit this starter."}
	if s.Projects != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		if f, err := s.Projects.ReadPipelineFile(ctx, name, ""); err == nil && f.Exists {
			prop = pipelineProposal{State: "found", Path: f.Path, Note: "Found " + f.Path + " in the repository; it runs as it is."}
		}
		cancel()
	}
	writeJSON(w, http.StatusCreated, map[string]any{"name": name, "unit": unit, "object": objectView(res[0].Object), "pipeline": prop})
}

func flightNameFrom(s string) string {
	s = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(s), "/"), ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	b := strings.Builder{}
	for _, c := range s {
		if unicode.IsLetter(c) && c < 128 || unicode.IsDigit(c) || c == '.' || c == '_' || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

// ---- moving ----

// pipelineTextFn reads a Flight's pipeline file for the secret-reference check; false when it cannot be read.
func (s *Server) pipelineTextFn(ctx context.Context) func(string) (string, bool) {
	if s.Projects == nil {
		return nil
	}
	return func(name string) (string, bool) {
		c, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		f, err := s.Projects.ReadPipelineFile(c, name, "")
		if err != nil || !f.Exists && !f.Configured {
			return "", false
		}
		return f.Text, true
	}
}

type moveBody struct {
	Flights  []string          `json:"flights"`
	Projects []string          `json:"projects"` // the permanent name, accepted for the same list
	To       string            `json:"to"`
	Homes    map[string]string `json:"homes"`
	Comment  string            `json:"comment"`
}

func (b moveBody) names() []string { return append(append([]string{}, b.Flights...), b.Projects...) }

// POST /api/v1/unit-moves/preview {flights, to}: what moving would change, per Flight, changing nothing.
func (s *Server) unitMovePreview(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	var in moveBody
	if !decode(w, r, &in) {
		return
	}
	plans, err := st.PlanMoves(r.Context(), in.names(), in.To, s.pipelineTextFn(r.Context()))
	if err != nil {
		s.configFail(w, err)
		return
	}
	blocked := 0
	for _, p := range plans {
		if p.Blocked {
			blocked++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"to": in.To, "plans": plans, "blocked": blocked})
}

// POST /api/v1/unit-moves {flights, to, comment}: move the Flights whose plan is not blocked, in one audited change.
// `homes` (Flight to Squadron) puts each Flight in its own Squadron: the undo and the organize use it.
func (s *Server) unitMove(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	var in moveBody
	if !decode(w, r, &in) {
		return
	}
	user := identityFrom(r).User
	setAudit(r, "unit/"+in.To, fmt.Sprintf("%d Flight(s)", len(in.names())))
	res, err := st.MoveFlights(r.Context(), user, in.names(), in.To, in.Homes, in.Comment, s.pipelineTextFn(r.Context()))
	if err != nil {
		s.configFail(w, err)
		return
	}
	for _, f := range res.Moved {
		s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: user, Source: s.clientAddr(r), RequestID: reqID(r), Action: "project.moved", Outcome: "ok", Target: f,
			Detail: fmt.Sprintf("from %q to %q", res.Undo[f], nzs(in.Homes[f], in.To))})
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- organize ----

type organizeGroupView struct {
	Name    string   `json:"name"`
	Flights []string `json:"flights"`
	// Source says where the idea came from: a name prefix, or a folder of the organize feature.
	Source string `json:"source"`
}

// GET /api/v1/unit-organize: for Flights that sit in no Squadron, a proposal from the folders that exist and from
// name prefixes. It changes nothing.
func (s *Server) unitOrganizeProposal(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	lay, err := st.Levels()
	if err != nil {
		s.fail(w, err)
		return
	}
	unfiled := []string{}
	for f, u := range lay.ProjectUnit {
		if u == "" {
			unfiled = append(unfiled, f)
		}
	}
	sort.Strings(unfiled)
	groups := []organizeGroupView{}
	taken := map[string]bool{}
	// Folders first: what people already arranged by hand.
	if s.Engine != nil {
		if folders, err := s.Engine.Folders("pipelines"); err == nil {
			if of, err := s.Engine.FolderOf("pipelines"); err == nil {
				paths := engine.FolderPaths(folders)
				top := map[string][]string{}
				for _, f := range unfiled {
					if id := of[f]; id != "" {
						first := strings.SplitN(paths[id], "/", 2)[0]
						top[first] = append(top[first], f)
					}
				}
				var keys []string
				for k := range top {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					nm := squadronNameFrom(k)
					if nm == "" || len(top[k]) < 1 {
						continue
					}
					groups = append(groups, organizeGroupView{Name: nm, Flights: top[k], Source: "folder"})
					for _, f := range top[k] {
						taken[f] = true
					}
				}
			}
		}
	}
	// Then prefixes: payments-api and payments-web share "payments".
	pre := map[string][]string{}
	for _, f := range unfiled {
		if taken[f] {
			continue
		}
		if i := strings.IndexAny(f, "-_./"); i >= 3 {
			pre[strings.ToLower(f[:i])] = append(pre[strings.ToLower(f[:i])], f)
		}
	}
	var pk []string
	for k := range pre {
		pk = append(pk, k)
	}
	sort.Strings(pk)
	for _, k := range pk {
		if len(pre[k]) < 2 {
			continue
		}
		groups = append(groups, organizeGroupView{Name: k, Flights: pre[k], Source: "prefix"})
		for _, f := range pre[k] {
			taken[f] = true
		}
	}
	rest := []string{}
	for _, f := range unfiled {
		if !taken[f] {
			rest = append(rest, f)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"unfiled": unfiled, "groups": groups, "leftover": rest, "may_manage": true})
}

// squadronNameFrom turns a folder's name into a Squadron name a person can edit.
func squadronNameFrom(s string) string {
	b := strings.Builder{}
	for _, c := range strings.TrimSpace(s) {
		if unicode.IsLetter(c) && c < 128 || unicode.IsDigit(c) || c == '.' || c == '_' || c == '-' {
			b.WriteRune(c)
		} else if c == ' ' {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

// POST /api/v1/unit-organize {groups: [{name, display_name, description, flights}]}: make the Squadrons that do not
// exist and file the Flights, as one change. The answer holds what undoes it.
func (s *Server) unitOrganize(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	var in struct {
		Groups []settings.OrganizeGroup `json:"groups"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, "unit-organize", fmt.Sprintf("%d group(s)", len(in.Groups)))
	res, err := st.OrganizeApply(r.Context(), identityFrom(r).User, in.Groups, s.pipelineTextFn(r.Context()))
	if err != nil {
		s.configFail(w, err)
		return
	}
	user := identityFrom(r).User
	for _, f := range res.Moved {
		s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: user, Source: s.clientAddr(r), RequestID: reqID(r), Action: "project.moved", Outcome: "ok", Target: f, Detail: "organized into Squadrons"})
	}
	writeJSON(w, http.StatusOK, res)
}

// POST /api/v1/unit-organize/undo {homes, delete_units}: put each Flight back and remove Squadrons the organize made.
func (s *Server) unitOrganizeUndo(w http.ResponseWriter, r *http.Request) {
	st := s.settingsOr404(w)
	if st == nil {
		return
	}
	var in struct {
		Homes       map[string]string `json:"homes"`
		DeleteUnits []string          `json:"delete_units"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, "unit-organize", "undo")
	res, err := st.OrganizeUndo(r.Context(), identityFrom(r).User, in.Homes, in.DeleteUnits)
	if err != nil {
		s.configFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
