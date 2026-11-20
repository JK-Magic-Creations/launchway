package pipeline

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SchemaCache is the server schema that brought build caches: the cache key
// of a step. A pipeline that uses it carries "requires": 25, so that older
// servers, which would run the step without restoring or saving anything
// and not know why, leave its runs alone.
const SchemaCache = 25

// Cache keeps a directory between runs, so that a step does not start from
// nothing each time. Before the step's script the runner restores the entry
// whose key matches; after it, it saves the paths under that key.
//
//	steps:
//	  test:
//	    image: docker.io/library/python:3.13-slim
//	    cache:
//	      - name: pip
//	        paths: [.cache/pip]
//	        key: { files: [requirements.txt, requirements-dev.txt], text: v1 }
//	        fallback: latest      # none (the default) or latest
//	        save: success         # success (the default), always or never
//	    env: { PIP_CACHE_DIR: .cache/pip }
//	    run: pip install -r requirements.txt && pytest
//
// The key is made from the contents of the files key.files names (their
// paths and SHA-256, in sorted order), the text of key.text mixed in
// literally, and the runner's operating system and architecture, which are
// always part of it. An entry is never changed once saved: a new key is a
// new entry.
type Cache struct {
	// Name tells the caches of a step apart and groups the entries of one
	// cache across keys; it is a name as for artifacts.
	Name string `json:"name"`
	// Paths are globs relative to the workspace, as for artifacts; a glob
	// that names a directory takes every file below it.
	Paths []string `json:"paths"`
	Key   CacheKey `json:"key"`
	// Fallback is none (written as empty) or latest: with latest, a step
	// whose exact key has no entry starts from the newest entry of the same
	// name and platform that its run may read.
	Fallback string `json:"fallback,omitempty"`
	// Save says after which outcomes the paths are saved: success (the
	// default, written as empty), always (succeeded or failed) or never.
	Save string `json:"save,omitempty"`
	Line int    `json:"-"`
}

// CacheKey says what the key of a cache entry is made of.
type CacheKey struct {
	// Files are globs whose files' paths and contents are hashed.
	Files []string `json:"files,omitempty"`
	// Text is mixed in literally, so that a team can start over by hand.
	Text string `json:"text,omitempty"`
}

// Values of Cache.Fallback and Cache.Save.
const (
	CacheFallbackNone   = "none"
	CacheFallbackLatest = "latest"
	CacheSaveSuccess    = "success"
	CacheSaveAlways     = "always"
	CacheSaveNever      = "never"
)

// Limits on caches.
const (
	MaxCachesPerStep  = 10
	MaxCachePaths     = 20
	MaxCacheKeyFiles  = 20
	MaxCacheKeyText   = 256
	cacheKeyDeferred  = "cache"
	cacheNamePathHint = "cache: [{ name: pip, paths: [.cache/pip], key: { files: [requirements.txt] } }]"
)

// SaveAfter reports whether the paths are saved after an attempt that ended
// with outcome: succeeded, failed or canceled. A canceled attempt never
// saves: what it left is not what a finished step would have.
func (c *Cache) SaveAfter(outcome string) bool {
	switch c.Save {
	case CacheSaveNever:
		return false
	case CacheSaveAlways:
		return outcome == "succeeded" || outcome == "failed"
	default:
		return outcome == "succeeded"
	}
}

// UsesSchemaCache reports whether a step has a cache, which servers older
// than schema 25 do not know.
func (p *Pipeline) UsesSchemaCache() bool {
	for _, s := range p.Steps {
		if len(s.Cache) > 0 {
			return true
		}
	}
	return false
}

// CheckCache returns why a cache, as it will run, is not valid, or "". The
// parser calls it for values written in place; for a step a loop repeats it
// is called again once the loop's values are in (see expandLoops), when the
// name may have become something else.
func CheckCache(c *Cache) string {
	switch {
	case c.Name == "":
		return "a cache needs a name, such as name: pip"
	case !ValidArtifactName(c.Name):
		return quoteText(c.Name) + " is not a valid cache name: use lower-case letters, digits, '.', '-' and '_', starting with a letter or digit, at most 64"
	case len(c.Paths) == 0:
		return "say which directories or files to keep, such as paths: [.cache/pip]"
	case len(c.Paths) > MaxCachePaths:
		return "at most " + strconv.Itoa(MaxCachePaths) + " paths per cache"
	case len(c.Key.Files) == 0 && c.Key.Text == "":
		return "a key needs files, text or both, such as key: { files: [requirements.txt] }; with neither, the entry would never change"
	case len(c.Key.Files) > MaxCacheKeyFiles:
		return "key.files has at most " + strconv.Itoa(MaxCacheKeyFiles) + " globs"
	case utf8.RuneCountInString(c.Key.Text) > MaxCacheKeyText || !utf8.ValidString(c.Key.Text) || strings.ContainsFunc(c.Key.Text, unicode.IsControl):
		return "key.text is at most " + strconv.Itoa(MaxCacheKeyText) + " characters of plain text"
	}
	for _, g := range c.Paths {
		if _, msg := checkGlob(g, "cached paths"); msg != "" {
			return msg
		}
	}
	for _, g := range c.Key.Files {
		if _, msg := checkGlob(g, "key files"); msg != "" {
			return msg
		}
	}
	return ""
}
