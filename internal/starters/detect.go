package starters

import (
	"regexp"
	"strings"
)

// Detection is the flight plan that fits a repository, and why.
type Detection struct {
	Flight string `json:"flight"` // a template of the library: go-service
	// Because says what was found, as a clause: "go.mod was found".
	Because string `json:"because"`
	// With are values of the flight plan's parameters worked out from the
	// repository's own files, such as the Go version in go.mod; empty when
	// the defaults fit.
	With map[string]string `json:"with,omitempty"`
	// Notes are things the person should know, one sentence each.
	Notes []string `json:"notes,omitempty"`
}

// Detect chooses a flight plan from the files of a repository. files are
// the repository's paths relative to its root (only the first two levels
// matter) and read returns the text of a small file, or "" if there is none.
// It returns nil when nothing fits: the repository then has no pipeline, as
// before, and nothing guesses.
//
// Order matters: a language wins over a Dockerfile (a Go service with a
// Dockerfile is go-service), and a repository whose only toolchain is
// Docker is container-image.
func Detect(files []string, read func(string) string) *Detection {
	top := map[string]bool{}
	for _, f := range files {
		if !strings.Contains(f, "/") {
			top[f] = true
		}
	}
	has := func(names ...string) string {
		for _, n := range names {
			if top[n] {
				return n
			}
		}
		return ""
	}
	docker := has("Dockerfile", "Containerfile") != ""
	pick := func(service, library, found string, with map[string]string) *Detection {
		d := &Detection{Because: found + " was found", With: with}
		if docker {
			d.Flight = service
			d.Because += " (and a Dockerfile)"
		} else {
			d.Flight = library
			// A library is tested on the version its own files name.
			for _, k := range []string{"go_version", "python_version", "node_version"} {
				if v := with[k]; v != "" {
					with["test_versions"] = `["` + v + `"]`
				}
			}
		}
		return d
	}
	switch {
	case top["go.mod"]:
		with := map[string]string{}
		if m := goVersionRe.FindStringSubmatch(read("go.mod")); m != nil {
			with["go_version"] = m[1]
		}
		return pick("go-service", "go-library", "go.mod", with)
	case has("uv.lock", "poetry.lock", "pdm.lock", "pyproject.toml", "requirements.txt", "setup.py", "Pipfile") != "":
		with := map[string]string{}
		if v := strings.TrimSpace(read(".python-version")); pyVersionRe.MatchString(v) {
			with["python_version"] = pyVersionRe.FindString(v)
		}
		return pick("python-service", "python-library", has("uv.lock", "poetry.lock", "pyproject.toml", "requirements.txt", "setup.py", "Pipfile"), with)
	case top["package.json"]:
		with := map[string]string{}
		for _, f := range []string{".nvmrc", ".node-version"} {
			if m := nodeVersionRe.FindStringSubmatch(read(f)); m != nil {
				with["node_version"] = m[1]
				break
			}
		}
		if m := nodeEnginesRe.FindStringSubmatch(read("package.json")); m != nil && with["node_version"] == "" {
			with["node_version"] = m[1]
		}
		return pick("node-app", "node-library", "package.json", with)
	case top["pom.xml"]:
		d := pick("java-maven", "java-maven", "pom.xml", javaWith(read("pom.xml")))
		if docker {
			d.With = joinWith(d.With, "image", "true")
		}
		return d
	case has("build.gradle", "build.gradle.kts") != "":
		d := pick("java-gradle", "java-gradle", has("build.gradle", "build.gradle.kts"), nil)
		if docker {
			d.With = joinWith(d.With, "image", "true")
		}
		return d
	case top["Chart.yaml"]:
		return &Detection{Flight: "helm-chart", Because: "Chart.yaml was found"}
	case docker:
		return &Detection{Flight: "container-image", Because: has("Dockerfile", "Containerfile") + " was found and nothing else that builds the codebase"}
	case top["index.html"]:
		return &Detection{Flight: "static-site", Because: "index.html was found and nothing else that builds the codebase"}
	}
	return nil
}

func joinWith(m map[string]string, k, v string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[k] = v
	return m
}

var (
	goVersionRe   = regexp.MustCompile(`(?m)^go\s+([0-9]+\.[0-9]+)`)
	pyVersionRe   = regexp.MustCompile(`^3\.[0-9]{1,2}`)
	nodeVersionRe = regexp.MustCompile(`^v?([0-9]{1,2})`)
	nodeEnginesRe = regexp.MustCompile(`"node"\s*:\s*"[^0-9"]*([0-9]{1,2})`)
	javaRe        = regexp.MustCompile(`<(?:maven\.compiler\.release|java\.version|maven\.compiler\.source)>\s*(?:1\.)?([0-9]{1,2})\s*<`)
)

func javaWith(pom string) map[string]string {
	if m := javaRe.FindStringSubmatch(pom); m != nil {
		return map[string]string{"java_version": m[1]}
	}
	return nil
}

// Wanted lists the paths a detector reads, so a caller can fetch only them.
func Wanted() []string {
	return []string{"go.mod", ".python-version", ".nvmrc", ".node-version", "package.json", "pom.xml"}
}
