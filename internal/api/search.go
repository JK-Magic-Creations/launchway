package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// One search over everything a person may see: projects (pipelines),
// repositories, folders, labels and Sorties (by number, commit or branch).
// It reads only what the caller may read, so a result or a count never
// reveals more. Names are ranked in memory over that permission-filtered list
// (exact, prefix, word prefix, substring, then a typo or a skipped letter);
// Sorties are searched in the database by number, commit prefix and branch.
// The page's own destinations (settings pages, guides) are matched in the
// browser, where they are known.

func (s *Server) searchRoutes() []route {
	return []route{{"GET /api/v1/search", signedIn, "", s.search}}
}

// SearchHit is one result.
type SearchHit struct {
	Kind   string `json:"kind"` // project, repository, folder, label, run
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	Path   string `json:"path,omitempty"` // where it lives
	Href   string `json:"href"`
	Score  int    `json:"score"`
}

// fuzzyScore ranks how well q matches text (both lower case): 0 is no match.
func fuzzyScore(q, text string) int {
	if q == "" || text == "" {
		return 0
	}
	if text == q {
		return 100
	}
	if strings.HasPrefix(text, q) {
		return 85 - min(len(text)-len(q), 20)/2
	}
	for _, w := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if strings.HasPrefix(w, q) {
			return 70
		}
	}
	if strings.Contains(text, q) {
		return 55
	}
	if len(q) >= 3 {
		// One typo: the query is within one edit of a prefix of the text, or of one of its words.
		for _, w := range append(strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), text) {
			if len(w) >= len(q)-1 {
				p := w[:min(len(w), len(q)+1)]
				if editWithin1(q, p) || editWithin1(q, w[:min(len(w), len(q))]) || editWithin1(q, w[:min(len(w), len(q)-1)]) {
					return 35
				}
			}
		}
		if subsequence(q, text) {
			return 20
		}
	}
	return 0
}

func subsequence(q, t string) bool {
	i := 0
	for _, r := range t {
		if i < len(q) && rune(q[i]) == r {
			i++
		}
	}
	return i == len(q)
}

// editWithin1 says a and b differ by at most one insertion, deletion, substitution or swap of neighbors.
func editWithin1(a, b string) bool {
	if a == b {
		return true
	}
	la, lb := len(a), len(b)
	if la-lb > 1 || lb-la > 1 {
		return false
	}
	i := 0
	for i < la && i < lb && a[i] == b[i] {
		i++
	}
	switch {
	case la == lb:
		if a[i+1:] == b[i+1:] {
			return true
		}
		return i+1 < la && a[i] == b[i+1] && a[i+1] == b[i] && a[i+2:] == b[i+2:]
	case la > lb:
		return a[i+1:] == b[i:]
	default:
		return a[i:] == b[i+1:]
	}
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	if len(q) > 100 {
		q = q[:100]
	}
	id := identityFrom(r)
	hits := []SearchHit{}
	if q == "" {
		recent, _ := s.visibleActivity(id, 6, s.retentionDays())
		writeJSON(w, http.StatusOK, map[string]any{"q": q, "hits": hits, "favorites": s.prefsOf(id.User).Favorites, "recent": recent})
		return
	}
	add := func(kind, name, detail, path, href string, extra ...string) {
		best := fuzzyScore(q, strings.ToLower(name))
		for _, x := range extra {
			if sc := fuzzyScore(q, strings.ToLower(x)) - 10; sc > best {
				best = sc
			}
		}
		if best > 0 {
			hits = append(hits, SearchHit{Kind: kind, Name: name, Detail: detail, Path: path, Href: href, Score: best})
		}
	}
	s.searchNames(r, id, add)
	s.searchFolders(r, add)
	hits = append(hits, s.searchRuns(id, q)...)
	rankHits(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"q": q, "hits": hits})
}

func authRead() auth.Action { return auth.Read }

// searchFolders offers the folders that hold something the caller can see to add, which scores them.
func (s *Server) searchFolders(r *http.Request, add func(kind, name, detail, path, href string, extra ...string)) {
	vis, err := s.visibleItems(r, "pipelines")
	if err != nil {
		return
	}
	fs, err := s.Engine.Folders("pipelines")
	if err != nil {
		return
	}
	of, _ := s.Engine.FolderOf("pipelines")
	by := map[string]engine.OrgFolder{}
	for _, f := range fs {
		by[f.ID] = f
	}
	has := map[string]bool{}
	for it, f := range of {
		if _, ok := vis[it]; !ok {
			continue
		}
		for n := 0; f != "" && n < 64; n++ {
			has[f] = true
			f = by[f].Parent
		}
	}
	paths := engine.FolderPaths(fs)
	for _, f := range fs {
		if has[f.ID] {
			add("folder", f.Name, f.Description, paths[f.ID], "/pipelines?folder="+f.ID)
		}
	}
}

// searchRuns finds Sorties by number ("#123" or "123"), the start of a commit or a branch, in the database.
func (s *Server) searchRuns(id *identity, q string) []SearchHit {
	only, err := s.visibleProjects(id)
	if err != nil {
		return nil
	}
	runs, err := s.Engine.SearchRuns(only, q, 12)
	if err != nil {
		return nil
	}
	var hits []SearchHit
	for _, ru := range runs {
		detail := ru.Branch
		if ru.PullRequest > 0 {
			detail = "pull request #" + strconv.Itoa(ru.PullRequest)
		}
		sc := 60
		if strings.TrimPrefix(q, "#") == strconv.Itoa(ru.Number) {
			sc = 90
		}
		hits = append(hits, SearchHit{Kind: "run", Name: ru.Project + " #" + strconv.Itoa(ru.Number), Detail: strings.TrimSpace(detail + " " + shortSHA(ru.SHA) + " " + string(ru.Status)),
			Href: "/runs/" + ru.ID, Score: sc})
	}
	return hits
}

// searchNames offers the projects and repositories the caller may read to add, which scores them.
func (s *Server) searchNames(r *http.Request, id *identity, add func(kind, name, detail, path, href string, extra ...string)) {
	unit, display := s.unitOf()
	labels, _ := s.Engine.LabelsOf("pipelines")
	for _, p := range s.projects() {
		if !s.authorize(id, authRead(), p.Name) {
			continue
		}
		path := ""
		if u := unit[p.Name]; u != "" {
			path = nzs(display[u], u)
		}
		add("project", p.Name, "", path, "/projects/"+p.Name+"/pipeline", strings.Join(labels[p.Name], " "))
	}
	if s.Forge != nil && s.Forge.Provider != nil {
		if page, err := s.Forge.Provider.Repositories().List(r.Context(), ""); err == nil {
			for _, x := range page.Items {
				if s.authorize(id, authRead(), x.Path) {
					add("repository", x.Path, "default branch "+x.DefaultBranch, "", "/repositories/"+x.Path)
				}
			}
		}
	}
}

// rankHits orders hits by score, then kind, then name.
func rankHits(hits []SearchHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Kind != hits[j].Kind {
			return hits[i].Kind < hits[j].Kind
		}
		return hits[i].Name < hits[j].Name
	})
}
