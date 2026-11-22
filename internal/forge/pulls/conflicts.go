package pulls

// Resolving a pull request's conflicts without leaving the browser: this file
// lists them (the three sides of each conflicting file, the merged text with
// its conflicting regions as data), resolve.go takes the person's answers and
// makes the merge commit of the target into the pull request's branch.
//
// "Ours" is the pull request's branch, "theirs" the target branch being merged
// into it, as when you run `git merge target` on the branch.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// Bounds. A file bigger than maxSideBytes on any side is listed without its
// text and can only be resolved by taking a side.
const (
	maxConflictFiles = 200
	maxSideBytes     = 1 << 20
	maxResolveBytes  = 8 << 20
	maxRegions       = 5000
)

// Side is one version of a conflicting file.
type Side struct {
	Exists   bool   `json:"exists"`
	Mode     string `json:"mode,omitempty"`
	SHA      string `json:"sha,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Binary   bool   `json:"binary,omitempty"`
	TooLarge bool   `json:"too_large,omitempty"`
	// Text is the file's text, for a text file within the size bound.
	Text string `json:"text,omitempty"`
}

// Choice is one answer a person may give for a file, in words.
type Choice struct {
	Take  string `json:"take"`
	Label string `json:"label"`
}

// Conflict kinds.
const (
	KindContent      = "content"       // both sides changed the same text
	KindBinary       = "binary"        // both changed a file that is not text
	KindDeleteModify = "delete_modify" // one side deleted what the other changed
	KindRename       = "rename"        // a rename that clashes with something
	KindStructure    = "structure"     // a file against a folder, or a change of type
	KindAddAdd       = "add_add"       // both added a file of the same name, differing
)

// ConflictFile is one conflicting path.
type ConflictFile struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Base    Side   `json:"base"`
	Ours    Side   `json:"ours"`
	Theirs  Side   `json:"theirs"`
	// Merged is the text as git left it, with conflict markers, for a text file.
	Merged string `json:"merged,omitempty"`
	// Regions is the same as data: the common text and the conflicting places.
	Regions []Region `json:"regions,omitempty"`
	Choices []Choice `json:"choices"`
}

// Conflicts is every conflict of merging the target into the branch.
type Conflicts struct {
	// Head is the pull request's branch tip and Target the target's, the two
	// the answers are for: a resolution names them and is refused if either moved.
	Head   string         `json:"head"`
	Target string         `json:"target"`
	Clean  bool           `json:"clean"`
	Files  []ConflictFile `json:"files"`
	// Truncated says more files conflict than are listed (then they cannot be
	// resolved here).
	Truncated bool `json:"truncated,omitempty"`
}

type stageEntry struct{ mode, sha string }

// parsedMerge is git merge-tree's answer, read.
type parsedMerge struct {
	tree   string
	stages map[string]*[3]stageEntry // path -> stages 1,2,3 (index 0,1,2)
	kinds  map[string]string         // path -> git's conflict type
	msgs   map[string]string         // path -> git's message
}

// mergeTree runs the trial merge of tip into head and reads which paths
// conflict, with their stages and git's own description of each.
func (g gitRunner) mergeTree(ctx context.Context, head, tip string) (parsedMerge, bool, error) {
	args := []string{"merge-tree", "--write-tree", "-z", head, tip}
	res, err := gitexec.Run(ctx, g.spec(args...))
	out := res.Stdout
	clean := err == nil
	if err != nil {
		var ee *gitexec.ExitError
		if !errors.As(err, &ee) || ee.Code != 1 {
			return parsedMerge{}, false, gitErr(args, err)
		}
		out = ee.Stdout
	}
	pm := parsedMerge{stages: map[string]*[3]stageEntry{}, kinds: map[string]string{}, msgs: map[string]string{}}
	recs := strings.Split(out, "\x00")
	if len(recs) == 0 || len(recs[0]) < 40 {
		return pm, false, fmt.Errorf("git merge-tree said %q", clipText(out))
	}
	pm.tree = recs[0]
	i := 1
	for ; i < len(recs) && recs[i] != ""; i++ {
		meta, path, ok := strings.Cut(recs[i], "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			return pm, false, fmt.Errorf("unexpected merge-tree entry %q", clipText(recs[i]))
		}
		st, _ := strconv.Atoi(f[2])
		if st < 1 || st > 3 {
			continue
		}
		e := pm.stages[path]
		if e == nil {
			e = &[3]stageEntry{}
			pm.stages[path] = e
		}
		e[st-1] = stageEntry{f[0], f[1]}
	}
	// Then the messages: count, that many paths, a type, a text.
	for i++; i < len(recs); {
		n, err := strconv.Atoi(recs[i])
		if err != nil || n < 0 || i+n+2 >= len(recs) {
			break
		}
		paths, typ, msg := recs[i+1:i+1+n], recs[i+1+n], recs[i+2+n]
		for _, p := range paths {
			if strings.HasPrefix(typ, "CONFLICT") {
				pm.kinds[p], pm.msgs[p] = typ, strings.TrimSpace(msg)
			}
		}
		i += n + 3
	}
	return pm, clean, nil
}

func clipText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// kindOf says what sort of conflict a path is, from its stages and git's note.
func kindOf(typ string, e *[3]stageEntry, binary bool) string {
	base, ours, theirs := e[0].sha != "", e[1].sha != "", e[2].sha != ""
	switch {
	case strings.Contains(typ, "rename"):
		return KindRename
	case strings.Contains(typ, "file/directory") || strings.Contains(typ, "distinct types"):
		return KindStructure
	case base && ours != theirs:
		return KindDeleteModify
	case !base && ours && theirs:
		if binary {
			return KindBinary
		}
		return KindAddAdd
	case base && ours && theirs:
		if binary {
			return KindBinary
		}
		return KindContent
	}
	return KindStructure
}

// choicesFor are the answers a kind of conflict takes, in words.
func choicesFor(kind string, e *[3]stageEntry, tooLarge bool, branch, target string) []Choice {
	ours, theirs := e[1].sha != "", e[2].sha != ""
	describe := func(has bool, who string) string {
		if has {
			return "keep the file as " + who + " has it"
		}
		return "delete the file, as " + who + " did"
	}
	cs := []Choice{{"ours", describe(ours, "the branch "+branch)}, {"theirs", describe(theirs, "the target "+target)}}
	if (kind == KindContent || kind == KindAddAdd) && !tooLarge {
		cs = append(cs, Choice{"both", "keep both versions' text, the branch's first"}, Choice{"custom", "write the file yourself"})
	}
	if ours || theirs {
		if (kind == KindRename || kind == KindStructure) && (ours && theirs) {
			cs = append(cs, Choice{"delete", "delete the file from the result"})
		}
	}
	return cs
}

func (g gitRunner) blobInfo(ctx context.Context, shas []string) (map[string]int64, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	out, err := g.raw(ctx, strings.Join(shas, "\n")+"\n", "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	sizes := map[string]int64{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(l)
		if len(f) == 3 {
			n, _ := strconv.ParseInt(f[2], 10, 64)
			sizes[f[0]] = n
		}
	}
	return sizes, nil
}

func (g gitRunner) blob(ctx context.Context, sha string, limit int64) ([]byte, error) {
	sp := g.spec("cat-file", "blob", sha)
	sp.MaxOutput = limit + 1
	res, err := gitexec.Run(ctx, sp)
	if err != nil {
		return nil, gitErr([]string{"cat-file"}, err)
	}
	return []byte(res.Stdout), nil
}

func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// Conflicts lists what stops the target from being merged into the pull
// request's branch, from the live tips, with the text of each side.
func (s *Service) Conflicts(ctx context.Context, repoID string, p PullRequest) (Conflicts, error) {
	out := Conflicts{Files: []ConflictFile{}}
	if p.State != Open {
		return out, conflict("this pull request is %s", p.State)
	}
	err := s.Git.Scratch(ctx, repoID, func(ctx context.Context, refs map[string]string, scratch string) error {
		head, tip := refs[p.headRef()], refs[branchRef(p.TargetBranch)]
		if head == "" || tip == "" {
			return conflict("the pull request's branch or its target no longer exists")
		}
		out.Head, out.Target = head, tip
		g := s.runner(scratch, true)
		var err error
		out.Files, out.Truncated, out.Clean, err = s.listConflicts(ctx, g, p, head, tip)
		return err
	})
	return out, err
}

func (s *Service) listConflicts(ctx context.Context, g gitRunner, p PullRequest, head, tip string) ([]ConflictFile, bool, bool, error) {
	pm, clean, err := g.mergeTree(ctx, head, tip)
	if err != nil {
		return nil, false, false, err
	}
	if clean {
		return []ConflictFile{}, false, true, nil
	}
	paths := make([]string, 0, len(pm.stages))
	for path := range pm.stages {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	truncated := false
	if len(paths) > maxConflictFiles {
		paths, truncated = paths[:maxConflictFiles], true
	}
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
		return nil, false, false, err
	}
	files := make([]ConflictFile, 0, len(paths))
	for _, path := range paths {
		f, err := s.conflictFile(ctx, g, p, path, pm, sizes)
		if err != nil {
			return nil, false, false, err
		}
		files = append(files, f)
	}
	return files, truncated, false, nil
}

func (s *Service) conflictFile(ctx context.Context, g gitRunner, p PullRequest, path string, pm parsedMerge, sizes map[string]int64) (ConflictFile, error) {
	e := pm.stages[path]
	f := ConflictFile{Path: path, Message: pm.msgs[path]}
	sides := [3]*Side{&f.Base, &f.Ours, &f.Theirs}
	texts := [3][]byte{}
	tooLarge, binary := false, false
	for i, se := range e {
		sd := sides[i]
		if se.sha == "" {
			continue
		}
		sd.Exists, sd.Mode, sd.SHA, sd.Size = true, se.mode, se.sha, sizes[se.sha]
		if sd.Size > maxSideBytes || se.mode == "160000" {
			sd.TooLarge, tooLarge = true, true
			continue
		}
		b, err := g.blob(ctx, se.sha, maxSideBytes)
		if err != nil {
			return f, err
		}
		texts[i] = b
		if isBinary(b) || se.mode == "120000" {
			sd.Binary, binary = true, true
		}
	}
	f.Kind = kindOf(pm.kinds[path], e, binary)
	if f.Message == "" {
		f.Message = "the two sides changed " + path + " in ways that cannot be combined automatically"
	}
	if !tooLarge && !binary {
		for i, sd := range sides {
			if sd.Exists {
				sd.Text = string(texts[i])
			}
		}
		if (f.Kind == KindContent || f.Kind == KindAddAdd) && e[1].sha != "" && e[2].sha != "" {
			merged, regions, err := g.mergeText(ctx, texts[0], texts[1], texts[2], p.SourceBranch, p.TargetBranch)
			if err != nil {
				return f, err
			}
			f.Merged = merged
			if len(regions) <= maxRegions {
				f.Regions = regions
			}
		}
	}
	f.Choices = choicesFor(f.Kind, e, tooLarge, p.SourceBranch, p.TargetBranch)
	return f, nil
}

// mergeText merges two texts against their ancestor with git merge-file and
// returns the text with markers and the regions read from it. Nothing is
// run in a shell, and the three texts are written to files the scratch
// repository owns.
func (g gitRunner) mergeText(ctx context.Context, base, ours, theirs []byte, oursLabel, theirsLabel string) (string, []Region, error) {
	dir, err := os.MkdirTemp(g.dir, "merge-file-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(dir)
	files := [3]string{filepath.Join(dir, "base"), filepath.Join(dir, "ours"), filepath.Join(dir, "theirs")}
	for i, b := range [3][]byte{base, ours, theirs} {
		if err := os.WriteFile(files[i], b, 0o600); err != nil {
			return "", nil, err
		}
	}
	// Without -p the result is written over the first file; the standard output
	// of a run that exits with the number of conflicts is not kept whole.
	args := []string{"merge-file", "--diff3", "-L", "ours", "-L", "base", "-L", "theirs", files[1], files[0], files[2]}
	if _, err := gitexec.Run(ctx, g.spec(args...)); err != nil {
		var ee *gitexec.ExitError
		if !errors.As(err, &ee) || ee.Code < 1 || ee.Code > 127 {
			return "", nil, gitErr(args, err)
		}
	}
	data, err := os.ReadFile(files[1])
	if err != nil {
		return "", nil, err
	}
	out := string(data)
	regions, ok := parseRegions(out)
	if !ok {
		regions = nil
	}
	return out, regions, nil
}

// ErrNoConflicts is for a resolution of a pull request that merges cleanly.
var ErrNoConflicts = errors.New("nothing conflicts")

var _ = storage.ErrNoSuchRef
