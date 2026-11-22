package api

import (
	"strings"
	"testing"
)

type activityPageT struct {
	Items []activityItem `json:"items"`
	Next  string         `json:"next"`
}

func kindsOf(items []activityItem) string {
	var k []string
	for _, it := range items {
		k = append(k, it.Kind)
	}
	return strings.Join(k, ",")
}

// A repository's feed has its pushes, tags and pull requests, newest first and in pages; the feed of another
// owner's private repository is closed to a person who cannot read it, in the repository's own route and in the owner's.
func TestOverviewActivityLanguagesAndContributors(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, vic := e.signIn(t, "ada"), e.signIn(t, "vic")
	repos := e.srv.URL + "/api/v1/repositories"
	ada.do("POST", repos, `{"owner":"acme","name":"shop","visibility":"private"}`)
	ada.do("POST", repos, `{"owner":"other","name":"secret","visibility":"private"}`)
	tok := e.tokenFor(t, "ada", "t")
	push := func(path string) string {
		src := workingCopy(t)
		commit(t, src, "main.go", "package main\n", "go")
		commit(t, src, "web/app.ts", "export {}\n", "ts")
		mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/"+path+".git", tok), "HEAD:refs/heads/main")
		mustGit(t, src, "checkout", "-q", "-b", "feature")
		commit(t, src, "b.txt", "b\n", "feature work")
		mustGit(t, src, "tag", "v1")
		mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/"+path+".git", tok), "feature:refs/heads/feature", "v1:refs/tags/v1")
		return src
	}
	push("acme/shop")
	push("other/secret")
	api := repos + "/acme/shop"
	if r := ada.do("POST", api+"/pulls", `{"title":"Add feature","source_branch":"feature"}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var secretPull activityPageT
	_ = secretPull

	var feed activityPageT
	vic.get(api+"/activity").json(t, &feed)
	if k := kindsOf(feed.Items); !strings.Contains(k, "pull_opened") || strings.Count(k, "push") != 2 || !strings.Contains(k, "tag") {
		t.Fatalf("the feed: %s", k)
	}
	for _, it := range feed.Items {
		if it.Repository != "acme/shop" || it.At == 0 {
			t.Fatalf("an item: %+v", it)
		}
	}
	// Pages: one at a time, each older than the last, none repeated, then the end.
	seen := map[string]bool{}
	var pages int
	next := ""
	for {
		var pg activityPageT
		u := api + "/activity?limit=1"
		if next != "" {
			u += "&before=" + next
		}
		vic.get(u).json(t, &pg)
		for _, it := range pg.Items {
			if seen[it.ID] {
				t.Fatalf("%s came twice", it.ID)
			}
			seen[it.ID] = true
		}
		pages++
		if pg.Next == "" || pages > 20 {
			break
		}
		next = pg.Next
	}
	if len(seen) != len(feed.Items) {
		t.Fatalf("paging gave %d items, one page %d", len(seen), len(feed.Items))
	}
	var only activityPageT
	vic.get(api+"/activity?kind=pull").json(t, &only)
	if kindsOf(only.Items) != "pull_opened" {
		t.Fatalf("kind=pull: %s", kindsOf(only.Items))
	}
	if r := vic.get(api + "/activity?kind=nonsense"); r.status != 400 {
		t.Fatalf("a kind that does not exist: %d", r.status)
	}
	if r := vic.get(api + "/activity?before=abc"); r.status != 400 {
		t.Fatalf("a cursor that is not one: %d", r.status)
	}

	// What vic may not read is not in any feed of vic's.
	if r := vic.get(repos + "/other/secret/activity"); r.status != 404 {
		t.Fatalf("the feed of a repository vic cannot read: %d %s", r.status, r.body)
	}
	var owner activityPageT
	vic.get(e.srv.URL+"/api/v1/owners/other/activity").json(t, &owner)
	if len(owner.Items) != 0 {
		t.Fatalf("an owner's feed shows a private repository to another person: %+v", owner.Items)
	}
	var mine activityPageT
	ada.get(e.srv.URL+"/api/v1/owners/other/activity").json(t, &mine)
	if len(mine.Items) == 0 {
		t.Fatal("the owner's feed is empty for an administrator")
	}
	vic.get(e.srv.URL+"/api/v1/owners/acme/activity").json(t, &owner)
	if len(owner.Items) != len(feed.Items) {
		t.Fatalf("the owner's feed: %d items, the repository's %d", len(owner.Items), len(feed.Items))
	}

	// Languages: Go and TypeScript by share, made once for the head commit.
	var lang languagesOut
	vic.get(api+"/languages").json(t, &lang)
	if lang.Cached || len(lang.Languages) != 2 || lang.Commit == "" {
		t.Fatalf("languages: %+v", lang)
	}
	var again languagesOut
	vic.get(api+"/languages").json(t, &again)
	if !again.Cached || again.Commit != lang.Commit {
		t.Fatalf("the second answer was not the cached one: %+v", again)
	}
	if r := vic.get(repos + "/other/secret/languages"); r.status != 404 {
		t.Fatalf("languages of a private repository: %d", r.status)
	}

	// Contributors: the request starts the job and answers at once; the answer comes with the head commit.
	var c contributorsOut
	vic.get(api+"/contributors?period=all").json(t, &c)
	if c.State != "computing" && c.State != "done" {
		t.Fatalf("contributors: %+v", c)
	}
	hostedWait(t, "the contributors job", func() bool {
		vic.get(api+"/contributors?period=all").json(t, &c)
		return c.State == "done"
	})
	if c.Commit != lang.Commit || c.Walked != 3 || len(c.Contributors) == 0 || c.AtLeast {
		t.Fatalf("contributors: %+v", c)
	}
	if r := vic.get(api + "/contributors?period=week"); r.status != 400 {
		t.Fatalf("a period that does not exist: %d", r.status)
	}
	if r := vic.get(repos + "/other/secret/contributors"); r.status != 404 {
		t.Fatalf("contributors of a private repository: %d", r.status)
	}
}

// A walk cut at the cap says "at least" and is never presented as complete.
func TestContributorsJobCutAtTheCapSaysAtLeast(t *testing.T) {
	old := overviewCommitCap
	overviewCommitCap = 1
	t.Cleanup(func() { overviewCommitCap = old })
	e, _ := hostedEnv(t, envOpts{})
	ada := e.signIn(t, "ada")
	ada.do("POST", e.srv.URL+"/api/v1/repositories", `{"owner":"acme","name":"shop"}`)
	tok := e.tokenFor(t, "ada", "t")
	src := workingCopy(t)
	commit(t, src, "b.txt", "b\n", "second")
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/acme/shop.git", tok), "HEAD:refs/heads/main")
	var c contributorsOut
	hostedWait(t, "the contributors job", func() bool {
		ada.get(e.srv.URL+"/api/v1/repositories/acme/shop/contributors?period=30d").json(t, &c)
		return c.State == "done"
	})
	if !c.AtLeast || c.Walked != 1 {
		t.Fatalf("%+v", c)
	}
}
