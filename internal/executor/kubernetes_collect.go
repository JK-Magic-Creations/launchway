package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
)

// CollectFiles reads report files from the run's pod, where the workspace
// is: the helper container lists the files below each glob's directory, and
// reads each chosen file, at most as much as is left of the limits. Only
// regular files are read; symbolic links are not followed.
func (k *Kubernetes) CollectFiles(ctx context.Context, s Spec, globs []string, lim CollectLimits) (*Collected, error) {
	if k.perStep() {
		dir, unlock := k.readWorkspace(s)
		defer unlock()
		return collectLocal(ctx, dir, globs, lim)
	}
	out := &Collected{}
	pod := podName(s.RunID, k.owner)
	k.mu.Lock()
	if rp := k.pods[s.RunID]; rp != nil {
		pod = rp.name
	}
	k.mu.Unlock()
	p := newPlanner(globs, lim)
	searched := map[string]bool{}
	for _, g := range globs {
		base, depth := globBase(g)
		key := fmt.Sprintf("%s\x00%d", base, depth)
		if searched[key] {
			continue
		}
		searched[key] = true
		// find prints NUL-separated paths, so any name is read correctly. It
		// does not follow symbolic links. ** never enters .git or
		// node_modules unless a glob names them.
		args := []string{"find", base}
		if depth >= 0 {
			args = append(args, "-maxdepth", fmt.Sprint(depth))
		}
		var prune []string
		for _, d := range skippedDirs {
			if !explicitlyNamed(globs, path.Join(base, d)) && base != d && !strings.HasPrefix(base, d+"/") {
				prune = append(prune, d)
			}
		}
		if len(prune) > 0 {
			args = append(args, "(")
			for i, d := range prune {
				if i > 0 {
					args = append(args, "-o")
				}
				args = append(args, "-name", d)
			}
			args = append(args, ")", "-prune", "-o")
		}
		args = append(args, "-type", "f", "-print0")
		var list, stderr bytes.Buffer
		lw := &limitedWriter{w: &list, n: 16 << 20}
		script := `cd /workspace || exit 1; [ -d "$1" ] && [ ! -L "$1" ] || exit 0; exec find "$@"`
		err := k.exec(ctx, pod, helperName, append([]string{"sh", "-c", script, "sh"}, args[1:]...), nil, lw, &stderr)
		if err != nil && !lw.full {
			return out, infra("listing files in pod %s: %v %s", pod, err, strings.TrimSpace(stderr.String()))
		}
		if lw.full {
			p.note("the workspace has too many files where %s looks; only part of it was searched", g)
		}
		for _, name := range strings.Split(list.String(), "\x00") {
			name = strings.TrimPrefix(name, "./")
			if name == "" || strings.ContainsAny(name, "\n\r") {
				continue
			}
			p.offer(name)
		}
	}
	var used int64
	for _, name := range p.chosen() {
		n := p.budget(used)
		if n <= 0 {
			p.note("%s was not collected: the limit of %s for all files together was reached", name, bytesText(lim.MaxBytes))
			continue
		}
		var data, stderr bytes.Buffer
		lw := &limitedWriter{w: &data, n: n + 1}
		// Only a regular file, never what a symbolic link points at.
		err := k.exec(ctx, pod, helperName, []string{"sh", "-c",
			`cd /workspace && [ -f "$1" ] && [ ! -L "$1" ] && [ "$(readlink -f "./$1")" = "/workspace/$1" ] || { echo "not a regular file inside the workspace" >&2; exit 3; }; exec head -c "$2" -- "$1"`,
			"sh", name, fmt.Sprint(n + 1)}, nil, lw, &stderr)
		if err != nil && !lw.full {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			p.note("%s could not be read: %s", name, oneLine(msg))
			continue
		}
		f := CollectedFile{Path: name, Data: data.Bytes()}
		if int64(len(f.Data)) > n {
			f.Data, f.Cut = f.Data[:n], true
			p.cutNote(name, n, 0)
		}
		used += int64(len(f.Data))
		out.Files = append(out.Files, f)
	}
	out.Notes = p.notes
	return out, nil
}

// limitedWriter keeps at most n bytes and then says it is full.
type limitedWriter struct {
	w    io.Writer
	n    int64
	full bool
}

func (l *limitedWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > l.n {
		l.full = true
		b = b[:l.n]
	}
	n, err := l.w.Write(b)
	l.n -= int64(n)
	if err == nil && l.full {
		return n, io.ErrShortWrite
	}
	return n, err
}
