package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, name, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const runnerYAML = `
server: http://from-file:8080
name: file-name
data: work
concurrency: 6
executor: kubernetes
kubernetes:
  namespace: file-ns
  helper_image: registry.example/busybox:1
  service_account: builder
  node_selector: {pool: builds}
  cpu: 250m
  memory: 512Mi
  memory_limit: 2Gi
  image_pull_secrets: [regcred]
  keep_failed: 30m
  start_timeout: 5m
secrets:
  files: {type: file, dir: secrets}
`

func TestRunnerConfigFile(t *testing.T) {
	t.Setenv("LAUNCHWAY_SERVER", "")
	t.Setenv("LAUNCHWAY_RUNNER_TOKEN", "tok")
	path := writeFile(t, "runner.yaml", runnerYAML)
	o, err := runnerArgs([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	k := o.Kubernetes
	if o.Server != "http://from-file:8080" || o.Name != "file-name" || o.Data != filepath.Join(dir, "work") ||
		o.Concurrency != 6 || o.Executor != "kubernetes" || o.Token != "tok" {
		t.Fatalf("settings %+v", o)
	}
	if k.Namespace != "file-ns" || k.HelperImage != "registry.example/busybox:1" || k.ServiceAccount != "builder" ||
		k.NodeSelector["pool"] != "builds" || k.CPU != "250m" || k.Memory != "512Mi" || k.MemoryLimit != "2Gi" ||
		len(k.ImagePullSecrets) != 1 || k.ImagePullSecrets[0] != "regcred" || k.KeepFailed != "30m" || k.StartTimeout != "5m" {
		t.Fatalf("kubernetes settings %+v", k)
	}
	if s := o.Secrets["files"]; s.Type != "file" || s.Dir != filepath.Join(dir, "secrets") {
		t.Fatalf("secret stores %+v", o.Secrets)
	}
}

// Flags win over the environment, which wins over the file, which wins over
// the defaults.
func TestRunnerSettingsPrecedence(t *testing.T) {
	t.Setenv("LAUNCHWAY_RUNNER_TOKEN", "tok")
	path := writeFile(t, "runner.yaml", runnerYAML)
	stores := writeFile(t, "stores.yaml", "vaulty: {type: env, keys: [A]}\n")

	t.Setenv("LAUNCHWAY_SERVER", "http://from-env:8080")
	o, err := runnerArgs([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if o.Server != "http://from-env:8080" {
		t.Fatalf("server %q: the environment should win over the file", o.Server)
	}

	o, err = runnerArgs([]string{"--config", path, "--server", "http://from-flag:8080", "--name", "flag-name",
		"--concurrency", "1", "--executor", "host", "--k8s-namespace", "flag-ns", "--k8s-context", "kind-x",
		"--secrets-config", stores, "--token", "flag-tok"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Server != "http://from-flag:8080" || o.Name != "flag-name" || o.Concurrency != 1 || o.Executor != "host" || o.Token != "flag-tok" {
		t.Fatalf("flags did not win: %+v", o)
	}
	if o.Kubernetes.Namespace != "flag-ns" || o.Kubernetes.Context != "kind-x" || o.Kubernetes.HelperImage != "registry.example/busybox:1" {
		t.Fatalf("kubernetes: %+v; the flags replace their own settings only", o.Kubernetes)
	}
	if _, ok := o.Secrets["files"]; ok || o.Secrets["vaulty"].Type != "env" {
		t.Fatalf("--secrets-config should replace the file's stores: %+v", o.Secrets)
	}

	t.Setenv("LAUNCHWAY_SERVER", "")
	o, err = runnerArgs([]string{"--server", "http://s"})
	host, _ := os.Hostname()
	if err != nil || o.Name != host || o.Data != "launchway-runner" || o.Concurrency != 2 || o.Executor != "auto" {
		t.Fatalf("defaults: %+v, %v", o, err)
	}
}

func TestRunnerConfigRejectsUnknownSettings(t *testing.T) {
	t.Setenv("LAUNCHWAY_RUNNER_TOKEN", "tok")
	for _, text := range []string{
		"server: http://s\nconcurency: 3\n",             // misspelt
		"server: http://s\nenabled: true\n",             // only for the built-in runner
		"server: http://s\nkubernetes: {namspace: x}\n", // misspelt, nested
		"server: http://s\ntoken: secret\n",             // the token is never in the file
		"server: http://s\nsecrets: {a: {type: env, x: 1}}\n",
	} {
		_, err := runnerArgs([]string{"--config", writeFile(t, "runner.yaml", text)})
		if err == nil || !strings.Contains(err.Error(), "not found in type") {
			t.Errorf("%q: got %v, want an unknown-field error", text, err)
		}
	}
}

func TestRunnerNeedsServerAndToken(t *testing.T) {
	t.Setenv("LAUNCHWAY_SERVER", "")
	t.Setenv("LAUNCHWAY_RUNNER_TOKEN", "")
	if _, err := runnerArgs([]string{"--server", "http://s"}); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("got %v", err)
	}
}
