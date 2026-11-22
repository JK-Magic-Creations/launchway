package api

// The MCP tools for forks, template repositories and resolving conflicts. Each
// calls the same route as the HTTP API, so the permission checks, the audit
// lines and the answers are the API's.

import (
	"context"
	"errors"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addForkTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_forks", Annotations: readOnly("List the forks of a repository"),
		Description: "The forks made directly from a repository that you may read: path, owner, visibility, who made it and the commit of the default branch it was made at."},
		mcpCallTimeout, listForksTool)
	addTool(srv, c, &mcp.Tool{Name: "get_fork_status", Annotations: readOnly("A repository's place among forks, and how far a fork is from its parent"),
		Description: "Its parent and root (or that the parent was deleted), the fork point, how many forks it has, whether forks are allowed and why, whether it is a template, and for a fork how many commits its default branch is ahead of and behind its parent's."},
		mcpCallTimeout, forkStatusTool)
	addTool(srv, c, &mcp.Tool{Name: "get_template", Annotations: readOnly("What a template repository asks for"),
		Description: "Whether the repository is a template and, if so, the variables its manifest (.launchway/template.yaml) declares: name, description, the pattern a value must match and the default."},
		mcpCallTimeout, getTemplateTool)
	addTool(srv, c, &mcp.Tool{Name: "list_pull_conflicts", Annotations: readOnly("List the conflicts of a pull request"),
		Description: "What stops the target branch from being merged into the pull request's branch: each conflicting file with its three sides (base, ours = the pull request's branch, theirs = the target), the merged text with markers, the conflicting regions as data, its kind (content, binary, delete_modify, rename, structure, add_add) and the answers it takes. The head and target tips are what a resolution must name. Also says whether you may resolve them and why not."},
		mcpCallTimeout, listPullConflictsTool)
	addTool(srv, c, &mcp.Tool{Name: "fork_repository", Annotations: changes("Fork a repository", false, false),
		Description: "Make a fork of a repository you can read: into your own space, or as an administrator under another owner. A fork of a private repository stays private and needs the repository (or its owner) to allow forks, which is off by default for repositories that are not public; a fork is never more visible than its parent. Objects pushed to the parent later never reach the fork. Audited; rate limited."},
		mcpCallTimeout, forkRepositoryTool)
	addTool(srv, c, &mcp.Tool{Name: "sync_fork", Annotations: changes("Bring a fork up to date with its parent", false, true),
		Description: "Fast-forward a branch of a fork to the parent's branch (mode fast-forward, the default) or merge it in (mode merge). A branch with commits of its own is left alone and reported as diverged; a merge that conflicts changes nothing and lists the files. Needs write access to the fork and read access to the parent. Audited."},
		mcpCallTimeout, syncForkTool)
	addTool(srv, c, &mcp.Tool{Name: "use_template", Annotations: changes("Make a repository from a template", false, false),
		Description: "Start a new repository from a template: its tree at the tip of the default branch as one commit with no history (all branches, each as one commit, with include_branches), the variables the manifest declares replaced in the files and names it lists. Values are checked against the manifest's patterns; only the declared tokens {{name}} are replaced and nothing is evaluated. Audited."},
		mcpCallTimeout, useTemplateTool)
	addTool(srv, c, &mcp.Tool{Name: "fork_settings", Annotations: changes("Read or set who may fork, and mark a template", false, true),
		Description: "With owner: read or set the owner's switch for all its repositories (on, off or default; administrators). With repository: set its own switch (allow_forks) or mark it as a template (template true or false); repository administrators. With nothing to set, reads. The default is on for a public repository and off for any other."},
		mcpCallTimeout, forkSettingsTool)
	addTool(srv, c, &mcp.Tool{Name: "set_maintainer_edits", Annotations: changes("Allow or forbid maintainer edits on a pull request from a fork", false, true),
		Description: "Whether the people who may write to the target repository may update the pull request's branch in the fork (update the branch, resolve conflicts). The author or a repository administrator. Audited."},
		mcpCallTimeout, maintainerEditsTool)
	addTool(srv, c, &mcp.Tool{Name: "approve_pull_run", Annotations: changes("Approve running the pipeline of a pull request from a fork", false, true),
		Description: "The pipeline of a pull request from a fork waits when its author has no pull request merged here. Approving lets the pipeline run for the head as it is now (not for later commits); it still runs restricted: no secrets, no launches. Needs the operator role on the target. Audited."},
		mcpCallTimeout, approvePullRunTool)
	addTool(srv, c, &mcp.Tool{Name: "resolve_pull_conflicts", Annotations: changes("Resolve a pull request's conflicts", false, false),
		Description: "Answer every conflicting file and make the merge commit of the target into the pull request's branch, by you. Name the head and target the conflicts list gave (expect_head, expect_target): if either moved, nothing is done. Each file takes one of take (ours, theirs, both, delete), content (the whole text), or regions (an answer for each conflicting region: ours, theirs, both or custom text). Conflict markers left in the text are refused unless keep_markers says the file really contains them. Binary files take a side only. Refused with a sentence when the branch is protected against you or lives in a fork that does not allow maintainer edits. Audited."},
		mcpCallTimeout, resolvePullConflictsTool)
}

type forkRepoIn struct {
	Owner string `json:"owner" jsonschema:"the repository's owner"`
	Name  string `json:"name" jsonschema:"the repository's name"`
}

func (in forkRepoIn) path(suffix string) (string, error) {
	if in.Owner == "" || in.Name == "" {
		return "", errors.New("say which repository: owner and name")
	}
	return apiPath("/api/v1/repositories/%s/%s", in.Owner, in.Name) + suffix, nil
}

func forkGet(ctx context.Context, c *mcpCall, tool string, in forkRepoIn, suffix string) (map[string]any, error) {
	p, err := in.path(suffix)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.apiJSON(ctx, tool, http.MethodGet, p, nil, &out)
	return out, err
}

func listForksTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in forkRepoIn) (map[string]any, error) {
	return forkGet(ctx, c, "list_forks", in, "/forks")
}

func forkStatusTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in forkRepoIn) (map[string]any, error) {
	return forkGet(ctx, c, "get_fork_status", in, "/fork")
}

func getTemplateTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in forkRepoIn) (map[string]any, error) {
	return forkGet(ctx, c, "get_template", in, "/template")
}

type forkIn struct {
	forkRepoIn
	AsOwner    string `json:"as_owner,omitempty" jsonschema:"where the fork goes (default: your own space)"`
	AsName     string `json:"as_name,omitempty" jsonschema:"the fork's name (default: the parent's)"`
	Visibility string `json:"visibility,omitempty" jsonschema:"private, internal or public; never wider than the parent's (default: the parent's)"`
}

func forkRepositoryTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in forkIn) (map[string]any, error) {
	p, err := in.path("/forks")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.apiJSON(ctx, "fork_repository", http.MethodPost, p, map[string]any{"owner": in.AsOwner, "name": in.AsName, "visibility": in.Visibility}, &out)
	return out, err
}

type syncForkIn struct {
	forkRepoIn
	Branch   string `json:"branch,omitempty" jsonschema:"the fork's branch (default: its default branch)"`
	Upstream string `json:"upstream,omitempty" jsonschema:"the parent's branch (default: the same name)"`
	Mode     string `json:"mode,omitempty" jsonschema:"fast-forward (default) or merge"`
}

func syncForkTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in syncForkIn) (map[string]any, error) {
	p, err := in.path("/sync")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.apiJSON(ctx, "sync_fork", http.MethodPost, p, map[string]any{"branch": in.Branch, "upstream": in.Upstream, "mode": in.Mode}, &out)
	return out, err
}

type useTemplateIn struct {
	forkRepoIn
	NewOwner        string            `json:"new_owner,omitempty" jsonschema:"where the new repository goes (default: your own space)"`
	NewName         string            `json:"new_name" jsonschema:"the new repository's name"`
	Visibility      string            `json:"visibility,omitempty" jsonschema:"never wider than the template's (default: the template's)"`
	Values          map[string]string `json:"values,omitempty" jsonschema:"a value for each variable the template declares (get_template lists them)"`
	IncludeBranches bool              `json:"include_branches,omitempty" jsonschema:"also bring the template's other branches, each as one commit (default: the default branch only)"`
}

func useTemplateTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in useTemplateIn) (map[string]any, error) {
	p, err := in.path("/generate")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.apiJSON(ctx, "use_template", http.MethodPost, p, map[string]any{"owner": in.NewOwner, "name": in.NewName, "visibility": in.Visibility, "values": in.Values, "include_branches": in.IncludeBranches}, &out)
	return out, err
}

type forkSettingsIn struct {
	Owner      string  `json:"owner,omitempty" jsonschema:"an owner: read or set its switch for all its repositories"`
	Repository string  `json:"repository,omitempty" jsonschema:"a repository, owner/name: set its own switch or its template mark"`
	AllowForks *string `json:"allow_forks,omitempty" jsonschema:"on, off or default"`
	Template   *bool   `json:"template,omitempty" jsonschema:"mark the repository as a template (true) or not (false)"`
}

func forkSettingsTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in forkSettingsIn) (map[string]any, error) {
	var out map[string]any
	switch {
	case in.Owner != "" && in.Repository == "":
		p := apiPath("/api/v1/owners/%s/fork-settings", in.Owner)
		if in.AllowForks == nil {
			return out, c.apiJSON(ctx, "fork_settings", http.MethodGet, p, nil, &out)
		}
		return out, c.apiJSON(ctx, "fork_settings", http.MethodPut, p, map[string]any{"allow_forks": *in.AllowForks}, &out)
	case in.Repository != "" && in.Owner == "":
		owner, name, ok := cutPath(in.Repository)
		if !ok {
			return nil, errors.New("a repository is owner/name")
		}
		p := apiPath("/api/v1/repositories/%s/%s", owner, name)
		if in.AllowForks == nil && in.Template == nil {
			return out, c.apiJSON(ctx, "fork_settings", http.MethodGet, p+"/fork", nil, &out)
		}
		return out, c.apiJSON(ctx, "fork_settings", http.MethodPatch, p, map[string]any{"allow_forks": in.AllowForks, "template": in.Template}, &out)
	}
	return nil, errors.New("name an owner or a repository (owner/name), not both")
}

func cutPath(s string) (string, string, bool) {
	for i := 1; i < len(s)-1; i++ {
		if s[i] == '/' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

type maintainerEditsIn struct {
	repoNumberIn
	Allow bool `json:"allow" jsonschema:"true lets maintainers of the target update the branch in the fork"`
}

func maintainerEditsTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in maintainerEditsIn) (map[string]any, error) {
	p, err := in.path("/maintainer-edits")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.apiJSON(ctx, "set_maintainer_edits", http.MethodPut, p, map[string]any{"allow": in.Allow}, &out)
	return out, err
}

func approvePullRunTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in repoNumberIn) (map[string]any, error) {
	p, err := in.path("/approve-run")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.apiJSON(ctx, "approve_pull_run", http.MethodPost, p, map[string]any{}, &out)
	return out, err
}

func listPullConflictsTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in repoNumberIn) (map[string]any, error) {
	p, err := in.path("/conflicts")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.apiJSON(ctx, "list_pull_conflicts", http.MethodGet, p, nil, &out)
	return out, err
}

type resolveConflictsIn struct {
	repoNumberIn
	ExpectHead   string           `json:"expect_head" jsonschema:"the head tip the conflicts list gave"`
	ExpectTarget string           `json:"expect_target" jsonschema:"the target tip the conflicts list gave"`
	Files        []map[string]any `json:"files" jsonschema:"one answer for every conflicting file: path and one of take, content, regions; keep_markers when the text really holds marker-like lines"`
	Message      string           `json:"message,omitempty" jsonschema:"the merge commit's message (default: a sentence naming the files)"`
}

func resolvePullConflictsTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in resolveConflictsIn) (map[string]any, error) {
	p, err := in.path("/conflicts/resolve")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.apiJSON(ctx, "resolve_pull_conflicts", http.MethodPost, p, map[string]any{"expect_head": in.ExpectHead, "expect_target": in.ExpectTarget, "files": in.Files, "message": in.Message}, &out)
	return out, err
}
