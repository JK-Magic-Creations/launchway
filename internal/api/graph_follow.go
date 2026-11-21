package api

// The software graph's answer to "what must rebuild after this push", turned into result triggers.
//
// A push to a library repository runs that library's pipeline. The projects that build the repositories which
// depend on it (the graph knows, from manifests, container files and the requirements file) can start when that
// run ends well, and so on down the dependencies: each project's result starts the next level, so the order is
// the graph's order, providers before consumers. This is a set of ordinary result triggers (run_triggers), not
// a second mechanism: the downstream project makes them, the upstream project must have allowed the downstream
// one, a cycle is refused, and nothing crosses but the result by reference. It is off by default: nothing is made
// until an operator of the downstream project asks for it here. Whatever the graph cannot see is not followed.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/graph"
)

func (s *Server) graphFollowRoutes() []route {
	const p = "/api/v1/projects/{name}/run-triggers/graph"
	return []route{
		{"GET " + p, projectRead, "", s.graphFollowPlan},
		{"POST " + p, projectOperate, "run_trigger.from_graph", s.graphFollowCreate},
	}
}

// FollowItem is one project the graph says this one depends on.
type FollowItem struct {
	UpstreamProject    string   `json:"upstream_project"`
	UpstreamRepository string   `json:"upstream_repository"`
	Level              int      `json:"level"` // 0 is built first; what each level builds is what the next one waits for
	Via                []string `json:"via,omitempty"`
	Allowed            bool     `json:"allowed"`           // the upstream project has allowed this one to trigger on its results
	Trigger            string   `json:"trigger,omitempty"` // the name of a trigger on that project that already exists
	Created            bool     `json:"created,omitempty"`
	Note               string   `json:"note"`
}

// FollowPlan is what following the graph would do for a project.
type FollowPlan struct {
	Project    string       `json:"project"`
	Repository string       `json:"repository,omitempty"`
	Items      []FollowItem `json:"items"`
	Cycles     []string     `json:"cycles,omitempty"`
	Note       string       `json:"note,omitempty"`
}

// followPlan works out which projects the graph says project depends on, whether each allows it and whether a
// trigger on it exists. It reads only what the caller may read.
func (s *Server) followPlan(ctx context.Context, id *identity, project string) (FollowPlan, error) {
	plan := FollowPlan{Project: project, Items: []FollowItem{}}
	if s.Graph.ProjectRepos == nil {
		plan.Note = "No project is tied to a repository on this server, so the graph cannot tell what this one depends on."
		return plan, nil
	}
	repos, err := s.Graph.ProjectRepos(ctx)
	if err != nil {
		return plan, err
	}
	repo := repos[project]
	if repo == "" {
		plan.Note = "This project does not build a repository hosted here, so the graph has nothing to say about what it depends on."
		return plan, nil
	}
	plan.Repository = repo
	view := s.projectRepoViewer(id, repos)
	order, err := s.Graph.Store.Order(ctx, view, graph.OrderQuery{Repositories: []string{repo}, Upstream: true})
	if err != nil {
		return plan, err
	}
	level := map[string]int{}
	for i, l := range order.Levels {
		for _, r := range l {
			level[r] = i
		}
	}
	for _, c := range order.Cycles {
		plan.Cycles = append(plan.Cycles, strings.Join(c.Path, " -> "))
	}
	existing, err := s.Engine.RunTriggers(project)
	if err != nil {
		return plan, err
	}
	for _, e := range order.Edges {
		if e.Consumer != repo || e.Override == "ignored" {
			continue
		}
		for up, ur := range repos {
			if ur != e.Provider || up == project {
				continue
			}
			it := FollowItem{UpstreamProject: up, UpstreamRepository: e.Provider, Level: level[e.Provider], Via: e.Via}
			if !s.authorize(id, auth.Read, up) {
				continue // a project the caller may not read is not named
			}
			if _, err := s.Engine.RunTriggerPermission(up, project); err == nil {
				it.Allowed = true
			}
			for _, t := range existing {
				if t.UpstreamProject == up && t.Kind == engine.TriggerOnRun {
					it.Trigger = t.Name
				}
			}
			switch {
			case it.Trigger != "":
				it.Note = "A trigger on it already exists."
			case !it.Allowed:
				it.Note = fmt.Sprintf("%s has not allowed %s to trigger on its results; an operator of %s allows it (launchway run-triggers allow %s %s).", up, project, up, up, project)
			default:
				it.Note = "Would be made: start a run of this project when a run of " + up + " ends well."
			}
			plan.Items = append(plan.Items, it)
		}
	}
	sort.Slice(plan.Items, func(i, j int) bool {
		a, b := plan.Items[i], plan.Items[j]
		if a.Level != b.Level {
			return a.Level < b.Level
		}
		return a.UpstreamProject < b.UpstreamProject
	})
	if len(plan.Items) == 0 {
		plan.Note = "The graph sees nothing this project depends on among the repositories hosted here."
	}
	return plan, nil
}

func (s *Server) graphFollowPlan(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	plan, err := s.followPlan(r.Context(), identityFrom(r), r.PathValue("name"))
	if err != nil {
		s.failGraph(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) graphFollowCreate(w http.ResponseWriter, r *http.Request) {
	if !s.graphOn(w) {
		return
	}
	name := r.PathValue("name")
	setAudit(r, name, "")
	plan, err := s.followPlan(r.Context(), identityFrom(r), name)
	if err != nil {
		s.failGraph(w, err)
		return
	}
	made := 0
	for i := range plan.Items {
		it := &plan.Items[i]
		if it.Trigger != "" || !it.Allowed {
			continue
		}
		t, err := s.Engine.CreateRunTrigger(engine.RunTrigger{Project: name, Name: "graph-" + it.UpstreamProject, Kind: engine.TriggerOnRun,
			UpstreamProject: it.UpstreamProject, When: "succeeded", CreatedBy: identityFrom(r).User})
		if err != nil {
			it.Note = "Not made: " + err.Error()
			continue
		}
		it.Trigger, it.Created, it.Note = t.Name, true, "Made: a run of this project starts when a run of "+it.UpstreamProject+" ends well."
		made++
	}
	setAudit(r, name, fmt.Sprintf("%d result triggers made from the software graph's dependencies of %s", made, plan.Repository))
	writeJSON(w, http.StatusOK, plan)
}

// ---- MCP ----

func addGraphFollowTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "get_graph_run_triggers", Annotations: readOnly("What following the graph would trigger"),
		Description: "For a project: the projects the software graph says its repository depends on, in build order, whether each has allowed this project to trigger on its results, and whether a trigger already exists. Nothing is made."},
		mcpCallTimeout, getGraphRunTriggers)
	if !c.mayOperate {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "follow_graph_with_run_triggers", Annotations: changes("Make result triggers from the software graph", false, false),
		Description: "Make this project start a run when a run of each project it depends on (by the software graph) ends well, for those that have allowed it. Providers build before consumers because each result starts the next level. Nothing is made unless this is called."},
		mcpCallTimeout, followGraphWithRunTriggers)
}

func getGraphRunTriggers(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in runTriggersIn) (FollowPlan, error) {
	var out FollowPlan
	err := c.apiJSON(ctx, "get_graph_run_triggers", http.MethodGet, apiPath("/api/v1/projects/%s/run-triggers/graph", in.Project), nil, &out)
	return out, err
}

func followGraphWithRunTriggers(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in runTriggersIn) (FollowPlan, error) {
	var out FollowPlan
	err := c.apiJSON(ctx, "follow_graph_with_run_triggers", http.MethodPost, apiPath("/api/v1/projects/%s/run-triggers/graph", in.Project), map[string]any{}, &out)
	return out, err
}
