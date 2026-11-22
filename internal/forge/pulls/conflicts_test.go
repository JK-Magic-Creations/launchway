package pulls

import (
	"errors"
	"strings"
	"testing"
)

const fileBase = "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\n"

// conflictedWorld has a branch "topic" and a main that disagree on text,
// on a binary file, and on a file one side deleted and the other changed.
func conflictedWorld(t *testing.T) (*world, PullRequest) {
	t.Helper()
	w := newWorld(t)
	w.commit("main", "", map[string]string{"a.txt": fileBase, "b.bin": "\x00base", "gone.txt": "gone\n", "ok.txt": "fine\n"}, "start")
	w.commit("topic", "main", map[string]string{"a.txt": "l1\nTOPIC2\nl3\nl4\nl5\nl6\nl7\nTOPIC8\nl9\n", "b.bin": "\x00topic", "gone.txt": "changed on topic\n"}, "topic work")
	w.commit("main", "", map[string]string{"a.txt": "l1\nMAIN2\nl3\nl4\nl5\nl6\nl7\nMAIN8\nl9\n", "b.bin": "\x00main", "gone.txt": ""}, "main work")
	p := w.open("ada", New{Title: "t", SourceBranch: "topic", TargetBranch: "main"})
	return w, w.get(p.Number)
}

func TestConflictsAreListedWithTheirSidesAndRegions(t *testing.T) {
	w, p := conflictedWorld(t)
	if p.Mergeability.State != MergeConflicts {
		t.Fatalf("%+v", p.Mergeability)
	}
	cs, err := w.svc.Conflicts(w.ctx, repoID, p)
	if err != nil || cs.Clean || len(cs.Files) != 3 || cs.Head != p.HeadSHA {
		t.Fatalf("%+v %v", cs, err)
	}
	byPath := map[string]ConflictFile{}
	for _, f := range cs.Files {
		byPath[f.Path] = f
	}
	a := byPath["a.txt"]
	if a.Kind != KindContent || !strings.Contains(a.Ours.Text, "TOPIC2") || !strings.Contains(a.Theirs.Text, "MAIN2") || a.Base.Text != fileBase {
		t.Fatalf("a.txt: %+v", a)
	}
	var conflicts []Region
	for _, r := range a.Regions {
		if r.Kind == "conflict" {
			conflicts = append(conflicts, r)
		}
	}
	if len(conflicts) != 2 || conflicts[0].Ours != "TOPIC2\n" || conflicts[0].Theirs != "MAIN2\n" || conflicts[0].Base != "l2\n" || !strings.Contains(a.Merged, "<<<<<<< ours") {
		t.Fatalf("regions: %+v", a.Regions)
	}
	if got, _ := applyRegions(a.Regions, []RegionChoice{{Index: conflicts[0].Index, Take: "ours"}, {Index: conflicts[1].Index, Take: "theirs"}}); got != "l1\nTOPIC2\nl3\nl4\nl5\nl6\nl7\nMAIN8\nl9\n" {
		t.Fatalf("applied: %q", got)
	}
	b := byPath["b.bin"]
	if b.Kind != KindBinary || !b.Ours.Binary || b.Ours.Text != "" || len(b.Regions) != 0 {
		t.Fatalf("b.bin: %+v", b)
	}
	for _, c := range b.Choices {
		if c.Take == "both" || c.Take == "custom" {
			t.Errorf("a binary file offers %s", c.Take)
		}
	}
	g := byPath["gone.txt"]
	if g.Kind != KindDeleteModify || !g.Ours.Exists || g.Theirs.Exists || len(g.Choices) < 2 || !strings.Contains(g.Choices[1].Label, "delete") {
		t.Fatalf("gone.txt: %+v", g)
	}
}

func TestResolvingMakesTheMergeCommitOnTheBranch(t *testing.T) {
	w, p := conflictedWorld(t)
	cs, _ := w.svc.Conflicts(w.ctx, repoID, p)
	custom := "l1\nCUSTOM\nl3\n"
	in := ResolveInput{ExpectHead: cs.Head, ExpectTarget: cs.Target, By: "grace", Files: []FileResolution{
		{Path: "a.txt", Regions: []RegionChoice{{Index: 1, Take: "both"}, {Index: 3, Take: "custom", Text: "mixed\n"}}},
		{Path: "b.bin", Take: "theirs"},
		{Path: "gone.txt", Take: "theirs"}, // theirs (the target) deleted it
	}}
	res, err := w.svc.ResolveConflicts(w.ctx, repoID, p, in)
	if err != nil {
		t.Fatal(err)
	}
	refs := w.refs()
	if refs["refs/heads/topic"] != res.Head {
		t.Fatalf("the branch is at %s, not %s", refs["refs/heads/topic"], res.Head)
	}
	_ = custom
	w.svc.Settle()
	np := w.get(p.Number)
	if np.HeadSHA != res.Head || np.Mergeability.State != MergeClean {
		t.Fatalf("after resolving: %+v", np.Mergeability)
	}
	var parents, author, a, bin, gone string
	err = w.st.WithCache(w.ctx, repoID, func(dir string) error {
		g := gitRunner{dir: dir, limits: w.st.Limits()}
		var err error
		if parents, err = g.out(w.ctx, "rev-list", "--parents", "-n", "1", res.Head); err != nil {
			return err
		}
		author, _ = g.out(w.ctx, "log", "-1", "--format=%an", res.Head)
		a, _ = g.raw(w.ctx, "", "show", res.Head+":a.txt")
		bin, _ = g.raw(w.ctx, "", "show", res.Head+":b.bin")
		_, gerr := g.out(w.ctx, "cat-file", "-e", res.Head+":gone.txt")
		gone = "deleted"
		if gerr == nil {
			gone = "kept"
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Fields(parents)) != 3 || author != "grace" {
		t.Fatalf("not a merge by grace: %q %q", parents, author)
	}
	if a != "l1\nTOPIC2\nMAIN2\nl3\nl4\nl5\nl6\nl7\nmixed\nl9\n" || bin != "\x00main" || gone != "deleted" {
		t.Fatalf("a=%q bin=%q gone=%s", a, bin, gone)
	}
	// And the pull request merges now.
	if _, err := w.svc.Merge(w.ctx, repoID, np, MergeInput{Method: "merge", Actor: "grace", ActorRole: RoleOperator, RepoPath: "ada/demo"}); err != nil {
		t.Fatal(err)
	}
}

func TestResolvingRefusesWhatIsNotAnswered(t *testing.T) {
	w, p := conflictedWorld(t)
	cs, _ := w.svc.Conflicts(w.ctx, repoID, p)
	base := func(files ...FileResolution) ResolveInput {
		return ResolveInput{ExpectHead: cs.Head, ExpectTarget: cs.Target, By: "grace", Files: files}
	}
	all := []FileResolution{{Path: "a.txt", Take: "ours"}, {Path: "b.bin", Take: "ours"}, {Path: "gone.txt", Take: "ours"}}
	marked := "<<<<<<< ours\nx\n=======\ny\n>>>>>>> theirs\n"
	cases := map[string]struct {
		in   ResolveInput
		want string
	}{
		"a file left out":      {base(all[:2]...), "gone.txt"},
		"a file that is fine":  {base(append([]FileResolution{{Path: "ok.txt", Take: "ours"}}, all...)...), "does not conflict"},
		"markers left in":      {base(FileResolution{Path: "a.txt", Content: &marked}, all[1], all[2]), "conflict markers"},
		"both for a binary":    {base(all[0], FileResolution{Path: "b.bin", Take: "both"}, all[2]), "not a text file"},
		"content for a binary": {base(all[0], FileResolution{Path: "b.bin", Content: new(string)}, all[2]), "not a text file"},
		"a region unanswered":  {base(FileResolution{Path: "a.txt", Regions: []RegionChoice{{Index: 1, Take: "ours"}}}, all[1], all[2]), "no answer"},
		"a rebase":             {ResolveInput{Method: "rebase", ExpectHead: cs.Head, ExpectTarget: cs.Target, Files: all}, "rebase"},
		"two answers":          {base(FileResolution{Path: "a.txt", Take: "ours", Content: new(string)}, all[1], all[2]), "exactly one"},
		"no tips":              {ResolveInput{Files: all}, "which tips"},
	}
	for name, c := range cases {
		_, err := w.svc.ResolveConflicts(w.ctx, repoID, p, c.in)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (want %q)", name, err, c.want)
		}
	}
	if w.refs()["refs/heads/topic"] != cs.Head {
		t.Error("a refused resolution moved the branch")
	}
	// The same text is accepted when the person says it really holds markers.
	ok := base(FileResolution{Path: "a.txt", Content: &marked, KeepMarkers: true}, all[1], all[2])
	if _, err := w.svc.ResolveConflicts(w.ctx, repoID, p, ok); err != nil {
		t.Fatal(err)
	}
}

func TestResolvingAMovedBranchChangesNothing(t *testing.T) {
	w, p := conflictedWorld(t)
	cs, _ := w.svc.Conflicts(w.ctx, repoID, p)
	w.commit("main", "", map[string]string{"new.txt": "x\n"}, "main moved")
	in := ResolveInput{ExpectHead: cs.Head, ExpectTarget: cs.Target, By: "grace", Files: []FileResolution{{Path: "a.txt", Take: "ours"}, {Path: "b.bin", Take: "ours"}, {Path: "gone.txt", Take: "ours"}}}
	if _, err := w.svc.ResolveConflicts(w.ctx, repoID, p, in); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("%v", err)
	}
	if w.refs()["refs/heads/topic"] != cs.Head {
		t.Error("the branch moved")
	}
}

func TestMarkerDetectionIgnoresUnderlines(t *testing.T) {
	for text, want := range map[string]bool{
		"Title\n=======\nBody\n":                        false,
		"<<<<<<< ours\nx\n=======\ny\n>>>>>>> theirs\n": true,
		"<<<<<<<\nx\n>>>>>>>\n":                         true,
		"a << b\n":                                      false,
		"<<<<<<< only an opening\n":                     false,
	} {
		if got := hasMarkers(text); got != want {
			t.Errorf("%q: %v", text, got)
		}
	}
}

// A pull request from a fork has its conflicts listed from the target's side and
// resolved in the fork's branch; the fork's other branches stay as they are.
func TestConflictsOfAPullRequestFromAFork(t *testing.T) {
	w := newWorld(t)
	w.commit("main", "", map[string]string{"a.txt": fileBase}, "start")
	w.makeFork()
	w.commitIn(forkID, "topic", map[string]string{"a.txt": "l1\nTOPIC2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\n"}, "fork work")
	w.commit("main", "", map[string]string{"a.txt": "l1\nMAIN2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\n"}, "main work")
	p, err := w.svc.Open(w.ctx, repoID, "ada", New{Title: "t", SourceRepo: forkID, SourceBranch: "topic", TargetBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := w.svc.Conflicts(w.ctx, repoID, w.get(p.Number))
	if err != nil || len(cs.Files) != 1 || cs.Files[0].Path != "a.txt" {
		t.Fatalf("%+v %v", cs, err)
	}
	res, err := w.svc.ResolveConflicts(w.ctx, repoID, w.get(p.Number), ResolveInput{ExpectHead: cs.Head, ExpectTarget: cs.Target, By: "grace",
		Files: []FileResolution{{Path: "a.txt", Take: "theirs"}}})
	if err != nil {
		t.Fatal(err)
	}
	frefs, _ := w.st.Refs(w.ctx, forkID)
	if frefs["refs/heads/topic"] != res.Head {
		t.Fatalf("the fork's branch is %s, want %s", frefs["refs/heads/topic"], res.Head)
	}
	np := w.get(p.Number)
	if np.HeadSHA != res.Head || w.refs()[PullRef(p.Number)] != res.Head || np.Mergeability.State != MergeClean {
		t.Fatalf("the pull request after: %+v refs %v", np.Mergeability, w.refs())
	}
	if _, ok := w.refs()["refs/heads/topic"]; ok {
		t.Error("the target gained the fork's branch")
	}
}

// The merge commit that resolving makes goes through commitMerge, and with the installation's key available it is
// signed with it, like every other commit the server makes.
func TestResolvingSignsTheMergeCommitWithTheInstallationsKey(t *testing.T) {
	w, p := conflictedWorld(t)
	svc := w.signingOn(Rule{Pattern: "main", RequireSigned: true, ApprovalRole: RoleOperator})
	cs, _ := w.svc.Conflicts(w.ctx, repoID, p)
	res, err := w.svc.ResolveConflicts(w.ctx, repoID, p, ResolveInput{ExpectHead: cs.Head, ExpectTarget: cs.Target, By: "grace", Files: []FileResolution{
		{Path: "a.txt", Take: "ours"}, {Path: "b.bin", Take: "theirs"}, {Path: "gone.txt", Take: "theirs"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.VerifyRepo(w.ctx, repoID, []string{res.Head})
	if err != nil || len(got) != 1 || got[0].State != "verified" || !got[0].Installation {
		t.Fatalf("the resolution's merge commit is not signed by the installation: %+v %v", got, err)
	}
}
