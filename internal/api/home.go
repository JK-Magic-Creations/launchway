package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// The Home page's one aggregated answer, and the attention rule behind it
// (see engine/attention.go) shared with the Sorties list, the command line and
// MCP. Everything is permission-filtered before anything is counted, so a
// count never tells a person about a project they cannot see.

func (s *Server) homeRoutes() []route {
	return []route{
		{"GET /api/v1/home", signedIn, "", s.homeView},
		{"GET /api/v1/attention", signedIn, "", s.attentionView},
		{"POST /api/v1/attention/dismiss", signedIn, "prefs.attention.dismiss", s.attentionDismiss},
		{"POST /api/v1/attention/restore", signedIn, "prefs.attention.restore", s.attentionRestore},
	}
}

// userPrefs is a person's own state that is not layout: what they follow and
// what they set aside. It is a personal preference only.
type userPrefs struct {
	Favorites   []string         `json:"favorites"`    // "project:name", "repository:name"
	Dismissed   map[string]int64 `json:"dismissed"`    // attention key → until (Unix ms, 0 = until it changes)
	TrailPaused bool             `json:"trail_paused"` // the trail of what the person opened is not being written
}

func (s *Server) prefsOf(user string) userPrefs {
	p := userPrefs{Favorites: []string{}, Dismissed: map[string]int64{}}
	if user == "" {
		return p
	}
	if d, err := s.Engine.UserLayout(user, "prefs"); err == nil {
		_ = json.Unmarshal([]byte(d), &p)
	}
	if p.Dismissed == nil {
		p.Dismissed = map[string]int64{}
	}
	if p.Favorites == nil {
		p.Favorites = []string{}
	}
	return p
}

func (s *Server) savePrefs(user string, p userPrefs) error {
	b, _ := json.Marshal(p)
	s.hc().drop(user)
	return s.Engine.SetUserLayout(user, "prefs", string(b))
}

// homeCaches holds each server's cache of Home answers (see homeCache), so the Server struct is unchanged.
var homeCaches sync.Map // *Server → *homeCache

func (s *Server) hc() *homeCache {
	v, _ := homeCaches.LoadOrStore(s, &homeCache{})
	return v.(*homeCache)
}

// listRunsOrAttention is GET /api/v1/runs: the list of runs, or with ?attention=needs|running the Sorties the
// attention rule names.
func (s *Server) listRunsOrAttention(w http.ResponseWriter, r *http.Request) {
	if a := r.URL.Query().Get("attention"); a == "needs" || a == "running" {
		only, err := s.visibleProjects(identityFrom(r))
		if err != nil {
			s.fail(w, err)
			return
		}
		s.listRunsByAttention(w, r, a, only)
		return
	}
	s.listRuns(w, r)
}

// homeCache keeps each person's answer for a few seconds, so a busy page and
// several tabs cost one computation.
type homeCache struct {
	mu sync.Mutex
	m  map[string]homeEntry
}
type homeEntry struct {
	at  time.Time
	val any
}

func (c *homeCache) get(k string, now time.Time, ttl time.Duration) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if ok && now.Sub(e.at) < ttl {
		return e.val, true
	}
	return nil, false
}
func (c *homeCache) put(k string, now time.Time, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 256 {
		c.m = map[string]homeEntry{}
	}
	c.m[k] = homeEntry{now, v}
}
func (c *homeCache) drop(user string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if strings.HasPrefix(k, user+"\x00") {
			delete(c.m, k)
		}
	}
}

// attentionFor computes the rule for the caller over what they may see.
func (s *Server) attentionFor(id *identity, only []string) (engine.AttentionResult, []engine.AttentionRun, error) {
	runs, deps, err := s.Engine.AttentionFacts(only)
	if err != nil {
		return engine.AttentionResult{}, nil, err
	}
	approvals, err := s.Engine.Approvals()
	if err != nil {
		return engine.AttentionResult{}, nil, err
	}
	allowed := map[string]bool{}
	for _, p := range only {
		allowed[p] = true
	}
	var visible []engine.Approval
	for _, a := range approvals {
		if only == nil || allowed[a.Project] {
			visible = append(visible, a)
		}
	}
	defaults := map[string]string{}
	for _, p := range s.briefs() {
		defaults[p.Name] = p.DefaultBranch
	}
	prefs := s.prefsOf(id.User)
	follows := map[string]bool{}
	for _, f := range prefs.Favorites {
		if n, ok := strings.CutPrefix(f, "project:"); ok {
			follows[n] = true
		}
	}
	res := engine.ComputeAttention(engine.AttentionInput{Runs: runs, Deployments: deps, Approvals: visible, Now: s.Now().UnixMilli(), User: id.User,
		DefaultBranch: func(p string) string { return defaults[p] }, Follows: follows})
	return res, runs, nil
}

// dismissedSplit separates what the person set aside from what is open.
func dismissedSplit(items []engine.AttentionItem, p userPrefs, now int64) (open, set []engine.AttentionItem) {
	open, set = []engine.AttentionItem{}, []engine.AttentionItem{}
	for _, it := range items {
		if until, ok := p.Dismissed[it.Key]; ok && (until == 0 || until > now) {
			set = append(set, it)
			continue
		}
		open = append(open, it)
	}
	return
}

type attentionAnswer struct {
	Needs      []engine.AttentionItem `json:"needs"`
	Dismissed  []engine.AttentionItem `json:"dismissed"`
	Retrying   []engine.Retry         `json:"retrying"`
	Count      int                    `json:"count"`
	Superseded int                    `json:"superseded"`
}

func (s *Server) attentionView(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	only, err := s.visibleProjects(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	res, _, err := s.attentionFor(id, only)
	if err != nil {
		s.fail(w, err)
		return
	}
	open, set := dismissedSplit(res.Needs, s.prefsOf(id.User), s.Now().UnixMilli())
	writeJSON(w, http.StatusOK, attentionAnswer{Needs: open, Dismissed: set, Retrying: res.Retrying, Count: len(open), Superseded: res.Superseded})
}

func (s *Server) attentionDismiss(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	var in struct {
		Key        string `json:"key"`
		SnoozeHour int    `json:"snooze_hours"` // 0: until something newer replaces it
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil || in.Key == "" || id.User == "" {
		writeErr(w, http.StatusBadRequest, "a key to set aside, and a signed-in person, are needed")
		return
	}
	p := s.prefsOf(id.User)
	until := int64(0)
	if in.SnoozeHour > 0 {
		until = s.Now().Add(time.Duration(min(in.SnoozeHour, 24*30)) * time.Hour).UnixMilli()
	}
	if len(p.Dismissed) > 500 { // keep it small: forget the ones that ended
		now := s.Now().UnixMilli()
		for k, u := range p.Dismissed {
			if u != 0 && u < now {
				delete(p.Dismissed, k)
			}
		}
	}
	p.Dismissed[in.Key] = until
	if err := s.savePrefs(id.User, p); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dismissed": in.Key, "until": until})
}

func (s *Server) attentionRestore(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	var in struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil || id.User == "" {
		writeErr(w, http.StatusBadRequest, "a key to bring back is needed")
		return
	}
	p := s.prefsOf(id.User)
	if in.Key == "" || in.Key == "*" {
		p.Dismissed = map[string]int64{}
	} else {
		delete(p.Dismissed, in.Key)
	}
	if err := s.savePrefs(id.User, p); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restored": in.Key})
}

// ---- Home ----

type homeRunning struct {
	ID        string `json:"id"`
	Project   string `json:"project"`
	Number    int    `json:"number"`
	Branch    string `json:"branch,omitempty"`
	PR        int    `json:"pull_request,omitempty"`
	Status    string `json:"status"`
	Step      string `json:"step,omitempty"`
	StartedAt int64  `json:"started_at"`
	CreatedAt int64  `json:"created_at"`
	Actor     string `json:"actor"`
	Message   string `json:"message,omitempty"`
	Mine      bool   `json:"mine"`
	Squadron  string `json:"unit,omitempty"`
	RetryOf   int    `json:"retry_of,omitempty"` // the number of the failed Sortie this one is retrying
	Waiting   bool   `json:"waiting,omitempty"`
}

type homeProject struct {
	Name     string   `json:"name"`
	Favorite bool     `json:"favorite"`
	Last     string   `json:"last,omitempty"` // latest default-branch result: succeeded, failed, running, queued
	LastID   string   `json:"last_id,omitempty"`
	LastNo   int      `json:"last_number,omitempty"`
	LastAt   int64    `json:"last_at,omitempty"`
	Running  int      `json:"running"`
	Needs    int      `json:"needs"`
	History  []string `json:"history"`
}

type homeCategory struct {
	ID       string        `json:"id"`
	Kind     string        `json:"kind"` // unit or none
	Label    string        `json:"label"`
	Projects []homeProject `json:"projects"`
	Needs    int           `json:"needs"`
	Running  int           `json:"running"`
	Failing  int           `json:"failing"`
}

type homeRecent struct {
	ID         string `json:"id"`
	Project    string `json:"project"`
	Number     int    `json:"number"`
	Branch     string `json:"branch,omitempty"`
	Status     string `json:"status"`
	FinishedAt int64  `json:"finished_at"`
}

type homeAnswer struct {
	View       string                 `json:"view"`
	At         int64                  `json:"at"`
	Needs      []engine.AttentionItem `json:"needs"`
	Dismissed  []engine.AttentionItem `json:"dismissed"`
	Running    []homeRunning          `json:"running"`
	Categories []homeCategory         `json:"categories"`
	Favorites  []homeProject          `json:"favorites"`
	Recent     []homeRecent           `json:"recent"`
	Counts     map[string]int         `json:"counts"`
	// Layout is the Home page arrangement in effect: the shared default under the person's own choices.
	Layout layoutAnswer `json:"layout"`
}

// unitOf maps each project to its unit's name and display name.
func (s *Server) unitOf() (map[string]string, map[string]string) {
	unit, display := map[string]string{}, map[string]string{}
	if s.Settings == nil {
		return unit, display
	}
	if _, lay, _ := s.Settings.Current(); lay != nil {
		for p, u := range lay.ProjectUnit {
			unit[p] = u
		}
		for _, u := range lay.Units {
			display[u.Name] = u.DisplayName
		}
	}
	return unit, display
}

// projectAgg is what the newest runs say about one project.
type projectAgg struct {
	last    *engine.AttentionRun // the newest default-branch result
	running int
	history []string // newest first
	mine    bool
}

// aggregateRuns summarizes the runs (newest first) per project.
func aggregateRuns(runs []engine.AttentionRun, defaults map[string]string, user string) map[string]*projectAgg {
	by := map[string]*projectAgg{}
	for i := range runs {
		r := &runs[i]
		a := by[r.Project]
		if a == nil {
			a = &projectAgg{}
			by[r.Project] = a
		}
		if r.Actor == user && user != "" {
			a.mine = true
		}
		if r.Status == engine.RunQueued || r.Status == engine.RunRunning {
			a.running++
		}
		if r.Branch != defaults[r.Project] || r.Event == "pull_request" || r.Event == "draft" {
			continue
		}
		if a.last == nil {
			a.last = r
		}
		if len(a.history) < 10 && r.Status != engine.RunQueued && r.Status != engine.RunRunning && r.Status != engine.RunCanceled {
			a.history = append(a.history, string(r.Status))
		}
	}
	return by
}

// homeInputs is what Home is worked out from, after permissions.
type homeInputs struct {
	names    []string
	defaults map[string]string
	by       map[string]*projectAgg
	needsBy  map[string]int
	fav      map[string]bool
	unit     map[string]string
	display  map[string]string
	view     string
}

func (in homeInputs) shown(p string) bool {
	return in.view != "mine" || in.fav[p] || (in.by[p] != nil && in.by[p].mine)
}

// categories groups the projects by Squadron, those with something to look at first.
func (in homeInputs) categories() ([]homeCategory, []homeProject) {
	cats := map[string]*homeCategory{}
	var order []string
	var favorites []homeProject
	for _, n := range in.names {
		if !in.shown(n) {
			continue
		}
		hp := homeProject{Name: n, Favorite: in.fav[n], Needs: in.needsBy[n], History: []string{}}
		if a := in.by[n]; a != nil {
			hp.Running = a.running
			hp.History = append(hp.History, a.history...)
			if a.last != nil {
				hp.Last, hp.LastID, hp.LastNo, hp.LastAt = string(a.last.Status), a.last.ID, a.last.Number, nz64(a.last.FinishedAt, a.last.CreatedAt)
			}
		}
		cid, label, kind := "none", "Not in a Squadron", "none"
		if u := in.unit[n]; u != "" {
			cid, label, kind = "unit:"+u, nzs(in.display[u], u), "unit"
		}
		c := cats[cid]
		if c == nil {
			c = &homeCategory{ID: cid, Kind: kind, Label: label, Projects: []homeProject{}}
			cats[cid] = c
			order = append(order, cid)
		}
		c.Projects = append(c.Projects, hp)
		c.Needs += hp.Needs
		c.Running += hp.Running
		if hp.Last == "failed" {
			c.Failing++
		}
		if hp.Favorite {
			favorites = append(favorites, hp)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := cats[order[i]], cats[order[j]]
		if (a.Kind == "none") != (b.Kind == "none") {
			return b.Kind == "none"
		}
		if (a.Needs > 0) != (b.Needs > 0) {
			return a.Needs > 0
		}
		return a.Label < b.Label
	})
	out := make([]homeCategory, 0, len(order))
	for _, c := range order {
		out = append(out, *cats[c])
	}
	return out, favorites
}

// runningCards are the Sorties in progress as cards: the person's own first, then their Squadron's, then the rest.
func (s *Server) runningCards(id *identity, runs []engine.AttentionRun, in homeInputs, retryOf map[string]int) ([]homeRunning, error) {
	var progress []string
	for _, r := range runs {
		if (r.Status == engine.RunQueued || r.Status == engine.RunRunning) && in.shown(r.Project) {
			progress = append(progress, r.ID)
			if len(progress) >= 60 {
				break
			}
		}
	}
	waiting := map[string]bool{}
	if aps, err := s.Engine.Approvals(); err == nil {
		for _, a := range aps {
			waiting[a.RunID] = true
		}
	}
	sums, err := s.Engine.RunsByID(progress)
	if err != nil {
		return nil, err
	}
	myUnits := map[string]bool{}
	for p, a := range in.by {
		if a.mine && in.unit[p] != "" {
			myUnits[in.unit[p]] = true
		}
	}
	out := []homeRunning{}
	for _, r := range sums {
		hr := homeRunning{ID: r.ID, Project: r.Project, Number: r.Number, Branch: r.Branch, PR: r.PullRequest, Status: string(r.Status), StartedAt: r.StartedAt,
			CreatedAt: r.CreatedAt, Actor: r.Actor, Message: r.Message, Mine: r.Actor == id.User && id.User != "", Squadron: in.unit[r.Project], RetryOf: retryOf[r.ID], Waiting: waiting[r.ID]}
		for _, st := range r.Steps {
			if st.Status == engine.StepRunning {
				hr.Step = st.ID
				break
			}
		}
		out = append(out, hr)
	}
	rank := func(h homeRunning) int {
		switch {
		case h.Mine:
			return 0
		case h.Squadron != "" && myUnits[h.Squadron]:
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

// recentFinished is the small strip of what finished lately, passed and failed alike.
func recentFinished(runs []engine.AttentionRun, in homeInputs) []homeRecent {
	out := []homeRecent{}
	for _, r := range runs {
		if len(out) >= 8 {
			break
		}
		if (r.Status == engine.RunSucceeded || r.Status == engine.RunFailed) && r.FinishedAt > 0 && in.shown(r.Project) {
			out = append(out, homeRecent{ID: r.ID, Project: r.Project, Number: r.Number, Branch: r.Branch, Status: string(r.Status), FinishedAt: r.FinishedAt})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].FinishedAt > out[j].FinishedAt })
	return out
}

// homeView answers GET /api/v1/home.
func (s *Server) homeView(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	lay := s.layoutOf(id, "home", "base")
	view := r.URL.Query().Get("view")
	if view == "" {
		var eff struct {
			View string `json:"view"`
		}
		_ = json.Unmarshal(lay.Effective, &eff)
		view = eff.View
	}
	if view != "mine" {
		view = "all"
	}
	key := id.User + "\x00" + view
	if v, ok := s.hc().get(key, s.Now(), 3*time.Second); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}
	only, err := s.visibleProjects(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	res, runs, err := s.attentionFor(id, only)
	if err != nil {
		s.fail(w, err)
		return
	}
	prefs := s.prefsOf(id.User)
	now := s.Now().UnixMilli()
	open, set := dismissedSplit(res.Needs, prefs, now)
	in := homeInputs{defaults: map[string]string{}, needsBy: map[string]int{}, fav: map[string]bool{}, view: view}
	for _, f := range prefs.Favorites {
		if n, ok := strings.CutPrefix(f, "project:"); ok {
			in.fav[n] = true
		}
	}
	in.unit, in.display = s.unitOf()
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
	retryOf := map[string]int{}
	for _, rt := range res.Retrying {
		retryOf[rt.NowID] = rt.FailedNo
	}
	out := homeAnswer{View: view, At: now, Needs: filterNeeds(open, in.shown), Dismissed: filterNeeds(set, in.shown), Layout: lay}
	out.Categories, out.Favorites = in.categories()
	if out.Favorites == nil {
		out.Favorites = []homeProject{}
	}
	if out.Running, err = s.runningCards(id, runs, in, retryOf); err != nil {
		s.fail(w, err)
		return
	}
	out.Recent = recentFinished(runs, in)
	out.Counts = map[string]int{"needs": len(out.Needs), "running": len(out.Running), "retrying": len(res.Retrying), "projects": len(in.names), "dismissed": len(out.Dismissed)}
	s.hc().put(key, s.Now(), out)
	writeJSON(w, http.StatusOK, out)
}

func filterNeeds(in []engine.AttentionItem, keep func(string) bool) []engine.AttentionItem {
	out := []engine.AttentionItem{}
	for _, n := range in {
		if keep(n.Project) {
			out = append(out, n)
		}
	}
	return out
}

func nz64(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// listRunsByAttention answers GET /api/v1/runs?attention=needs|running: the
// Sorties the attention rule lists (needs), or those in progress (running).
func (s *Server) listRunsByAttention(w http.ResponseWriter, r *http.Request, which string, only []string) {
	id := identityFrom(r)
	res, runs, err := s.attentionFor(id, only)
	if err != nil {
		s.fail(w, err)
		return
	}
	var ids []string
	if which == "needs" {
		open, _ := dismissedSplit(res.Needs, s.prefsOf(id.User), s.Now().UnixMilli())
		for _, n := range open {
			if n.RunID != "" {
				ids = append(ids, n.RunID)
			}
		}
	} else {
		for _, run := range runs {
			if run.Status == engine.RunQueued || run.Status == engine.RunRunning {
				ids = append(ids, run.ID)
			}
		}
	}
	if len(ids) > 200 {
		ids = ids[:200]
	}
	out, err := s.Engine.RunsByID(ids)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// briefs lists the projects with their default branches without reading anything per project (see source.Brief).
func (s *Server) briefs() []source.Brief {
	if s.Projects == nil {
		return nil
	}
	return s.Projects.Briefs()
}
