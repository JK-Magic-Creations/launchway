package pipeline

import (
	"strings"
	"testing"
)

func warningsOf(t *testing.T, cache, run string) Problems {
	t.Helper()
	p, err := Parse([]byte("pipeline: p\nsteps:\n  build:\n    image: x\n    cache:\n" + cache + "    run: " + run + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return CacheWarnings(p)
}

func codes(ps Problems) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Code)
	}
	return strings.Join(out, " ")
}

// Each unsafe pattern the audit found is flagged, with a sentence that says
// what is wrong and what to do, and a safe cache is not.
func TestCacheWarnings(t *testing.T) {
	for name, tc := range map[string]struct {
		cache, run, want string
	}{
		"build output":          {"      - { name: o, paths: [target, build/classes], key: { files: [Cargo.lock] } }\n", "make", CodeCacheOutputs},
		"node_modules":          {"      - { name: n, paths: [node_modules], key: { files: [package-lock.json] } }\n", "npm ci", CodeCacheOutputs},
		"pycache":               {"      - { name: n, paths: [src/__pycache__], key: { text: v1 } }\n", "make", CodeCacheOutputs},
		"gradle build cache":    {"      - { name: n, paths: [.gradle/build-cache], key: { files: [gradle.lockfile] } }\n", "make", CodeCacheOutputs},
		"whole .m2":             {"      - { name: m, paths: [.m2], key: { files: [pom.xml] } }\n", "mvn test", CodeCacheWholeHome + " " + CodeCacheNoLock + " " + CodeCacheCredentials},
		"whole .m2, with -U":    {"      - { name: m, paths: [.m2], key: { files: [pom.xml] } }\n", "mvn -U test", CodeCacheNoLock + " " + CodeCacheCredentials},
		"manifest only":         {"      - { name: m, paths: [.m2/repository], key: { files: [pom.xml] } }\n", "mvn test", CodeCacheNoLock},
		"no key files":          {"      - { name: n, paths: [.npm], key: { text: v1 } }\n", "npm ci", CodeCacheNoLock},
		"fallback without age":  {"      - { name: n, paths: [.npm], key: { files: [package-lock.json] }, fallback: latest }\n", "npm ci", CodeCacheFallback},
		"credential path":       {"      - { name: n, paths: [.docker, .aws/cache], key: { text: v1 } }\n", "make", CodeCacheCredentials},
		"save always":           {"      - { name: n, paths: [.npm], key: { files: [package-lock.json] }, save: always }\n", "npm ci", CodeCacheSaveAlways},
		"safe npm":              {"      - { name: n, paths: [.npm], key: { files: [package-lock.json] } }\n", "npm ci", ""},
		"safe maven repository": {"      - { name: m, paths: [.m2/repository], key: { files: [pom.xml], versions: [java -version] }, max_age: 3d }\n", "mvn -U test", CodeCacheNoLock},
		"safe fallback":         {"      - { name: n, paths: [.cache/pip], key: { files: [poetry.lock] }, fallback: latest, max_age: 7d }\n", "pip install", ""},
	} {
		ws := warningsOf(t, tc.cache, tc.run)
		if got := codes(ws); got != tc.want {
			t.Errorf("%s: got %q, want %q: %v", name, got, tc.want, ws)
		}
		for _, w := range ws {
			if w.Path != "steps.build.cache[0]" || w.Line == 0 || len(w.Message) < 80 {
				t.Errorf("%s: a warning without a place or a message: %+v", name, w)
			}
		}
	}
}
