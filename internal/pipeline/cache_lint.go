package pipeline

import (
	"fmt"
	"path"
	"strings"
)

// Lint of cache patterns that hand a build stale or unsafe content. These are
// warnings, never problems: a pipeline that has them still runs, and the
// author decides. Each carries a code so that an installation's policy can,
// later, make one blocking (the hook is Problem.Code; nothing blocks on them
// today, and the pipeline reference says so).
const (
	CodeCacheOutputs     = "cache-build-outputs"
	CodeCacheWholeHome   = "cache-whole-tool-home"
	CodeCacheNoLock      = "cache-key-without-lock"
	CodeCacheFallback    = "cache-fallback-without-max-age"
	CodeCacheCredentials = "cache-credential-path" //nolint:gosec // the name of a warning, not a credential
	CodeCacheSaveAlways  = "cache-save-always"
)

// outputDirs are directories a build writes its results into. What is in
// them depends on the sources of the commit that made them; restored into
// another commit they are stale output, and tools that decide by time or
// by name may keep it.
var outputDirs = map[string]bool{"target": true, "build": true, "dist": true, "out": true, "node_modules": true, "__pycache__": true,
	"_build": true, ".next": true, ".nuxt": true, "obj": true}

// toolHomes are a package tool's whole state directory: downloaded
// artifacts, and also the resolver's records of what it asked and what was
// missing.
var toolHomes = []string{".m2", ".gradle", ".ivy2", ".sbt"}

// credentialDirs are where tools keep credentials.
var credentialDirs = []string{".m2", ".npmrc", ".docker", ".aws", ".kube", ".ssh", ".gnupg", ".netrc", ".config/gcloud", ".config/gh", ".cargo", ".gradle", ".pypirc", ".git-credentials"}

// lockFiles name what pins the versions a dependency install resolved.
var lockFiles = []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "poetry.lock", "pipfile.lock", "cargo.lock", "go.sum",
	"gemfile.lock", "composer.lock", "gradle.lockfile", "uv.lock", "pdm.lock", "mix.lock", "pubspec.lock", "packages.lock.json", "bun.lockb", "conda-lock.yml"}

// dependencyHints say a cache path holds downloaded dependencies.
var dependencyHints = []string{".m2", ".gradle", ".npm", "node_modules", ".cache/pip", ".cache/yarn", ".yarn", ".pnpm-store", ".cargo", "go/pkg/mod", "pkg/mod",
	".venv", "venv", "vendor", ".bundle", ".nuget", ".ivy2", ".sbt", ".cache/uv", ".cache/poetry"}

func cleanCachePath(p string) string {
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "~/")
	for _, suf := range []string{"/**", "/*", "/"} {
		p = strings.TrimSuffix(p, suf)
	}
	return p
}

// hasSegment reports whether a path has the segment (or the segments, for a
// name with a slash).
func hasSegment(p, name string) bool {
	return p == name || strings.HasPrefix(p, name+"/") || strings.HasSuffix(p, "/"+name) || strings.Contains(p, "/"+name+"/")
}

// CacheWarnings returns what is unsafe about the caches of a pipeline, each
// as a plain sentence with the fix, in the order of the steps.
func CacheWarnings(p *Pipeline) Problems {
	var out Problems
	for _, s := range p.Steps {
		for i, c := range s.Cache {
			where := fmt.Sprintf("steps.%s.cache[%d]", s.ID, i)
			add := func(code, format string, a ...any) {
				out = append(out, Problem{Line: c.Line, Path: where, Code: code, Message: fmt.Sprintf(format, a...)})
			}
			warnCache(s, c, add)
		}
	}
	return out
}

// pathKinds says what the paths of a cache hold.
type pathKinds struct {
	outputs, homes, creds []string
	dependency            bool
}

func classifyPaths(paths []string) pathKinds {
	var k pathKinds
	for _, raw := range paths {
		p := cleanCachePath(raw)
		for _, seg := range strings.Split(p, "/") {
			if outputDirs[seg] {
				k.outputs = append(k.outputs, raw)
				break
			}
		}
		if strings.Contains(p, "build-cache") {
			k.outputs = append(k.outputs, raw)
		}
		for _, h := range toolHomes {
			if p == h || path.Base(p) == h && !strings.Contains(path.Dir(p), "repository") {
				k.homes = append(k.homes, raw)
				break
			}
		}
		for _, h := range dependencyHints {
			if hasSegment(p, h) {
				k.dependency = true
			}
		}
		if holdsCredentials(p) {
			k.creds = append(k.creds, raw)
		}
	}
	return k
}

// holdsCredentials reports whether a cleaned path is, or is inside, a place
// where tools leave credentials, other than where they keep downloads.
func holdsCredentials(p string) bool {
	if p == "." || p == "*" || p == "**" || p == "" {
		return true
	}
	for _, d := range credentialDirs {
		if p == d || strings.HasPrefix(p, d+"/") && !downloadsOnly(d, strings.TrimPrefix(p, d+"/")) {
			return true
		}
	}
	return false
}

// lockedKey reports whether the key is made of a file that pins versions.
func lockedKey(c *Cache) bool {
	for _, f := range c.Key.Files {
		base := strings.ToLower(path.Base(f))
		for _, l := range lockFiles {
			if base == l || strings.HasSuffix(base, l) {
				return true
			}
		}
		if strings.HasPrefix(base, "requirements") && strings.Contains(base, "lock") {
			return true
		}
	}
	return false
}

func warnCache(s *Step, c *Cache, add func(code, format string, a ...any)) {
	refreshes := strings.Contains(s.Run, " -U") || strings.Contains(s.Run, "--update-snapshots") || strings.Contains(s.Run, "--refresh-dependencies") ||
		strings.Contains(s.Run, "lastUpdated")
	k := classifyPaths(c.Paths)
	name := "cache " + c.Name
	if len(k.outputs) > 0 {
		add(CodeCacheOutputs, "%s keeps build output (%s). What a build wrote depends on the commit that made it: restored into another commit it is stale output, and tools that decide by file time or name may keep it. Cache what is downloaded instead (the package manager's own download cache, such as .npm, .cache/pip or .m2/repository) and let the build recreate its output",
			name, strings.Join(dedupe(k.outputs), ", "))
	}
	if len(k.homes) > 0 && !refreshes {
		add(CodeCacheWholeHome, "%s keeps all of %s. A tool's whole home holds not only downloads but its record of what it asked for and what was not found (Maven's maven-metadata and .lastUpdated files, Gradle's resolution state), so a snapshot or a version published since can be missed. Cache only the downloaded artifacts (.m2/repository), and either run Maven with -U (Gradle with --refresh-dependencies) or delete the *.lastUpdated and maven-metadata-*.xml files before the cache is saved",
			name, strings.Join(dedupe(k.homes), ", "))
	}
	if k.dependency && !lockedKey(c) {
		have := "no files"
		if len(c.Key.Files) > 0 {
			have = strings.Join(c.Key.Files, ", ")
		}
		add(CodeCacheNoLock, "%s keeps downloaded dependencies and its key is made of %s, none of them a lock file. A manifest does not say which versions were resolved, so the entry is kept while a version it would pick today is different. Put a lock file in key.files (package-lock.json, yarn.lock, poetry.lock, Cargo.lock, go.sum, ...) and install from it (npm ci, pip install -r a pinned file); where the tool has none, as Maven has not, set a short max_age so the cache is rebuilt",
			name, have)
	}
	if c.Fallback == CacheFallbackLatest && c.MaxAge == 0 {
		add(CodeCacheFallback, "%s uses fallback: latest without max_age. A fallback starts from the newest entry of the name, however its key differs, and what was saved from it is the next fallback: it grows, and keeps what dependencies dropped, until the cache policy's maximum age ends the chain. Set max_age (for example 7d) so that the cache is rebuilt from nothing on a schedule you chose", name)
	}
	if len(k.creds) > 0 {
		add(CodeCacheCredentials, "%s keeps %s, where tools commonly leave credentials (a settings.xml, .npmrc, docker or cloud login). A cache is restored by every run that may read it, so a credential in one reaches them all; the cache is not saved at all if a file that looks like one is found. Narrow the path to the downloads (.m2/repository, .npm/_cacache) and use short-lived credentials that are not written to the workspace",
			name, strings.Join(dedupe(k.creds), ", "))
	}
	if c.Save == CacheSaveAlways {
		add(CodeCacheSaveAlways, "%s has save: always, so what a failed step left, possibly half-installed or corrupt, is saved and restored by the next run. Use the default save: success unless a failed run's downloads are known to be worth keeping", name)
	}
}

// downloadsOnly reports whether a path below a tool's directory is only where
// the tool keeps what it downloaded.
func downloadsOnly(dir, rest string) bool {
	for _, ok := range map[string][]string{".m2": {"repository"}, ".gradle": {"caches", "build-cache", "wrapper"}, ".cargo": {"registry", "git"}}[dir] {
		if rest == ok || strings.HasPrefix(rest, ok+"/") {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
