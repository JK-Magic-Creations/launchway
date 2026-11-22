package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/files"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/graph"
)

// The integrated software graph over HTTP (internal/graph): what depends on
// what, what is built from what, what runs where, and what each thing needs.
// Every answer is for the caller alone: a node, an edge or a count that the
// caller could not read in its own place is not in it. The graph lives in the
// database; there is nothing else to run.

// GraphService is what the API needs of the graph.
type GraphService struct {
	Store  *graph.Store
	Runner *graph.Runner
	// Advisories answers vulnerability questions (nil: no vulnerability data).
	Advisories graph.Advisories
	// Watch reads repositories again after pushes and notices runs and
	// deployments (nil: only the routes that ask for it read anything).
	Watch *graph.Watcher
	// ProjectRepos maps a project to the hosted repository it builds (nil: no project is tied to a repository).
	ProjectRepos func(ctx context.Context) (map[string]string, error)
}

// graphViewer says what the caller may read, by asking the same access policy
// every other route asks.
type graphViewer struct {
	s  *Server
	id *identity
}

// CanRead maps a graph subject to the access policy.
func (v graphViewer) CanRead(subject string) bool {
	switch {
	case subject == "":
		return true
	case strings.HasPrefix(subject, "repo:"):
		return v.s.authorize(v.id, auth.Read, strings.TrimPrefix(subject, "repo:"))
	case strings.HasPrefix(subject, "project:"):
		return v.s.authorize(v.id, auth.Read, strings.TrimPrefix(subject, "project:"))
	default: // the installation's machines and platforms: anyone who can read any project
		return v.s.authorize(v.id, auth.ReadAny, "")
	}
}

func (s *Server) graphRoutes() []route {
	const g = "/api/v1/graph"
	const r = "/api/v1/repositories/{owner}/{name}"
	return []route{
		{"GET " + g, signedIn, "", s.graphSummary},
		{"GET " + g + "/search", signedIn, "", s.graphSearch},
		{"GET " + g + "/node", signedIn, "", s.graphNode},
		{"GET " + g + "/neighbours", signedIn, "", s.graphNeighbours},
		{"GET " + g + "/path", signedIn, "", s.graphPath},
		{"GET " + g + "/impact", signedIn, "", s.graphImpact},
		{"GET " + g + "/where", signedIn, "", s.graphWhere},
		{"GET " + g + "/contents", signedIn, "", s.graphContents},
		{"GET " + g + "/order", signedIn, "", s.graphOrder},
		{"GET " + g + "/rebuild-set", signedIn, "", s.graphRebuildSet},
		{"GET " + g + "/overrides", signedIn, "", s.graphOverrides},
		{"POST " + g + "/overrides", signedIn, "graph.override.add", s.graphAddOverride},
		{"DELETE " + g + "/overrides/{id}", signedIn, "graph.override.remove", s.graphRemoveOverride},
		{"GET " + g + "/needs", signedIn, "", s.graphNeeds},
		{"GET " + g + "/placement", signedIn, "", s.graphPlacement},
		{"GET " + g + "/specification", signedIn, "", s.graphSpecification},
		{"GET " + g + "/reach", signedIn, "", s.graphReach},
		{"POST " + g + "/rescan", administer, "graph.rescan", s.graphRescan},
		{"POST " + g + "/rebuild", administer, "graph.rebuild", s.graphRebuild},
		{"POST " + r + "/graph/refresh", repoWrite, "graph.refresh", s.graphRefresh},
		{"POST " + r + "/requirements/proposal", repoWrite, "graph.requirements.propose", s.graphProposeRequirements},
	}
}

func (s *Server) graphOn(w http.ResponseWriter) bool {
	if s.Graph == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "the software graph is not available on this server", "code": "feature_off",
			"next": "The graph is part of the server; if you see this, the server was started without a database it can use for it. Nothing was deleted."})
		return false
	}
	return true
}

func (s *Server) failGraph(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, graph.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error(), "next": "Check the node (kind:key, for example repository:team/app), and that you may read it; the graph does not tell you about things you cannot read."})
	case errors.Is(err, graph.ErrInvalid):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid: "))
	default:
		s.fail(w, err)
	}
}

// graphCtx reads who is asking, the limits and the moment of a request.
func (s *Server) graphCtx(w http.ResponseWriter, r *http.Request) (graph.Viewer, graph.Limits, graph.When, bool) {
	q := r.URL.Query()
	var lim graph.Limits
	for name, dst := range map[string]*int{"depth": &lim.Depth, "nodes": &lim.Nodes, "timeout_ms": &lim.Time} {
		if v := q.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeErr(w, http.StatusBadRequest, name+" must be a whole number")
				return nil, lim, graph.When{}, false
			}
			*dst = n
		}
	}
	var when graph.When
	switch {
	case q.Get("as_of") != "":
		t, err := time.Parse(time.RFC3339, q.Get("as_of"))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "as_of must be a time such as 2026-11-01T09:00:00Z")
			return nil, lim, when, false
		}
		when.At = t.UnixMilli()
	case q.Get("commit") != "":
		repo := q.Get("repository")
		if repo == "" {
			writeErr(w, http.StatusBadRequest, "name the repository the commit belongs to (repository=owner/name)")
			return nil, lim, when, false
		}
		wh, err := s.Graph.Store.AsOfCommit(r.Context(), graphViewer{s, identityFrom(r)}, repo, q.Get("commit"))
		if err != nil {
			s.failGraph(w, err)
			return nil, lim, when, false
		}
		when = wh
	}
	return graphViewer{s, identityFrom(r)}, lim, when, true
}

func graphRef(w http.ResponseWriter, r *http.Request, name string) (graph.NodeRef, bool) {
	ref, err := graph.ParseRef(r.URL.Query().Get(name))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return ref, false
	}
	return ref, true
}

func (s *Server) graphSummary(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	sum, err := s.Graph.Store.Summary(r.Context(), graphViewer{s, identityFrom(r)})
	if err != nil {
		s.failGraph(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) graphSearch(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, _, _, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	var kinds []graph.NodeKind
	for _, k := range r.URL.Query()["kind"] {
		if !graph.NodeKind(k).Valid() {
			writeErr(w, http.StatusBadRequest, k+" is not a kind of node")
			return
		}
		kinds = append(kinds, graph.NodeKind(k))
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	nodes, more, err := s.Graph.Store.Search(r.Context(), v, r.URL.Query().Get("q"), kinds, limit)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": nodes, "more": more})
}

func (s *Server) graphNode(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, _, when, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	ref, ok := graphRef(w, r, "ref")
	if !ok {
		return
	}
	n, err := s.Graph.Store.Get(r.Context(), v, ref, when)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	edges, more, err := s.Graph.Store.Edges(r.Context(), v, ref, when, 500)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": n, "edges": edges, "more_edges": more})
}

func (s *Server) graphNeighbours(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, lim, when, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	ref, ok := graphRef(w, r, "ref")
	if !ok {
		return
	}
	dir := graph.Downstream
	switch r.URL.Query().Get("direction") {
	case "", "downstream":
	case "upstream":
		dir = graph.Upstream
	default:
		writeErr(w, http.StatusBadRequest, "direction is downstream (what a change here reaches) or upstream (what reaches here)")
		return
	}
	if lim.Depth == 0 {
		lim.Depth = 1
	}
	out, err := s.Graph.Store.Neighbours(r.Context(), v, ref, dir, lim, when)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	out.Fill()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) graphPath(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, lim, when, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	from, ok := graphRef(w, r, "from")
	if !ok {
		return
	}
	to, ok := graphRef(w, r, "to")
	if !ok {
		return
	}
	out, err := s.Graph.Store.Path(r.Context(), v, from, to, lim, when)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) graphImpact(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, lim, when, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	ref, ok := graphRef(w, r, "ref")
	if !ok {
		return
	}
	out, err := s.Graph.Store.Impact(r.Context(), v, ref, lim, when)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	out.Fill()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) graphWhere(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, lim, when, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	ref, ok := graphRef(w, r, "ref")
	if !ok {
		return
	}
	out, err := s.Graph.Store.Where(r.Context(), v, ref, lim, when)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) graphContents(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, lim, when, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	ref, ok := graphRef(w, r, "ref")
	if !ok {
		return
	}
	out, err := s.Graph.Store.Contents(r.Context(), v, ref, lim, when)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) graphOrder(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, lim, when, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	q := graph.OrderQuery{Repositories: r.URL.Query()["repository"], Upstream: r.URL.Query().Get("upstream") == "true", Limits: lim, When: when}
	out, err := s.Graph.Store.Order(r.Context(), v, q)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	out.Fill()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) graphRebuildSet(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, lim, when, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	repo := r.URL.Query().Get("repository")
	if repo == "" {
		writeErr(w, http.StatusBadRequest, "name the repository that was pushed (repository=owner/name)")
		return
	}
	out, err := s.Graph.Store.Rebuild(r.Context(), v, repo, lim, when)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	out.Fill()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) graphOverrides(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	list, err := s.Graph.Store.Overrides(r.Context(), graphViewer{s, identityFrom(r)})
	if err != nil {
		s.failGraph(w, err)
		return
	}
	if list == nil {
		list = []graph.Override{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) graphAddOverride(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	var in graph.Override
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	// Changing how repositories are ordered is a change to the way they are built:
	// it needs the operator role on both repositories, not just the right to read them.
	for _, p := range []string{in.From, in.To} {
		if p != "" && s.authorize(id, auth.Read, p) && !s.authorize(id, auth.Operate, p) {
			writeErr(w, http.StatusForbidden, "you need the operator role on "+p+" to change how it is ordered")
			return
		}
	}
	in.Author = id.User
	out, err := s.Graph.Store.AddOverride(r.Context(), graphViewer{s, id}, in)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	setAudit(r, in.From+" "+in.To, in.Kind+": "+in.Reason)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) graphRemoveOverride(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "the override's number is a whole number")
		return
	}
	id := identityFrom(r)
	v := graphViewer{s, id}
	list, _ := s.Graph.Store.Overrides(r.Context(), v)
	for _, o := range list {
		if o.ID == n {
			for _, p := range []string{o.From, o.To} {
				if !s.authorize(id, auth.Operate, p) {
					writeErr(w, http.StatusForbidden, "you need the operator role on "+p+" to take this override away")
					return
				}
			}
		}
	}
	if err := s.Graph.Store.RemoveOverride(r.Context(), v, n); err != nil {
		s.failGraph(w, err)
		return
	}
	setAudit(r, strconv.FormatInt(n, 10), "removed")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) graphNeeds(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, _, when, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	ref, ok := graphRef(w, r, "ref")
	if !ok {
		return
	}
	out, err := s.Graph.Store.Needs(r.Context(), v, ref, when)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	if out == nil {
		out = []graph.ComponentNeeds{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "file": graph.RequirementsPath})
}

func (s *Server) graphPlacement(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, _, when, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	ref, ok := graphRef(w, r, "ref")
	if !ok {
		return
	}
	out, err := s.Graph.Store.Placement(r.Context(), v, ref, when)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	if out == nil {
		out = []graph.PlacementResult{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) graphSpecification(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, _, when, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	var refs []graph.NodeRef
	for _, x := range r.URL.Query()["ref"] {
		ref, err := graph.ParseRef(x)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		writeErr(w, http.StatusBadRequest, "name at least one component or repository (ref=repository:team/app); repeat ref for several that run together")
		return
	}
	head, _ := strconv.Atoi(r.URL.Query().Get("headroom"))
	spec, err := s.Graph.Store.Specification(r.Context(), v, refs, graph.SpecOptions{HeadroomPercent: head}, when)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	if r.URL.Query().Get("format") == "yaml" {
		y, err := spec.YAML()
		if err != nil {
			s.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte(y)) //nolint:gosec // G705: YAML written from structured data, served as application/yaml and never as a page
		return
	}
	writeJSON(w, http.StatusOK, spec)
}

func (s *Server) graphReach(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	v, lim, when, ok := s.graphCtx(w, r)
	if !ok {
		return
	}
	ref, ok := graphRef(w, r, "ref")
	if !ok {
		return
	}
	out, err := s.Graph.Store.Reach(r.Context(), v, ref, s.Graph.Advisories, lim, when)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	if out.Versions == nil {
		out.Versions = []graph.ReachVersion{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) graphRescan(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	if s.Graph.Runner == nil {
		writeErr(w, http.StatusConflict, "this server has no sources to read the graph from")
		return
	}
	res, err := s.Graph.Runner.Rescan(r.Context())
	if err != nil {
		s.failGraph(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scopes": len(res), "results": res})
}

func (s *Server) graphRebuild(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	if s.Graph.Runner == nil {
		writeErr(w, http.StatusConflict, "this server has no sources to read the graph from")
		return
	}
	res, err := s.Graph.Runner.Rebuild(r.Context())
	if err != nil {
		s.failGraph(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scopes": len(res), "results": res})
}

func (s *Server) graphRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	if s.Graph.Runner == nil {
		writeErr(w, http.StatusConflict, "this server has no sources to read the graph from")
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	res, err := s.Graph.Runner.PushedRepository(r.Context(), repo.Path)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	setAudit(r, repo.Path, "graph refreshed")
	writeJSON(w, http.StatusOK, res)
}

// graphProposeRequirements opens a pull request that makes the requirements
// file what the body says. Launchway advises; a person reviews and merges.
func (s *Server) graphProposeRequirements(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	if s.Forge == nil || s.Forge.Pulls == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "opening a pull request is not available on this server", "code": "feature_off",
			"next": "Turn Git hosting and pull requests on; or copy the file below into " + graph.RequirementsPath + " yourself."})
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Content string `json:"content"`
		Title   string `json:"title"`
	}
	if !decode(w, r, &in) {
		return
	}
	if _, err := graph.ParseRequirements([]byte(in.Content)); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error(), "code": "invalid", "next": "Fix the file and send it again; nothing was opened."})
		return
	}
	if in.Title == "" {
		in.Title = "Update " + graph.RequirementsPath
	}
	pr, err := s.proposeRequirements(r.Context(), repo, identityFrom(r).User, in.Title, []byte(in.Content))
	if err != nil {
		switch {
		case errors.Is(err, files.ErrConflict):
			writeErr(w, http.StatusConflict, err.Error())
		case files.IsStale(err):
			writeErr(w, http.StatusConflict, "the repository changed while the pull request was made; try again")
		default:
			s.failForge(w, err)
		}
		return
	}
	setAudit(r, repo.Path, "requirements pull request "+strconv.Itoa(pr.Number))
	writeJSON(w, http.StatusCreated, map[string]any{"number": pr.Number, "title": pr.Title, "branch": pr.SourceBranch, "repository": repo.Path})
}

// graphPushed tells the graph that a repository's default branch moved.
func (s *Server) graphPushed(ctx context.Context, repo forge.Repository, updates []githttp.RefUpdate) {
	if s.Graph == nil || s.Graph.Watch == nil {
		return
	}
	for _, u := range updates {
		if u.Name == "refs/heads/"+repo.DefaultBranch || repo.DefaultBranch == "" {
			s.Graph.Watch.Pushed(ctx, repo.Path)
			return
		}
	}
}

// proposeRequirements makes a branch from the default branch, puts the file on
// it in one commit, and opens a pull request. Nothing lands until a person
// merges it.
func (s *Server) proposeRequirements(ctx context.Context, repo forge.Repository, author, title string, content []byte) (pulls.PullRequest, error) {
	if repo.DefaultBranch == "" {
		return pulls.PullRequest{}, fmt.Errorf("%w: this repository has no default branch yet: push one first", forge.ErrInvalid)
	}
	st := s.Forge.Pulls.Git
	branch := fmt.Sprintf("launchway/requirements-%d", time.Now().UnixMilli())
	if _, err := st.Mutate(ctx, repo.ID, func(_ context.Context, refs map[string]string, _ string) ([]storage.RefUpdate, error) {
		tip := refs["refs/heads/"+repo.DefaultBranch]
		if tip == "" {
			return nil, fmt.Errorf("%w: the default branch %s does not exist", forge.ErrInvalid, repo.DefaultBranch)
		}
		return []storage.RefUpdate{{Name: "refs/heads/" + branch, New: tip}}, nil
	}); err != nil {
		return pulls.PullRequest{}, err
	}
	fs := &files.Service{Git: st}
	expect := ""
	if cur, err := fs.Read(ctx, repo.ID, repo.DefaultBranch, graph.RequirementsPath); err == nil && cur.Type == "file" {
		expect = cur.SHA
	}
	if _, err := fs.Put(ctx, repo.ID, files.Write{Branch: branch, Path: graph.RequirementsPath, Content: content, Message: title, ExpectedSHA: expect, Actor: author}, nil); err != nil {
		return pulls.PullRequest{}, err
	}
	body := "This changes " + graph.RequirementsPath + ", which says what the software in this repository needs (architectures, CPU, memory, storage, devices, operating-system features, ports and data services).\n\n" +
		"What is written in the file counts; what Launchway inferred is only advice and is never applied without a change like this one. Opened from the graph page of the repository."
	return s.Forge.Pulls.Open(ctx, repo.ID, author, pulls.New{Title: title, Body: body, SourceBranch: branch, TargetBranch: repo.DefaultBranch})
}
