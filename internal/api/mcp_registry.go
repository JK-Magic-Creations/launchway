package api

// The MCP tools for the built-in registry: what a project has pushed,
// inspecting an image, deleting, policy, retention previews and collection.
// Each calls the same route as the API (see registry.go), so the answer for a
// caller is the API's: reading needs the viewer role on the project, deleting
// and policy the admin role, and only an administrator is offered
// collection and the installation's policy.

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addRegistryTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "registry_status", Annotations: readOnly("Built-in registry status"),
		Description: "Whether the built-in OCI registry is on, its address, how to log in, its limits, how many repositories you may see, and what protects blobs at rest (blobs_encrypted_by_launchway says plainly whether Launchway itself encrypts them)."},
		mcpCallTimeout, registryStatusTool)
	addTool(srv, c, &mcp.Tool{Name: "list_registry_images", Annotations: readOnly("List registry images"),
		Description: "A project's registry: its repositories (images, tags, bytes) with usage against the quota; with repository, that repository's images: each with tags, digest, size, platforms, which run pushed it, signature/SBOM/vulnerability summary, and the pull reference."},
		mcpCallTimeout, listRegistryImages)
	addTool(srv, c, &mcp.Tool{Name: "inspect_registry_image", Annotations: readOnly("Inspect a registry image"),
		Description: "One image or OCI artifact (Helm chart, SBOM) by tag or digest: its manifest, attachments, platforms and the pull reference."},
		mcpCallTimeout, inspectRegistryImage)
	addTool(srv, c, &mcp.Tool{Name: "get_registry_policy", Annotations: readOnly("Get the registry policy"),
		Description: "The registry policy of a project (or one repository, or without project the installation's): quota, retention rule, immutable tag patterns and which projects' runs may pull; with the effective values after the levels are combined."},
		mcpCallTimeout, getRegistryPolicy)
	addTool(srv, c, &mcp.Tool{Name: "preview_registry_retention", Annotations: readOnly("Preview registry retention"),
		Description: "What the retention rules of a project (or one repository) would delete right now, each image with the reason. Deletes nothing."},
		mcpCallTimeout, previewRegistryRetention)
	if !c.mayOperate {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "delete_registry_image", Annotations: changes("Delete a registry image", true, true),
		Description: "Project administrators only. Delete an image by digest or tag (with its signatures, SBOMs and tags), or only a tag with tag set. Immutable tags and images a deployment uses are refused. The bytes go at the next garbage collection. Audited."},
		mcpCallTimeout, deleteRegistryImage)
	addTool(srv, c, &mcp.Tool{Name: "set_registry_policy", Annotations: changes("Set the registry policy", false, true),
		Description: "Project administrators only (an installation administrator for the installation's policy, when project is left out). Replace the policy: quota_bytes, retention {keep_last, keep_tags, untagged_days}, immutable_tags, pull_from. Audited."},
		mcpCallTimeout, setRegistryPolicy)
	addTool(srv, c, &mcp.Tool{Name: "run_registry_retention", Annotations: changes("Run registry retention", true, true),
		Description: "Project administrators only. Delete what preview_registry_retention lists. Audited."},
		mcpCallTimeout, runRegistryRetention)
	if !c.mayAdminister {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "registry_gc", Annotations: changes("Registry garbage collection", true, true),
		Description: "Administrators only. Collect garbage in the registry's blob store: blobs and uploads nothing refers to, older than the grace period. dry_run lists what would go and removes nothing; last returns the last report instead. Safe beside pushes. Audited."},
		mcpCallTimeout, registryGC)
}

func registryStatusTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ struct{}) (map[string]any, error) {
	var out map[string]any
	err := c.apiJSON(ctx, "registry_status", http.MethodGet, "/api/v1/registry", nil, &out)
	return out, err
}

type registryProjectIn struct {
	Project    string `json:"project" jsonschema:"the project's name, as list_projects gives it"`
	Repository string `json:"repository,omitempty" jsonschema:"a repository of the project, such as pay-api/web (the project's name first)"`
}

func listRegistryImages(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in registryProjectIn) (map[string]any, error) {
	if in.Project == "" {
		return nil, errors.New("say which project")
	}
	var out map[string]any
	path := apiPath("/api/v1/projects/%s/registry", in.Project)
	if in.Repository != "" {
		path = apiPath("/api/v1/projects/%s/registry/images", in.Project) + "?repository=" + url.QueryEscape(in.Repository)
	}
	err := c.apiJSON(ctx, "list_registry_images", http.MethodGet, path, nil, &out)
	return out, err
}

type registryImageIn struct {
	Project    string `json:"project" jsonschema:"the project's name"`
	Repository string `json:"repository" jsonschema:"the repository, such as pay-api/web"`
	Reference  string `json:"reference,omitempty" jsonschema:"a tag or a digest (sha256:...)"`
	Tag        string `json:"tag,omitempty" jsonschema:"to delete only this tag and keep the image"`
}

func inspectRegistryImage(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in registryImageIn) (map[string]any, error) {
	if in.Project == "" || in.Repository == "" || in.Reference == "" {
		return nil, errors.New("give project, repository and reference (a tag or digest)")
	}
	var out map[string]any
	err := c.apiJSON(ctx, "inspect_registry_image", http.MethodGet,
		apiPath("/api/v1/projects/%s/registry/image", in.Project)+"?repository="+url.QueryEscape(in.Repository)+"&reference="+url.QueryEscape(in.Reference), nil, &out)
	return out, err
}

func deleteRegistryImage(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in registryImageIn) (map[string]any, error) {
	if in.Project == "" || in.Repository == "" || (in.Reference == "") == (in.Tag == "") {
		return nil, errors.New("give project, repository and either reference (the image, by digest or tag) or tag (only the tag), not both")
	}
	var out map[string]any
	path := apiPath("/api/v1/projects/%s/registry/image", in.Project) + "?repository=" + url.QueryEscape(in.Repository) + "&reference=" + url.QueryEscape(in.Reference)
	if in.Tag != "" {
		path = apiPath("/api/v1/projects/%s/registry/tag", in.Project) + "?repository=" + url.QueryEscape(in.Repository) + "&tag=" + url.QueryEscape(in.Tag)
	}
	err := c.apiJSON(ctx, "delete_registry_image", http.MethodDelete, path, nil, &out)
	return out, err
}

func policyPath(project, repository string) string {
	if project == "" {
		return "/api/v1/registry/policy"
	}
	p := apiPath("/api/v1/projects/%s/registry/policy", project)
	if repository != "" {
		p += "?repository=" + url.QueryEscape(repository)
	}
	return p
}

func getRegistryPolicy(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
	Project    string `json:"project,omitempty" jsonschema:"a project; without one, the installation's policy"`
	Repository string `json:"repository,omitempty" jsonschema:"one repository of the project"`
}) (map[string]any, error) {
	var out map[string]any
	err := c.apiJSON(ctx, "get_registry_policy", http.MethodGet, policyPath(in.Project, in.Repository), nil, &out)
	return out, err
}

type setRegistryPolicyIn struct {
	Project    string `json:"project,omitempty" jsonschema:"a project; without one, the installation's policy"`
	Repository string `json:"repository,omitempty" jsonschema:"one repository of the project"`
	QuotaBytes *int64 `json:"quota_bytes,omitempty" jsonschema:"what a project may store, in bytes; 0 for none"`
	Retention  *struct {
		KeepLast     int      `json:"keep_last,omitempty" jsonschema:"keep the newest N tagged images"`
		KeepTags     []string `json:"keep_tags,omitempty" jsonschema:"keep images with a tag matching these patterns, such as v*"`
		UntaggedDays int      `json:"untagged_days,omitempty" jsonschema:"delete untagged images older than this many days"`
	} `json:"retention,omitempty" jsonschema:"which images a collection may delete: keep_last, keep_tags and untagged_days"`
	ImmutableTags []string `json:"immutable_tags,omitempty" jsonschema:"tag patterns that cannot be overwritten or deleted once pushed, such as v*"`
	PullFrom      []string `json:"pull_from,omitempty" jsonschema:"projects (patterns) whose runs may pull this project's repositories"`
}

func setRegistryPolicy(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in setRegistryPolicyIn) (map[string]any, error) {
	body := map[string]any{}
	if in.QuotaBytes != nil {
		body["quota_bytes"] = *in.QuotaBytes
	}
	if in.Retention != nil {
		body["retention"] = in.Retention
	}
	if len(in.ImmutableTags) > 0 {
		body["immutable_tags"] = in.ImmutableTags
	}
	if len(in.PullFrom) > 0 {
		body["pull_from"] = in.PullFrom
	}
	if in.Project == "" {
		body = map[string]any{"scope": "installation", "policy": body}
	}
	var out map[string]any
	err := c.apiJSON(ctx, "set_registry_policy", http.MethodPut, policyPath(in.Project, in.Repository), body, &out)
	return out, err
}

func previewRegistryRetention(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in registryProjectIn) (map[string]any, error) {
	if in.Project == "" {
		return nil, errors.New("say which project")
	}
	var out map[string]any
	path := apiPath("/api/v1/projects/%s/registry/retention", in.Project)
	if in.Repository != "" {
		path += "?repository=" + url.QueryEscape(in.Repository)
	}
	err := c.apiJSON(ctx, "preview_registry_retention", http.MethodGet, path, nil, &out)
	return out, err
}

func runRegistryRetention(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in registryProjectIn) (map[string]any, error) {
	if in.Project == "" {
		return nil, errors.New("say which project")
	}
	var out map[string]any
	path := apiPath("/api/v1/projects/%s/registry/retention", in.Project)
	if in.Repository != "" {
		path += "?repository=" + url.QueryEscape(in.Repository)
	}
	err := c.apiJSON(ctx, "run_registry_retention", http.MethodPost, path, map[string]any{}, &out)
	return out, err
}

func registryGC(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
	DryRun bool `json:"dry_run,omitempty" jsonschema:"list what would be removed and remove nothing"`
	Last   bool `json:"last,omitempty" jsonschema:"return the last collection's report instead of running one"`
}) (map[string]any, error) {
	var out map[string]any
	if in.Last {
		err := c.apiJSON(ctx, "registry_gc", http.MethodGet, "/api/v1/registry/gc", nil, &out)
		return out, err
	}
	err := c.apiJSON(ctx, "registry_gc", http.MethodPost, "/api/v1/registry/gc", map[string]any{"dry_run": in.DryRun}, &out)
	return out, err
}
