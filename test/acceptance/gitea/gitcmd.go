package giteaaccept

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// gitEnv is the environment of every Git command: no user or system
// configuration (so no stored credential is ever used), no prompt, a fixed
// author, and the token as an extra header held in the environment.
func (c *Config) gitEnv(auth bool, extra ...string) []string {
	e := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false", "SSH_ASKPASS=/bin/false", "DISPLAY=",
		"GIT_AUTHOR_NAME=lw-acceptance", "GIT_AUTHOR_EMAIL=acceptance@invalid",
		"GIT_COMMITTER_NAME=lw-acceptance", "GIT_COMMITTER_EMAIL=acceptance@invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=protocol.version", "GIT_CONFIG_VALUE_0=2")
	if auth {
		e = append(e, "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_1=http.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: "+c.basic())
	}
	return append(e, extra...)
}

// git runs git in dir with the token (auth) or without, and returns stdout
// and stderr joined. err is the command's error; the output is scrubbed.
func (c *Config) git(dir string, auth bool, args ...string) (string, error) {
	return c.gitWith(dir, c.gitEnv(auth), args...)
}

func (c *Config) gitWith(dir string, env []string, args ...string) (string, error) {
	// A Git command that hangs (a prompt, a stalled host) is killed, with the helpers it started, after three minutes.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second
	cmd.Dir, cmd.Env = dir, env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return c.scrub(out.String()), err
}

// must runs git with the token and fails the test on error.
func (c *Config) must(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := c.git(dir, true, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%.600s", strings.Join(args, " "), err, out)
	}
	return out
}

// fingerprint is the identity of a repository's content: every ref with its
// target, and a hash of every object reachable from them (what the doc's
// "git rev-list --all --objects | sha256sum" compares).
func (c *Config) fingerprint(t *testing.T, dir string) (refs, objects string) {
	t.Helper()
	r := c.must(t, dir, "for-each-ref", "--format=%(refname) %(objectname) %(objecttype)")
	ls := strings.Split(strings.TrimSpace(c.must(t, dir, "rev-list", "--all", "--objects")), "\n")
	sort.Strings(ls)
	return strings.TrimSpace(r), fmt.Sprintf("%x (%d objects)", sha256.Sum256([]byte(strings.Join(ls, "\n"))), len(ls))
}

// seedRepo makes a working repository with history that exercises what an
// import must preserve: two branches, a merge, a lightweight and an annotated
// tag, an executable file and a binary file. It returns its directory.
func (c *Config) seedRepo(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	g := func(args ...string) { t.Helper(); c.must(t, d, args...) }
	write := func(name, body string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	g("init", "-q", "-b", "main")
	write("README.md", "# acceptance\n", 0o644)
	write("run.sh", "#!/bin/sh\necho hi\n", 0o755)
	write("blob.bin", string(bytes.Repeat([]byte{0, 1, 2, 255, 254}, 40000)), 0o644)
	g("add", "-A")
	g("commit", "-q", "-m", "first")
	g("tag", "v0.1")
	g("checkout", "-q", "-b", "feature")
	write("feature.txt", "feature\n", 0o644)
	g("add", "-A")
	g("commit", "-q", "-m", "feature work")
	g("checkout", "-q", "main")
	write("main.txt", "main\n", 0o644)
	g("add", "-A")
	g("commit", "-q", "-m", "main work")
	g("merge", "-q", "--no-ff", "-m", "merge feature", "feature")
	g("tag", "-a", "v1.0", "-m", "release one")
	return d
}

// commit adds a file and a commit in dir and returns the new commit id.
func (c *Config) commit(t *testing.T, dir, file, body, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c.must(t, dir, "add", "-A")
	c.must(t, dir, "commit", "-q", "-m", msg)
	return strings.TrimSpace(c.must(t, dir, "rev-parse", "HEAD"))
}

// remoteRefs is "git ls-remote" as a sorted map of ref to id.
func (c *Config) remoteRefs(url string, auth bool, header string) (map[string]string, error) {
	env := c.gitEnv(auth)
	if header != "" {
		env = append(env, "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_1=http.extraHeader", "GIT_CONFIG_VALUE_1=Authorization: "+header)
	}
	out, err := c.gitWith("", env, "ls-remote", url)
	if err != nil {
		return nil, fmt.Errorf("ls-remote: %v: %.200s", err, out)
	}
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			m[f[1]] = f[0]
		}
	}
	return m, nil
}
