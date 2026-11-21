package api

// The MCP tools for the integrated software graph: what depends on what,
// what is built from what, what runs where and what it needs. Each calls the
// same route as the API for the caller's own token, so an answer holds only
// what that caller may read, and a change is audited.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// graphIn is what every question of the graph takes: where to look, and how far.
type graphIn struct {
	Ref        string   `json:"ref,omitempty" jsonschema:"the node, as kind:key, for example repository:team/app, package:go:example.com/lib, image:registry/app or deployment:prod/shop/api"`
	From       string   `json:"from,omitempty" jsonschema:"for a path: where it starts (kind:key)"`
	To         string   `json:"to,omitempty" jsonschema:"for a path: where it ends (kind:key)"`
	Refs       []string `json:"refs,omitempty" jsonschema:"for a specification: components or repositories (kind:key) that run on one machine"`
	Query      string   `json:"query,omitempty" jsonschema:"text to find in names"`
	Kind       string   `json:"kind,omitempty" jsonschema:"only nodes of this kind"`
	Direction  string   `json:"direction,omitempty" jsonschema:"downstream (what a change here reaches, the default) or upstream (what reaches here)"`
	Repository string   `json:"repository,omitempty" jsonschema:"a repository, owner/name"`
	Repos      []string `json:"repositories,omitempty" jsonschema:"repositories to order, owner/name; empty means every repository with a dependency"`
	Upstream   bool     `json:"include_upstream,omitempty" jsonschema:"for an order: also include what the repositories depend on"`
	Depth      int      `json:"depth,omitempty" jsonschema:"follow at most this many steps (default 8, at most 64)"`
	Nodes      int      `json:"nodes,omitempty" jsonschema:"return at most this many nodes (default 2000, at most 50000)"`
	AsOf       string   `json:"as_of,omitempty" jsonschema:"answer as of a time, RFC 3339"`
	Commit     string   `json:"commit,omitempty" jsonschema:"answer as of the graph's reading of this commit of repository"`
	Headroom   int      `json:"headroom_percent,omitempty" jsonschema:"for a specification: percent added to CPU, memory and storage (default 25)"`
}

func (in graphIn) values() url.Values {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("ref", in.Ref)
	set("from", in.From)
	set("to", in.To)
	set("q", in.Query)
	set("kind", in.Kind)
	set("direction", in.Direction)
	set("as_of", in.AsOf)
	set("commit", in.Commit)
	if in.Commit != "" {
		set("repository", in.Repository)
	}
	if in.Depth > 0 {
		q.Set("depth", strconv.Itoa(in.Depth))
	}
	if in.Nodes > 0 {
		q.Set("nodes", strconv.Itoa(in.Nodes))
	}
	if in.Headroom > 0 {
		q.Set("headroom", strconv.Itoa(in.Headroom))
	}
	for _, r := range in.Refs {
		q.Add("ref", r)
	}
	for _, r := range in.Repos {
		q.Add("repository", r)
	}
	if in.Upstream {
		q.Set("upstream", "true")
	}
	return q
}

type graphRead struct {
	tool, title, path, desc string
}

var graphReads = []graphRead{
	{"graph_summary", "Software graph summary", "", "What the software graph holds that this token may read (nodes by kind, edges) and when each source last read."},
	{"search_graph", "Search the software graph", "/search", "Find nodes by part of a name or key (query), optionally of one kind. Only nodes this token may read are found."},
	{"get_graph_node", "Get a graph node", "/node", "One node (ref) and the edges directly connected to it, each with the version constraint and resolved version, where the fact came from (source, commit) and how sure it is (declared, extracted or inferred)."},
	{"graph_impact", "What a change affects", "/impact", "What is affected if a repository, package, component, base image, machine or platform (ref) changes: the downstream closure with the way to each thing (why), grouped by owner, with the deployments that run affected digests. Bounded by depth and nodes; truncated says when it is not the whole answer."},
	{"graph_where_running", "Where is this running", "/where", "Where a repository, package, commit or image (ref) is running: the deployments it reaches, their Airfields, digests and versions, and whether the digest itself contains it (exact) or it is reached through a dependency."},
	{"graph_contents", "What is in this", "/contents", "What is in a deployment, image or component (ref): the images, base images, packages with the versions resolved, commits and repositories behind it."},
	{"graph_path", "Path between two nodes", "/path", "How two nodes are connected (from, to): the shortest path with each edge, going with a change (affects) or against it (affected-by). No path is an empty answer, and a path through something this token may not read is not shown."},
	{"graph_build_order", "Build order of repositories", "/order", "The order repositories must be built in, providers before consumers, in levels that can be built together. Dependencies between repositories hosted here are found from manifests (a module path or package name resolved to the repository that publishes it) and from base images another repository builds. Cycles are returned with the path; the user's overrides are listed and marked."},
	{"graph_rebuild_set", "What must rebuild after a push", "/rebuild-set", "What must be built again after a push to repository, in build order, with the way each depends on it. Dependencies a person pinned or ignored are not rebuilt and are listed as skipped."},
	{"list_graph_overrides", "List build-order overrides", "/overrides", "The corrections people made to the build order (before, ignore, pin), each with its reason and author."},
	{"graph_requirements", "What a component needs", "/needs", "What a component (or each component of a repository) needs: architectures, CPU, memory, storage, devices, operating-system features, ports, data services, with each fact marked declared (from .launchway/requirements.yaml) or inferred (with the evidence), and advice where an inferred fact differs from a declared one."},
	{"graph_placement", "Where can it run", "/placement", "Which machines and platforms in the graph satisfy a component's needs, near misses with what is missing, and gaps (needs nothing satisfies, or satisfies together)."},
	{"graph_machine_specification", "Machine specification", "/specification", "A neutral machine specification for components (refs) that run together: architecture, CPU, memory and storage totals with headroom, devices, operating-system features, ports, data services, with provenance and conflicts. An export only; nothing is provisioned."},
	{"graph_vulnerability_reach", "Vulnerable versions and where they run", "/reach", "For a package (ref), the versions in use that have known advisories, what uses each and which deployments run them."},
}

func addGraphTools(srv *mcp.Server, c *mcpCall) {
	for _, g := range graphReads {
		addTool(srv, c, &mcp.Tool{Name: g.tool, Annotations: readOnly(g.title), Description: g.desc}, mcpCallTimeout,
			func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in graphIn) (map[string]any, error) {
				out := map[string]any{}
				path := "/api/v1/graph" + g.path
				if q := in.values(); len(q) > 0 {
					path += "?" + q.Encode()
				}
				err := c.apiJSON(ctx, g.tool, http.MethodGet, path, nil, &out)
				return out, err
			})
	}
}

type graphOverrideIn struct {
	Kind   string `json:"kind" jsonschema:"before (from is built before to), ignore (from does not depend on to) or pin (from stays at value of to)"`
	From   string `json:"from" jsonschema:"a repository, owner/name"`
	To     string `json:"to" jsonschema:"a repository, owner/name"`
	Value  string `json:"value,omitempty" jsonschema:"for a pin, the version"`
	Reason string `json:"reason" jsonschema:"why; required"`
}

type graphOverrideRemoveIn struct {
	ID int64 `json:"id" jsonschema:"the override's number, from list_graph_overrides"`
}

type graphRepoIn struct {
	Repository string `json:"repository" jsonschema:"the repository, owner/name"`
}

type graphProposeIn struct {
	Repository string `json:"repository" jsonschema:"the repository, owner/name"`
	Content    string `json:"content" jsonschema:"the whole .launchway/requirements.yaml"`
	Title      string `json:"title,omitempty" jsonschema:"the pull request's title"`
}

func addGraphChangeTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "add_graph_override", Annotations: changes("Override the build order", false, false),
		Description: "Correct the build order of two repositories: before (from is built before to), ignore (from does not really depend on to: use it to break a cycle or a false dependency) or pin (from stays at a version of to, so a push to to does not rebuild it). A reason is required. Needs the operator role on both repositories. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in graphOverrideIn) (map[string]any, error) {
			out := map[string]any{}
			err := c.apiJSON(ctx, "add_graph_override", http.MethodPost, "/api/v1/graph/overrides", in, &out)
			return out, err
		})
	addTool(srv, c, &mcp.Tool{Name: "remove_graph_override", Annotations: changes("Remove a build-order override", true, true),
		Description: "Take a build-order override away (the row is kept, marked removed). Needs the operator role on both repositories. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in graphOverrideRemoveIn) (map[string]any, error) {
			out := map[string]any{}
			err := c.apiJSON(ctx, "remove_graph_override", http.MethodDelete, "/api/v1/graph/overrides/"+strconv.FormatInt(in.ID, 10), nil, &out)
			return out, err
		})
	addTool(srv, c, &mcp.Tool{Name: "propose_requirements_file", Annotations: changes("Propose a requirements file", false, false),
		Description: "Open a pull request that makes .launchway/requirements.yaml in a hosted repository what content says. The file is checked first and nothing is opened if it is wrong. It lands only when a person merges it. Needs the operator role on the repository. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in graphProposeIn) (map[string]any, error) {
			out := map[string]any{}
			owner, name, err := splitRepo(in.Repository)
			if err != nil {
				return out, err
			}
			err = c.apiJSON(ctx, "propose_requirements_file", http.MethodPost, apiPath("/api/v1/repositories/%s/%s", owner, name)+"/requirements/proposal", map[string]string{"content": in.Content, "title": in.Title}, &out)
			return out, err
		})
	addTool(srv, c, &mcp.Tool{Name: "refresh_graph", Annotations: changes("Read a repository into the graph again", false, true),
		Description: "Read one hosted repository again into the software graph (needs the operator role on it). Pushes do this by themselves; use it after changing something the graph reads from elsewhere. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in graphRepoIn) (map[string]any, error) {
			out := map[string]any{}
			owner, name, err := splitRepo(in.Repository)
			if err != nil {
				return out, err
			}
			err = c.apiJSON(ctx, "refresh_graph", http.MethodPost, apiPath("/api/v1/repositories/%s/%s", owner, name)+"/graph/refresh", nil, &out)
			return out, err
		})
	for _, t := range []struct{ name, path, title, desc string }{
		{"rescan_graph", "/api/v1/graph/rescan", "Read everything into the graph again", "Administrators only. Read every repository, project and the installation again into the software graph. Nothing is removed first. Audited."},
		{"rebuild_graph", "/api/v1/graph/rebuild", "Rebuild the graph from scratch", "Administrators only. Clear the software graph and read everything again; the result equals what incremental updates built. The build-order overrides are kept. Audited."},
	} {
		addTool(srv, c, &mcp.Tool{Name: t.name, Annotations: changes(t.title, t.name == "rebuild_graph", true), Description: t.desc}, 10*time.Minute,
			func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ struct{}) (map[string]any, error) {
				out := map[string]any{}
				err := c.apiJSON(ctx, t.name, http.MethodPost, t.path, nil, &out)
				return out, err
			})
	}
}
