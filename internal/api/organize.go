package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// How things are organized: shared folders and labels, layouts at each level
// and each person's own arrangement on top (see engine/org_store.go). A
// person never learns that a folder exists unless they can see something in it
// (or may arrange its level).

func (s *Server) organizeRoutes() []route {
	return []route{
		{"GET /api/v1/organize/{area}", signedIn, "", s.organizeGet},
		{"POST /api/v1/organize/{area}/folders", signedIn, "folder.create", s.folderCreate},
		{"PATCH /api/v1/organize/folders/{id}", signedIn, "folder.update", s.folderUpdate},
		{"DELETE /api/v1/organize/folders/{id}", signedIn, "folder.delete", s.folderDelete},
		{"POST /api/v1/organize/{area}/move", signedIn, "folder.move", s.folderMove},
		{"PUT /api/v1/organize/{area}/labels", signedIn, "labels.set", s.labelsSet},
		{"GET /api/v1/layouts/{page}", signedIn, "", s.layoutGet},
		{"PUT /api/v1/layouts/{page}", signedIn, "layout.set", s.layoutSet},
		{"DELETE /api/v1/layouts/{page}", signedIn, "layout.reset", s.layoutDelete},
		{"PUT /api/v1/me/layouts/{page}", signedIn, "prefs.layout.set", s.myLayoutSet},
		{"DELETE /api/v1/me/layouts/{page}", signedIn, "prefs.layout.reset", s.myLayoutReset},
		{"DELETE /api/v1/me/layouts", signedIn, "prefs.layout.reset_all", s.myLayoutsReset},
		{"POST /api/v1/me/favorites", signedIn, "prefs.favorite", s.favoriteSet},
		{"GET /api/v1/layouts-export", signedIn, "", s.layoutExport},
		{"POST /api/v1/layouts-import", signedIn, "layout.import", s.layoutImport},
	}
}

// visibleItems lists the items of an area the caller may see, with their unit scope.
func (s *Server) visibleItems(r *http.Request, area string) (map[string]string, error) {
	id := identityFrom(r)
	out := map[string]string{}
	unit, _ := s.unitOf()
	switch area {
	case "pipelines", "projects", "runs":
		only, err := s.visibleProjects(id)
		if err != nil {
			return nil, err
		}
		for _, p := range s.projects() {
			if only == nil || contains(only, p.Name) {
				out[p.Name] = unit[p.Name]
			}
		}
	case "repositories":
		if s.Forge == nil || s.Forge.Provider == nil {
			return out, nil
		}
		page, err := s.Forge.Provider.Repositories().List(r.Context(), "")
		if err != nil {
			return nil, err
		}
		for _, x := range page.Items {
			if s.authorize(id, auth.Read, x.Path) {
				out[x.Path] = ""
			}
		}
	}
	return out, nil
}

// mayArrange says whether the caller may change the shared arrangement at a scope.
func (s *Server) mayArrange(id *identity, scope string) bool {
	kind, name, _ := strings.Cut(scope, ":")
	switch kind {
	case "base", "organization":
		return s.authorize(id, auth.Administer, "")
	case "unit":
		unit, _ := s.unitOf()
		n := 0
		for p, u := range unit {
			if u == name {
				n++
				if !s.authorize(id, auth.AdministerProject, p) {
					return false
				}
			}
		}
		return n > 0 || s.authorize(id, auth.Administer, "")
	case "project":
		return s.authorize(id, auth.AdministerProject, name)
	}
	return false
}

func validScope(scope string) bool {
	kind, name, _ := strings.Cut(scope, ":")
	switch kind {
	case "base", "organization":
		return name == ""
	case "unit", "project":
		return name != "" && len(name) <= 200
	}
	return false
}

// scopeChain is the levels that apply to a scope, outermost first.
func (s *Server) scopeChain(scope string) []string {
	kind, name, _ := strings.Cut(scope, ":")
	unit, _ := s.unitOf()
	switch kind {
	case "base":
		return []string{"base"}
	case "organization":
		return []string{"base", "organization"}
	case "unit":
		return []string{"base", "organization", "unit:" + name}
	case "project":
		if u := unit[name]; u != "" {
			return []string{"base", "organization", "unit:" + u, "project:" + name}
		}
		return []string{"base", "organization", "project:" + name}
	}
	return []string{"base"}
}

// itemScopeOK says whether an item may live in a folder of this scope.
func (s *Server) itemScopeOK(area, item, scope string, itemUnit string) bool {
	kind, name, _ := strings.Cut(scope, ":")
	switch kind {
	case "base", "organization":
		return true
	case "unit":
		return itemUnit == name
	case "project":
		return item == name
	}
	return false
}

type folderView struct {
	engine.OrgFolder
	Path  string `json:"path"`
	Count int    `json:"count"`
	May   bool   `json:"may_arrange"`
}

type itemOrg struct {
	Folder string   `json:"folder,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

func (s *Server) organizeGet(w http.ResponseWriter, r *http.Request) {
	area := r.PathValue("area")
	if !validAreaName(area) {
		writeErr(w, http.StatusNotFound, "no such list to organize")
		return
	}
	id := identityFrom(r)
	vis, err := s.visibleItems(r, area)
	if err != nil {
		s.fail(w, err)
		return
	}
	folders, err := s.Engine.Folders(area)
	if err != nil {
		s.fail(w, err)
		return
	}
	of, err := s.Engine.FolderOf(area)
	if err != nil {
		s.fail(w, err)
		return
	}
	labels, err := s.Engine.LabelsOf(area)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := map[string]itemOrg{}
	for it := range vis {
		o := itemOrg{Folder: of[it], Labels: labels[it]}
		if o.Folder != "" || len(o.Labels) > 0 {
			items[it] = o
		}
	}
	// A folder is shown when something visible is in it or below it, or its level may be arranged by the caller.
	by := map[string]engine.OrgFolder{}
	for _, f := range folders {
		by[f.ID] = f
	}
	count := map[string]int{}
	for _, o := range items {
		for f, n := o.Folder, 0; f != "" && n < 64; n++ {
			count[f]++
			f = by[f].Parent
		}
	}
	paths := engine.FolderPaths(folders)
	views := []folderView{}
	arrange := map[string]bool{}
	for _, f := range folders {
		may, ok := arrange[f.Scope]
		if !ok {
			may = s.mayArrange(id, f.Scope)
			arrange[f.Scope] = may
		}
		if count[f.ID] == 0 && !may {
			continue
		}
		views = append(views, folderView{OrgFolder: f, Path: paths[f.ID], Count: count[f.ID], May: may})
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].Path < views[j].Path })
	page := area
	writeJSON(w, http.StatusOK, map[string]any{"area": area, "folders": views, "items": items, "favorites": s.prefsOf(id.User).Favorites,
		"layout": s.layoutOf(id, page, "base"), "may_arrange_base": s.mayArrange(id, "base")})
}

func validAreaName(a string) bool {
	for _, x := range engine.OrgAreas {
		if x == a {
			return true
		}
	}
	return false
}

func (s *Server) failOrg(w http.ResponseWriter, err error) { s.fail(w, err) }

func (s *Server) folderCreate(w http.ResponseWriter, r *http.Request) {
	area := r.PathValue("area")
	var in engine.OrgFolder
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	if !validAreaName(area) || !validScope(in.Scope) {
		writeErr(w, http.StatusBadRequest, "a folder needs one of the lists and a scope: base, organization, unit:<name> or project:<name>")
		return
	}
	if !s.mayArrange(id, in.Scope) {
		writeErr(w, http.StatusForbidden, "you may not arrange for everyone at that level")
		return
	}
	in.Area, in.CreatedBy = area, id.User
	f, err := s.Engine.CreateFolder(in)
	if err != nil {
		s.failOrg(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) folderUpdate(w http.ResponseWriter, r *http.Request) {
	f, err := s.Engine.Folder(r.PathValue("id"))
	if err != nil {
		s.failOrg(w, err)
		return
	}
	if !s.mayArrange(identityFrom(r), f.Scope) {
		writeErr(w, http.StatusNotFound, "no such folder")
		return
	}
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Position    *int    `json:"position"`
		Parent      *string `json:"parent"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.Engine.UpdateFolder(f.ID, in.Name, in.Description, in.Position, in.Parent)
	if err != nil {
		s.failOrg(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) folderDelete(w http.ResponseWriter, r *http.Request) {
	f, err := s.Engine.Folder(r.PathValue("id"))
	if err != nil || !s.mayArrange(identityFrom(r), f.Scope) {
		writeErr(w, http.StatusNotFound, "no such folder")
		return
	}
	if err := s.Engine.DeleteFolder(f.ID); err != nil {
		s.failOrg(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": f.ID})
}

func (s *Server) folderMove(w http.ResponseWriter, r *http.Request) {
	area := r.PathValue("area")
	var in struct {
		Item     string `json:"item"`
		Folder   string `json:"folder"` // "" takes it out of its folder
		Position int    `json:"position"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	vis, err := s.visibleItems(r, area)
	if err != nil {
		s.fail(w, err)
		return
	}
	unitOfItem, ok := vis[in.Item]
	if !ok {
		writeErr(w, http.StatusNotFound, "no such item")
		return
	}
	cur, _ := s.Engine.FolderOf(area)
	if from := cur[in.Item]; from != "" { // taking it out of a folder is arranging that folder's level
		if f, err := s.Engine.Folder(from); err == nil && !s.mayArrange(id, f.Scope) {
			writeErr(w, http.StatusForbidden, "you may not arrange for everyone at the level of the folder it is in")
			return
		}
	}
	if in.Folder != "" {
		f, err := s.Engine.Folder(in.Folder)
		if err != nil {
			s.failOrg(w, err)
			return
		}
		if !s.mayArrange(id, f.Scope) {
			writeErr(w, http.StatusForbidden, "you may not arrange for everyone at that level")
			return
		}
		if !s.itemScopeOK(area, in.Item, f.Scope, unitOfItem) {
			writeErr(w, http.StatusBadRequest, "that folder belongs to another level than this item")
			return
		}
	}
	if err := s.Engine.MoveItem(area, in.Item, in.Folder, in.Position); err != nil {
		s.failOrg(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"item": in.Item, "folder": in.Folder})
}

func (s *Server) labelsSet(w http.ResponseWriter, r *http.Request) {
	area := r.PathValue("area")
	var in struct {
		Item   string   `json:"item"`
		Labels []string `json:"labels"`
	}
	if !decode(w, r, &in) {
		return
	}
	vis, err := s.visibleItems(r, area)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, ok := vis[in.Item]; !ok {
		writeErr(w, http.StatusNotFound, "no such item")
		return
	}
	if !s.authorize(identityFrom(r), auth.Operate, in.Item) {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("you need the operator role on %s to label it", in.Item))
		return
	}
	if err := s.Engine.SetLabels(area, in.Item, in.Labels); err != nil {
		s.failOrg(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": in.Item, "labels": in.Labels})
}

// ---- layouts ----

type layoutAnswer struct {
	Page       string          `json:"page"`
	Scope      string          `json:"scope"`
	Shared     json.RawMessage `json:"shared"`   // the shared layout in effect (levels merged)
	Personal   json.RawMessage `json:"personal"` // the caller's own
	Effective  json.RawMessage `json:"effective"`
	Levels     []layoutLevel   `json:"levels"`
	LockedAt   string          `json:"locked_at,omitempty"`
	Customized bool            `json:"customized"`
	May        map[string]bool `json:"may_arrange"` // scope → the caller may change it
}

type layoutLevel struct {
	Scope     string          `json:"scope"`
	Data      json.RawMessage `json:"data"`
	Locked    bool            `json:"locked"`
	UpdatedBy string          `json:"updated_by,omitempty"`
	UpdatedAt int64           `json:"updated_at,omitempty"`
}

// mergeLayout puts over on top of base: an object's keys replace; nothing is
// deep-merged, so an arrangement is replaced whole by the level that sets it.
func mergeLayout(base, over map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func raw(m map[string]json.RawMessage) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}

func parseDoc(s string) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// layoutOf resolves a page's layout at a scope: the shared levels from the
// base down (a level that is locked cannot be overridden below it), then the
// caller's own on top.
func (s *Server) layoutOf(id *identity, page, scope string) layoutAnswer {
	chain := s.scopeChain(scope)
	rows, _ := s.Engine.SharedLayouts(page, chain)
	byScope := map[string]engine.SharedLayout{}
	for _, l := range rows {
		byScope[l.Scope] = l
	}
	a := layoutAnswer{Page: page, Scope: scope, Levels: []layoutLevel{}, May: map[string]bool{}}
	shared := map[string]json.RawMessage{}
	for _, sc := range chain {
		a.May[sc] = s.mayArrange(id, sc)
		l, ok := byScope[sc]
		if !ok {
			continue
		}
		a.Levels = append(a.Levels, layoutLevel{Scope: sc, Data: json.RawMessage(l.Data), Locked: l.Locked, UpdatedBy: l.UpdatedBy, UpdatedAt: l.UpdatedAt})
		if a.LockedAt == "" {
			shared = mergeLayout(shared, parseDoc(l.Data))
			if l.Locked {
				a.LockedAt = sc
			}
		}
	}
	personalDoc, _ := s.Engine.UserLayout(id.User, page)
	personal := parseDoc(personalDoc)
	a.Shared, a.Personal = raw(shared), raw(personal)
	eff := shared
	if a.LockedAt == "" { // a locked level keeps the arrangement of everything it sets; own choices of other keys still apply
		eff = mergeLayout(shared, personal)
	} else {
		for k, v := range personal {
			if _, set := shared[k]; !set {
				eff[k] = v
			}
		}
	}
	a.Effective = raw(eff)
	a.Customized = len(personal) > 0
	return a
}

func (s *Server) layoutGet(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "base"
	}
	if !validScope(scope) {
		writeErr(w, http.StatusBadRequest, "scope is base, organization, unit:<name> or project:<name>")
		return
	}
	writeJSON(w, http.StatusOK, s.layoutOf(identityFrom(r), r.PathValue("page"), scope))
}

func (s *Server) layoutSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope  string          `json:"scope"`
		Data   json.RawMessage `json:"data"`
		Locked bool            `json:"locked"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	if !validScope(in.Scope) || !json.Valid(in.Data) {
		writeErr(w, http.StatusBadRequest, "a scope and a layout document are needed")
		return
	}
	if !s.mayArrange(id, in.Scope) {
		writeErr(w, http.StatusForbidden, "you may not arrange for everyone at that level")
		return
	}
	// A level above that locks the layout stops this one.
	for _, sc := range s.scopeChain(in.Scope) {
		if sc == in.Scope {
			break
		}
		for _, l := range mustShared(s, r.PathValue("page"), []string{sc}) {
			if l.Locked {
				writeErr(w, http.StatusConflict, "the layout is locked at "+sc+"; ask someone who may arrange there")
				return
			}
		}
	}
	err := s.Engine.SetSharedLayout(engine.SharedLayout{Scope: in.Scope, Page: r.PathValue("page"), Data: string(in.Data), Locked: in.Locked, UpdatedBy: id.User})
	if err != nil {
		s.failOrg(w, err)
		return
	}
	s.homeCache.drop(id.User)
	writeJSON(w, http.StatusOK, s.layoutOf(id, r.PathValue("page"), in.Scope))
}

func mustShared(s *Server, page string, scopes []string) []engine.SharedLayout {
	l, _ := s.Engine.SharedLayouts(page, scopes)
	return l
}

func (s *Server) layoutDelete(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if !validScope(scope) || !s.mayArrange(identityFrom(r), scope) {
		writeErr(w, http.StatusForbidden, "you may not arrange for everyone at that level")
		return
	}
	if err := s.Engine.DeleteSharedLayout(scope, r.PathValue("page")); err != nil {
		s.failOrg(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.layoutOf(identityFrom(r), r.PathValue("page"), scope))
}

func (s *Server) myLayoutSet(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	page := r.PathValue("page")
	var doc json.RawMessage
	if !decode(w, r, &doc) {
		return
	}
	if id.User == "" || page == "prefs" || !json.Valid(doc) {
		writeErr(w, http.StatusBadRequest, "a signed-in person and a layout document are needed")
		return
	}
	if err := s.Engine.SetUserLayout(id.User, page, string(doc)); err != nil {
		s.failOrg(w, err)
		return
	}
	s.homeCache.drop(id.User)
	writeJSON(w, http.StatusOK, s.layoutOf(id, page, "base"))
}

func (s *Server) myLayoutReset(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if r.PathValue("page") == "prefs" {
		writeErr(w, http.StatusBadRequest, "favorites and set-aside items are not a layout")
		return
	}
	if err := s.Engine.SetUserLayout(id.User, r.PathValue("page"), ""); err != nil {
		s.failOrg(w, err)
		return
	}
	s.homeCache.drop(id.User)
	writeJSON(w, http.StatusOK, s.layoutOf(id, r.PathValue("page"), "base"))
}

func (s *Server) myLayoutsReset(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if err := s.Engine.ResetUserLayouts(id.User); err != nil {
		s.failOrg(w, err)
		return
	}
	s.homeCache.drop(id.User)
	writeJSON(w, http.StatusOK, map[string]bool{"reset": true})
}

func (s *Server) favoriteSet(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	var in struct {
		Item string `json:"item"` // kind:name
		On   bool   `json:"on"`
	}
	if !decode(w, r, &in) {
		return
	}
	kind, name, ok := strings.Cut(in.Item, ":")
	if !ok || name == "" || (kind != "project" && kind != "repository") || id.User == "" {
		writeErr(w, http.StatusBadRequest, "item is project:<name> or repository:<path>")
		return
	}
	area := "projects"
	if kind == "repository" {
		area = "repositories"
	}
	vis, err := s.visibleItems(r, area)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, ok := vis[name]; !ok {
		writeErr(w, http.StatusNotFound, "no such item")
		return
	}
	p := s.prefsOf(id.User)
	keep := []string{}
	for _, f := range p.Favorites {
		if f != in.Item {
			keep = append(keep, f)
		}
	}
	if in.On && len(keep) < 200 {
		keep = append(keep, in.Item)
	}
	p.Favorites = keep
	if err := s.savePrefs(id.User, p); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"favorites": keep})
}

// ---- export and import: a layout as a document, so it can be kept in Git ----

type layoutDoc struct {
	Version int                        `json:"version"`
	Scope   string                     `json:"scope"`
	Layouts map[string]json.RawMessage `json:"layouts"`
	Locked  map[string]bool            `json:"locked,omitempty"`
	Folders []layoutDocFolder          `json:"folders,omitempty"`
}

type layoutDocFolder struct {
	Area        string   `json:"area"`
	Path        string   `json:"path"` // "Payments/Api"
	Description string   `json:"description,omitempty"`
	Items       []string `json:"items,omitempty"`
}

func (s *Server) layoutExport(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	id := identityFrom(r)
	if !validScope(scope) || !s.mayArrange(id, scope) {
		writeErr(w, http.StatusForbidden, "you may export the layout of a level you may arrange")
		return
	}
	doc := layoutDoc{Version: 1, Scope: scope, Layouts: map[string]json.RawMessage{}, Locked: map[string]bool{}}
	for _, page := range []string{"home", "pipelines", "projects", "repositories", "runs"} {
		for _, l := range mustShared(s, page, []string{scope}) {
			doc.Layouts[page] = json.RawMessage(l.Data)
			if l.Locked {
				doc.Locked[page] = true
			}
		}
	}
	for _, area := range engine.OrgAreas {
		fs, err := s.Engine.Folders(area, scope)
		if err != nil {
			s.fail(w, err)
			return
		}
		paths := engine.FolderPaths(fs)
		of, _ := s.Engine.FolderOf(area)
		items := map[string][]string{}
		for it, f := range of {
			items[f] = append(items[f], it)
		}
		for _, f := range fs {
			sort.Strings(items[f.ID])
			doc.Folders = append(doc.Folders, layoutDocFolder{Area: area, Path: paths[f.ID], Description: f.Description, Items: items[f.ID]})
		}
	}
	sort.SliceStable(doc.Folders, func(i, j int) bool {
		if doc.Folders[i].Area != doc.Folders[j].Area {
			return doc.Folders[i].Area < doc.Folders[j].Area
		}
		return doc.Folders[i].Path < doc.Folders[j].Path
	})
	writeJSON(w, http.StatusOK, doc)
}

// layoutImport applies a document. It adds and updates what the document names;
// it never deletes a folder or a layout that the document does not mention.
// With ?dry_run=1 it says what it would do.
func (s *Server) layoutImport(w http.ResponseWriter, r *http.Request) {
	var doc layoutDoc
	if !decode(w, r, &doc) {
		return
	}
	id := identityFrom(r)
	if doc.Version != 1 || !validScope(doc.Scope) {
		writeErr(w, http.StatusBadRequest, "the document is version 1 with a scope")
		return
	}
	if !s.mayArrange(id, doc.Scope) {
		writeErr(w, http.StatusForbidden, "you may not arrange for everyone at that level")
		return
	}
	dry := r.URL.Query().Get("dry_run") == "1"
	var plan []string
	for page, data := range doc.Layouts {
		plan = append(plan, "layout "+page)
		if !dry {
			if err := s.Engine.SetSharedLayout(engine.SharedLayout{Scope: doc.Scope, Page: page, Data: string(data), Locked: doc.Locked[page], UpdatedBy: id.User}); err != nil {
				s.failOrg(w, err)
				return
			}
		}
	}
	for _, f := range doc.Folders {
		if !validAreaName(f.Area) || f.Path == "" {
			writeErr(w, http.StatusBadRequest, "each folder needs an area and a path")
			return
		}
		plan = append(plan, "folder "+f.Area+" "+f.Path)
		if dry {
			continue
		}
		existing, err := s.Engine.Folders(f.Area, doc.Scope)
		if err != nil {
			s.failOrg(w, err)
			return
		}
		paths := engine.FolderPaths(existing)
		have := map[string]string{}
		for fid, p := range paths {
			have[p] = fid
		}
		parent := ""
		built := ""
		for i, part := range strings.Split(f.Path, "/") {
			built = strings.TrimPrefix(built+"/"+part, "/")
			fid, ok := have[built]
			if !ok {
				nf, err := s.Engine.CreateFolder(engine.OrgFolder{Scope: doc.Scope, Area: f.Area, Parent: parent, Name: part, Position: i, CreatedBy: id.User})
				if err != nil {
					s.failOrg(w, err)
					return
				}
				fid = nf.ID
				have[built] = fid
			}
			parent = fid
		}
		for _, it := range f.Items {
			if err := s.Engine.MoveItem(f.Area, it, parent, 0); err != nil {
				s.failOrg(w, err)
				return
			}
		}
	}
	sort.Strings(plan)
	writeJSON(w, http.StatusOK, map[string]any{"dry_run": dry, "applied": plan})
}
