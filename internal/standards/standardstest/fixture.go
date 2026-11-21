// Package standardstest writes small repositories with planted problems,
// for the tests of Standing orders and for demonstrations. Nothing here is a
// real credential: the "secret" is a made-up token assembled when the
// files are written, so the source of this package holds nothing a secret
// scanner would report.
package standardstest

import (
	"os"
	"path/filepath"
	"strings"
)

// Planted says what each fixture file holds, for the tests to look for.
var Planted = []struct{ Path, What string }{
	{"config/settings.env", "a secret: a made-up GitHub token"},
	{"go/go.mod", "a vulnerable dependency: golang.org/x/net 0.17.0"},
	{"go/main.go", "insecure code: a weak hash and a command built from a request"},
	{"py/requirements.txt", "vulnerable dependencies: flask 0.12.2, pyyaml 5.3"},
	{"py/app.py", "insecure code: a shell command and unpickling"},
	{"js/package-lock.json", "a vulnerable dependency: lodash 4.17.15"},
	{"Dockerfile", "a bad Dockerfile: latest tag, root, a download piped into a shell"},
	{"k8s/deploy.yaml", "a misconfigured manifest: a privileged container with a latest image"},
	{"infra/main.tf", "misconfigured infrastructure: a public bucket"},
	{"sh/run.sh", "a shell script with quoting mistakes"},
	{".launchway/pipeline.yaml", "unsafe pipeline: an image tag, a plain secret, a download piped into a shell"},
}

// token is assembled here so that this file never holds it whole.
func token() string {
	return "ghp_" + "aB3dE5gH7jK9mN1pQ3sT5vX7zA9cE1gH3jK5"
}

var files = map[string]string{
	"config/settings.env": "# deployment settings\nGITHUB_TOKEN=" + "@TOKEN@" + "\n",
	"go/go.mod":           "module example.com/fixture\n\ngo 1.22\n\nrequire golang.org/x/net v0.17.0\n",
	"go/go.sum": "golang.org/x/net v0.17.0 h1:pVaXccu2ozPjCXewfr1S7xza/zcXTity9cCdXQYSjIM=\n" +
		"golang.org/x/net v0.17.0/go.mod h1:NxSsAGuq816PNPmqtQdLE42eU2Fs7NoRIZrHJAlaCOE=\n",
	"go/main.go": `package main

import (
	"crypto/md5"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"golang.org/x/net/html"
)

func main() {
	fmt.Println(md5.New())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_ = exec.Command("sh", "-c", r.URL.Query().Get("c")).Run()
	})
	_, _ = html.Parse(strings.NewReader("<p>"))
	_ = http.ListenAndServe(":8080", nil)
}
`,
	"py/requirements.txt": "flask==0.12.2\npyyaml==5.3\n",
	"py/app.py": `import subprocess, hashlib, pickle


def run(cmd):
    subprocess.call(cmd, shell=True)
    return hashlib.md5(b"x").hexdigest()


def load(b):
    return pickle.loads(b)
`,
	"js/package.json":      `{"name":"fixture-js","version":"1.0.0","dependencies":{"lodash":"4.17.15"}}` + "\n",
	"js/package-lock.json": `{"name":"fixture-js","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"fixture-js","version":"1.0.0","dependencies":{"lodash":"4.17.15"}},"node_modules/lodash":{"version":"4.17.15","resolved":"https://registry.npmjs.org/lodash/-/lodash-4.17.15.tgz","integrity":"sha512-8xOcRHvCjnocdS5cpwXQXVzmmh5e5+saE2QGoeQmbKmRS6J3VQppPOIt0MutmVzpXmDVwDnv1b2Hzmqpwxe0QQ==","license":"MIT"}}}` + "\n",
	"Dockerfile":           "FROM golang:latest\nRUN curl http://example.com/install.sh | sh\nADD . /app\nUSER root\n",
	"k8s/deploy.yaml": `apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
spec:
  replicas: 1
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers:
      - name: web
        image: nginx:latest
        securityContext: {privileged: true}
`,
	"infra/main.tf": `resource "aws_s3_bucket" "logs" {
  bucket = "fixture-logs"
  acl    = "public-read"
}
`,
	"sh/run.sh": "#!/bin/bash\nfor f in $(ls *.txt); do cat $f; done\nrm -rf $DIR/*\n",
	".launchway/pipeline.yaml": `pipeline: fixture
on: { push: { branches: [main] }, pull_request: {} }
steps:
  build:
    image: alpine:latest
    env:
      API_TOKEN: @PLAIN@
    run: curl https://example.com/get.sh | sh
`,
}

// Write puts the fixture repository's files in dir. Only the named
// top-level parts are written (for example "go", "py", "Dockerfile"); with
// none, everything is.
func Write(dir string, only ...string) error {
	for name, body := range files {
		if len(only) > 0 && !wanted(name, only) {
			continue
		}
		body = strings.ReplaceAll(strings.ReplaceAll(body, "@TOKEN@", token()), "@PLAIN@", "abcd1234"+"efgh5678ijkl")
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func wanted(name string, only []string) bool {
	for _, o := range only {
		if name == o || strings.HasPrefix(name, o+"/") {
			return true
		}
	}
	return false
}

// Paths lists the fixture's files, repository-relative.
func Paths() []string {
	var out []string
	for n := range files {
		out = append(out, n)
	}
	return out
}
