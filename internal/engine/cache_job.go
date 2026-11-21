package engine

import (
	"slices"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// How a step's cache: key reaches the runner and comes back (schema 25).
// The server hands the runner what the step declared and the limits that
// apply; the runner restores before the script and saves after it, through
// the routes of cache.go, and reports what each cache did.

// JobCache says which caches a runner restores before a step and saves
// after it.
type JobCache struct {
	Caches []JobCacheEntry `json:"caches"`
	// MaxEntryBytes is the most one entry may hold under the project's
	// policy, so that a runner does not pack what the server would refuse;
	// ChunkBytes is the size of every chunk of an upload but the last.
	MaxEntryBytes int64 `json:"max_entry_bytes"`
	ChunkBytes    int64 `json:"chunk_bytes"`
	// Problem, when not empty, is why no cache can be used on this server
	// (it keeps no store).
	Problem string `json:"problem,omitempty"`
}

// JobCacheEntry is one cache of a step, as the pipeline wrote it.
type JobCacheEntry struct {
	Name     string   `json:"name"`
	Paths    []string `json:"paths"`
	KeyFiles []string `json:"key_files,omitempty"`
	KeyText  string   `json:"key_text,omitempty"`
	// KeyVersions are the author's version inputs (key.versions).
	KeyVersions []pipeline.CacheVersion `json:"key_versions,omitempty"`
	Fallback    string                  `json:"fallback,omitempty"`
	Save        string                  `json:"save,omitempty"`
}

// Outcomes of a cache in a step, in CacheResult.Result.
const (
	CacheHit      = "hit"       // restored the entry with the key
	CacheFallback = "fallback"  // restored the newest entry instead
	CacheMiss     = "miss"      // nothing to restore
	CacheSaved    = "saved"     // saved an entry
	CacheNotSaved = "not_saved" // would have saved, and could not or may not
)

// CacheResult is what one cache of a step attempt did, as the runner
// reports it and as step.finished records it. Both a restore and a save
// are results of the same cache: Result is where the attempt ended up (saved
// or not_saved when it saved, else what the restore found) and Restored what
// the restore found.
type CacheResult struct {
	Name string `json:"name"`
	// Key is the key worked out for the step, 64 hex digits, or empty if it
	// could not be worked out.
	Key string `json:"key,omitempty"`
	// KeyParts is what the key was made of, in words: the image resolved to
	// its digest, the platform, the author's versions, the key files.
	KeyParts []CacheKeyPart `json:"key_parts,omitempty"`
	// Result is hit, fallback or miss for a restore, and saved or not_saved
	// for a save that was tried.
	Result string `json:"result"`
	// Restored is what the restore found (hit, fallback or miss) when Result
	// is about the save.
	Restored string `json:"restored,omitempty"`
	// Bytes is the size of the entry restored, or saved.
	Bytes int64 `json:"bytes,omitempty"`
	// Scope is the scope the entry restored came from, or the one it was
	// saved in, as the server told the runner.
	Scope string `json:"scope,omitempty"`
	// Reason says, in plain words, why there was no entry, or why nothing
	// was saved.
	Reason string `json:"reason,omitempty"`
	// Seconds is how long the restore or save took.
	Seconds float64 `json:"seconds,omitempty"`
}

// CacheKeyPart is one input of a cache key, as shown on the run page.
type CacheKeyPart struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

var cacheResults = []string{CacheHit, CacheFallback, CacheMiss, CacheSaved, CacheNotSaved}

// needsCache reports whether a step needs a runner that knows caches.
func needsCache(def *pipeline.Step) bool { return def != nil && len(def.Cache) > 0 }

// jobCache says what a runner does with a step's caches, or nil if it has
// none.
func (c *cmd) jobCache(def *pipeline.Step) *JobCache {
	if !c.e.knows(schemaCache) || len(def.Cache) == 0 {
		return nil
	}
	jc := &JobCache{ChunkBytes: ArtifactChunkBytes}
	for _, ca := range def.Cache {
		jc.Caches = append(jc.Caches, JobCacheEntry{Name: ca.Name, Paths: slices.Clone(ca.Paths), KeyFiles: slices.Clone(ca.Key.Files),
			KeyText: ca.Key.Text, KeyVersions: slices.Clone(ca.Key.Versions), Fallback: ca.Fallback, Save: ca.Save})
	}
	if lim, err := c.e.readCacheLimits(c.tx, c.st.Project); err == nil {
		jc.MaxEntryBytes = lim.maxEntry
	}
	if c.e.arts.Store == nil {
		jc.Problem = "this server keeps no caches: " + c.e.arts.StoreProblem
	}
	return jc
}

// keepCacheResults checks what a runner says its step's caches did and
// returns what is recorded: only caches the step declares, once each,
// with outcomes this server knows and text it has cleaned. The runner is
// trusted to say what happened, not to put anything else in the record.
func (c *cmd) keepCacheResults(def *pipeline.Step, in []CacheResult) []CacheResult {
	if !c.e.knows(schemaCache) || def == nil || len(def.Cache) == 0 || len(in) == 0 {
		return nil
	}
	declared := map[string]bool{}
	for _, ca := range def.Cache {
		declared[ca.Name] = true
	}
	var out []CacheResult
	seen := map[string]bool{}
	for _, r := range in {
		if !declared[r.Name] || seen[r.Name] || !slices.Contains(cacheResults, r.Result) {
			continue
		}
		seen[r.Name] = true
		r.Key = cleanKey(r.Key)
		r.KeyParts = cleanKeyParts(r.KeyParts)
		if r.Restored != "" && !slices.Contains(cacheResults[:3], r.Restored) {
			r.Restored = ""
		}
		r.Bytes = max(r.Bytes, 0)
		r.Seconds = min(max(r.Seconds, 0), 86400)
		r.Scope = cleanText(r.Scope, 200)
		r.Reason = cleanText(r.Reason, 400)
		out = append(out, r)
	}
	return out
}

func cleanKey(k string) string {
	if validSHA256(k) {
		return k
	}
	return ""
}

// cleanKeyParts keeps a bounded number of short lines of text.
func cleanKeyParts(in []CacheKeyPart) []CacheKeyPart {
	var out []CacheKeyPart
	for _, p := range in {
		if len(out) == 40 {
			break
		}
		out = append(out, CacheKeyPart{Label: cleanText(p.Label, 200), Value: cleanText(p.Value, 300)})
	}
	return out
}
