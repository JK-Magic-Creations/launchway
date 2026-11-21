package imagebuild

import (
	"strings"
	"testing"
)

// The protected cache is written only by a protected run; every other run
// writes a tag of its own scope, and reads its own and the protected one.
func TestCacheReferencesArePerTrustScope(t *testing.T) {
	const repo = "reg.example.com/team/cache:"
	// A default-branch run: reads and writes the protected tag.
	from, to := cacheRefs(repo, "", false, "", true)
	if strings.Join(from, " ") != repo+"buildcache" || to != repo+"buildcache" {
		t.Fatalf("%v %q", from, to)
	}
	// A pull request: imports its own, then the protected cache; exports only to its own.
	from, to = cacheRefs(repo, "pull-7", false, "", true)
	if strings.Join(from, " ") != repo+"buildcache-pull-7 "+repo+"buildcache" || to != repo+"buildcache-pull-7" {
		t.Fatalf("%v %q", from, to)
	}
	if to == repo+"buildcache" {
		t.Fatal("a pull request writes the protected cache")
	}
	// A fork's pull request does not read the protected cache.
	from, _ = cacheRefs(repo, "pull-9", true, "", true)
	if strings.Join(from, " ") != repo+"buildcache-pull-9" {
		t.Fatalf("%v", from)
	}
	// A run that may only read exports nothing; platforms add a suffix.
	from, to = cacheRefs(repo, "ref-0123456789ab", false, "-linux-arm64", false)
	if to != "" || from[0] != repo+"buildcache-ref-0123456789ab-linux-arm64" {
		t.Fatalf("%v %q", from, to)
	}
	// A scope the server would not make is no scope at all, and is never
	// taken for the protected one.
	if f, to := cacheRefs(repo, "../x", false, "", true); f != nil || to != "" {
		t.Fatalf("%v %q", f, to)
	}
}

func TestCacheNoteSaysWhatHappensWhenAnImportFails(t *testing.T) {
	n := cacheNote([]string{"r/x:buildcache-pull-1", "r/x:buildcache"}, "r/x:buildcache-pull-1", "pull-1")
	for _, want := range []string{"importing r/x:buildcache-pull-1, r/x:buildcache", "exporting to r/x:buildcache-pull-1", "ignore-error", "runs without it"} {
		if !strings.Contains(n, want) {
			t.Errorf("no %q in %q", want, n)
		}
	}
	if !cacheTrouble("#7 ERROR: failed to configure registry cache importer: not found") || cacheTrouble("#3 [2/4] RUN make") {
		t.Error("cacheTrouble")
	}
}
