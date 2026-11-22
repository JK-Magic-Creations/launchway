package api

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/work"
)

// The work list can be asked for a repository's items, an item made for a repository lands in it, and people react to
// items and comments from a fixed set, one each, with counts and "mine" in every answer.
func TestWorkByRepositoryAndReactions(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	vic, otto := e.signIn(t, "vic"), e.signIn(t, "otto")
	hostedRepo(t, e, "acme/shop")
	hostedRepo(t, e, "acme/lib")
	base := e.srv.URL + "/api/v1/work"

	// Made "for" the repository, with no scope: it lives in the repository's own scope.
	var a, b, c work.Item
	otto.do("POST", base+"/items", `{"title":"Own scope","repository":"acme/shop"}`).json(t, &a)
	if a.Scope.ID != "acme/shop" {
		t.Fatalf("the default scope: %+v", a.Scope)
	}
	// Made for the repository but kept in another scope: tied to the repository.
	otto.do("POST", base+"/items?scope=project:acme/lib", `{"title":"Elsewhere","repository":"acme/shop"}`).json(t, &b)
	otto.do("POST", base+"/items?scope=project:acme/lib", `{"title":"Not for shop"}`).json(t, &c)
	var pg work.Page
	vic.get(base+"/items?repository=acme/shop").json(t, &pg)
	if pg.Total != 2 {
		t.Fatalf("the repository's items: %+v", pg)
	}
	vic.get(base+"/items?q=repo:acme/shop+type:issue").json(t, &pg)
	if pg.Total != 2 {
		t.Fatalf("the repo: term: %d", pg.Total)
	}
	vic.get(base+"/items?repository=acme/lib").json(t, &pg)
	if pg.Total != 2 || strings.Contains(pg.Items[0].Title+pg.Items[1].Title, "Own") {
		t.Fatalf("the other repository's items: %+v", pg.Items)
	}
	if r := vic.get(base + "/items?repository=acme/none"); r.status != 200 {
		t.Fatalf("%d", r.status)
	}

	// Reactions.
	cm := otto.do("POST", base+"/items/"+a.Ref+"/comments", `{"body":"hello"}`)
	var comment work.Comment
	cm.json(t, &comment)
	if r := vic.do("PUT", base+"/items/"+a.Ref+"/reactions/nonsense", ``); r.status != 400 {
		t.Fatalf("a reaction outside the set: %d %s", r.status, r.body)
	}
	var rc struct {
		Reactions []work.ReactionCount `json:"reactions"`
	}
	for i := 0; i < 2; i++ { // twice: still one
		vic.do("PUT", base+"/items/"+a.Ref+"/reactions/heart", ``).json(t, &rc)
	}
	otto.do("PUT", base+"/items/"+a.Ref+"/reactions/heart", ``).json(t, &rc)
	otto.do("PUT", base+"/items/"+a.Ref+"/reactions/rocket", ``).json(t, &rc)
	if len(rc.Reactions) != 2 || rc.Reactions[0].Reaction != "heart" || rc.Reactions[0].Count != 2 || !rc.Reactions[0].Mine {
		t.Fatalf("counts: %+v", rc)
	}
	var got work.Item
	vic.get(base+"/items/"+a.Ref).json(t, &got)
	if len(got.Reactions) != 2 || got.Reactions[0].Count != 2 || !got.Reactions[0].Mine || got.Reactions[1].Mine {
		t.Fatalf("detail: %+v", got.Reactions)
	}
	vic.get(base+"/items?repository=acme/shop").json(t, &pg)
	for _, it := range pg.Items {
		if it.Ref == a.Ref && (len(it.Reactions) != 2 || it.Reactions[0].Count != 2) {
			t.Fatalf("list: %+v", it.Reactions)
		}
	}
	vic.do("PUT", base+"/comments/"+comment.ID+"/reactions/eyes", ``).json(t, &rc)
	var list struct {
		Comments []work.Comment `json:"comments"`
	}
	otto.get(base+"/items/"+a.Ref+"/comments").json(t, &list)
	cms := list.Comments
	if len(cms) != 1 || len(cms[0].Reactions) != 1 || cms[0].Reactions[0].Mine || cms[0].Reactions[0].Count != 1 {
		t.Fatalf("comments: %+v", cms)
	}
	vic.do("DELETE", base+"/items/"+a.Ref+"/reactions/heart", ``).json(t, &rc)
	if rc.Reactions[0].Count != 1 || rc.Reactions[0].Mine {
		t.Fatalf("after taking it back: %+v", rc)
	}
	// What a person cannot see cannot be reacted to.
	var conf work.Item
	otto.do("POST", base+"/items?scope=project:acme/shop", `{"title":"Secret","confidential":true}`).json(t, &conf)
	if r := vic.do("PUT", base+"/items/"+conf.Ref+"/reactions/heart", ``); r.status != 404 {
		t.Fatalf("a reaction on a confidential item: %d", r.status)
	}
	if r := vic.do("PUT", base+"/comments/nope/reactions/heart", ``); r.status != 404 {
		t.Fatalf("a reaction on a comment that is not there: %d", r.status)
	}
}
