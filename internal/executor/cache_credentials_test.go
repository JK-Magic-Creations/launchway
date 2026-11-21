package executor

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCredentialFilesAreFoundByNameAndByContent(t *testing.T) {
	dir := t.TempDir()
	put := func(p, data string) StagedFile {
		local := filepath.Join(dir, strings.ReplaceAll(p, "/", "_"))
		if err := os.WriteFile(local, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return StagedFile{Path: p, Local: local, Size: int64(len(data))}
	}
	bad := map[string]string{
		".m2/settings.xml":        "<settings><servers><server><id>r</id><password>s3cret</password></server></servers></settings>",
		"home/.npmrc":             "//registry.example.com/:_authToken=abc123\n",
		".netrc":                  "machine x login y password z",
		"x/.pypirc":               "[pypi]\npassword = hunter2\n",
		".config/pip/pip.conf":    "[global]\nindex-url = https://user:pw@pypi.example.com/simple\n",
		".docker/config.json":     `{"auths":{"r.example.com":{"auth":"dXNlcjpwdw=="}}}`,
		".git-credentials":        "https://u:p@example.com",
		"keys/id_rsa":             "x",
		".aws/credentials":        "[default]\naws_secret_access_key = abc\n",
		".kube/config":            "users:\n- user:\n    token: abc\n",
		"srv/.env":                "A=1",
		"gradle.properties":       "systemProp.x=1\nrepoPassword=abc\n",
		"certs/server.pem":        "-----BEGIN RSA PRIVATE KEY-----\nabc\n",
		".cargo/credentials.toml": "[registry]\ntoken = \"abc\"\n",
		"deploy/prod.tfvars":      "x = 1",
	}
	ok := map[string]string{
		".m2/settings.xml":      "<settings><mirrors><mirror><id>m</id></mirror></mirrors></settings>",
		".m2/settings.xml.tmpl": "<password>x</password>",
		"home/.npmrc":           "registry=https://registry.npmjs.org/\n//registry.example.com/:_authToken=${NPM_TOKEN}\n",
		".docker/config.json":   `{"auths":{},"credsStore":""}`,
		"certs/ca.pem":          "-----BEGIN CERTIFICATE-----\nabc\n",
		"id_ed25519.pub":        "ssh-ed25519 AAAA",
		".env.example":          "A=",
		"src/main.go":           "package main",
		"gradle.properties":     "org.gradle.jvmargs=-Xmx2g\n",
		"x/pip.conf":            "[global]\ntimeout = 60\n",
	}
	var files []StagedFile
	got := map[string]bool{}
	for p, d := range bad {
		files = append(files, put(p, d))
	}
	for _, f := range CredentialFiles(files) {
		got[f.Path] = true
		if f.Why == "" {
			t.Errorf("%s has no reason", f.Path)
		}
	}
	var missed []string
	for p := range bad {
		if !got[p] {
			missed = append(missed, p)
		}
	}
	sort.Strings(missed)
	if len(missed) > 0 {
		t.Errorf("credentials not found: %v", missed)
	}
	files = nil
	for p, d := range ok {
		files = append(files, put("ok/"+p, d)) // another directory than the bad ones: names are shared
	}
	for _, f := range CredentialFiles(files) {
		// ok/ prefixed paths keep their meaning for the rules by base name and directory.
		t.Errorf("%s was taken for a credential: %s", f.Path, f.Why)
	}
}
