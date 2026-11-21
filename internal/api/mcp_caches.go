package api

// The MCP tools for build caches (schema 25): listing a project's entries
// and deleting them, and the policy that decides how much is kept. Each
// calls the same routes as the API, so the answer for a caller is the API's:
// listing needs the rights to read the project, deleting those to operate
// it, and only an administrator is offered the tool that changes the policy.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

func addCacheTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_caches", Annotations: readOnly("List a project's caches"),
		Description: "A project's build caches: each entry with its name, key, scope (default, ref:<ref> or pull:<n>), platform, size, when it was last restored, how often, and the run that wrote it, with the total against the project's quota and idle days."},
		mcpCallTimeout, listCaches)
	addTool(srv, c, &mcp.Tool{Name: "get_cache_policy", Annotations: readOnly("Get the cache policy"),
		Description: "Whether build caches are kept and how much: max_project_gib, max_entry_gib, idle_days and max_age_days, for one project (its own override, else the installation's) or, without a project, the installation's."},
		mcpCallTimeout, getCachePolicy)
	if c.mayOperate {
		addCacheChangeTools(srv, c)
	}
	if !c.mayAdminister {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "set_cache_policy", Annotations: changes("Set the cache policy", true, true),
		Description: "Administrators only. Change the cache policy of one project (an override) or, without a project, the installation's: enabled, max_project_gib, max_entry_gib, idle_days, max_age_days. A field left out keeps its value. Lowering the quota ends the project's least recently used entries. Audited."},
		mcpCallTimeout, setCachePolicy)
}

func addCacheChangeTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "delete_cache", Annotations: changes("Delete a cache", true, true),
		Description: "End one cache entry (by id from list_caches) or every entry of a cache (by name, in every scope and key). The next run that wanted it starts without it and saves it again. Audited."},
		mcpCallTimeout, deleteCache)
}

type cachesIn struct {
	Project string `json:"project" jsonschema:"the project's name, as list_projects gives it"`
}

type cacheEntryOut struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Key        string `json:"key"`
	Scope      string `json:"scope"`
	Platform   string `json:"platform"`
	Bytes      int64  `json:"bytes"`
	Files      int    `json:"files"`
	Status     string `json:"status"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	Hits       int64  `json:"hits"`
	Run        string `json:"run_id"`
	RunNumber  int    `json:"run_number,omitempty"`
}

type cachesOut struct {
	Summary string                 `json:"summary"`
	Entries []cacheEntryOut        `json:"entries"`
	Totals  engine.CacheTotals     `json:"totals"`
	Policy  engine.CachePolicyView `json:"policy"`
}

func listCaches(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in cachesIn) (cachesOut, error) {
	if in.Project == "" {
		return cachesOut{}, errors.New("say which project")
	}
	var l engine.CacheList
	if err := c.apiJSON(ctx, "list_caches", http.MethodGet, apiPath("/api/v1/projects/%s/caches", in.Project), nil, &l); err != nil {
		return cachesOut{}, err
	}
	out := cachesOut{Entries: []cacheEntryOut{}, Totals: l.Totals, Policy: l.Policy}
	for _, v := range l.Entries {
		out.Entries = append(out.Entries, cacheEntryOut{ID: v.ID, Name: untrusted(v.Name, 80), Key: v.Key, Scope: untrusted(v.Scope, 200), Platform: v.Platform,
			Bytes: v.Bytes, Files: v.Files, Status: v.Status, LastUsedAt: ts(v.LastUsedAt), Hits: v.Hits, Run: v.RunID, RunNumber: v.RunNumber})
	}
	switch {
	case !l.Policy.Enabled:
		out.Summary = "Caching is turned off for this project (cache policy)."
	case len(out.Entries) == 0:
		out.Summary = "Nothing is cached yet."
	default:
		out.Summary = fmt.Sprintf("%d entries, %s of %s", l.Totals.Entries, sizeText(l.Totals.Bytes), sizeText(l.Totals.QuotaBytes))
	}
	return out, nil
}

type deleteCacheIn struct {
	Project string `json:"project" jsonschema:"the project's name"`
	ID      string `json:"id,omitempty" jsonschema:"one entry, by the id list_caches gives"`
	Name    string `json:"name,omitempty" jsonschema:"every entry of this cache, in every scope and key"`
}

func deleteCache(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in deleteCacheIn) (engine.CachesDeleted, error) {
	var out engine.CachesDeleted
	switch {
	case in.Project == "":
		return out, errors.New("say which project")
	case (in.ID == "") == (in.Name == ""):
		return out, errors.New("give either id (one entry) or name (a whole cache), not both and not neither")
	case in.ID != "":
		err := c.apiJSON(ctx, "delete_cache", http.MethodDelete, apiPath("/api/v1/projects/%s/caches/%s", in.Project, in.ID), nil, &out)
		return out, err
	}
	err := c.apiJSON(ctx, "delete_cache", http.MethodDelete, apiPath("/api/v1/projects/%s/caches", in.Project)+"?name="+url.QueryEscape(in.Name), nil, &out)
	return out, err
}

type cachePolicyToolIn struct {
	Project       string   `json:"project,omitempty" jsonschema:"a project to override the policy for; without one, the installation's policy"`
	Enabled       *bool    `json:"enabled,omitempty" jsonschema:"whether caches are kept at all"`
	MaxProjectGiB *float64 `json:"max_project_gib,omitempty" jsonschema:"how much one project's entries may add up to, in GiB (10 by default)"`
	MaxEntryGiB   *float64 `json:"max_entry_gib,omitempty" jsonschema:"the most one entry may hold, in GiB (2 by default)"`
	IdleDays      *int     `json:"idle_days,omitempty" jsonschema:"how long an entry nobody restored is kept (7 by default)"`
	MaxAgeDays    *int     `json:"max_age_days,omitempty" jsonschema:"how old an entry may be, from when its contents were started from scratch, before a restore treats it as missing (7 by default); a cache's own max_age may be shorter"`
}

func getCachePolicy(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
	Project string `json:"project,omitempty" jsonschema:"a project; without one, the installation's policy"`
}) (engine.CachePolicyView, error) {
	var out engine.CachePolicyView
	path := "/api/v1/cache-policy"
	if in.Project != "" {
		path = apiPath("/api/v1/projects/%s/cache-policy", in.Project)
	}
	err := c.apiJSON(ctx, "get_cache_policy", http.MethodGet, path, nil, &out)
	return out, err
}

func setCachePolicy(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in cachePolicyToolIn) (engine.CachePolicyView, error) {
	var out engine.CachePolicyView
	path := "/api/v1/cache-policy"
	if in.Project != "" {
		path = apiPath("/api/v1/projects/%s/cache-policy", in.Project)
	}
	body := cachePolicyIn{Enabled: in.Enabled, MaxProjectGiB: in.MaxProjectGiB, MaxEntryGiB: in.MaxEntryGiB, IdleDays: in.IdleDays, MaxAgeDays: in.MaxAgeDays}
	err := c.apiJSON(ctx, "set_cache_policy", http.MethodPut, path, body, &out)
	return out, err
}

// cacheBrief is what one build cache of a step did, as a run's description
// gives it.
type cacheBrief struct {
	Name     string `json:"name"`
	Result   string `json:"result" jsonschema:"hit, fallback, miss, expired, saved or not_saved"`
	Restored string `json:"restored,omitempty" jsonschema:"when the cache was saved or not saved: what the restore before the step found"`
	Bytes    int64  `json:"bytes,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Reason   string `json:"reason,omitempty" jsonschema:"why there was no entry, or why nothing was saved"`
	Key      string `json:"key,omitempty" jsonschema:"the key worked out for the step"`
	// EntryKey, Generation and AgeSeconds describe the entry restored (or found too old), whose key differs from the step's on a fallback.
	EntryKey        string                `json:"entry_key,omitempty" jsonschema:"the key of the entry that was restored or found too old"`
	Generation      int                   `json:"generation,omitempty" jsonschema:"which generation of its key that entry is"`
	AgeSeconds      int64                 `json:"age_seconds,omitempty" jsonschema:"how long ago that entry's contents were started from scratch"`
	MaxAgeSeconds   int64                 `json:"max_age_seconds,omitempty" jsonschema:"the longest the cache may be before a restore treats it as missing"`
	SavedGeneration int                   `json:"saved_generation,omitempty" jsonschema:"the generation this attempt saved, if it did"`
	KeyParts        []engine.CacheKeyPart `json:"key_parts,omitempty" jsonschema:"what the key was made of: image digest, platform, versions, key files"`
}

// cacheBriefs describes what a step's caches did. The text comes from the
// runner, so it is data, never instructions.
func cacheBriefs(in []engine.CacheResult) []cacheBrief {
	var out []cacheBrief
	for _, ca := range in {
		out = append(out, cacheBrief{Name: untrusted(ca.Name, 80), Result: ca.Result, Restored: ca.Restored, Bytes: ca.Bytes,
			Scope: untrusted(ca.Scope, 200), Reason: untrusted(ca.Reason, 400), Key: ca.Key, EntryKey: ca.EntryKey, Generation: ca.Generation,
			AgeSeconds: ca.AgeSeconds, MaxAgeSeconds: ca.MaxAgeSeconds, SavedGeneration: ca.SavedGeneration, KeyParts: untrustedParts(ca.KeyParts)})
	}
	return out
}

// untrustedParts clips the parts of a key, whose text comes from the runner.
func untrustedParts(in []engine.CacheKeyPart) []engine.CacheKeyPart {
	var out []engine.CacheKeyPart
	for _, p := range in {
		out = append(out, engine.CacheKeyPart{Label: untrusted(p.Label, 200), Value: untrusted(p.Value, 300)})
	}
	return out
}
