package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/files"
)

// The repository Overview: what happened lately, the languages by share and the contributors. The feed is read from the
// audit trail, the pull request table, the runs and the releases of the Flights that watch the repository; whatever the
// caller may not read is not in it. Languages are made once per head commit. Contributors are made by a background job
// keyed by head commit and period, never inside the request: the request starts the job and answers what is there.

func (s *Server) overviewRoutes() []route {
	const base = "/api/v1/repositories/{owner}/{name}"
	return []route{
		{"GET " + base + "/activity", repoRead, "", s.repoActivity},
		{"GET /api/v1/owners/{owner}/activity", signedIn, "", s.ownerActivity},
		{"GET " + base + "/languages", repoRead, "", s.repoLanguages},
		{"GET " + base + "/contributors", repoRead, "", s.repoContributors},
	}
}

// ---- activity ----

type activityItem struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"` // push, tag, pull_opened, pull_merged, pull_closed, release, run
	At         int64  `json:"at"`   // milliseconds
	Actor      string `json:"actor,omitempty"`
	Repository string `json:"repository"`
	Title      string `json:"title,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Number     int    `json:"number,omitempty"`  // pull request number or run number
	Project    string `json:"project,omitempty"` // the Flight of a run or release
	Status     string `json:"status,omitempty"`  // of a run
	Version    string `json:"version,omitempty"` // of a release
}

const (
	activityDefault = 30
	activityMost    = 100
)

var activityKinds = map[string]bool{"push": true, "tag": true, "pull_opened": true, "pull_merged": true, "pull_closed": true, "release": true, "run": true}

// activityFilter reads ?kind= (kinds, or "pull" for the three pull request kinds), ?limit= and ?before=.
type activityFilter struct {
	kinds   map[string]bool
	limit   int
	atBound int64 // 0: no cursor
	idBound string
	// actor, when set, keeps what that person did (the public profile uses it).
	actor string
}

func parseActivityFilter(r *http.Request) (activityFilter, string) {
	f := activityFilter{limit: activityDefault}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return f, "limit must be a positive number"
		}
		f.limit = min(n, activityMost)
	}
	if v := r.URL.Query().Get("kind"); v != "" {
		f.kinds = map[string]bool{}
		for _, k := range strings.Split(v, ",") {
			k = strings.TrimSpace(k)
			switch {
			case k == "pull":
				f.kinds["pull_opened"], f.kinds["pull_merged"], f.kinds["pull_closed"] = true, true, true
			case activityKinds[k]:
				f.kinds[k] = true
			default:
				return f, "kind must be push, tag, pull, pull_opened, pull_merged, pull_closed, release or run"
			}
		}
	}
	if v := r.URL.Query().Get("before"); v != "" {
		at, id, ok := strings.Cut(v, ".")
		n, err := strconv.ParseInt(at, 10, 64)
		if !ok || err != nil || n <= 0 {
			return f, "before is the cursor an earlier answer gave"
		}
		f.atBound, f.idBound = n, id
	}
	return f, ""
}

func (f activityFilter) wants(kind string) bool { return f.kinds == nil || f.kinds[kind] }

// after says whether an item is older than the cursor.
func (f activityFilter) after(it activityItem) bool {
	return f.atBound == 0 || it.At < f.atBound || (it.At == f.atBound && it.ID < f.idBound)
}

type activityPage struct {
	Items []activityItem `json:"items"`
	// Next is the cursor for the page after this one ("" at the end): pass it as ?before=.
	Next string `json:"next,omitempty"`
}

func (s *Server) repoActivity(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Forge.Provider.Repositories().Get(r.Context(), repoSubject(r.PathValue("owner"), r.PathValue("name")))
	if err != nil {
		s.failForge(w, err)
		return
	}
	s.writeActivity(w, r, []forge.Repository{repo})
}

func (s *Server) ownerActivity(w http.ResponseWriter, r *http.Request) {
	if !s.forgeOn(w) {
		return
	}
	page, err := s.Forge.Provider.Repositories().List(r.Context(), "")
	if err != nil {
		s.failForge(w, err)
		return
	}
	id := identityFrom(r)
	var repos []forge.Repository
	for _, x := range page.Items {
		if x.Home == r.PathValue("owner") && s.authorize(id, auth.Read, x.Path) && (!id.repoLimited() || id.repoIDMatches(x)) {
			repos = append(repos, x)
		}
	}
	s.writeActivity(w, r, repos)
}

func (s *Server) writeActivity(w http.ResponseWriter, r *http.Request, repos []forge.Repository) {
	f, bad := parseActivityFilter(r)
	if bad != "" {
		writeErr(w, http.StatusBadRequest, bad)
		return
	}
	items, err := s.activityOf(r.Context(), identityFrom(r), repos, f)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := activityPage{Items: items}
	if len(items) > f.limit {
		out.Items = items[:f.limit]
		last := out.Items[f.limit-1]
		out.Next = formatCursor(last)
	}
	writeJSON(w, http.StatusOK, out)
}

// formatCursor is the ?before= that follows an item.
func formatCursor(it activityItem) string { return strconv.FormatInt(it.At, 10) + "." + it.ID }

// refsOfPush picks the references out of a push's audit detail ("name old..new, name old..new", maybe after a note about the credential).
func refsOfPush(detail string) (refs []string) {
	for _, part := range strings.Split(detail, ", ") {
		if i := strings.LastIndex(part, "; "); i >= 0 {
			part = part[i+2:]
		}
		if strings.HasPrefix(part, "refs/") {
			if name, _, ok := strings.Cut(part, " "); ok {
				refs = append(refs, name)
			}
		}
	}
	return refs
}

// activityOf merges the sources, newest first, and returns at most limit+1 items (the extra one says there is more).
func (s *Server) activityOf(ctx context.Context, id *identity, repos []forge.Repository, f activityFilter) ([]activityItem, error) {
	if len(repos) == 0 || s.Engine == nil {
		return []activityItem{}, nil
	}
	var all []activityItem
	for _, source := range []func(context.Context, *identity, []forge.Repository, activityFilter) ([]activityItem, error){s.activityPushes, s.activityPulls, s.activityFlights} {
		items, err := source(ctx, id, repos, f)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	out := all[:0]
	for _, it := range all {
		if it.At > 0 && f.wants(it.Kind) && f.after(it) {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At > out[j].At
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > f.limit+1 {
		out = out[:f.limit+1]
	}
	if out == nil {
		out = []activityItem{}
	}
	return out, nil
}

// want is how many rows each source is asked for: a little more than a page, for the cursor and for what is filtered after.
func (f activityFilter) want() int { return f.limit + 20 }

// activityPushes are the pushes and tags the audit trail holds for the repositories.
func (s *Server) activityPushes(ctx context.Context, _ *identity, repos []forge.Repository, f activityFilter) ([]activityItem, error) {
	if !f.wants("push") && !f.wants("tag") {
		return nil, nil
	}
	paths := make([]string, len(repos))
	for i, x := range repos {
		paths[i] = x.Path
	}
	rows, err := s.Engine.AuditActivity(ctx, paths, []string{"git.push", "repository.tag_create"}, f.actor, f.atBound, f.want())
	if err != nil {
		return nil, err
	}
	var out []activityItem
	for _, row := range rows {
		if row.Kind == "repository.tag_create" {
			name := strings.TrimPrefix(strings.Fields(row.Detail + " ")[0], "refs/tags/")
			out = append(out, activityItem{ID: "tag-" + row.Key, Kind: "tag", At: row.At, Actor: row.Who, Repository: row.Target, Ref: name, Title: name})
			continue
		}
		var heads, tags []string
		for _, ref := range refsOfPush(row.Detail) {
			if t, ok := strings.CutPrefix(ref, "refs/tags/"); ok {
				tags = append(tags, t)
			} else if h, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
				heads = append(heads, h)
			}
		}
		if len(heads) > 0 || len(tags) == 0 {
			out = append(out, activityItem{ID: "push-" + row.Key, Kind: "push", At: row.At, Actor: row.Who, Repository: row.Target, Ref: strings.Join(heads, ", "), Title: strings.Join(heads, ", ")})
		}
		for i, t := range tags {
			out = append(out, activityItem{ID: "tag-" + row.Key + "-" + strconv.Itoa(i), Kind: "tag", At: row.At, Actor: row.Who, Repository: row.Target, Ref: t, Title: t})
		}
	}
	return out, nil
}

// activityPulls are the pull requests opened, merged and closed.
func (s *Server) activityPulls(ctx context.Context, _ *identity, repos []forge.Repository, f activityFilter) ([]activityItem, error) {
	if !f.wants("pull_opened") && !f.wants("pull_merged") && !f.wants("pull_closed") {
		return nil, nil
	}
	ids := make([]string, len(repos))
	byID := map[string]forge.Repository{}
	for i, x := range repos {
		ids[i], byID[x.ID] = x.ID, x
	}
	rows, err := s.Engine.PullActivity(ctx, ids, f.actor, f.atBound, f.want())
	if err != nil {
		return nil, err
	}
	var out []activityItem
	for _, row := range rows {
		out = append(out, activityItem{ID: row.Kind + "-" + row.Key, Kind: row.Kind, At: row.At, Actor: row.Who, Repository: byID[row.Target].Path, Number: row.Number, Title: row.Title})
	}
	return out, nil
}

// activityFlights are the runs and releases of the Flights that watch the repositories, for those the caller may read.
func (s *Server) activityFlights(ctx context.Context, id *identity, repos []forge.Repository, f activityFilter) ([]activityItem, error) {
	if (!f.wants("run") && !f.wants("release")) || s.Projects == nil {
		return nil, nil
	}
	var out []activityItem
	for _, repo := range repos {
		for _, pr := range s.Projects.List() {
			if !addressIsRepo(pr.URL, repo.Path) || !s.authorize(id, auth.Read, pr.Name) {
				continue
			}
			if f.wants("run") {
				flt := engine.RunFilter{Project: pr.Name, Limit: f.want()}
				if f.atBound > 0 {
					flt.Before = &engine.RunCursor{CreatedAt: f.atBound, ID: "\xff"}
				}
				runs, err := s.Engine.ListRuns(flt)
				if err != nil {
					return nil, err
				}
				for _, ru := range runs {
					if f.actor == "" || ru.Actor == f.actor {
						out = append(out, activityItem{ID: "run-" + ru.ID, Kind: "run", At: ru.CreatedAt, Actor: ru.Actor, Repository: repo.Path, Project: ru.Project,
							Number: ru.Number, Status: string(ru.Status), Ref: firstOf(ru.Branch, ru.Tag), Title: ru.Pipeline})
					}
				}
			}
			if f.wants("release") {
				rels, err := s.Engine.ListReleases(engine.ReleaseFilter{Project: pr.Name, State: "published", Limit: f.want()})
				if err != nil {
					continue // releases are not kept by this database: there are none to show
				}
				for _, rl := range rels {
					if f.actor == "" || rl.PublishedBy == f.actor {
						out = append(out, activityItem{ID: "release-" + rl.ID, Kind: "release", At: rl.PublishedAt, Actor: rl.PublishedBy, Repository: repo.Path, Project: rl.Project, Version: rl.Version, Title: firstOf(rl.Title, rl.Version)})
					}
				}
			}
		}
	}
	return out, nil
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- languages ----

type languagesOut struct {
	files.LanguageReport
	Cached bool `json:"cached"`
}

func (s *Server) repoLanguages(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	head, found, err := s.headOf(r.Context(), repo, ref)
	if err != nil {
		s.failFiles(w, err)
		return
	}
	if !found { // an empty repository
		writeJSON(w, http.StatusOK, languagesOut{LanguageReport: files.LanguageReport{Languages: []files.Language{}, Rules: "built-in"}})
		return
	}
	cacheFor(w, head)
	byDefault := ref == repo.DefaultBranch
	if byDefault {
		if commit, data, ok, err := s.Engine.LanguagesCached(r.Context(), repo.ID); err == nil && ok && commit == head {
			var rep files.LanguageReport
			if json.Unmarshal([]byte(data), &rep) == nil {
				writeJSON(w, http.StatusOK, languagesOut{LanguageReport: rep, Cached: true})
				return
			}
		}
	}
	if !s.limitRead(w, r, overviewBuckets, 10, 30, "the languages of a repository") {
		return
	}
	rep, err := s.filesService().Languages(r.Context(), repo.ID, head)
	if err != nil {
		s.failFiles(w, err)
		return
	}
	if byDefault {
		if b, err := json.Marshal(rep); err == nil {
			_ = s.Engine.SaveLanguages(r.Context(), repo.ID, head, string(b))
		}
	}
	writeJSON(w, http.StatusOK, languagesOut{LanguageReport: rep})
}

var overviewBuckets = &bucket{last: map[string]time.Time{}, n: map[string]float64{}}

// headOf resolves a ref to its commit; found is false when the repository has no such ref yet (an empty repository).
func (s *Server) headOf(ctx context.Context, repo forge.Repository, ref string) (string, bool, error) {
	cs, err := s.filesService().Commits(ctx, repo.ID, ref, "", 1)
	if err != nil {
		if ref == repo.DefaultBranch && errors.Is(err, forge.ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	if len(cs) == 0 {
		return "", false, nil
	}
	return cs[0].SHA, true, nil
}

// ---- contributors ----

// overviewCommitCap is the most commits one contributors job walks (a test lowers it).
var overviewCommitCap = files.MaxContributorCommits

var contributorPeriods = map[string]int{"30d": 30, "90d": 90, "365d": 365, "all": 0}

type contributorsOut struct {
	// State is done (Report is the answer for the current head commit), computing (a job is making it) or failed.
	State  string `json:"state"`
	Period string `json:"period"`
	Commit string `json:"commit"` // the head commit the answer is for
	files.ContributorReport
	// Stale says Report is an older answer, kept until the new one is made.
	Stale bool   `json:"stale,omitempty"`
	Error string `json:"error,omitempty"`
}

type overviewJobs struct {
	mu       sync.Mutex
	inflight map[string]bool
	sem      chan struct{}
}

func (s *Server) repoContributors(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.filesRepo(w, r)
	if !ok {
		return
	}
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "90d"
	}
	days, known := contributorPeriods[period]
	if !known {
		writeErr(w, http.StatusBadRequest, "period must be 30d, 90d, 365d or all")
		return
	}
	head, found, err := s.headOf(r.Context(), repo, repo.DefaultBranch)
	if err != nil {
		s.failFiles(w, err)
		return
	}
	out := contributorsOut{Period: period, Commit: head, State: "done", ContributorReport: files.ContributorReport{Contributors: []files.Contributor{}}}
	if !found {
		writeJSON(w, http.StatusOK, out)
		return
	}
	// The answer is made for a head commit and, when it has a window, for the day the window ends on.
	key := head
	if days > 0 {
		key += "@" + s.Now().UTC().Format("2006-01-02")
	}
	need, err := s.Engine.RequestContributors(r.Context(), repo.ID, period, key)
	if err != nil {
		s.fail(w, err)
		return
	}
	row, _, err := s.Engine.ContributorsRow(r.Context(), repo.ID, period)
	if err != nil {
		s.fail(w, err)
		return
	}
	switch {
	case row.State == "done" && row.Commit == key:
		_ = json.Unmarshal([]byte(row.Data), &out.ContributorReport)
	case row.State == "failed":
		out.State, out.Error = "failed", row.Data
	default:
		out.State = "computing"
		if need || !s.overviewJobRunning(repo.ID, period) {
			s.startContributorsJob(r.Context(), engine.ContributorsJob{RepoID: repo.ID, Period: period, Commit: key}, days)
		}
	}
	if out.Contributors == nil {
		out.Contributors = []files.Contributor{}
	}
	cacheFor(w, "")
	writeJSON(w, http.StatusOK, out)
}

func jobKey(j engine.ContributorsJob) string { return j.RepoID + "|" + j.Period }

func (s *Server) overviewJobRunning(repoID, period string) bool {
	s.Forge.overview.mu.Lock()
	defer s.Forge.overview.mu.Unlock()
	return s.Forge.overview.inflight[repoID+"|"+period]
}

// startContributorsJob makes the answer in the background: at most two at a time, each bounded in time and in commits.
func (s *Server) startContributorsJob(parent context.Context, j engine.ContributorsJob, days int) {
	s.Forge.overview.mu.Lock()
	if s.Forge.overview.inflight == nil {
		s.Forge.overview.inflight, s.Forge.overview.sem = map[string]bool{}, make(chan struct{}, 2)
	}
	if s.Forge.overview.inflight[jobKey(j)] {
		s.Forge.overview.mu.Unlock()
		return
	}
	s.Forge.overview.inflight[jobKey(j)] = true
	sem := s.Forge.overview.sem
	s.Forge.overview.mu.Unlock()
	stop := s.stopping()
	go func() {
		defer func() {
			s.Forge.overview.mu.Lock()
			delete(s.Forge.overview.inflight, jobKey(j))
			s.Forge.overview.mu.Unlock()
		}()
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-stop:
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Minute) // the request that started it may end first
		defer cancel()
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		if ok, err := s.Engine.StartContributors(ctx, j); err != nil || !ok {
			return
		}
		var since time.Time
		if days > 0 {
			since = s.Now().AddDate(0, 0, -days)
		}
		head, _, _ := strings.Cut(j.Commit, "@")
		rep, err := s.filesService().Contributors(ctx, j.RepoID, head, since, overviewCommitCap)
		state, data := "done", ""
		if err != nil {
			state, data = "failed", "the contributors could not be counted: "+err.Error()
		} else if b, err := json.Marshal(rep); err == nil {
			data = string(b)
		}
		if ctx.Err() != nil {
			return // stopped: the row stays running and is taken up again when the server starts
		}
		if err := s.Engine.FinishContributors(context.WithoutCancel(ctx), j, state, data); err != nil && s.Log != nil {
			s.Log.Warn("contributors job could not be saved", "repo", j.RepoID, "error", err)
		}
	}()
}
