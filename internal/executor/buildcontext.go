package executor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

// What an image step sends to the builder: the build context, after its
// ignore file. The builder resolves the ignore file like this, and so does
// everything here, so that the limits (images.max_context_mib and the file
// count) apply to what the builder would be sent and nothing else:
//
//   - the file named like the Dockerfile with ".dockerignore" added
//     (Dockerfile.dockerignore), looked for beside the Dockerfile; else
//   - .dockerignore in the root of the context.
//
// Both are read with the pattern rules of the builder (the same matcher): an
// allow-list that starts with * and re-includes with ! works as it does there.
// The Dockerfile and the ignore files themselves are always kept, so that the
// builder, which reads them from the context, finds them and applies the same
// rules to what it receives.

// ContextSpec says where the build context and its Dockerfile are, both
// relative to the run's workspace, with slashes.
type ContextSpec struct {
	Dir        string // the context, "." for the workspace itself
	Dockerfile string // the Dockerfile
}

// IgnoreFiles returns where the two possible ignore files of a build are,
// relative to the workspace, in the order the builder tries them.
func (c ContextSpec) IgnoreFiles() (specific, general string) {
	return c.Dockerfile + ".dockerignore", path.Join(c.Dir, ".dockerignore")
}

// ContextEntry is something in the build context: a directory, a regular
// file or a symbolic link (never followed).
type ContextEntry struct {
	Path string // relative to the context, with slashes
	Dir  bool
	Link bool
	Size int64
}

// DirSize is a directory and the bytes kept under it.
type DirSize struct {
	Path  string
	Bytes int64
}

// ContextFilter decides what of a build context is kept.
type ContextFilter struct {
	Source   string // the ignore file the patterns came from; "" for none
	Patterns int
	m        *patternmatcher.PatternMatcher
	always   map[string]bool
}

// NewContextFilter reads an ignore file's text. Paths in always (relative to
// the context) are kept whatever the patterns say.
func NewContextFilter(source string, text []byte, always ...string) (*ContextFilter, error) {
	f := &ContextFilter{Source: source, always: map[string]bool{}}
	for _, a := range always {
		f.always[a] = true
	}
	if source == "" {
		return f, nil
	}
	pats, err := ignorefile.ReadAll(strings.NewReader(string(text)))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", source, err)
	}
	if len(pats) == 0 {
		return f, nil
	}
	if f.m, err = patternmatcher.New(pats); err != nil {
		return nil, fmt.Errorf("the pattern in %s is not valid: %w", source, err)
	}
	f.Patterns = len(pats)
	return f, nil
}

// ContextPlan is what a filter decided about a context's entries.
type ContextPlan struct {
	Keep      []ContextEntry // files and links to copy; directories come with them
	KeptBytes int64
	KeptFiles int
	// LeftBytes and LeftFiles count what was seen and left out; a whole
	// directory that was not looked into is in Skipped instead.
	LeftBytes int64
	LeftFiles int
	Skipped   []string
	dirBytes  map[string]int64
	info      map[string]patternmatcher.MatchInfo
	err       error
}

// NewPlan starts a plan.
func (f *ContextFilter) NewPlan() *ContextPlan {
	return &ContextPlan{dirBytes: map[string]int64{}, info: map[string]patternmatcher.MatchInfo{}}
}

// Add decides one entry; entries come in the order of a walk, a directory
// before what is in it. It reports whether to look into a directory.
func (f *ContextFilter) Add(p *ContextPlan, e ContextEntry) (descend bool) {
	excluded := false
	if f.m != nil && !f.always[e.Path] {
		parent := p.info[path.Dir(e.Path)]
		m, info, err := f.m.MatchesUsingParentResults(e.Path, parent)
		if err != nil && p.err == nil {
			p.err = fmt.Errorf("matching %s against %s: %w", e.Path, f.Source, err)
		}
		excluded = m
		if e.Dir {
			p.info[e.Path] = info
		}
	}
	if e.Dir {
		if excluded && !f.mayReinclude(e.Path) {
			p.Skipped = append(p.Skipped, e.Path)
			return false
		}
		return true
	}
	if excluded {
		p.LeftBytes += e.Size
		p.LeftFiles++
		return false
	}
	p.Keep = append(p.Keep, e)
	p.KeptBytes += e.Size
	p.KeptFiles++
	for d := path.Dir(e.Path); d != "." && d != "/"; d = path.Dir(d) {
		p.dirBytes[d] += e.Size
	}
	return false
}

// Err is the first pattern that could not be matched.
func (p *ContextPlan) Err() error { return p.err }

// mayReinclude says whether a pattern that re-includes (!) could match
// something inside the excluded directory dir. It errs towards yes: a
// directory looked into for nothing costs time, one skipped wrongly would
// leave a file out that the builder asks for.
func (f *ContextFilter) mayReinclude(dir string) bool {
	if f.m == nil || !f.m.Exclusions() {
		return false
	}
	d := strings.Split(dir, "/")
	for _, pat := range f.m.Patterns() {
		if !pat.Exclusion() {
			continue
		}
		if patternReaches(strings.Trim(path.Clean(filepath.ToSlash(strings.TrimPrefix(pat.String(), "!"))), "/"), d) {
			return true
		}
	}
	return false
}

// patternReaches says whether a pattern could match a path below the
// directory whose components are d.
func patternReaches(pat string, d []string) bool {
	comps := strings.Split(pat, "/")
	for i, dc := range d {
		if i >= len(comps) {
			return false
		}
		switch c := comps[i]; {
		case c == "**":
			return true
		case strings.ContainsAny(c, `*?[\`):
			if ok, err := path.Match(c, dc); err != nil || !ok {
				return err != nil
			}
		case c != dc:
			return false
		}
	}
	return len(comps) > len(d)
}

// Largest returns up to n directories that hold the most kept bytes. A
// directory that holds most of its parent's bytes is replaced by the
// biggest thing inside it, so that the answer says where to look.
func (p *ContextPlan) Largest(n int) []DirSize {
	var tops []DirSize
	for d, b := range p.dirBytes {
		if !strings.Contains(d, "/") {
			tops = append(tops, DirSize{d, b})
		}
	}
	sort.Slice(tops, func(i, j int) bool {
		if tops[i].Bytes != tops[j].Bytes {
			return tops[i].Bytes > tops[j].Bytes
		}
		return tops[i].Path < tops[j].Path
	})
	if len(tops) > n {
		tops = tops[:n]
	}
	for i := range tops {
		for depth := 0; depth < 3; depth++ {
			var best DirSize
			for d, b := range p.dirBytes {
				if path.Dir(d) != tops[i].Path {
					continue
				}
				if b > best.Bytes || b == best.Bytes && d < best.Path {
					best = DirSize{d, b}
				}
			}
			if best.Path == "" || best.Bytes*10 < tops[i].Bytes*6 {
				break
			}
			tops[i] = best
		}
	}
	return tops
}

// ContextTooLarge is the error for a build context that is over the limits
// after its ignore file was applied.
type ContextTooLarge struct {
	Context       string
	Ignore        string // the ignore file used; "" for none
	Kept, Left    int64
	KeptFiles     int
	MaxBytes      int64
	MaxFiles      int
	Top           []DirSize
	LeftIsAtLeast bool // directories left out whole were sized by a second look
}

func (e *ContextTooLarge) Is(target error) bool { return target == ErrTooLarge }

func (e *ContextTooLarge) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "the build context %s keeps %s in %d files", e.Context, bytesText(e.Kept), e.KeptFiles)
	if e.Ignore != "" {
		b.WriteString(" after " + e.Ignore)
	} else {
		b.WriteString(" (there is no .dockerignore, so everything in it is kept)")
	}
	switch {
	case e.MaxBytes > 0 && e.Kept > e.MaxBytes:
		fmt.Fprintf(&b, ", more than the %s that images.max_context_mib allows", bytesText(e.MaxBytes))
	default:
		fmt.Fprintf(&b, ", more than the %d files allowed", e.MaxFiles)
	}
	if e.Ignore != "" {
		if e.LeftIsAtLeast {
			fmt.Fprintf(&b, "; the ignore file left out at least %s", bytesText(e.Left))
		} else {
			fmt.Fprintf(&b, "; the ignore file left out %s", bytesText(e.Left))
		}
	}
	if len(e.Top) > 0 {
		var parts []string
		for _, d := range e.Top {
			parts = append(parts, fmt.Sprintf("%s (%s)", d.Path, bytesText(d.Bytes)))
		}
		b.WriteString("; the largest directories kept are " + strings.Join(parts, ", "))
	}
	b.WriteString(". Add what the build does not read to .dockerignore, or raise images.max_context_mib")
	return b.String()
}

// Finish checks the limits on a finished plan. sizeOf, if not nil, says how
// many bytes the paths take, for the message only (it is called only when a
// limit is passed).
func (f *ContextFilter) Finish(p *ContextPlan, context string, lim ExportLimits, sizeOf func(paths []string) int64) error {
	if p.err != nil {
		return p.err
	}
	if (lim.MaxBytes <= 0 || p.KeptBytes <= lim.MaxBytes) && (lim.MaxFiles <= 0 || p.KeptFiles <= lim.MaxFiles) {
		return nil
	}
	e := &ContextTooLarge{Context: context, Ignore: f.Source, Kept: p.KeptBytes, KeptFiles: p.KeptFiles, Left: p.LeftBytes,
		MaxBytes: lim.MaxBytes, MaxFiles: lim.MaxFiles, Top: p.Largest(3)}
	if len(p.Skipped) > 0 {
		e.LeftIsAtLeast = true
		if sizeOf != nil {
			e.Left += sizeOf(p.Skipped)
		}
	}
	return e
}

// ContextIgnore finds and reads the ignore file of a build the way the
// builder does. read returns a workspace file's bytes, or false if there is
// none. The filter keeps the Dockerfile and the ignore files.
func ContextIgnore(c ContextSpec, read func(rel string) ([]byte, bool, error)) (*ContextFilter, error) {
	specific, general := c.IgnoreFiles()
	var always []string
	for _, w := range []string{c.Dockerfile, specific, general} {
		if rel, ok := relativeTo(c.Dir, w); ok {
			always = append(always, rel)
		}
	}
	for _, w := range []string{specific, general} {
		text, ok, err := read(w)
		if err != nil {
			return nil, err
		}
		if ok {
			return NewContextFilter(w, text, always...)
		}
	}
	return NewContextFilter("", nil, always...)
}

// relativeTo returns file's path under dir, both relative to the workspace.
func relativeTo(dir, file string) (string, bool) {
	if dir == "." {
		return file, validRelative(file)
	}
	rel, ok := strings.CutPrefix(file, dir+"/")
	return rel, ok && validRelative(rel)
}

// CheckLocalContext applies the same rules to a build context that is a
// directory on the runner (the workspace itself, with no pod): it walks the
// directory the way the builder would, looks at what is kept, and applies
// the limits to that. The builder sends only what is kept; nothing is copied.
func CheckLocalContext(workspace string, c ContextSpec, lim ExportLimits) error {
	root, err := filepath.EvalSymlinks(filepath.Join(workspace, filepath.FromSlash(c.Dir)))
	if err != nil {
		return err
	}
	read := func(rel string) ([]byte, bool, error) {
		b, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(rel)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, false, nil
		case err != nil:
			return nil, false, fmt.Errorf("reading %s: %w", rel, err)
		}
		return b, true, nil
	}
	f, err := ContextIgnore(c, read)
	if err != nil {
		return err
	}
	plan := f.NewPlan()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return rerr
		}
		e := ContextEntry{Path: filepath.ToSlash(rel), Dir: d.IsDir(), Link: d.Type()&fs.ModeSymlink != 0}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				e.Size = info.Size()
			}
		}
		if !e.Dir && !e.Link && !d.Type().IsRegular() {
			return nil // devices and pipes are not part of a context
		}
		if !f.Add(plan, e) && e.Dir {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return err
	}
	return f.Finish(plan, c.Dir, lim, func(paths []string) int64 {
		var n int64
		for _, s := range paths {
			_ = filepath.WalkDir(filepath.Join(root, filepath.FromSlash(s)), func(_ string, d fs.DirEntry, err error) error {
				if err == nil && d.Type().IsRegular() {
					if info, err := d.Info(); err == nil {
						n += info.Size()
					}
				}
				return nil
			})
		}
		return n
	})
}
