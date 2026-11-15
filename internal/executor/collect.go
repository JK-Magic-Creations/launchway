package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
)

// Collecting report files. After a step ends, passed or failed, the runner
// reads the files it wrote that match the globs its pipeline declares
// (reports.tests), from the workspace: from the run's pod on Kubernetes, and
// from the workspace directory otherwise. Collecting never fails a step; what
// could not be collected is described in notes, which the runner writes to
// the step's log and sends to the server.
//
// Globs are relative to the workspace, separated by /. Each path part is
// matched as by path.Match (*, ?, [a-z]); a part ** matches any number of
// directories, but never enters .git or node_modules. Only regular files
// are read: symbolic links, devices and pipes are not, so a step cannot make
// the runner read something outside its workspace.

// CollectLimits bound what is collected after one step attempt.
type CollectLimits struct {
	MaxFiles     int   // files read
	MaxFileBytes int64 // bytes read from one file; a larger one is cut
	MaxBytes     int64 // bytes read from all of them together
}

// Defaults for CollectLimits.
const (
	DefaultCollectFiles     = 100
	DefaultCollectFileBytes = 10 << 20
	DefaultCollectBytes     = 20 << 20
)

// WithDefaults fills in the limits left at zero.
func (l CollectLimits) WithDefaults() CollectLimits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultCollectFiles
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = DefaultCollectFileBytes
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultCollectBytes
	}
	return l
}

// CollectedFile is one file read from the workspace.
type CollectedFile struct {
	Path string // relative to the workspace
	Data []byte
	Size int64 // the file's size, when known
	Cut  bool  // only the start of it, len(Data) bytes, was read
}

// Collected is what was collected after a step.
type Collected struct {
	Files []CollectedFile
	// Notes say, in plain words, what was not collected and why.
	Notes []string
}

// FileCollector is implemented by executors whose workspace is not a
// directory on the runner's own disk.
type FileCollector interface {
	CollectFiles(ctx context.Context, s Spec, globs []string, lim CollectLimits) (*Collected, error)
}

// Collect reads the files of a step's workspace that match the globs. The
// error is for a failure to look at all, such as a pod that is gone; what
// was collected before it is returned too.
func Collect(ctx context.Context, ex Executor, s Spec, globs []string, lim CollectLimits) (*Collected, error) {
	lim = lim.WithDefaults()
	if fc, ok := ex.(FileCollector); ok {
		return fc.CollectFiles(ctx, s, globs, lim)
	}
	return collectLocal(ctx, s.Workspace, globs, lim)
}

// maxListed bounds how many entries one collection looks at while searching
// for matches, so that a glob such as **/*.xml over a huge workspace ends.
const maxListed = 200_000

// skippedDirs are directories ** does not enter.
var skippedDirs = []string{".git", "node_modules"}

// globBase splits a glob into the directory to search, its parts before the
// first pattern, and how many levels below it to look: -1 for any number
// (when it has **).
func globBase(glob string) (base string, depth int) {
	parts := strings.Split(glob, "/")
	i := 0
	for i < len(parts)-1 && !hasMeta(parts[i]) {
		i++
	}
	base = strings.Join(parts[:i], "/")
	if base == "" {
		base = "."
	}
	rest := parts[i:]
	if slices.Contains(rest, "**") {
		return base, -1
	}
	return base, len(rest)
}

func hasMeta(part string) bool { return strings.ContainsAny(part, `*?[\`) }

// matchGlob reports whether a path, relative to the workspace, matches a
// glob.
func matchGlob(glob, name string) bool {
	return matchParts(strings.Split(glob, "/"), strings.Split(name, "/"))
}

func matchParts(glob, name []string) bool {
	for len(glob) > 0 {
		if glob[0] == "**" {
			rest := glob[1:]
			for i := 0; i <= len(name); i++ {
				if i > 0 && slices.Contains(skippedDirs, name[i-1]) {
					return false
				}
				if matchParts(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(glob[0], name[0]); err != nil || !ok {
			return false
		}
		glob, name = glob[1:], name[1:]
	}
	return len(name) == 0
}

// planner gathers matches for several globs and decides which to read.
type planner struct {
	globs   []string
	lim     CollectLimits
	matched map[string]bool // paths
	hits    map[string]int  // glob → number of matches
	notes   []string
}

func newPlanner(globs []string, lim CollectLimits) *planner {
	return &planner{globs: globs, lim: lim, matched: map[string]bool{}, hits: map[string]int{}}
}

// offer considers a regular file found while searching.
func (p *planner) offer(name string) {
	for _, g := range p.globs {
		if matchGlob(g, name) {
			p.hits[g]++
			p.matched[name] = true
		}
	}
}

func (p *planner) note(format string, a ...any) { p.notes = append(p.notes, fmt.Sprintf(format, a...)) }

// chosen are the files to read, in name order, within MaxFiles; with notes
// for globs that matched nothing and files left out.
func (p *planner) chosen() []string {
	for _, g := range p.globs {
		if p.hits[g] == 0 {
			p.note("%s matched no files", g)
		}
	}
	names := make([]string, 0, len(p.matched))
	for n := range p.matched {
		names = append(names, n)
	}
	slices.Sort(names)
	if len(names) > p.lim.MaxFiles {
		p.note("%d files matched; only the first %d (in name order) were collected", len(names), p.lim.MaxFiles)
		names = names[:p.lim.MaxFiles]
	}
	return names
}

// budget is how much of the next file to read: the file limit, or what is
// left of the total. Zero means none is left.
func (p *planner) budget(used int64) int64 { return min(p.lim.MaxFileBytes, p.lim.MaxBytes-used) }

// cutNote explains a file read only in part.
func (p *planner) cutNote(name string, kept, size int64) {
	what := "the per-file limit"
	if kept < p.lim.MaxFileBytes {
		what = "the limit for all files together"
	}
	if size > 0 {
		p.note("%s is %s; only its first %s were collected (%s)", name, bytesText(size), bytesText(kept), what)
	} else {
		p.note("%s is larger than %s; only its first %s were collected (%s)", name, bytesText(kept), bytesText(kept), what)
	}
}

func bytesText(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1024), ".0") + " KiB"
	default:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/(1<<20)), ".0") + " MiB"
	}
}

// collectLocal collects from a workspace directory on this machine. It works
// through os.Root, so nothing outside the directory is ever opened, even
// through a symbolic link a step made.
func collectLocal(ctx context.Context, dir string, globs []string, lim CollectLimits) (*Collected, error) {
	out := &Collected{}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return out, fmt.Errorf("opening the workspace: %w", err)
	}
	defer root.Close()
	p := newPlanner(globs, lim)
	listed := 0
	walked := map[string]bool{}
	errStop := errors.New("stop")
	for _, g := range globs {
		base, depth := globBase(g)
		if walked[base+"\x00"+fmt.Sprint(depth)] {
			continue
		}
		walked[base+"\x00"+fmt.Sprint(depth)] = true
		if info, err := root.Lstat(base); err != nil || !info.IsDir() {
			continue // nothing there, or not a directory: no match
		}
		err = fs.WalkDir(root.FS(), base, func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable: skip it
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if listed++; listed > maxListed {
				return errStop
			}
			if name == base {
				return nil
			}
			below := name // the path below base
			if base != "." {
				below = strings.TrimPrefix(name, base+"/")
			}
			if d.IsDir() {
				// Files deeper than the glob reaches cannot match.
				if depth >= 0 && strings.Count(below, "/")+1 >= depth {
					return fs.SkipDir
				}
				if slices.Contains(skippedDirs, d.Name()) && !explicitlyNamed(globs, name) {
					return fs.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() {
				p.offer(name)
			}
			return nil
		})
		switch {
		case errors.Is(err, errStop):
			p.note("the workspace has more than %d files and directories where the globs look; only the first %d were searched", maxListed, maxListed)
		case err != nil:
			return out, err
		}
		if listed > maxListed {
			break
		}
	}
	var used int64
	for _, name := range p.chosen() {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		n := p.budget(used)
		if n <= 0 {
			p.note("%s was not collected: the limit of %s for all files together was reached", name, bytesText(lim.MaxBytes))
			continue
		}
		f, err := readLocal(root, name, n)
		if err != nil {
			p.note("%s could not be read: %v", name, err)
			continue
		}
		if f.Cut {
			p.cutNote(name, int64(len(f.Data)), f.Size)
		}
		used += int64(len(f.Data))
		out.Files = append(out.Files, f)
	}
	out.Notes = p.notes
	return out, nil
}

// explicitlyNamed reports whether a glob names a skipped directory itself,
// such as node_modules/.cache/report.xml, rather than reaching it through
// a pattern.
func explicitlyNamed(globs []string, dir string) bool {
	for _, g := range globs {
		if strings.HasPrefix(g, dir+"/") {
			return true
		}
	}
	return false
}

// readLocal reads at most n bytes of a regular file inside root. It opens
// the file without blocking, then checks that it is a regular file, so a
// pipe put in its place after the search cannot hang the runner.
func readLocal(root *os.Root, name string, n int64) (CollectedFile, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|openNonBlock, 0)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err // the name is said already
		}
		return CollectedFile{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return CollectedFile{}, err
	}
	if !info.Mode().IsRegular() {
		return CollectedFile{}, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, n+1))
	if err != nil {
		return CollectedFile{}, err
	}
	cf := CollectedFile{Path: name, Data: data, Size: info.Size()}
	if int64(len(data)) > n {
		cf.Data, cf.Cut = data[:n], true
	}
	return cf, nil
}
