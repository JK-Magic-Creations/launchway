package imagebuild

import (
	"regexp"
	"strings"
)

// The registry cache of image steps is kept per trust scope, as the caches
// of script steps are. A cache that any run that pushes may write, and every
// build reads, lets a branch or a pull request leave layers that the default
// branch's release build then reuses: the layers of a RUN instruction are
// reused by the text of the instruction, whatever produced them.
//
//	<repository>:buildcache[-<platform>]           protected: written only by a
//	                                               default-branch run that pushes
//	<repository>:buildcache-<scope>[-<platform>]   a branch's, tag's or pull
//	                                               request's own
//
// A run imports its own scope's cache and the protected one (a fork's pull
// request, the least trusted of all, imports only its own), and exports only
// to its own scope. The server names the scope; the runner cannot choose it.

var scopeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

// cacheRefs works out which cache references a build imports and which it
// exports to (empty for none). repo is host/repository followed by a colon.
func cacheRefs(repo, scope string, noProtected bool, suffix string, write bool) (from []string, to string) {
	if scope != "" && !scopeRe.MatchString(scope) {
		// Not a name the server would make: use no scope's cache at all.
		return nil, ""
	}
	protected := repo + "buildcache" + suffix
	own := protected
	if scope != "" {
		own = repo + "buildcache-" + scope + suffix
		from = append(from, own)
	}
	if scope == "" || !noProtected {
		from = append(from, protected)
	}
	if write {
		to = own
	}
	return from, to
}

// cacheNote says in the build log what the cache is doing.
func cacheNote(from []string, to, scope string) string {
	who := "the protected cache (default branch)"
	if scope != "" {
		who = "the cache of scope " + scope
	}
	var b strings.Builder
	b.WriteString("Image cache: importing " + strings.Join(from, ", "))
	if to != "" {
		b.WriteString("; exporting to " + to + " (" + who + ")")
	} else {
		b.WriteString("; not exporting (this run may only read)")
	}
	b.WriteString(". An import that fails (nothing there yet, or the registry refuses it) is skipped and the build runs without it; an export that fails does not fail the build (ignore-error), and is said below.")
	return b.String()
}

// cacheTrouble reports whether a line of the builder's progress output is
// about a cache that failed to import or export.
func cacheTrouble(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "cache") && (strings.Contains(l, "error") || strings.Contains(l, "failed") || strings.Contains(l, "ignoring"))
}
