package propose

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/executor"
)

// StepRunner runs a Step on a copy of the repository's files and returns the
// files it changed. The production implementation is ExecRunner.
type StepRunner interface {
	Run(ctx context.Context, step Step, tree string, log func(string)) error
}

// ExecRunner runs steps with the executor runners use (podman, docker,
// kubernetes or the host). The container gets the copy of the tree only: no
// Git metadata, no secrets, the environment named by Env (the registries the
// installation allows, e.g. GOPROXY) and nothing else.
type ExecRunner struct {
	Exec executor.Executor
	Env  map[string]string // registry settings: GOPROXY, npm_config_registry, UV_INDEX_URL ...
	Meta string            // a directory for the step's script and outputs
}

// Run writes the commands into one script and runs it in the image.
func (r *ExecRunner) Run(ctx context.Context, st Step, tree string, log func(string)) error {
	meta, err := os.MkdirTemp(r.Meta, "step-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(meta)
	var sb strings.Builder
	dir := executor.ContainerWorkspace
	if _, host := r.Exec.(*executor.Host); host {
		dir = tree
	}
	if st.Dir != "" {
		dir = filepath.ToSlash(filepath.Join(dir, st.Dir))
	}
	sb.WriteString("cd " + quote(dir) + "\n")
	for _, c := range st.Commands {
		for i, a := range c {
			if i > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(quote(a))
		}
		sb.WriteByte('\n')
	}
	env := map[string]string{"CI": "true", "HOME": filepath.Join(os.TempDir(), "launchway-updater-home")}
	for k, v := range r.Env {
		env[k] = v
	}
	if _, host := r.Exec.(*executor.Host); host {
		env["HOME"] = meta
		env["GOCACHE"] = filepath.Join(meta, "gocache")
		env["GOMODCACHE"] = filepath.Join(meta, "gomod")
		if env["GOFLAGS"] == "" {
			env["GOFLAGS"] = "-modcacherw" // the module cache must be removable
		}
	}
	spec := executor.Spec{RunID: "dependency-update", Name: "dep-update-" + filepath.Base(meta), Image: st.Image,
		Containers: []executor.ContainerSpec{{Name: "default", Image: st.Image}}, Container: "default",
		Script: sb.String(), Env: env, Workspace: tree, WorkspaceID: filepath.Base(meta), MetaDir: meta}
	var tail []string
	code, err := r.Exec.Run(ctx, spec, func(stream, text string) {
		if log != nil {
			log(text)
		}
		tail = append(tail, text)
		if len(tail) > 40 {
			tail = tail[1:]
		}
	})
	if err != nil {
		return fmt.Errorf("the toolchain step could not run: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("the toolchain step failed (exit %d): %s", code, strings.Join(tail, " | "))
	}
	return nil
}

func quote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%+=:,./-_") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// copyTree copies a directory without .git into dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if rel == ".git" {
			return fs.SkipDir
		}
		t := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(t, 0o755)
		case d.Type().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(t, b, 0o644)
		}
		return nil // links and special files are not copied
	})
}

// Diff compares a tree before and after a step and returns what changed:
// path to new content, nil for a deletion.
func diffTrees(before, after string) (map[string][]byte, error) {
	seen := map[string]bool{}
	out := map[string][]byte{}
	err := filepath.WalkDir(after, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(after, p)
		rel = filepath.ToSlash(rel)
		seen[rel] = true
		nb, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		ob, oerr := os.ReadFile(filepath.Join(before, rel))
		if oerr != nil || !bytes.Equal(ob, nb) {
			out[rel] = nb
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(before, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(before, p)
		rel = filepath.ToSlash(rel)
		if !seen[rel] {
			out[rel] = nil
		}
		return nil
	})
	return out, err
}

// Paths of changes, sorted.
func paths(m map[string][]byte) []string {
	var o []string
	for k := range m {
		o = append(o, k)
	}
	sort.Strings(o)
	return o
}
