package pulls

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// FileResolution is a person's answer for one conflicting file: one of Take, a
// whole Content, or a choice for each conflicting region.
type FileResolution struct {
	Path string `json:"path"`
	// Take is "ours" (the pull request's branch has it), "theirs" (the target
	// has it), "both" (text: ours, then theirs) or "delete".
	Take string `json:"take,omitempty"`
	// Content is the whole resolved text of a text file.
	Content *string `json:"content,omitempty"`
	// Regions answers each conflicting region of a text file.
	Regions []RegionChoice `json:"regions,omitempty"`
	// KeepMarkers confirms that the resolved text really contains lines that
	// look like conflict markers (a file about merging, a test fixture).
	KeepMarkers bool `json:"keep_markers,omitempty"`
}

// ResolveInput is a request to resolve the conflicts of a pull request.
type ResolveInput struct {
	// ExpectHead and ExpectTarget are the tips the answers were written for
	// (Conflicts.Head and Target): if either moved, nothing is done.
	ExpectHead, ExpectTarget string
	Files                    []FileResolution
	// Method is "merge", the default; a rebase cannot be answered all at once.
	Method string
	// Message overrides the merge commit's message.
	Message string
	// By is who resolves, and the author of the commit.
	By string
}

// ResolveResult is what resolving did.
type ResolveResult struct {
	Head     string   `json:"head"`
	Resolved []string `json:"resolved"`
}

// commitMerge makes the merge commit of the target into the branch, authored by
// the person. Every commit the server makes for the branch's update goes through
// here so that what signs them (commit signing) has one place to find them.
func (s *Service) commitMerge(ctx context.Context, g gitRunner, by, tree, head, tip, message string) (string, error) {
	g.env = ident(by, s.now())
	return g.out(ctx, "commit-tree", tree, "-p", head, "-p", tip, "-m", message)
}

func (in ResolveInput) check() error {
	if in.Method != "" && in.Method != "merge" {
		return invalid("conflicts are resolved by a merge of the target into the branch; a rebase has to be answered commit by commit, which is done on your computer")
	}
	if len(in.Files) == 0 {
		return invalid("name at least one file to resolve")
	}
	if len(in.Files) > maxConflictFiles {
		return invalid("at most %d files can be resolved at once", maxConflictFiles)
	}
	seen := map[string]bool{}
	total := 0
	for _, f := range in.Files {
		if seen[f.Path] {
			return invalid("%s is answered twice", f.Path)
		}
		seen[f.Path] = true
		n := 0
		if f.Take != "" {
			n++
		}
		if f.Content != nil {
			n++
			if len(*f.Content) > maxSideBytes {
				return invalid("the text given for %s is %d KiB, more than the %d KiB a file may have here", f.Path, len(*f.Content)>>10, maxSideBytes>>10)
			}
			total += len(*f.Content)
		}
		if len(f.Regions) > 0 {
			n++
			if len(f.Regions) > maxRegions {
				return invalid("%s has more than %d regions", f.Path, maxRegions)
			}
			for _, r := range f.Regions {
				total += len(r.Text)
			}
		}
		if n != 1 {
			return invalid("%s: give exactly one of take, content or regions", f.Path)
		}
	}
	if total > maxResolveBytes {
		return invalid("the answers hold %d MiB of text, more than the %d MiB one resolution may", total>>20, maxResolveBytes>>20)
	}
	if len(in.Message) > 4096 {
		return invalid("the message is longer than 4096 characters")
	}
	return nil
}

// resolution builds the merge commit from the person's answers, in a scratch
// repository that can read head and tip.
func (s *Service) resolution(ctx context.Context, g gitRunner, p PullRequest, head, tip string, in ResolveInput) (string, error) {
	pm, clean, err := g.mergeTree(ctx, head, tip)
	if err != nil {
		return "", err
	}
	if clean {
		return "", conflict("%s merges into %s without conflicts now; update the branch instead", p.TargetBranch, p.SourceBranch)
	}
	if len(pm.stages) > maxConflictFiles {
		return "", conflict("%d files conflict, more than the %d that can be resolved here; resolve them on your computer", len(pm.stages), maxConflictFiles)
	}
	given := map[string]FileResolution{}
	for _, f := range in.Files {
		given[f.Path] = f
		if pm.stages[f.Path] == nil {
			return "", invalid("%s does not conflict; only the conflicting files are answered", f.Path)
		}
	}
	var open []string
	for path := range pm.stages {
		if _, ok := given[path]; !ok {
			open = append(open, path)
		}
	}
	if len(open) > 0 {
		sort.Strings(open)
		return "", invalid("these files still conflict and have no answer: %s", strings.Join(open, ", "))
	}
	zero := strings.Repeat("0", len(tip))
	var idx strings.Builder
	var paths []string
	for path := range given {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var shas []string
	for _, path := range paths {
		for _, e := range pm.stages[path] {
			if e.sha != "" {
				shas = append(shas, e.sha)
			}
		}
	}
	sizes, err := g.blobInfo(ctx, shas)
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		mode, sha, err := s.answer(ctx, g, p, pm, sizes, given[path])
		if err != nil {
			return "", err
		}
		if sha == "" {
			fmt.Fprintf(&idx, "0 %s\t%s\n", zero, path)
		} else {
			fmt.Fprintf(&idx, "%s %s\t%s\n", mode, sha, path)
		}
	}
	index := filepath.Join(g.dir, "resolve-index")
	ig := g
	ig.env = []string{"GIT_INDEX_FILE=" + index}
	if _, err := ig.out(ctx, "read-tree", pm.tree); err != nil {
		return "", err
	}
	if _, err := ig.raw(ctx, idx.String(), "update-index", "--index-info"); err != nil {
		return "", err
	}
	tree, err := ig.out(ctx, "write-tree")
	if err != nil {
		return "", err
	}
	msg := in.Message
	if strings.TrimSpace(msg) == "" {
		msg = fmt.Sprintf("Merge branch '%s' into %s\n\nConflicts resolved in: %s", p.TargetBranch, p.SourceBranch, strings.Join(paths, ", "))
	}
	return s.commitMerge(ctx, g, in.By, tree, head, tip, msg)
}

// answer turns one resolution into the entry the file gets: its mode and the
// blob, or no blob when the file is deleted.
func (s *Service) answer(ctx context.Context, g gitRunner, p PullRequest, pm parsedMerge, sizes map[string]int64, r FileResolution) (mode, sha string, err error) {
	e := pm.stages[r.Path]
	side := func(i int) (string, string) { return e[i].mode, e[i].sha }
	switch r.Take {
	case "ours":
		m, h := side(1)
		return m, h, nil
	case "theirs":
		m, h := side(2)
		return m, h, nil
	case "delete":
		return "", "", nil
	}
	file, err := s.conflictFile(ctx, g, p, r.Path, pm, sizes)
	if err != nil {
		return "", "", err
	}
	if file.Ours.Binary || file.Theirs.Binary || file.Ours.TooLarge || file.Theirs.TooLarge || file.Base.Binary || file.Base.TooLarge {
		return "", "", invalid("%s is not a text file this editor can combine (a binary file, a symbolic link, a submodule or a very large file): take the branch's version or the target's", r.Path)
	}
	if file.Kind != KindContent && file.Kind != KindAddAdd {
		return "", "", invalid("%s is a %s conflict: take the branch's version, the target's, or delete it", r.Path, strings.ReplaceAll(file.Kind, "_", "/"))
	}
	var text string
	switch {
	case r.Take == "both":
		text = file.Ours.Text
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += file.Theirs.Text
	case r.Content != nil:
		text = *r.Content
	case r.Take != "":
		return "", "", invalid("%s: take ours, theirs, both or delete", r.Path)
	default:
		if len(file.Regions) == 0 {
			return "", "", invalid("%s cannot be answered region by region (its text has lines that look like markers, or it is too long); give its whole text", r.Path)
		}
		if text, err = applyRegions(file.Regions, r.Regions); err != nil {
			return "", "", fmt.Errorf("%s: %w", r.Path, err)
		}
	}
	if len(text) > maxSideBytes {
		return "", "", invalid("%s would be %d KiB, more than the %d KiB a file may have here", r.Path, len(text)>>10, maxSideBytes>>10)
	}
	if !r.KeepMarkers && hasMarkers(text) {
		return "", "", invalid("%s still has conflict markers (<<<<<<< ... >>>>>>>); remove them, or say the file really contains such text (keep_markers)", r.Path)
	}
	mode = file.Ours.Mode
	if mode == "" {
		mode = file.Theirs.Mode
	}
	if mode == "" {
		mode = "100644"
	}
	sha, err = g.hashText(ctx, text)
	return mode, sha, err
}

func (g gitRunner) hashText(ctx context.Context, text string) (string, error) {
	out, err := g.raw(ctx, text, "hash-object", "-w", "--stdin")
	return strings.TrimSpace(out), err
}

// ResolveConflicts answers a pull request's conflicts: it makes the merge
// commit of the target into the pull request's branch with the answers, and
// moves the branch to it, as UpdateBranch does when nothing conflicts. A
// branch in a fork is updated in the fork. The caller checked that the person
// may push to that branch (and, for a fork, may edit it) and that the branch is
// not protected against them.
func (s *Service) ResolveConflicts(ctx context.Context, repoID string, p PullRequest, in ResolveInput) (ResolveResult, error) {
	if p.State != Open {
		return ResolveResult{}, conflict("this pull request is %s", p.State)
	}
	if err := in.check(); err != nil {
		return ResolveResult{}, err
	}
	if in.ExpectHead == "" || in.ExpectTarget == "" {
		return ResolveResult{}, invalid("say which tips the answers are for (head and target from the conflicts list), so a branch that moved is not overwritten")
	}
	tref := branchRef(p.TargetBranch)
	var made, dstRepo string
	var sref string
	plan := func(ctx context.Context, scratch string, head, tip string) ([]storage.RefUpdate, error) {
		if head == "" || tip == "" {
			return nil, conflict("the pull request's branch or its target no longer exists")
		}
		if head != in.ExpectHead || tip != in.ExpectTarget {
			return nil, conflict("the branch or its target moved since the conflicts were listed; list them again and answer again")
		}
		var err error
		made, err = s.resolution(ctx, s.runner(scratch, true), p, head, tip, in)
		if err != nil {
			return nil, err
		}
		return []storage.RefUpdate{{Name: sref, Old: head, New: made}}, nil
	}
	var err error
	if p.SourceRepo == "" {
		dstRepo, sref = repoID, branchRef(p.SourceBranch)
		_, err = s.Git.Mutate(ctx, repoID, func(ctx context.Context, refs map[string]string, scratch string) ([]storage.RefUpdate, error) {
			if refs[sref] != p.HeadSHA {
				return nil, conflict("the branch %s moved since the pull request last looked; refresh and try again", p.SourceBranch)
			}
			return plan(ctx, scratch, refs[sref], refs[tref])
		})
	} else {
		dstRepo, sref = p.SourceRepo, branchRef(p.SourceBranch)
		_, err = s.Git.Transfer(ctx, dstRepo, repoID, []string{tref}, func(ctx context.Context, scratch string, dst, tips map[string]string) ([]storage.RefUpdate, error) {
			if dst[sref] != p.HeadSHA {
				return nil, conflict("the fork's branch %s moved since the pull request last looked; refresh and try again", p.SourceBranch)
			}
			return plan(ctx, scratch, dst[sref], tips[tref])
		})
	}
	if err != nil {
		return ResolveResult{}, err
	}
	ups := []RefUpdate{{Name: sref, Old: in.ExpectHead, New: made}}
	s.Pushed(ctx, dstRepo, in.By, ups)
	if s.AfterBranchMoved != nil {
		s.AfterBranchMoved(ctx, dstRepo, in.By, ups)
	}
	var names []string
	for _, f := range in.Files {
		names = append(names, f.Path)
	}
	sort.Strings(names)
	return ResolveResult{Head: made, Resolved: names}, nil
}
