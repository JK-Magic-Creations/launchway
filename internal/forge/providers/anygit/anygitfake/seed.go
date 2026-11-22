package anygitfake

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// seed makes the repository: main with a README, feature with one more file.
func (h *Host) seed() {
	if err := os.MkdirAll(filepath.Dir(h.gitDir()), 0o750); err != nil {
		panic(err)
	}
	cmd := exec.CommandContext(context.Background(), "git", "init", "--bare", "-q", "-b", "main", h.gitDir()) //nolint:gosec // G204: git on the fake's own temporary directory
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.dir}
	if out, err := cmd.CombinedOutput(); err != nil {
		panic(string(out))
	}
	for _, kv := range [][2]string{{"http.receivepack", "true"}, {"http.uploadpack", "true"}, {"uploadpack.allowReachableSHA1InWant", "false"}} {
		if _, err := h.git("config", kv[0], kv[1]); err != nil {
			panic(err)
		}
	}
	first, err := h.commit("", "README.md", "hello\n", "first")
	if err != nil {
		panic(err)
	}
	if _, err := h.git("update-ref", "refs/heads/main", first); err != nil {
		panic(err)
	}
	second, err := h.commit(first, "feature.txt", "feature\n", "add a feature")
	if err != nil {
		panic(err)
	}
	if _, err := h.git("update-ref", "refs/heads/feature", second); err != nil {
		panic(err)
	}
}

// commit makes a commit with one file on top of parent ("" for a root), with git's plumbing and a temporary index.
func (h *Host) commit(parent, path, content, msg string) (string, error) {
	idx := filepath.Join(h.dir, "index-seed")
	defer os.Remove(idx)
	run := func(stdin string, args ...string) (string, error) {
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"--git-dir", h.gitDir()}, args...)...) //nolint:gosec // G204: the fake's own repository
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_INDEX_FILE=" + idx, "GIT_AUTHOR_NAME=Mary Major", "GIT_AUTHOR_EMAIL=mary@example.com",
			"GIT_COMMITTER_NAME=Mary Major", "GIT_COMMITTER_EMAIL=mary@example.com"}
		if stdin != "" {
			cmd.Stdin = stringsReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		return trim(string(out)), err
	}
	if parent != "" {
		if _, err := run("", "read-tree", parent); err != nil {
			return "", err
		}
	}
	blob, err := run(content, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	if _, err := run("", "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path); err != nil {
		return "", err
	}
	tree, err := run("", "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree, "-m", msg}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return run("", args...)
}

func trim(s string) string { return strings.TrimSpace(s) }

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }
