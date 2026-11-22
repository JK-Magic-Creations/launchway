package api

// Work tracking (internal/work): the store, who is calling, what they may
// reach, and how the store's errors become answers. The routes are in
// work_items.go, work_plan.go, work_agents.go and work_import.go.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/webhooks"
	"github.com/jk-magic-creations/launchway/internal/work"
)

type workTxDB struct{ *engine.Database }

func (d workTxDB) BeginTx(ctx context.Context) (work.Tx, error) { return d.Begin(ctx) }

var workOnce sync.Map // *Server -> *work.Store

// work is the server's work store, made on first use.
func (s *Server) work() *work.Store {
	if v, ok := workOnce.Load(s); ok {
		return v.(*work.Store)
	}
	st := work.New(workTxDB{s.Engine.Database()})
	st.SetChains(s.workChain)
	st.SetHook(s.workHook)
	v, _ := workOnce.LoadOrStore(s, st)
	return v.(*work.Store)
}

// workChain gives the levels that apply to a scope, nearest first: the project,
// its Squadron, the Wing, Base.
func (s *Server) workChain(sc work.Scope) []work.Scope {
	chain := []work.Scope{sc}
	switch sc.Kind {
	case work.ScopeProject:
		if s.Settings != nil {
			if lay, err := s.Settings.Levels(); err == nil && lay != nil {
				if u := lay.ProjectUnit[sc.ID]; u != "" {
					chain = append(chain, work.Scope{Kind: work.ScopeUnit, ID: u})
				}
			}
		}
		chain = append(chain, work.Scope{Kind: work.ScopeOrganization})
	case work.ScopeUnit:
		chain = append(chain, work.Scope{Kind: work.ScopeOrganization})
	}
	if sc.Kind != work.ScopeInstallation {
		chain = append(chain, work.Scope{Kind: work.ScopeInstallation})
	}
	return chain
}

// workCaller works out who is calling and what they may reach.
func (s *Server) workCaller(r *http.Request) work.Caller {
	id := identityFrom(r)
	via := "api"
	if _, ok := r.Context().Value(mcpIdentityKey).(*identity); ok || r.Header.Get("X-Launchway-Via") == "mcp" {
		via = "mcp"
	} else if v := r.Header.Get("X-Launchway-Via"); v == "cli" || v == "ui" {
		via = v
	}
	c := work.Caller{Actor: work.Actor{Name: id.User, Kind: work.KindPerson, Via: via}}
	if id.agent != nil {
		a := id.agent
		c.Actor = work.Actor{Name: a.Name, Kind: work.KindAgent, Via: via}
		c.Agent = a
		cover := func(sc work.Scope) bool { return s.work().AgentCovers(a, sc) }
		c.Read, c.Write = cover, cover
		c.Maintain = func(work.Scope) bool { return false }
		return c
	}
	c.Read = func(sc work.Scope) bool { return s.workAllows(id, sc, auth.Read) }
	c.Write = func(sc work.Scope) bool { return s.workAllows(id, sc, auth.Operate) }
	c.Maintain = func(sc work.Scope) bool { return s.workMaintains(id, sc) }
	return c
}

// workAllows applies a project-level action to a work scope. A Squadron's
// work is open to those who may act on any of its projects; the Wing's and
// Base's to those who may act on any project.
func (s *Server) workAllows(id *identity, sc work.Scope, a auth.Action) bool {
	if s.Auth == nil {
		return true
	}
	switch sc.Kind {
	case work.ScopeProject:
		return s.authorize(id, a, sc.ID)
	case work.ScopeUnit:
		for _, p := range s.unitProjects(sc.ID) {
			if s.authorize(id, a, p) {
				return true
			}
		}
		return false
	}
	known, err := s.knownProjects()
	if err != nil {
		return false
	}
	for _, p := range known {
		if s.authorize(id, a, p) {
			return true
		}
	}
	return false
}

// workMaintains says whether the caller maintains a scope: admin of the
// project, of every project of the Squadron, or of every project (Wing, Base).
func (s *Server) workMaintains(id *identity, sc work.Scope) bool {
	if s.Auth == nil {
		return true
	}
	switch sc.Kind {
	case work.ScopeProject:
		return s.authorize(id, auth.AdministerProject, sc.ID)
	case work.ScopeUnit:
		ps := s.unitProjects(sc.ID)
		return len(ps) > 0 && !slices.ContainsFunc(ps, func(p string) bool { return !s.authorize(id, auth.AdministerProject, p) })
	}
	return s.authorize(id, auth.Administer, "")
}

// parseScope reads a scope written project:owner/name, unit:name, organization
// or installation (a bare owner/name is a project).
func parseWorkScope(v string) (work.Scope, error) {
	if v == "" {
		return work.Scope{}, errors.New("say the scope: Flight:owner/name, unit:name, organization or installation")
	}
	kind, id, ok := strings.Cut(v, ":")
	if !ok {
		if strings.Contains(v, "/") {
			return work.Scope{Kind: work.ScopeProject, ID: v}, nil
		}
		kind, id = v, ""
	}
	sc := work.Scope{Kind: kind, ID: id}
	return sc, sc.Validate()
}

func (s *Server) scopeParam(w http.ResponseWriter, r *http.Request) (work.Scope, bool) {
	sc, err := parseWorkScope(r.URL.Query().Get("scope"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return sc, false
	}
	return sc, true
}

// failWork maps the store's errors onto answers.
func (s *Server) failWork(w http.ResponseWriter, err error) {
	msg := err.Error()
	for _, p := range []string{"not found: ", "invalid: ", "conflict: ", "forbidden: "} {
		msg = strings.TrimPrefix(msg, p)
	}
	switch {
	case errors.Is(err, work.ErrNotFound):
		writeErr(w, http.StatusNotFound, msg)
	case errors.Is(err, work.ErrInvalid):
		writeErr(w, http.StatusBadRequest, msg)
	case errors.Is(err, work.ErrConflict):
		writeErr(w, http.StatusConflict, msg)
	case errors.Is(err, work.ErrForbidden):
		writeErr(w, http.StatusForbidden, msg)
	default:
		s.fail(w, err)
	}
}

// workHook turns what the store reports into events a receiver may be signed
// for: the project's webhooks, when the project is a hosted repository.
func (s *Server) workHook(ctx context.Context, e work.Event) {
	if s.Forge == nil || s.Forge.Hooks == nil || e.Data == nil {
		return
	}
	ref, _ := e.Data["ref"].(string)
	if ref == "" {
		return
	}
	it, err := s.work().Get(ctx, work.System("system"), ref)
	if err != nil || it.Scope.Kind != work.ScopeProject {
		return
	}
	repo, err := s.Forge.Provider.Repositories().Get(ctx, it.Scope.ID)
	if err != nil {
		return
	}
	data := map[string]any{"kind": e.Kind, "ref": ref, "title": it.Title, "state": it.State}
	for k, v := range e.Data {
		data[k] = v
	}
	s.Forge.Hooks.Emit(ctx, repo.ID, repo.Home, webhooks.EventWorkItem, webhooks.Payload{Repository: s.hookRepo(repo), Sender: webhooks.Sender{Name: "launchway"}, Action: e.Kind, Work: data})
}
