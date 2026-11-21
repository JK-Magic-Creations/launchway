package mirror_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

// fillEveryKind gives a source what a busy forge repository has: branches,
// an orphan branch, lightweight and annotated tags, notes, and pull request
// refs that no branch holds.
func fillEveryKind(s *source) {
	populate(s)
	s.git("checkout", "-q", "--orphan", "docs-only")
	s.git("rm", "-rqf", ".")
	s.commit("an unrelated history")
	s.git("tag", "-a", "-m", "annotated on the orphan", "orphan-v1")
	s.git("checkout", "-q", "main")
	s.git("checkout", "-q", "-b", "feature-x")
	pr1 := s.commit("pull request one")
	s.git("checkout", "-q", "main")
	s.git("checkout", "-q", "-b", "feature-y")
	pr2 := s.commit("pull request two")
	s.git("checkout", "-q", "main")
	s.git("branch", "-q", "-D", "feature-x", "feature-y")
	s.pushAll()
	s.git("push", "-q", "origin", "docs-only:refs/heads/docs-only")
	s.git("push", "-q", "origin", pr1+":refs/pull/1/head", pr2+":refs/pull/2/head", pr2+":refs/pull/2/merge", pr1+":refs/changes/01/1/1")
}

func TestImportBringsEveryRefAndVerifyReportsThemOneByOne(t *testing.T) {
	o := newOldServer(t, oldToken)
	web := o.add(t, "acme/web", fillEveryKind)
	w, ctx := importWorld(t, o), context.Background()
	src := mirror.SourceRequest{Connection: "old", Refs: true}
	if _, err := w.h.Mirrors.Import(ctx, mirror.ImportRequest{SourceRequest: src, Wait: true}, "ada"); err != nil {
		t.Fatal(err)
	}
	res, err := w.h.Mirrors.Verify(ctx, src)
	if err != nil || !res.Equal || len(res.Items) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	it := res.Items[0]
	counts := map[string][3]int{}
	for _, n := range it.Namespaces {
		counts[n.Namespace] = [3]int{n.Source, n.Hosted, n.Equal}
	}
	// main, dev, docs-only; v1, v2, orphan-v1; the notes ref; three pull refs.
	for ns, want := range map[string][3]int{"refs/heads": {3, 3, 3}, "refs/tags": {3, 3, 3}, "refs/notes": {1, 1, 1}, "refs/imported/pull": {3, 3, 3}} {
		if counts[ns] != want {
			t.Errorf("%s: %v, want %v (%+v)", ns, counts[ns], want, it.Namespaces)
		}
	}
	if it.NotImported["refs/changes"] != 1 {
		t.Errorf("the refs that are not copied are not reported: %v", it.NotImported)
	}
	byName := map[string]mirror.RefRow{}
	for _, r := range it.Refs {
		byName[r.Ref] = r
	}
	for _, n := range []string{"refs/imported/pull/1/head", "refs/imported/pull/2/head", "refs/imported/pull/2/merge", "refs/tags/orphan-v1", "refs/tags/v2", "refs/heads/docs-only"} {
		r, ok := byName[n]
		if !ok || r.Status != mirror.RefEqual || len(r.Source) != 40 || r.Source != r.Hosted {
			t.Errorf("%s: %+v", n, r)
		}
	}
	// The annotated tag is the tag object, as at the source; a hosted clone has the history of a pull ref.
	if want := web.git("rev-parse", "refs/tags/v2"); byName["refs/tags/v2"].Hosted != want {
		t.Errorf("annotated tag: %s, want %s", byName["refs/tags/v2"].Hosted, want)
	}
	if _, ok := w.allRefs(t, "acme/web")["refs/pull/1/head"]; ok {
		t.Error("a source pull ref was stored in refs/pull/, which is this server's own")
	}

	// A pull request that appears at the source later is a difference until synced; without
	// refs: true only the differing refs are listed.
	web.git("push", "-q", "origin", web.git("rev-parse", "main")+":refs/pull/3/head")
	res, _ = w.h.Mirrors.Verify(ctx, mirror.SourceRequest{Connection: "old"})
	it = res.Items[0]
	if res.Equal || len(it.Refs) != 1 || it.Refs[0].Ref != "refs/imported/pull/3/head" || it.Refs[0].Status != mirror.RefMissing {
		t.Fatalf("%+v", it)
	}
	if !strings.Contains(strings.Join(it.Differences, "\n"), "refs/imported/pull/3/head: missing here") {
		t.Errorf("%v", it.Differences)
	}
	for _, m := range mustAll(t, w) {
		w.mustSync(m.ID)
	}
	if res, _ = w.h.Mirrors.Verify(ctx, mirror.SourceRequest{Connection: "old"}); !res.Equal {
		t.Fatalf("after a sync: %+v", res)
	}
}

// allRefs are all the refs of record of a hosted repository.
func (w *world) allRefs(t *testing.T, path string) map[string]string {
	t.Helper()
	conns, err := w.h.Forge.Connections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range conns {
		if r, err := w.h.Forge.RepositoryByPath(context.Background(), c.ID, path); err == nil {
			all, err := w.h.Store.Refs(context.Background(), r.ID)
			if err != nil {
				t.Fatal(err)
			}
			return all
		}
	}
	t.Fatalf("no repository %s", path)
	return nil
}
