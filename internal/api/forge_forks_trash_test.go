package api

import (
	"strings"
	"testing"
)

// A base that was renamed keeps receiving pull requests from its forks (through its new address and through the old
// one, which answers with a redirect that keeps the method), and the fork keeps syncing from it.
func TestARenamedBaseKeepsReceivingForkPullRequests(t *testing.T) {
	f := newForkEnv(t)
	f.ada.do("PATCH", f.acme, `{"allow_forks":"on"}`)
	if r := f.vic.do("POST", f.acme+"/forks", `{}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	work := t.TempDir()
	mustGit(t, work, "clone", "-q", withToken(f.e.srv.URL, "/git/vic/shop.git", f.vicTok), "w")
	w := work + "/w"
	mustGit(t, w, "checkout", "-q", "-b", "topic")
	commit(t, w, "code.txt", "l1\nTOPIC\nl3\nl4\nl5\nl6\nl7\nl8\nl9\n", "topic change")
	mustGit(t, w, "push", "-q", "origin", "topic")
	mustGit(t, w, "checkout", "-q", "-b", "second", "main")
	commit(t, w, "other.txt", "o\n", "second change")
	mustGit(t, w, "push", "-q", "origin", "second")

	if r := f.ada.do("PATCH", f.acme, `{"name":"store"}`); r.status != 200 {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	renamed := f.e.srv.URL + "/api/v1/repositories/acme/store"
	// Through the old address: a redirect that keeps the method; following it opens the pull request.
	r := f.vic.do("POST", f.acme+"/pulls/from-fork", `{"head":"vic:topic","title":"Through the old address"}`)
	if r.status != 308 || !strings.HasSuffix(r.location(), "/api/v1/repositories/acme/store/pulls/from-fork") {
		t.Fatalf("the old address: %d %q", r.status, r.location())
	}
	var pr prView
	r = f.vic.do("POST", f.e.srv.URL+r.location(), `{"head":"vic:topic","title":"Through the old address"}`)
	r.json(t, &pr)
	if r.status != 201 || pr.SourceRepo != "vic/shop" || pr.FetchRef != "refs/pull/1/head" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// And through the new one.
	r = f.vic.do("POST", renamed+"/pulls/from-fork", `{"head":"vic:second","title":"Through the new address"}`)
	if r.status != 201 {
		t.Fatalf("the new address: %d %s", r.status, r.body)
	}
	// The fork still knows its parent, now by its new name, and syncs from it.
	var st struct {
		Parent string `json:"parent"`
	}
	f.vic.get(f.e.srv.URL+"/api/v1/repositories/vic/shop/fork").json(t, &st)
	if st.Parent != "acme/store" {
		t.Fatalf("the fork's parent after the rename: %q", st.Parent)
	}
	f.pushMain(t, "new.txt", "n\n", "the base moves")
	if r := f.vic.do("POST", f.e.srv.URL+"/api/v1/repositories/vic/shop/sync", `{}`); r.status != 200 {
		t.Fatalf("a sync from the renamed parent: %d %s", r.status, r.body)
	}
}

// A repository that is a fork parent, or a fork, keeps the relationship through the trash and a restore; the permanent
// removal of the parent never takes a pack the fork still names, and the removal of the fork then leaves nothing.
func TestForkRelationshipSurvivesTheTrashAndPurgeKeepsSharedPacks(t *testing.T) {
	f := newForkEnv(t)
	f.ada.do("PATCH", f.acme, `{"allow_forks":"on"}`)
	if r := f.vic.do("POST", f.acme+"/forks", `{}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	fork := f.e.srv.URL + "/api/v1/repositories/vic/shop"
	trash := f.e.srv.URL + "/api/v1/repository-trash"
	standing := func() string {
		var st struct {
			Parent string `json:"parent"`
		}
		r := f.vic.get(fork + "/fork")
		r.json(t, &st)
		return st.Parent
	}
	idOf := func(path string) string {
		var page struct {
			Items []struct {
				ID, Path string
			} `json:"items"`
		}
		f.ada.get(trash).json(t, &page)
		for _, it := range page.Items {
			if it.Path == path {
				return it.ID
			}
		}
		t.Fatalf("%s is not in the trash: %+v", path, page)
		return ""
	}
	// The parent goes to the trash and comes back: the fork still has it as its parent.
	if r := f.ada.do("DELETE", f.acme, ""); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	if r := f.ada.do("POST", trash+"/"+idOf("acme/shop")+"/restore", `{}`); r.status != 200 {
		t.Fatalf("restore: %d %s", r.status, r.body)
	}
	if got := standing(); got != "acme/shop" {
		t.Fatalf("the parent after a restore: %q", got)
	}
	// The fork too.
	if r := f.vic.do("DELETE", fork, ""); r.status != 204 {
		t.Fatalf("delete the fork: %d %s", r.status, r.body)
	}
	if r := f.vic.do("POST", trash+"/"+idOf("vic/shop")+"/restore", `{}`); r.status != 200 {
		t.Fatalf("restore the fork: %d %s", r.status, r.body)
	}
	if got := standing(); got != "acme/shop" {
		t.Fatalf("the fork after a restore: %q", got)
	}
	// Remove the parent for good: the fork still clones, whole.
	if r := f.ada.do("DELETE", f.acme, ""); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	if r := f.ada.do("DELETE", trash+"/"+idOf("acme/shop"), `{"confirm":"acme/shop"}`); r.status != 200 && r.status != 204 {
		t.Fatalf("purge the parent: %d %s", r.status, r.body)
	}
	clone := t.TempDir()
	mustGit(t, clone, "clone", "-q", withToken(f.e.srv.URL, "/git/vic/shop.git", f.vicTok), "c")
	mustGit(t, clone+"/c", "fsck", "--full", "--strict")
}
