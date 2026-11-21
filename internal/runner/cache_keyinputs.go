package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/executor"
)

// What a cache's key is made of besides the files and text its author
// wrote (see executor.CacheKey): the step's image resolved to its digest,
// and the version inputs the author named (key.versions).

// versionTimeout bounds running the author's version commands.
const versionTimeout = 2 * time.Minute

// imageIdentity resolves the step's image once per attempt.
func (cr *cacheRun) imageIdentity(ctx context.Context) executor.ImageIdentity {
	if !cr.imageDone {
		cr.imageDone = true
		cr.image = executor.ResolveImage(ctx, cr.r.execFor(cr.job), cr.spec.Image)
		if cr.image.ID == "" && cr.spec.Image != "" && cr.image.Source == "" {
			cr.sys("Caches: the image %s could not be resolved to a digest, so the keys use the reference as written; a tag that moves does not change them. Pin the image by digest, or name its tool versions under key.versions.", cr.spec.Image)
		}
	}
	return cr.image
}

// versionValues works out the author's version inputs for a cache, or says
// why it cannot.
func (cr *cacheRun) versionValues(ctx context.Context, t *cacheTask) ([]executor.VersionValue, string) {
	if len(t.entry.KeyVersions) == 0 {
		return nil, ""
	}
	out := make([]executor.VersionValue, len(t.entry.KeyVersions))
	var cmds []int
	for i, v := range t.entry.KeyVersions {
		if v.File != "" {
			dir := filepath.Join(cr.dir, "version-"+t.entry.Name+"-"+strconv.Itoa(i))
			staged, err := executor.StageWorkspace(ctx, cr.r.execFor(cr.job), cr.spec, []string{v.File}, cacheKeyLimits, dir)
			os.RemoveAll(dir)
			if err != nil || len(staged.Files) == 0 {
				return nil, "the version file " + v.File + " cannot be read or matched no file, so the cache's key cannot be worked out"
			}
			sorted := slices.Clone(staged.Files)
			slices.SortFunc(sorted, func(a, b executor.StagedFile) int { return strings.Compare(a.Path, b.Path) })
			var parts []string
			for _, f := range sorted {
				parts = append(parts, f.Path+"="+f.SHA256)
			}
			out[i] = executor.VersionValue{Label: "file: " + v.File, Value: strings.Join(parts, ",")}
			continue
		}
		out[i].Label = "command: " + v.Run
		cmds = append(cmds, i)
	}
	if len(cmds) == 0 {
		return out, ""
	}
	vals, err := cr.runVersionCommands(ctx, t, cmds)
	if err != nil {
		return nil, "a version command of the key could not be run: " + err.Error()
	}
	for j, i := range cmds {
		out[i].Value = vals[j]
	}
	return out, ""
}

// runVersionCommands runs the commands in the step's image, in one go, and
// returns what each wrote (standard output and error together; java writes
// its version to the second). A command that fails counts as its output
// and exit code: a tool that is not there is a fact of the key.
func (cr *cacheRun) runVersionCommands(ctx context.Context, t *cacheTask, idx []int) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	var script strings.Builder
	for j, i := range idx {
		fmt.Fprintf(&script, "echo '@@launchway-version %d'\n( %s ) 2>&1 || echo \"exit status $?\"\n", j, t.entry.KeyVersions[i].Run)
	}
	meta := filepath.Join(cr.dir, "version-meta-"+t.entry.Name)
	if err := os.MkdirAll(meta, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(meta)
	sp := cr.spec
	sp.Name, sp.MetaDir, sp.Script = cr.spec.Name+"-versions", meta, script.String()
	// A fresh environment is not needed: the step's own is what the tools
	// the commands ask about will see when the step runs.
	vals := make([]strings.Builder, len(idx))
	cur := -1
	_, err := cr.r.Exec.Run(ctx, sp, func(stream, text string) {
		if stream == "system" {
			return
		}
		if rest, ok := strings.CutPrefix(text, "@@launchway-version "); ok {
			cur, _ = strconv.Atoi(strings.TrimSpace(rest))
			return
		}
		if cur >= 0 && cur < len(vals) {
			vals[cur].WriteString(text + "\n")
		}
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, len(idx))
	for j := range vals {
		out[j] = strings.TrimSpace(vals[j].String())
	}
	return out, nil
}

// rekey works out the key again, after the step, from the key files as the
// step left them, and uses it from here on. A step may change the files its
// key is made of (an install that rewrites a lock file, `go mod tidy`): an
// entry saved under the key of the files as they were before would hold what
// was built for the files as they are now, and a later run with the old
// files would restore it. changed says the key is not the one worked out
// before the step; the entry is then a new one under the new key, whatever the
// restore found. why is set, and nothing saved, if the files cannot be read.
func (cr *cacheRun) rekey(ctx context.Context, t *cacheTask) (changed bool, why string) {
	if len(t.entry.KeyFiles) == 0 {
		return false, ""
	}
	dir := filepath.Join(cr.dir, "rekey-"+t.entry.Name)
	staged, err := executor.StageWorkspace(ctx, cr.r.execFor(cr.job), cr.spec, t.entry.KeyFiles, cacheKeyLimits, dir)
	defer os.RemoveAll(dir)
	if err != nil {
		return false, "the key's files cannot be read after the step, so the key cannot be checked: " + strings.TrimPrefix(err.Error(), executor.ErrNotKept.Error()+": ")
	}
	in := t.keyIn
	in.Files = staged.Files
	key, parts := executor.CacheKey(in)
	if key == t.key {
		return false, ""
	}
	before := map[string]string{}
	for _, f := range t.keyIn.Files {
		before[f.Path] = f.SHA256
	}
	var names []string
	seen := map[string]bool{}
	for _, f := range staged.Files {
		seen[f.Path] = true
		if before[f.Path] != f.SHA256 {
			names = append(names, f.Path)
		}
	}
	for p := range before {
		if !seen[p] {
			names = append(names, p)
		}
	}
	slices.Sort(names)
	if len(names) > 3 {
		names = append(names[:3], fmt.Sprintf("and %d more", len(names)-3))
	}
	cr.sys("Cache %s: the step changed its key files (%s); the entry is saved under the key of the files as the step left them, never under the key from before it", t.entry.Name, cr.mask(strings.Join(names, ", ")))
	t.key, t.keyParts = key, parts
	t.res.Key, t.res.KeyParts = key, keyParts(parts)
	return true, ""
}
