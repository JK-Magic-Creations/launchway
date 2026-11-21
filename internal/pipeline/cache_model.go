package pipeline

import (
	"path"
	"strconv"
	"strings"
	"time"
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
// literally, the values of key.versions (commands run in the step's image,
// files), and, always, the runner's operating system and architecture and
// the step's image resolved to its digest: a cache made in one JDK, Node or
// base image is never restored into another. An entry is never changed once
// saved: a new key is a new entry.
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
	// Sensitive marks a cache that is never read outside the scope that
	// wrote it: a pull request, a branch or a draft does not restore what
	// the default branch saved.
	Sensitive bool `json:"sensitive,omitempty"`
	// Share, when set, is the name of a group of caches shared by pipelines
	// of the project: caches that name the same group (and the same cache
	// name and scope) see each other's entries. Without it, a cache is the
	// pipeline's own.
	Share string `json:"share,omitempty"`
	// MaxAge is how old an entry may be, from when it was made, before a
	// restore treats it as missing and the step rebuilds it; 0 is the cache
	// policy's (7 days unless changed).
	MaxAge Duration `json:"max_age,omitempty"`
	Line   int      `json:"-"`
}

// CacheKey says what the key of a cache entry is made of.
type CacheKey struct {
	// Files are globs whose files' paths and contents are hashed.
	Files []string `json:"files,omitempty"`
	// Text is mixed in literally, so that a team can start over by hand.
	Text string `json:"text,omitempty"`
	// Versions name things whose value the cache depends on and the
	// product cannot know: the output of a command run in the step's image
	// (java -version) or the contents of a file (.nvmrc). A change of any of
	// them is a different key.
	Versions []CacheVersion `json:"versions,omitempty"`
}

// CacheVersion is one version input of a cache key: a command run in the
// step's image, whose output is the value, or a file, whose SHA-256 is.
// Exactly one is set.
type CacheVersion struct {
	Run  string `json:"run,omitempty"`
	File string `json:"file,omitempty"`
}

// Limits of version inputs.
const (
	MaxCacheVersions   = 10
	MaxCacheVersionRun = 200
)

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
	MinCacheMaxAge    = time.Hour
	MaxCacheMaxAge    = 365 * 24 * time.Hour
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

// CacheProblems returns everything wrong with a cache as it will run, in
// words, or nothing. The parser calls it for values written in place; for a
// step a loop repeats it is called again once the loop's values are in (see
// the expansion of loops), when the name may have become something else.
func CacheProblems(c *Cache) []string {
	var out []string
	switch {
	case c.Name == "":
		out = append(out, "a cache needs a name, such as name: pip")
	case !ValidArtifactName(c.Name):
		out = append(out, quoteText(c.Name)+" is not a valid cache name: use lower-case letters, digits, '.', '-' and '_', starting with a letter or digit, at most 64")
	}
	switch {
	case len(c.Paths) == 0:
		out = append(out, "say which directories or files to keep, such as paths: [.cache/pip]")
	case len(c.Paths) > MaxCachePaths:
		out = append(out, "at most "+strconv.Itoa(MaxCachePaths)+" paths per cache")
	}
	for _, g := range c.Paths {
		clean, msg := checkGlob(g, "cached paths")
		if msg == "" && namesGitDir(clean) {
			msg = quoteText(clipText(g, 60)) + " would keep a .git directory: it holds the repository's history and often credentials, and every run that may restore the cache would read them; cache the directories the build makes instead"
		}
		if msg != "" {
			out = append(out, msg)
		}
	}
	if c.Share != "" && !ValidArtifactName(c.Share) {
		out = append(out, quoteText(c.Share)+" is not a valid share name: use lower-case letters, digits, '.', '-' and '_', starting with a letter or digit, at most 64")
	}
	if c.MaxAge != 0 && (c.MaxAge.D() < MinCacheMaxAge || c.MaxAge.D() > MaxCacheMaxAge) {
		out = append(out, "max_age is between 1h and 365d, such as 7d")
	}
	switch {
	case len(c.Key.Files) == 0 && c.Key.Text == "":
		out = append(out, "a key needs files, text or both, such as key: { files: [requirements.txt] }; with neither, the entry would never change")
	case len(c.Key.Files) > MaxCacheKeyFiles:
		out = append(out, "key.files has at most "+strconv.Itoa(MaxCacheKeyFiles)+" globs")
	}
	for _, g := range c.Key.Files {
		if _, msg := checkGlob(g, "key files"); msg != "" {
			out = append(out, msg)
		}
	}
	if len(c.Key.Versions) > MaxCacheVersions {
		out = append(out, "key.versions has at most "+strconv.Itoa(MaxCacheVersions)+" entries")
	}
	for _, v := range c.Key.Versions {
		switch {
		case (v.Run == "") == (v.File == ""):
			out = append(out, "a version is a command (java -version) or { file: .nvmrc }, not both and not neither")
		case v.Run != "" && (utf8.RuneCountInString(v.Run) > MaxCacheVersionRun || strings.ContainsFunc(v.Run, func(r rune) bool { return unicode.IsControl(r) })):
			out = append(out, "a version command is at most "+strconv.Itoa(MaxCacheVersionRun)+" characters on one line")
		case v.File != "":
			if _, msg := checkGlob(v.File, "version files"); msg != "" {
				out = append(out, msg)
			}
		}
	}
	if utf8.RuneCountInString(c.Key.Text) > MaxCacheKeyText || !utf8.ValidString(c.Key.Text) || strings.ContainsFunc(c.Key.Text, unicode.IsControl) {
		out = append(out, "key.text is at most "+strconv.Itoa(MaxCacheKeyText)+" characters of plain text")
	}
	return out
}

// namesGitDir reports whether a cached path's glob names the .git
// directory: a part that is .git, or a pattern that starts with a dot and
// matches it (.g*, .*).
func namesGitDir(glob string) bool {
	for _, part := range strings.Split(glob, "/") {
		if part == ".git" {
			return true
		}
		if strings.HasPrefix(part, ".") && !strings.Contains(part, "**") {
			if ok, _ := path.Match(part, ".git"); ok {
				return true
			}
		}
	}
	return false
}

// requiresNewer is the schema a pipeline needs, given the one the older
// features it uses need: raised for step resources and build caches, which
// servers older than they are would run without.
func (p *Pipeline) requiresNewer(cur int) int {
	if p.UsesSchemaResources() {
		cur = max(cur, SchemaResources)
	}
	if p.UsesSchemaCache() {
		cur = max(cur, SchemaCache)
	}
	return cur
}
