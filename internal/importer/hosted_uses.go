package importer

import (
	"strings"
)

// The marketplace steps that are mapped. A step's `uses:` names an action as
// owner/name@version; only the owner/name matters here.

type setupKind struct {
	imageFmt            string // image with %s for the version
	versionKey, fileKey string
}

var setups = map[string]setupKind{
	"actions/setup-go":     {"docker.io/library/golang:%s", "go-version", "go-version-file"},
	"actions/setup-node":   {"docker.io/library/node:%s", "node-version", "node-version-file"},
	"actions/setup-python": {"docker.io/library/python:%s", "python-version", "python-version-file"},
	"actions/setup-java":   {"docker.io/library/eclipse-temurin:%s-jdk", "java-version", "java-version-file"},
	"actions/setup-dotnet": {"mcr.microsoft.com/dotnet/sdk:%s", "dotnet-version", "global-json-file"},
	"ruby/setup-ruby":      {"docker.io/library/ruby:%s", "ruby-version", ".ruby-version"},
}

// equivalents are third-party actions whose job is one command.
var equivalents = map[string]string{
	"golangci/golangci-lint-action": "golangci-lint run",
	"aquasecurity/trivy-action":     "trivy fs .",
	"hadolint/hadolint-action":      "hadolint Dockerfile",
	"pre-commit/action":             "pre-commit run --all-files",
	"gradle/actions/setup-gradle":   "",
}

func actionName(u string) string {
	if i := strings.IndexByte(u, '@'); i >= 0 {
		u = u[:i]
	}
	return u
}

func (h *hostedReader) uses(hj *hostedJob, s *N, ref, cond string) {
	j := hj.j
	act := actionName(ref)
	with := s.Get("with")
	wv := func(k string) string { return h.xlate(hj, with.Get(k).Str(), with.LineOf(k)) }
	line := s.LineOf("uses")
	switch {
	case act == "actions/checkout":
		var opts []string
		for _, k := range []string{"path", "ref", "fetch-depth", "submodules", "token", "repository"} {
			if with.Has(k) {
				opts = append(opts, k)
			}
		}
		if len(opts) > 0 {
			h.rp.note("hosted.uses.checkout_opts", line, strings.Join(opts, ", "))
		} else {
			h.rp.note("hosted.uses.checkout", line)
		}
	case setups[act].imageFmt != "":
		sk := setups[act]
		ver := wv(sk.versionKey)
		if ver == "" && with.Has(sk.fileKey) {
			h.rp.note("hosted.uses.setup_file", line, "the version file "+with.Get(sk.fileKey).Str())
			ver = "latest"
			if act == "actions/setup-go" {
				ver = "1"
			}
		}
		if ver == "" {
			ver = "latest"
			if act == "actions/setup-go" {
				ver = "latest"
			}
		}
		// Ranges and x versions are written as the nearest tag.
		if strings.ContainsAny(ver, "<>=~^") || strings.HasSuffix(ver, ".x") {
			ver = strings.TrimSuffix(strings.Trim(ver, "<>=~^ "), ".x")
			h.rp.note("hosted.uses.setup_file", line, "the range was written as "+ver)
		}
		img := strings.Replace(sk.imageFmt, "%s", ver, 1)
		if hj.setupIm != "" {
			h.rp.note("hosted.uses.setup_second", line, act)
			return
		}
		hj.setupIm = img
		hj.setups++
		if with.Get(sk.versionKey).Str() != "" || ver != "latest" {
			h.rp.note("hosted.uses.setup", line, img)
		}
		if c := with.Get("cache").Str(); c != "" {
			h.addToolCache(hj, c, line)
		}
	case act == "actions/cache" || act == "actions/cache/restore" || act == "actions/cache/save":
		h.cacheStep(hj, with, line, act)
	case act == "actions/upload-artifact":
		name := wv("name")
		if name == "" {
			name = "artifact"
		}
		a := Artifact{Name: sanitizeArtifact(name), When: "success"}
		for _, p := range strings.Split(wv("path"), "\n") {
			if p = strings.TrimSpace(p); p != "" {
				a.Paths = append(a.Paths, strings.TrimPrefix(p, "./"))
			}
		}
		if d := wv("retention-days"); d != "" {
			a.Retain = d + "d"
		}
		if cond == "always()" || strings.Contains(cond, "always()") {
			a.When = "always"
		} else if strings.Contains(cond, "failure()") {
			a.When = "failure"
		}
		j.Artifacts = append(j.Artifacts, a)
		h.rp.note("hosted.uses.upload", line)
	case act == "actions/download-artifact":
		f := Fetch{Name: sanitizeArtifact(wv("name")), Into: strings.TrimPrefix(wv("path"), "./")}
		if wv("name") == "" {
			f.Name = "artifact"
		}
		j.Fetch = append(j.Fetch, f)
		h.rp.note("hosted.uses.download", line)
	case act == "docker/build-push-action":
		h.buildPush(hj, with, line)
	case act == "docker/setup-buildx-action" || act == "docker/setup-qemu-action":
		h.rp.note("hosted.uses.buildx", line)
	case act == "docker/login-action":
		reg := wv("registry")
		if reg == "" {
			reg = "the default registry"
		}
		h.rp.note("hosted.uses.login", line, reg)
	case act == "docker/metadata-action":
		h.rp.note("hosted.uses.metadata", line)
	default:
		if cmd, ok := equivalents[act]; ok && cmd != "" {
			j.Steps = append(j.Steps, Step{Name: ref, Run: cmd, Line: line})
			h.rp.note("hosted.uses.equiv", line, act, cmd)
			return
		}
		h.rp.note("hosted.uses.third", line, ref).Step = j.ID
		j.Steps = append(j.Steps, Step{Name: ref, Run: "echo 'NEEDS A PERSON: a third-party action ran here: " + strings.ReplaceAll(oneLine(ref, 100), "'", "") + "'; exit 1", Line: line})
	}
}

func sanitizeArtifact(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, s)
	s = strings.TrimLeft(s, ".-_")
	if s == "" {
		s = "artifact"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// addToolCache handles setup-X's cache: npm|pip|yarn|... input.
func (h *hostedReader) addToolCache(hj *hostedJob, tool string, line int) {
	switch tool {
	case "npm":
		hj.j.Caches = append(hj.j.Caches, Cache{Name: "npm", Paths: []string{".cache/npm"}, KeyFiles: []string{"**/package-lock.json"}})
		hj.j.Env = appendOnce(hj.j.Env, KV{"npm_config_cache", ".cache/npm"})
	case "pip":
		hj.j.Caches = append(hj.j.Caches, Cache{Name: "pip", Paths: []string{".cache/pip"}, KeyFiles: []string{"**/requirements*.txt"}})
		hj.j.Env = appendOnce(hj.j.Env, KV{"PIP_CACHE_DIR", ".cache/pip"})
	case "yarn":
		hj.j.Caches = append(hj.j.Caches, Cache{Name: "yarn", Paths: []string{".cache/yarn"}, KeyFiles: []string{"**/yarn.lock"}})
		hj.j.Env = appendOnce(hj.j.Env, KV{"YARN_CACHE_FOLDER", ".cache/yarn"})
	case "maven":
		hj.j.Caches = append(hj.j.Caches, Cache{Name: "maven", Paths: []string{".cache/m2"}, KeyFiles: []string{"**/pom.xml"}})
		hj.j.Env = appendOnce(hj.j.Env, KV{"MAVEN_OPTS", "-Dmaven.repo.local=.cache/m2"})
	case "gradle":
		hj.j.Caches = append(hj.j.Caches, Cache{Name: "gradle", Paths: []string{".cache/gradle"}, KeyFiles: []string{"**/*.gradle*", "**/gradle-wrapper.properties"}})
		hj.j.Env = appendOnce(hj.j.Env, KV{"GRADLE_USER_HOME", ".cache/gradle"})
	default:
		return
	}
	h.rp.note("hosted.uses.cache", line, tool)
}

func appendOnce(l []KV, kv KV) []KV {
	for _, x := range l {
		if x.K == kv.K {
			return l
		}
	}
	return append(l, kv)
}

func (h *hostedReader) cacheStep(hj *hostedJob, with *N, line int, act string) {
	key := with.Get("key").Str()
	c := Cache{}
	var files []string
	text := exprRe.ReplaceAllStringFunc(key, func(m string) string {
		e := exprRe.FindStringSubmatch(m)[1]
		if strings.HasPrefix(e, "hashFiles(") {
			for _, a := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(e, "hashFiles("), ")"), ",") {
				files = append(files, strings.Trim(strings.TrimSpace(a), `'"`))
			}
		}
		return ""
	})
	c.KeyFiles = files
	text = strings.Trim(strings.NewReplacer("--", "-").Replace(text), "-_ ")
	c.KeyText = text
	var shown []string
	for _, p := range strings.Split(with.Get("path").Str(), "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if np, ok := HomeCachePath(p); ok {
			c.Paths = append(c.Paths, np)
			shown = append(shown, np)
			c.Name = nameFromPath(np)
			hj.homePaths = append(hj.homePaths, p)
			continue
		}
		if strings.HasPrefix(p, "~") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "..") {
			h.rp.note("hosted.uses.cache_path", with.LineOf("path"), p)
			continue
		}
		c.Paths = append(c.Paths, strings.TrimPrefix(p, "./"))
		shown = append(shown, p)
	}
	if len(c.Paths) == 0 {
		return
	}
	if c.Name == "" {
		c.Name = nameFromPath(c.Paths[0])
	}
	if len(c.KeyFiles) == 0 && c.KeyText == "" {
		c.KeyText = c.Name
	}
	for i := range hj.j.Caches {
		if hj.j.Caches[i].Name == c.Name {
			return // restore and save halves
		}
	}
	hj.j.Caches = append(hj.j.Caches, c)
	for _, hp := range hj.homePaths {
		if e := homeCacheExports[hp]; e != "" {
			k, v := splitExport(e)
			hj.j.Env = appendOnce(hj.j.Env, KV{k, v})
		}
	}
	h.rp.note("hosted.uses.cache", line, strings.Join(shown, ", "))
}

func nameFromPath(p string) string {
	p = strings.Trim(p, "./*")
	p = strings.ReplaceAll(p, "/", "-")
	p = strings.ReplaceAll(p, ".", "")
	if p == "" {
		return "cache"
	}
	if len(p) > 40 {
		p = p[:40]
	}
	return strings.ToLower(p)
}

// splitExport reads `export NAME="value"` written for a script, as an env pair relative to the workspace.
func splitExport(e string) (string, string) {
	e = strings.TrimPrefix(e, "export ")
	k, v, _ := strings.Cut(e, "=")
	v = strings.Trim(v, `"`)
	v = strings.ReplaceAll(v, "$PWD/", "")
	v = strings.ReplaceAll(v, "$MAVEN_OPTS ", "")
	return k, v
}

func (h *hostedReader) buildPush(hj *hostedJob, with *N, line int) {
	j := hj.j
	ib := &ImageBuild{Context: with.Get("context").Str(), File: with.Get("file").Str(), Target: with.Get("target").Str()}
	if ib.Context == "" {
		ib.Context = "."
	}
	push, _ := with.Get("push").Bool()
	ib.Push = push
	ib.Platforms = append(ib.Platforms, strings.FieldsFunc(with.Get("platforms").Str(), func(r rune) bool { return r == ',' || r == '\n' || r == ' ' })...)
	for _, t := range strings.Split(with.Get("tags").Str(), "\n") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		repo, tag := t, "latest"
		if i := strings.LastIndex(t, ":"); i > strings.LastIndex(t, "/") {
			repo, tag = t[:i], t[i+1:]
		}
		repo = h.xlate(hj, repo, with.LineOf("tags"))
		tagx := h.xlate(hj, tag, with.LineOf("tags"))
		if tagx == "${LAUNCHWAY_COMMIT}" {
			tagx = "{sha}"
		}
		if strings.Contains(tagx, "${") || strings.Contains(tagx, "{{") {
			h.rp.note("hosted.uses.metadata", with.LineOf("tags"))
			continue
		}
		if parts := strings.Split(repo, "/"); len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
			repo = strings.Join(parts[1:], "/")
		}
		if strings.Contains(repo, "${") {
			h.rp.note("hosted.uses.metadata", with.LineOf("tags"))
			continue
		}
		ib.Repo = repo
		ib.Tags = append(ib.Tags, tagx)
	}
	for _, l := range strings.Split(with.Get("build-args").Str(), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && !strings.Contains(v, "${{") {
			ib.BuildArgs = append(ib.BuildArgs, KV{k, v})
		} else if ok {
			if x := h.xlate(hj, v, with.LineOf("build-args")); x == "${LAUNCHWAY_COMMIT}" {
				ib.BuildArgs = append(ib.BuildArgs, KV{k, "{sha}"})
			}
		}
	}
	if ib.Push && len(ib.Tags) == 0 {
		ib.Push = false
		h.rp.note("hosted.uses.metadata", line)
	}
	j.Image2 = ib
	h.rp.note("hosted.uses.buildpush", line)
}
