package importer

import (
	"regexp"
	"strings"
)

// A job plainly is the standard job of a language when its image is that
// language's image and everything it runs is on the short list for that
// template. Anything else is written as explicit steps.

var verRe = regexp.MustCompile(`:([0-9][0-9.]*)`)

func versionOf(image string) string {
	if m := verRe.FindStringSubmatch(image); m != nil {
		return strings.TrimRight(m[1], ".")
	}
	return ""
}

func detectTemplate(j *Job) {
	if j.Matrix != nil || len(j.Services) > 0 || len(j.SecretEnv) > 0 || len(j.Artifacts) > 0 || len(j.Fetch) > 0 ||
		j.Image2 != nil || len(j.Env) > 0 || j.AllowFail || len(j.Steps) == 0 || j.Approval != nil {
		return
	}
	var cmds []string
	for _, s := range j.Steps {
		if s.Dir != "" || len(s.Env) > 0 || s.Cond != "" || strings.Contains(s.Run, "NEEDS A PERSON") {
			return
		}
		for _, l := range strings.Split(s.Run, "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				cmds = append(cmds, l)
			}
		}
	}
	key := strings.Join(cmds, " && ")
	img := j.Image
	ver := versionOf(img)
	if t := templateFor(img, key, ver); t != nil {
		j.Template = t
	}
}

// templateFor picks the template whose image and command line match, or nil.
func templateFor(img, key, ver string) *TemplateUse {
	set := func(name, vkey string) *TemplateUse {
		t := &TemplateUse{Name: name}
		if ver != "" && vkey != "" {
			t.With = append(t.With, KV{vkey, ver})
		}
		return t
	}
	switch {
	case strings.Contains(img, "/golang:"):
		switch key {
		case "go test ./...":
			return set("go-test", "go_version")
		case "go test -race ./...":
			t := set("go-test", "go_version")
			t.With = append(t.With, KV{"race", "true"})
			return t
		case "go build ./...":
			return set("go-build", "go_version")
		}
	case strings.Contains(img, "/node:"):
		switch key {
		case "npm ci && npm test", "npm ci && npm run test", "npm install && npm test":
			return set("node-test", "node_version")
		case "npm ci && npm run build":
			return set("node-build", "node_version")
		}
	case strings.Contains(img, "/python:"):
		switch key {
		case "pip install -r requirements.txt && pytest", "pytest", "pip install -r requirements.txt && python -m pytest":
			return set("python-test", "python_version")
		}
	case strings.Contains(img, "/eclipse-temurin:"), strings.Contains(img, "/maven:"):
		switch key {
		case "mvn -B verify", "mvn verify":
			if v := strings.TrimSuffix(strings.TrimPrefix(versionOf(img), ""), "-jdk"); v != "" {
				return &TemplateUse{Name: "maven-verify", With: []KV{{"java_version", v}}}
			}
		case "./gradlew build", "gradle build":
			return set("gradle-build", "java_version")
		}
	}
	return nil
}

// betterJavaImage swaps a bare JDK image for one that has the build tool the
// job's commands call.
func betterJavaImage(j *Job) string {
	img := j.Image
	if !strings.Contains(img, "/eclipse-temurin:") {
		return img
	}
	ver := strings.TrimSuffix(versionOf(img), "-jdk")
	if ver == "" {
		return img
	}
	var all strings.Builder
	for _, s := range j.Steps {
		all.WriteString(s.Run + "\n")
	}
	switch {
	case strings.Contains(all.String(), "mvn "):
		return "docker.io/library/maven:3.9-eclipse-temurin-" + ver
	case regexp.MustCompile(`(^|\s)gradle\s`).MatchString(all.String()):
		return "docker.io/library/gradle:8-jdk" + ver
	}
	return img
}
