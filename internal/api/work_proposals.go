package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/files"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/work"
)

// Proposals: analyzers send findings, the server groups and caps them, a
// person reviews them (or the level lets a source file without one), and a
// re-run keeps the same items in line. The built-in repository health check is
// the first proposer; the security findings and code-quality checks send to the
// same route.

func (s *Server) workProposalRoutes() []route {
	const p = "/api/v1/work/proposals"
	return []route{
		{"GET " + p, signedIn, "", s.proposalList},
		{"POST " + p, signedIn, "work.propose", s.proposalPropose},
		{"POST " + p + "/accept", signedIn, "work.proposal_accept", s.proposalAccept},
		{"POST " + p + "/dismiss", signedIn, "work.proposal_dismiss", s.proposalDismiss},
		{"POST " + p + "/merge", signedIn, "work.proposal_merge", s.proposalMerge},
		{"POST /api/v1/work/health", signedIn, "work.health", s.workHealthRun},
	}
}

func (s *Server) proposalList(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeParam(w, r)
	if !ok {
		return
	}
	gs, err := s.work().Proposals(r.Context(), s.workCaller(r), sc, r.URL.Query().Get("state"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": gs})
}

func (s *Server) proposalPropose(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope string `json:"scope"`
		work.Batch
	}
	if !decode(w, r, &in) {
		return
	}
	sc, err := parseWorkScope(in.Scope)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.work().Propose(r.Context(), s.workCaller(r), sc, in.Batch)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, sc.String(), in.Source+": "+strings.TrimSpace(summarize(res)))
	writeJSON(w, http.StatusOK, res)
}

func summarize(r *work.ProposeResult) string {
	return strings.Join([]string{count(r.New, "new"), count(r.Updated, "known"), count(r.Fixed, "fixed"), count(r.Opened, "items opened"), count(r.Waiting, "groups waiting")}, ", ")
}

func count(n int, what string) string { return strings.TrimSpace(strconv.Itoa(n) + " " + what) }

type reviewBodyIn struct {
	Scope  string                    `json:"scope"`
	Source string                    `json:"source"`
	Groups []string                  `json:"groups"`
	Into   string                    `json:"into"`
	Reason string                    `json:"reason"`
	Edits  map[string]work.GroupEdit `json:"edits"`
}

func (s *Server) reviewIn(w http.ResponseWriter, r *http.Request) (reviewBodyIn, work.Scope, bool) {
	var in reviewBodyIn
	if !decode(w, r, &in) {
		return in, work.Scope{}, false
	}
	sc, err := parseWorkScope(in.Scope)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return in, sc, false
	}
	return in, sc, true
}

func (s *Server) proposalAccept(w http.ResponseWriter, r *http.Request) {
	in, sc, ok := s.reviewIn(w, r)
	if !ok {
		return
	}
	res, err := s.work().AcceptProposals(r.Context(), s.workCaller(r), sc, in.Source, in.Groups, in.Edits)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, sc.String(), "accepted "+strconv.Itoa(res.Opened)+" items")
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) proposalDismiss(w http.ResponseWriter, r *http.Request) {
	in, sc, ok := s.reviewIn(w, r)
	if !ok {
		return
	}
	n, err := s.work().DismissProposals(r.Context(), s.workCaller(r), sc, in.Source, in.Groups, in.Reason)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, sc.String(), "dismissed "+strconv.Itoa(n)+" findings: "+in.Reason)
	writeJSON(w, http.StatusOK, map[string]any{"dismissed": n})
}

func (s *Server) proposalMerge(w http.ResponseWriter, r *http.Request) {
	in, sc, ok := s.reviewIn(w, r)
	if !ok {
		return
	}
	if err := s.work().MergeProposals(r.Context(), s.workCaller(r), sc, in.Source, in.Groups, in.Into); err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, sc.String(), "merged groups into "+in.Into)
	writeJSON(w, http.StatusOK, map[string]any{"merged": true})
}

// ---- the repository health proposer ----

func (s *Server) workHealthRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repository string `json:"repository"`
	}
	if !decode(w, r, &in) || !s.forgeOn(w) {
		return
	}
	repo, err := s.Forge.Provider.Repositories().Get(r.Context(), in.Repository)
	if err != nil {
		s.failForge(w, err)
		return
	}
	res, err := s.runHealth(r.Context(), s.workCaller(r), repo)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, repo.Path, "repository health: "+summarize(res))
	writeJSON(w, http.StatusOK, res)
}

// runHealth looks at a hosted repository and proposes what is missing, as
// one group: the checklist of things to set up.
func (s *Server) runHealth(ctx context.Context, c work.Caller, repo forge.Repository) (*work.ProposeResult, error) {
	fs := s.filesService()
	if fs == nil {
		return nil, work.ErrNotFound
	}
	root, err := fs.Read(ctx, repo.ID, repo.DefaultBranch, "")
	if err != nil {
		return nil, err
	}
	has := func(prefixes ...string) bool {
		for _, e := range root.Entries {
			for _, p := range prefixes {
				if strings.HasPrefix(strings.ToUpper(e.Name), p) {
					return true
				}
			}
		}
		return false
	}
	exists := func(p string) bool { _, err := fs.Read(ctx, repo.ID, repo.DefaultBranch, p); return err == nil }
	var gaps []work.Proposal
	add := func(key, title, why string, sev int) {
		gaps = append(gaps, work.Proposal{DedupeKey: key, GroupKey: "repository-hygiene", GroupTitle: "Set up the repository basics ({n} left)", Title: title, Body: why, Severity: sev, Labels: []string{"hygiene"}, Type: work.TypeTask})
	}
	if !has("README") {
		add("no-readme", "Add a README that says what this is and how to run it", "A new reader has nowhere to start.", 1)
	}
	if !has("LICENSE", "LICENCE", "COPYING") {
		add("no-license", "Add a license", "Without one, nobody may legally use or contribute to the code.", 2)
	}
	if !has("CODEOWNERS", "OWNERS") && !exists(".github/CODEOWNERS") && !exists("docs/CODEOWNERS") {
		add("no-owners", "Add an owners file so reviews reach the right people", "", 1)
	}
	if !exists(".launchway/pipeline.yaml") {
		add("no-pipeline", "Add a pipeline file (.launchway/pipeline.yaml) so changes are checked", "No pipeline file was found, so nothing checks a change.", 2)
	}
	if rules, err := s.Forge.Pulls.Store.Rules(ctx, repo.ID); err == nil {
		if _, covered := pulls.RuleFor(rules, repo.DefaultBranch); !covered {
			add("unprotected-default-branch", "Protect the default branch ("+repo.DefaultBranch+") with a merge rule", "Anyone with push access can change it without a review or a passing check.", 3)
		}
	}
	if !s.detectTests(ctx, repo, root.Entries) {
		add("no-tests", "Add tests, or say where they are", "No test files were found in the places they usually are.", 2)
	}
	return s.work().Propose(ctx, c, work.Scope{Kind: work.ScopeProject, ID: repo.Path}, work.Batch{Source: "repository-health", RunID: time.Now().UTC().Format(time.RFC3339), Complete: true, Proposals: gaps})
}

// detectTests looks for test files in the root and in the usual directories.
func (s *Server) detectTests(ctx context.Context, repo forge.Repository, root []files.Entry) bool {
	fs := s.filesService()
	for _, e := range root {
		if work.IsTestPath(e.Path) || (e.Type == "dir" && work.IsTestPath(e.Path+"/x")) {
			return true
		}
	}
	for _, d := range []string{"src", "internal", "pkg", "lib", "app", "cmd", "web", "test", "tests", "spec"} {
		for _, e := range root {
			if e.Type != "dir" || e.Name != d {
				continue
			}
			it, err := fs.Read(ctx, repo.ID, repo.DefaultBranch, d)
			if err != nil {
				continue
			}
			for _, x := range it.Entries {
				if work.IsTestPath(x.Path) || (x.Type == "dir" && work.IsTestPath(x.Path+"/x")) {
					return true
				}
			}
		}
	}
	return false
}

// healthAfterImport runs the health check once an imported repository has its
// first commits: the copy runs in the background, so it waits for them (a few
// minutes at most) and then looks once.
func (s *Server) healthAfterImport(parent context.Context, repoPath string) {
	go func() {
		stop := s.stopping()
		deadline := time.Now().Add(15 * time.Minute)
		for time.Now().Before(deadline) {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Second):
			}
			ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Minute)
			repo, err := s.Forge.Provider.Repositories().Get(ctx, repoPath)
			if err == nil {
				if _, err = s.runHealth(ctx, work.System("import"), repo); err == nil {
					cancel()
					return
				}
			}
			cancel()
		}
	}()
}
