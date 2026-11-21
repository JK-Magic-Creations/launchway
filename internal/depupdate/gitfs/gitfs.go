// Package gitfs reads the files the updater cares about out of a bare Git
// repository at one revision, as an in-memory file tree. The server uses it
// to look at a repository's default branch without a working copy and
// without running anything from the repository.
package gitfs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"testing/fstest"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// Limits bound what is read.
type Limits struct {
	MaxFiles    int   // default 5000 files listed, 2000 read
	MaxFileSize int64 // default 8 MiB
	MaxTotal    int64 // default 64 MiB
}

func (l Limits) def() Limits {
	if l.MaxFiles == 0 {
		l.MaxFiles = 2000
	}
	if l.MaxFileSize == 0 {
		l.MaxFileSize = 8 << 20
	}
	if l.MaxTotal == 0 {
		l.MaxTotal = 64 << 20
	}
	return l
}

// Interesting says whether a file may hold dependencies: manifests, lock
// files, Dockerfiles, pipeline and workflow files, the updater's own settings,
// and files that match the extra globs. Everything else is not read.
func Interesting(p string, extra []string) bool {
	b := path.Base(p)
	lb := strings.ToLower(b)
	switch {
	case b == "go.mod", b == "go.sum", b == "package.json", b == "package-lock.json", b == "pnpm-lock.yaml", b == "yarn.lock",
		b == "pyproject.toml", b == "uv.lock", b == "poetry.lock", p == ".launchway/updates.yaml":
		return true
	case strings.HasPrefix(lb, "requirements") && (strings.HasSuffix(lb, ".txt") || strings.HasSuffix(lb, ".in")):
		return true
	case lb == "dockerfile" || lb == "containerfile" || strings.HasPrefix(lb, "dockerfile.") || strings.HasSuffix(lb, ".dockerfile"):
		return true
	case (strings.HasPrefix(p, ".launchway/") || strings.Contains(p, "/.launchway/")) && (strings.HasSuffix(lb, ".yaml") || strings.HasSuffix(lb, ".yml")):
		return true
	case strings.Contains(p, ".github/workflows/") && (strings.HasSuffix(lb, ".yaml") || strings.HasSuffix(lb, ".yml")):
		return true
	}
	for _, g := range extra {
		if globMatch(g, p) {
			return true
		}
	}
	return false
}

func globMatch(pattern, name string) bool {
	if ok, _ := path.Match(pattern, name); ok {
		return true
	}
	return false
}

// Commit returns the commit a revision (a branch name) points at.
func Commit(ctx context.Context, gitDir, rev string) (string, error) {
	out, err := gitexec.Output(ctx, gitexec.Spec{GitDir: gitDir, Args: []string{"rev-parse", "--verify", "--quiet", "refs/heads/" + rev + "^{commit}"}})
	if err != nil {
		return "", fmt.Errorf("the branch %s does not exist", rev)
	}
	return strings.TrimSpace(out), nil
}

// Read returns the interesting files of a commit. Files over the limits are
// skipped and listed in the returned notes.
func Read(ctx context.Context, gitDir, commit string, extra []string, lim Limits) (fstest.MapFS, []string, error) {
	lim = lim.def()
	out, err := gitexec.Output(ctx, gitexec.Spec{GitDir: gitDir, Args: []string{"ls-tree", "-r", "-z", "--long", commit}, MaxOutput: 64 << 20})
	if err != nil {
		return nil, nil, err
	}
	type ent struct {
		sha, path string
		size      int64
	}
	var want []ent
	var notes []string
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, p, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) < 4 || f[1] != "blob" || !Interesting(p, extra) {
			continue
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		if size > lim.MaxFileSize {
			notes = append(notes, p+" is larger than the limit and was skipped")
			continue
		}
		want = append(want, ent{f[2], p, size})
		if len(want) >= lim.MaxFiles {
			notes = append(notes, "more files than the limit: the rest were skipped")
			break
		}
	}
	files := fstest.MapFS{}
	if len(want) == 0 {
		return files, notes, nil
	}
	var in bytes.Buffer
	for _, e := range want {
		in.WriteString(e.sha + "\n")
	}
	res, err := gitexec.Run(ctx, gitexec.Spec{GitDir: gitDir, Args: []string{"cat-file", "--batch"}, Stdin: &in, MaxOutput: lim.MaxTotal + 1<<20})
	if err != nil {
		return nil, nil, err
	}
	rd := bufio.NewReader(strings.NewReader(res.Stdout))
	var total int64
	for _, e := range want {
		line, err := rd.ReadString('\n')
		if err != nil {
			return nil, nil, fmt.Errorf("reading %s: %w", e.path, err)
		}
		f := strings.Fields(line)
		if len(f) != 3 || f[1] != "blob" {
			return nil, nil, fmt.Errorf("unexpected answer for %s: %q", e.path, strings.TrimSpace(line))
		}
		n, _ := strconv.ParseInt(f[2], 10, 64)
		buf := make([]byte, n+1)
		if _, err := io.ReadFull(rd, buf); err != nil {
			return nil, nil, err
		}
		total += n
		if total > lim.MaxTotal {
			notes = append(notes, "the files are larger than the limit together: the rest were skipped")
			break
		}
		files[e.path] = &fstest.MapFile{Data: buf[:n]}
	}
	return files, notes, nil
}
