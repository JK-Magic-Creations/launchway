package api

import (
	"errors"
	"net/http"
	"sort"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
)

func (s *Server) peopleRoutes() []route {
	return []route{
		{"GET /api/v1/people/{name}", signedIn, "", s.publicProfile},
		{"GET /api/v1/units/{name}/team", signedIn, "", s.unitTeam},
	}
}

// readableRepos lists the hosted repositories the caller may read (the same rule as the repository list).
func (s *Server) readableRepos(r *http.Request) ([]forge.Repository, error) {
	if s.Forge == nil {
		return nil, nil
	}
	page, err := s.Forge.Provider.Repositories().List(r.Context(), "")
	if err != nil {
		return nil, err
	}
	id := identityFrom(r)
	var out []forge.Repository
	for _, x := range page.Items {
		if s.authorize(id, auth.Read, x.Path) && (!id.repoLimited() || id.repoIDMatches(x)) {
			out = append(out, x)
		}
	}
	return out, nil
}

// publicProfile is a person's public face: their name and display name, and what they did lately on repositories the
// person ASKING may read. The email, the groups, the roles, the tokens and when they last signed in are not in it, and
// a repository the viewer cannot read adds nothing, not even a count. A person who never signed in is not found.
func (s *Server) publicProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	known := false
	display := ""
	if _, prof, err := s.Engine.LastSignIn(name); err == nil {
		known, display = true, prof.Name
	} else if !errors.Is(err, engine.ErrNotFound) {
		s.fail(w, err)
		return
	}
	if !known {
		writeErr(w, http.StatusNotFound, "there is no person "+name+" here")
		return
	}
	f, bad := parseActivityFilter(r)
	if bad != "" {
		writeErr(w, http.StatusBadRequest, bad)
		return
	}
	f.actor = name
	repos, err := s.readableRepos(r)
	if err != nil {
		s.failForge(w, err)
		return
	}
	items, err := s.activityOf(r.Context(), identityFrom(r), repos, f)
	if err != nil {
		s.fail(w, err)
		return
	}
	page := activityPage{Items: items}
	if len(items) > f.limit {
		page.Items = items[:f.limit]
		last := page.Items[f.limit-1]
		page.Next = formatCursor(last)
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "display_name": display, "activity": page})
}

// unitTeam is a Squadron's team: the people and groups that hold a role at the Squadron, by role, the repositories
// behind its Flights and its Flights, as the caller may see them. A caller who sees none of the Squadron's Flights and
// does not administer them is told there is no such Squadron.
func (s *Server) unitTeam(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	by, lay, _, err := s.squadronData(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var unit *struct{ display string }
	if lay != nil {
		for i := range lay.Units {
			if lay.Units[i].Name == name {
				unit = &struct{ display string }{nzs(lay.Units[i].DisplayName, name)}
			}
		}
	}
	flights := by[name]
	if unit == nil || len(flights) == 0 && !s.mayManage(r) {
		writeErr(w, http.StatusNotFound, "There is no Squadron called "+name+".")
		return
	}
	if flights == nil {
		flights = []flightRow{}
	}
	members := map[string][]personRole{"admin": {}, "operator": {}, "viewer": {}}
	if st := s.Settings; st != nil && st.Available() {
		people, _ := s.squadronSharing(st, name)
		for _, p := range people {
			members[p.Role] = append(members[p.Role], p)
		}
		for _, list := range members {
			sort.Slice(list, func(i, j int) bool { return list[i].Who < list[j].Who })
		}
	}
	repos := []map[string]string{}
	if s.Projects != nil {
		readable, err := s.readableRepos(r)
		if err != nil {
			s.failForge(w, err)
			return
		}
		urls := map[string]string{}
		for _, p := range s.Projects.List() {
			urls[p.Name] = p.URL
		}
		seen := map[string]bool{}
		for _, f := range flights {
			for _, rp := range readable {
				if !seen[rp.Path] && addressIsRepo(urls[f.Name], rp.Path) {
					seen[rp.Path] = true
					repos = append(repos, map[string]string{"path": rp.Path, "visibility": string(rp.Visibility)})
				}
			}
		}
		sort.Slice(repos, func(i, j int) bool { return repos[i]["path"] < repos[j]["path"] })
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "display_name": unit.display, "members": members, "repositories": repos, "flights": flights})
}
