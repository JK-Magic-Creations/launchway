package forks

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// Bounds of what a template is turned into.
const (
	maxEntries       = 50000
	maxSubstituteLen = 1 << 20 // a file larger than this is copied as it is
	maxBranches      = 25
)

// SetTemplate marks a repository as a template, or not.
func (s *Service) SetTemplate(ctx context.Context, id string, on bool) error {
	return store.SetTemplate(ctx, s.DB, id, on)
}

// Manifest reads .launchway/template.yaml at the tip of a template's default
// branch. A template with no manifest has no variables.
func (s *Service) Manifest(ctx context.Context, tpl forge.Repository) (Manifest, error) {
	refs, err := s.Git.Refs(ctx, tpl.ID)
	if err != nil {
		return Manifest{}, err
	}
	tip := refs[branchRef(tpl.DefaultBranch)]
	if tip == "" {
		return Manifest{}, fmt.Errorf("%w: the template has no commits on %s", ErrRelation, tpl.DefaultBranch)
	}
	var data []byte
	err = s.Git.WithCache(ctx, tpl.ID, func(dir string) error {
		run := func(args ...string) (gitexec.Result, error) {
			return gitexec.Run(ctx, gitexec.Spec{GitDir: dir, Args: args, Timeout: time.Minute, Limits: s.Git.Limits(), MaxOutput: maxManifestBytes + 1})
		}
		spec := tip + ":" + ManifestPath
		if _, err := run("cat-file", "-e", spec); err != nil {
			return nil // none
		}
		res, err := run("cat-file", "blob", spec)
		if err != nil {
			return fmt.Errorf("reading %s: %w", ManifestPath, err)
		}
		data = []byte(res.Stdout)
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	return ParseManifest(data)
}

// NewFromTemplate says what a repository started from a template is made as.
type NewFromTemplate struct {
	Owner, Name string
	// Visibility defaults to the template's and may not be wider.
	Visibility forge.Visibility
	// Values are the variables' values, by name; a variable with a default may
	// be left out.
	Values map[string]string
	// IncludeBranches also brings every other branch of the template, each as
	// one commit with no history. Off by default: the default branch only.
	IncludeBranches bool
	By              string
}

// Use makes a new repository from a template: the template's tree at the tip of
// its default branch (and, if asked, of its other branches) as ONE new commit
// each, with no history, the declared variables replaced. The caller checked
// that the person may read the template and make repositories under the owner.
func (s *Service) Use(ctx context.Context, tpl forge.Repository, n NewFromTemplate) (forge.Repository, error) {
	info, err := s.Info(ctx, tpl.ID)
	if err != nil {
		return forge.Repository{}, err
	}
	if !info.IsTemplate {
		return forge.Repository{}, fmt.Errorf("%w: %s is not a template repository", ErrRelation, tpl.Path)
	}
	vis, err := ForkVisibility(tpl.Visibility, n.Visibility)
	if err != nil {
		return forge.Repository{}, err
	}
	man, err := s.Manifest(ctx, tpl)
	if err != nil {
		return forge.Repository{}, err
	}
	values, err := man.Resolve(n.Values)
	if err != nil {
		return forge.Repository{}, fmt.Errorf("%w: %v", forge.ErrInvalid, err)
	}
	refs, err := s.Git.Refs(ctx, tpl.ID)
	if err != nil {
		return forge.Repository{}, err
	}
	want := []string{branchRef(tpl.DefaultBranch)}
	if refs[want[0]] == "" {
		return forge.Repository{}, fmt.Errorf("%w: the template has no commits on %s", ErrRelation, tpl.DefaultBranch)
	}
	if n.IncludeBranches {
		var others []string
		for name := range refs {
			if strings.HasPrefix(name, "refs/heads/") && name != want[0] {
				others = append(others, name)
			}
		}
		sort.Strings(others)
		if len(others) > maxBranches-1 {
			return forge.Repository{}, fmt.Errorf("%w: the template has %d other branches, more than the %d one can bring", forge.ErrInvalid, len(others), maxBranches-1)
		}
		want = append(want, others...)
	}
	if n.Owner == "" {
		n.Owner = n.By
	}
	child, err := s.Repos.Create(ctx, forge.NewRepository{Home: n.Owner, Name: n.Name, DefaultBranch: tpl.DefaultBranch, Visibility: vis})
	if err != nil {
		return forge.Repository{}, err
	}
	undo := func(cause error) (forge.Repository, error) {
		bg := context.WithoutCancel(ctx)
		_ = s.Repos.Delete(bg, child.Path)
		_, _ = s.Git.Purge(bg, child.ID)
		return forge.Repository{}, cause
	}
	_, err = s.Git.Transfer(ctx, child.ID, tpl.ID, want, func(ctx context.Context, scratch string, dst, tips map[string]string) ([]storage.RefUpdate, error) {
		g := s.runner(scratch, ident(n.By, s.now()))
		var ups []storage.RefUpdate
		for i, ref := range want {
			commit, err := s.fromTree(ctx, g, i, tips[ref], tpl.Path, man, values)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", strings.TrimPrefix(ref, "refs/heads/"), err)
			}
			ups = append(ups, storage.RefUpdate{Name: ref, New: commit})
		}
		return ups, nil
	})
	if err != nil {
		return undo(err)
	}
	return child, nil
}

// Resolve completes the values with the defaults and checks every one against
// its rules; a name the manifest does not declare is refused.
func (m Manifest) Resolve(given map[string]string) (map[string]string, error) {
	out := map[string]string{}
	declared := map[string]Variable{}
	for _, v := range m.Variables {
		declared[v.Name] = v
	}
	for k := range given {
		if _, ok := declared[k]; !ok {
			return nil, fmt.Errorf("the template has no variable %q", k)
		}
	}
	for _, v := range m.Variables {
		val, ok := given[v.Name]
		if !ok {
			if v.Default == "" {
				return nil, fmt.Errorf("give a value for %s (%s)", v.Name, v.Description)
			}
			val = v.Default
		}
		if err := v.check(val, m.Names); err != nil {
			return nil, fmt.Errorf("%s: %v", v.Name, err)
		}
		out[v.Name] = val
	}
	return out, nil
}

// tree entry of git ls-tree -l.
type entry struct {
	mode, kind, sha string
	size            int64
	path            string
}

func parseTree(out string) ([]entry, error) {
	var es []entry
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, path, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 4 {
			return nil, fmt.Errorf("unexpected tree entry %q", rec)
		}
		size := int64(-1)
		if f[3] != "-" {
			size, _ = strconv.ParseInt(f[3], 10, 64)
		}
		es = append(es, entry{f[0], f[1], f[2], size, path})
	}
	return es, nil
}

// fromTree makes the commit of one branch of the template: its tree with the
// variables replaced, no parent.
func (s *Service) fromTree(ctx context.Context, g runner, n int, tip, tplPath string, m Manifest, values map[string]string) (string, error) {
	out, err := g.raw(ctx, "ls-tree", "-r", "-z", "-l", tip)
	if err != nil {
		return "", err
	}
	es, err := parseTree(out)
	if err != nil {
		return "", err
	}
	if len(es) > maxEntries {
		return "", fmt.Errorf("the template has %d files, more than the %d one can bring", len(es), maxEntries)
	}
	var globs []*regexp.Regexp
	for _, p := range m.Contents {
		re, err := globRE(p)
		if err != nil {
			return "", err
		}
		globs = append(globs, re)
	}
	rep := replacer(m, values)
	var idx strings.Builder
	seen := map[string]bool{}
	for _, e := range es {
		if e.path == ManifestPath {
			continue
		}
		path := e.path
		if m.Names && rep != nil {
			path = rep.Replace(path)
		}
		if err := safePath(path); err != nil {
			return "", err
		}
		if seen[path] {
			return "", fmt.Errorf("two files of the template would both be named %s", path)
		}
		seen[path] = true
		sha := e.sha
		if rep != nil && e.kind == "blob" && e.mode != "120000" && e.size >= 0 && e.size <= maxSubstituteLen && matchesAny(globs, e.path) {
			sha, err = s.substituted(ctx, g, e.sha, rep)
			if err != nil {
				return "", err
			}
		}
		fmt.Fprintf(&idx, "%s %s\t%s\n", e.mode, sha, path)
	}
	index := fmt.Sprintf("%s/launchway-index-%d", g.dir, n)
	ig := g
	ig.env = append(append([]string{}, g.env...), "GIT_INDEX_FILE="+index)
	if _, err := ig.out(ctx, idx.String(), "update-index", "--add", "--index-info"); err != nil {
		return "", err
	}
	tree, err := ig.out(ctx, "", "write-tree")
	if err != nil {
		return "", err
	}
	return g.out(ctx, "", "commit-tree", tree, "-m", "Initial commit from the template "+tplPath)
}

// substituted reads a blob and, when it is text with a declared token in it,
// stores the text with the values in place of the tokens.
func (s *Service) substituted(ctx context.Context, g runner, sha string, rep *strings.Replacer) (string, error) {
	body, err := g.raw(ctx, "cat-file", "blob", sha)
	if err != nil {
		return "", err
	}
	if strings.IndexByte(body, 0) >= 0 {
		return sha, nil // binary
	}
	now := rep.Replace(body)
	if now == body {
		return sha, nil
	}
	return g.out(ctx, now, "hash-object", "-w", "--stdin")
}

// replacer replaces the declared tokens {{name}} by their values, literally,
// and only those. It is nil when the template declares none.
func replacer(m Manifest, values map[string]string) *strings.Replacer {
	if len(m.Variables) == 0 {
		return nil
	}
	var pairs []string
	for _, v := range m.Variables {
		pairs = append(pairs, "{{"+v.Name+"}}", values[v.Name])
	}
	return strings.NewReplacer(pairs...)
}

func matchesAny(globs []*regexp.Regexp, path string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, re := range globs {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// safePath refuses a path a new repository must never hold: one that leaves
// the repository, names Git's own directory or is not a plain relative path.
func safePath(p string) error {
	if p == "" || len(p) > 4096 || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\\\n") {
		return fmt.Errorf("%q is not a path the new repository can hold", clip(p))
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." || strings.EqualFold(c, ".git") {
			return fmt.Errorf("%q is not a path the new repository can hold", clip(p))
		}
	}
	return nil
}

func clip(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
