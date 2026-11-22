package api

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
)

func forkPolicy(t *testing.T) *auth.Policy {
	t.Helper()
	p, err := auth.NewPolicy([]auth.Binding{
		{Group: "admins", Role: auth.Admin},
		{Group: "pay-view", Role: auth.Viewer, Projects: []string{"acme/*"}},
		{Group: "pay-ops", Role: auth.Operator, Projects: []string{"acme/*"}},
		{User: "otto", Role: auth.Admin, Projects: []string{"otto/*"}}, // their own space
		{User: "vic", Role: auth.Admin, Projects: []string{"vic/*"}},
		{User: "dana", Role: auth.Operator, Projects: []string{"web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type forkEnv struct {
	e              *authEnv
	ada, otto, vic *browser
	dana, nobody   *browser
	acme           string // the API address of acme/shop
	adaTok, vicTok string
	main           string // a working copy that tracks acme/shop
}

// A private acme/shop with one commit, and vic (a viewer of acme/*, with their own space) as the person forking it.
func newForkEnv(t *testing.T) *forkEnv {
	t.Helper()
	e, _ := hostedEnv(t, envOpts{policy: forkPolicy(t)})
	f := &forkEnv{e: e, ada: e.signIn(t, "ada"), otto: e.signIn(t, "otto"), vic: e.signIn(t, "vic"), dana: e.signIn(t, "dana"), nobody: e.signIn(t, "nobody")}
	hostedRepo(t, e, "acme/shop")
	f.acme = e.srv.URL + "/api/v1/repositories/acme/shop"
	f.adaTok, f.vicTok = e.tokenFor(t, "ada", "git"), e.tokenFor(t, "vic", "git")
	f.main = t.TempDir()
	mustGit(t, f.main, "init", "-q", "-b", "main")
	commit(t, f.main, "code.txt", "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\n", "first")
	mustGit(t, f.main, "push", "-q", withToken(e.srv.URL, "/git/acme/shop.git", f.adaTok), "HEAD:refs/heads/main")
	return f
}

func (f *forkEnv) pushMain(t *testing.T, file, content, msg string) {
	t.Helper()
	commit(t, f.main, file, content, msg)
	mustGit(t, f.main, "push", "-q", withToken(f.e.srv.URL, "/git/acme/shop.git", f.adaTok), "HEAD:refs/heads/main")
}

func TestForkingFollowsTheRulesAndNeverWidensAccess(t *testing.T) {
	f := newForkEnv(t)
	forks := f.acme + "/forks"
	// A private repository takes no forks until its administrator turns them on.
	if r := f.vic.do("POST", forks, `{}`); r.status != 403 || !strings.Contains(string(r.body), "off by default") {
		t.Fatalf("a fork of a private repository by default: %d %s", r.status, r.body)
	}
	// Someone who cannot read it does not learn that it exists.
	if r := f.dana.do("POST", forks, `{"owner":"web"}`); r.status != 404 {
		t.Fatalf("a fork by someone who cannot read the repository: %d %s", r.status, r.body)
	}
	if r := f.otto.do("PATCH", f.acme, `{"allow_forks":"on"}`); r.status != 403 { // an operator, not an administrator
		t.Fatalf("an operator turned forks on: %d %s", r.status, r.body)
	}
	if r := f.ada.do("PATCH", f.acme, `{"allow_forks":"on"}`); r.status != 200 || !strings.Contains(string(r.body), `"allow_forks":true`) {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// Only where the person may make repositories, and never wider than the parent.
	if r := f.vic.do("POST", forks, `{"owner":"acme","name":"mine"}`); r.status != 403 {
		t.Fatalf("a fork into someone else's space: %d %s", r.status, r.body)
	}
	if r := f.vic.do("POST", forks, `{"owner":"vic","visibility":"public"}`); r.status != 403 || !strings.Contains(string(r.body), "more visible") {
		t.Fatalf("a public fork of a private repository: %d %s", r.status, r.body)
	}
	// A token limited to this repository cannot make a fork, nor one limited to the fork write to the parent.
	_, lim := repoToken(t, f.e, `{"name":"lim","repositories":["acme/shop"],"repo_access":"write"}`)
	if r := newBrowser(t).do("POST", forks, `{"owner":"vic"}`, bearer(lim)...); r.status != 403 || !strings.Contains(string(r.body), "limited to the repositories it names") {
		t.Fatalf("a repository-limited token forked: %d %s", r.status, r.body)
	}
	var fork struct {
		forge.Repository
		Fork forkView `json:"fork"`
	}
	r := f.vic.do("POST", forks, `{}`)
	r.json(t, &fork)
	if r.status != 201 || fork.Path != "vic/shop" || fork.Visibility != forge.Private || fork.Fork.Parent != "acme/shop" || fork.Fork.Root != "acme/shop" || fork.Fork.ForkPoint == "" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// A secret pushed to the parent after the fork never reaches it.
	f.pushMain(t, "secret.txt", "s3cret\n", "a secret, after the fork")
	secret := strings.TrimSpace(mustGit(t, f.main, "rev-parse", "HEAD"))
	clone := t.TempDir()
	mustGit(t, clone, "clone", "-q", withToken(f.e.srv.URL, "/git/vic/shop.git", f.vicTok), "c")
	if _, err := git(t, clone+"/c", "cat-file", "-e", secret); err == nil {
		t.Fatal("AN OBJECT PUSHED TO THE PRIVATE PARENT AFTER THE FORK IS REACHABLE THROUGH THE FORK")
	}
	if out, err := git(t, clone+"/c", "fetch", withToken(f.e.srv.URL, "/git/vic/shop.git", f.vicTok), secret); err == nil {
		t.Fatalf("the fork handed out the parent's later commit by name:\n%s", out)
	}
	// What the fork has at the fork, vic can read; what is in the fork is not the parent's to read.
	if r := f.dana.get(f.e.srv.URL + "/api/v1/repositories/vic/shop"); r.status != 404 {
		t.Errorf("someone else read a private fork: %d", r.status)
	}
	// The fork's parent shows to people who can read it, and the forks list only what the caller may read.
	var list struct {
		Items []map[string]any `json:"items"`
	}
	f.ada.get(forks).json(t, &list)
	if len(list.Items) != 1 || list.Items[0]["path"] != "vic/shop" {
		t.Fatalf("%+v", list)
	}
	f.otto.get(forks).json(t, &list)
	if len(list.Items) != 0 {
		t.Fatalf("an operator of the parent sees a private fork of someone else: %+v", list)
	}
	// Audit: the fork and its settings are on record.
	audit := f.ada.get(f.e.srv.URL + "/api/v1/audit?limit=200").body
	for _, want := range []string{"repository.fork", "vic/shop"} {
		if !strings.Contains(string(audit), want) {
			t.Errorf("the audit trail lacks %q", want)
		}
	}
	// The parent goes: the fork stays whole, says so, and can be cloned.
	if r := f.ada.do("DELETE", f.acme, ""); r.status != 204 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var after struct {
		Fork forkView `json:"fork"`
	}
	f.vic.get(f.e.srv.URL+"/api/v1/repositories/vic/shop").json(t, &after)
	if !after.Fork.ParentGone {
		t.Errorf("the fork does not know its parent is gone: %+v", after.Fork)
	}
	mustGit(t, t.TempDir(), "clone", "-q", withToken(f.e.srv.URL, "/git/vic/shop.git", f.vicTok), "again")
	if r := f.vic.do("POST", f.e.srv.URL+"/api/v1/repositories/vic/shop/sync", `{}`); r.status != 409 || !strings.Contains(string(r.body), "parent was deleted") {
		t.Errorf("syncing from a deleted parent: %d %s", r.status, r.body)
	}
}

func TestPullRequestFromAForkThroughTheAPI(t *testing.T) {
	f := newForkEnv(t)
	f.ada.do("PATCH", f.acme, `{"allow_forks":"on"}`)
	if r := f.vic.do("POST", f.acme+"/forks", `{}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	vicShop := withToken(f.e.srv.URL, "/git/vic/shop.git", f.vicTok)
	work := t.TempDir()
	mustGit(t, work, "clone", "-q", vicShop, "w")
	w := work + "/w"
	mustGit(t, w, "checkout", "-q", "-b", "topic")
	topic := commit(t, w, "code.txt", "l1\nTOPIC\nl3\nl4\nl5\nl6\nl7\nl8\nl9\n", "topic change")
	mustGit(t, w, "push", "-q", "origin", "topic")
	mustGit(t, w, "checkout", "-q", "-b", "private-work", "main")
	other := commit(t, w, "diary.txt", "not for the target\n", "private work")
	mustGit(t, w, "push", "-q", "origin", "private-work")

	// Before opening one, the person can see what it would contain.
	var cmp struct {
		Ahead   int `json:"ahead"`
		Behind  int `json:"behind"`
		Commits []struct {
			SHA string `json:"sha"`
		} `json:"commits"`
		Mergeability pulls.Mergeability `json:"mergeability"`
	}
	f.vic.get(f.acme+"/compare?head=vic:topic").json(t, &cmp)
	if cmp.Ahead != 1 || cmp.Behind != 0 || len(cmp.Commits) != 1 || cmp.Mergeability.State != pulls.MergeClean {
		t.Fatalf("compare with a fork's branch: %+v", cmp)
	}
	if r := f.dana.get(f.acme + "/compare?head=vic:topic"); r.status != 404 {
		t.Fatalf("a stranger compared: %d", r.status)
	}
	pullsURL := f.acme + "/pulls/from-fork"
	// Anyone who can read the target and the fork may offer a branch; a stranger cannot.
	if r := f.dana.do("POST", pullsURL, `{"head":"vic:topic","title":"x"}`); r.status != 404 {
		t.Fatalf("a stranger: %d %s", r.status, r.body)
	}
	if r := f.vic.do("POST", pullsURL, `{"head":"vic","title":"x"}`); r.status != 400 {
		t.Fatalf("no branch: %d %s", r.status, r.body)
	}
	var pr prView
	r := f.vic.do("POST", pullsURL, `{"head":"vic:topic","title":"From my fork","allow_maintainer_edits":false}`)
	r.json(t, &pr)
	if r.status != 201 || pr.Number != 1 || pr.SourceRepo != "vic/shop" || pr.HeadSHA != topic || pr.FetchRef != "refs/pull/1/head" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// The head is the target's pull ref; the fork's other branch is not in the target.
	adaClone := t.TempDir()
	mustGit(t, adaClone, "clone", "-q", withToken(f.e.srv.URL, "/git/acme/shop.git", f.adaTok), "c")
	mustGit(t, adaClone+"/c", "fetch", "-q", "origin", "refs/pull/1/head")
	if _, err := git(t, adaClone+"/c", "cat-file", "-e", topic); err != nil {
		t.Fatalf("the pull request's head is not in the target: %v", err)
	}
	if _, err := git(t, adaClone+"/c", "cat-file", "-e", other); err == nil {
		t.Fatal("THE FORK'S OTHER BRANCH IS IN THE TARGET")
	}
	if out := mustGit(t, adaClone+"/c", "ls-remote", "origin"); strings.Contains(out, "private-work") || strings.Contains(out, "refs/heads/topic") {
		t.Fatalf("the target advertises the fork's branches:\n%s", out)
	}
	prURL := f.acme + "/pulls/1"
	pr = waitMergeable(t, f.otto, prURL, pulls_MergeClean)
	// The fork's branch moves: the pull request follows.
	topic2 := commit(t, w, "code.txt", "l1\nTOPIC again\nl3\nl4\nl5\nl6\nl7\nl8\nl9\n", "more")
	_ = topic2
	mustGit(t, w, "checkout", "-q", "topic")
	topic2 = commit(t, w, "code.txt", "l1\nTOPIC again\nl3\nl4\nl5\nl6\nl7\nl8\nl9\n", "more")
	mustGit(t, w, "push", "-q", "origin", "topic")
	hostedWait(t, "the pull request to follow the fork", func() bool {
		f.otto.get(prURL).json(t, &pr)
		return pr.HeadSHA == topic2
	})

	// Maintainer edits: off, an operator of the target cannot update the branch in the fork.
	f.pushMain(t, "other.txt", "x\n", "target moves")
	upd := prURL + "/update-branch"
	if r := f.otto.do("POST", upd, `{}`); r.status != 403 || !strings.Contains(string(r.body), "did not allow edits by maintainers") {
		t.Fatalf("maintainer edits are off: %d %s", r.status, r.body)
	}
	if r := f.otto.do("PUT", prURL+"/maintainer-edits", `{"allow":true}`); r.status != 403 {
		t.Fatalf("a maintainer turned on edits for someone else's pull request: %d %s", r.status, r.body)
	}
	if r := f.vic.do("PUT", prURL+"/maintainer-edits", `{"allow":true}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// A token limited to the parent cannot write to the fork, nor one limited to the fork to the parent.
	_, parentTok := repoToken(t, f.e, `{"name":"parent-only","repositories":["acme/shop"],"repo_access":"write"}`)
	_, forkTok := repoToken(t, f.e, `{"name":"fork-only","repositories":["vic/shop"],"repo_access":"write"}`)
	cli := newBrowser(t)
	if r := cli.do("POST", upd, `{}`, bearer(parentTok)...); r.status != 403 || !strings.Contains(string(r.body), "limited to repositories") {
		t.Fatalf("a token limited to the target updated a branch in the fork: %d %s", r.status, r.body)
	}
	if r := cli.do("POST", f.e.srv.URL+"/api/v1/repositories/vic/shop/sync", `{}`, bearer(parentTok)...); r.status != 404 {
		t.Fatalf("a token limited to the target reached the fork: %d %s", r.status, r.body)
	}
	if r := cli.do("POST", upd, `{}`, bearer(forkTok)...); r.status != 404 {
		t.Fatalf("a token limited to the fork reached the target: %d %s", r.status, r.body)
	}
	if code := gitStatus(f.e, parentTok, "/vic/shop", "git-receive-pack"); code != 404 {
		t.Fatalf("a token limited to the target pushed to the fork: %d", code)
	}
	r = f.otto.do("POST", upd, `{}`)
	if r.status != 200 {
		t.Fatalf("with maintainer edits on: %d %s", r.status, r.body)
	}
	var head struct {
		Head string `json:"head"`
	}
	r.json(t, &head)
	frefs := mustGit(t, work, "ls-remote", vicShop, "refs/heads/topic")
	if !strings.HasPrefix(frefs, head.Head) {
		t.Fatalf("the fork's branch is not at the new head %s: %s", head.Head, frefs)
	}
	hostedWait(t, "the pull request to take the updated head", func() bool {
		f.otto.get(prURL).json(t, &pr)
		return pr.HeadSHA == head.Head
	})

	// Conflicts: the target and the branch change the same line. They are listed and resolved in the fork.
	f.pushMain(t, "code.txt", "l1\nMAIN\nl3\nl4\nl5\nl6\nl7\nl8\nl9\n", "main edits the line")
	var cs struct {
		Conflicts struct {
			Head   string `json:"head"`
			Target string `json:"target"`
			Files  []struct {
				Path    string `json:"path"`
				Kind    string `json:"kind"`
				Regions []struct {
					Index int    `json:"index"`
					Kind  string `json:"kind"`
				} `json:"regions"`
			} `json:"files"`
		} `json:"conflicts"`
		MayResolve bool `json:"may_resolve"`
	}
	hostedWait(t, "the conflict to be seen", func() bool {
		f.otto.get(prURL+"/conflicts").json(t, &cs)
		return len(cs.Conflicts.Files) == 1
	})
	if !cs.MayResolve || cs.Conflicts.Files[0].Path != "code.txt" || cs.Conflicts.Files[0].Kind != "content" {
		t.Fatalf("%+v", cs)
	}
	conflictIdx := -1
	for _, rg := range cs.Conflicts.Files[0].Regions {
		if rg.Kind == "conflict" {
			conflictIdx = rg.Index
		}
	}
	bad := `{"expect_head":"` + cs.Conflicts.Head + `","expect_target":"` + cs.Conflicts.Target + `","files":[{"path":"code.txt","content":"<<<<<<< ours\nx\n=======\ny\n>>>>>>> theirs\n"}]}`
	if r := f.otto.do("POST", prURL+"/conflicts/resolve", bad); r.status != 400 || !strings.Contains(string(r.body), "conflict markers") {
		t.Fatalf("markers left in: %d %s", r.status, r.body)
	}
	if r := f.dana.do("POST", prURL+"/conflicts/resolve", bad); r.status != 404 {
		t.Fatalf("a stranger resolved conflicts: %d %s", r.status, r.body)
	}
	good := `{"expect_head":"` + cs.Conflicts.Head + `","expect_target":"` + cs.Conflicts.Target + `","files":[{"path":"code.txt","regions":[{"index":` + strconv.Itoa(conflictIdx) + `,"take":"both"}]}]}`
	r = f.otto.do("POST", prURL+"/conflicts/resolve", good)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r.json(t, &head)
	if got := mustGit(t, work, "ls-remote", vicShop, "refs/heads/topic"); !strings.HasPrefix(got, head.Head) {
		t.Fatalf("the fork's branch is not at the resolution: %s", got)
	}
	pr = waitMergeable(t, f.otto, prURL, pulls_MergeClean)
	if r := f.otto.do("POST", prURL+"/merge", `{"method":"merge"}`); r.status != 200 {
		t.Fatalf("merging the resolved pull request: %d %s", r.status, r.body)
	}
	// The fork's branch survives the merge.
	if got := mustGit(t, work, "ls-remote", vicShop, "refs/heads/topic"); !strings.Contains(got, "refs/heads/topic") {
		t.Fatalf("merging deleted the fork's branch: %q", got)
	}
	audit := string(f.ada.get(f.e.srv.URL + "/api/v1/audit?limit=300").body)
	for _, want := range []string{"pull.resolve_conflicts", "pull.maintainer_edits", "pull.update_branch"} {
		if !strings.Contains(audit, want) {
			t.Errorf("the audit trail lacks %q", want)
		}
	}
}

const pulls_MergeClean = pulls.MergeClean

func TestSyncAForkThroughTheAPI(t *testing.T) {
	f := newForkEnv(t)
	f.ada.do("PATCH", f.acme, `{"allow_forks":"on"}`)
	f.vic.do("POST", f.acme+"/forks", `{}`)
	fork := f.e.srv.URL + "/api/v1/repositories/vic/shop"
	f.pushMain(t, "new.txt", "n\n", "parent moves")
	var st struct {
		Standing struct{ Ahead, Behind int } `json:"standing"`
	}
	f.vic.get(fork+"/fork").json(t, &st)
	if st.Standing.Behind != 1 || st.Standing.Ahead != 0 {
		t.Fatalf("%+v", st)
	}
	if r := f.nobody.do("POST", fork+"/sync", `{}`); r.status != 404 {
		t.Fatalf("a stranger synced: %d", r.status)
	}
	if r := f.vic.do("POST", fork+"/sync", `{"mode":"squash"}`); r.status != 400 {
		t.Fatalf("an unknown mode: %d %s", r.status, r.body)
	}
	if r := f.vic.do("POST", fork+"/sync", `{}`); r.status != 200 || !strings.Contains(string(r.body), "fast_forwarded") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := f.vic.do("POST", fork+"/sync", `{}`); r.status != 200 || !strings.Contains(string(r.body), "up_to_date") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// The fork has a commit of its own and the parent another: fast-forward says so, merge works, a conflict says which files.
	work := t.TempDir()
	mustGit(t, work, "clone", "-q", withToken(f.e.srv.URL, "/git/vic/shop.git", f.vicTok), "w")
	commit(t, work+"/w", "mine.txt", "m\n", "fork work")
	mustGit(t, work+"/w", "push", "-q", "origin", "main")
	f.pushMain(t, "code.txt", "PARENT\n", "parent rewrites code")
	if r := f.vic.do("POST", fork+"/sync", `{}`); r.status != 409 || !strings.Contains(string(r.body), "diverged") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := f.vic.do("POST", fork+"/sync", `{"mode":"merge"}`); r.status != 200 || !strings.Contains(string(r.body), `"merged"`) {
		t.Fatalf("%d %s", r.status, r.body)
	}
	mustGit(t, work+"/w", "pull", "-q", "--ff-only", "origin", "main")
	commit(t, work+"/w", "code.txt", "FORK\n", "fork rewrites code")
	mustGit(t, work+"/w", "push", "-q", "origin", "main")
	f.pushMain(t, "code.txt", "PARENT AGAIN\n", "parent rewrites code again")
	if r := f.vic.do("POST", fork+"/sync", `{"mode":"merge"}`); r.status != 409 || !strings.Contains(string(r.body), "code.txt") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// A fork that is more visible than its parent takes nothing from it.
	if r := f.vic.do("PATCH", fork, `{"visibility":"public"}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := f.vic.do("POST", fork+"/sync", `{"mode":"merge"}`); r.status != 403 || !strings.Contains(string(r.body), "more visible") && !strings.Contains(string(r.body), "make the fork private first") {
		t.Fatalf("a public fork syncing from a private parent: %d %s", r.status, r.body)
	}
}

func TestTemplateRepositoriesThroughTheAPI(t *testing.T) {
	f := newForkEnv(t)
	tplAPI := f.acme
	mustGit(t, f.main, "checkout", "-q", "-b", "scratch")
	commit(t, f.main, ".launchway/template.yaml", "variables:\n  - name: project\n    description: the name\n    pattern: '[a-z][a-z0-9-]{1,20}'\nnames: true\n", "manifest")
	commit(t, f.main, "{{project}}/README.md", "# {{project}}\n", "readme")
	mustGit(t, f.main, "push", "-q", withToken(f.e.srv.URL, "/git/acme/shop.git", f.adaTok), "HEAD:refs/heads/main")
	if r := f.vic.do("POST", tplAPI+"/generate", `{"owner":"vic","name":"app","values":{"project":"demo"}}`); r.status != 409 || !strings.Contains(string(r.body), "not a template") {
		t.Fatalf("a repository that is no template: %d %s", r.status, r.body)
	}
	if r := f.vic.do("PATCH", tplAPI, `{"template":true}`); r.status != 403 {
		t.Fatalf("a viewer marked a template: %d", r.status)
	}
	if r := f.ada.do("PATCH", tplAPI, `{"template":true}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var m struct {
		Template  bool `json:"template"`
		Variables []struct {
			Name string `json:"name"`
		} `json:"variables"`
	}
	f.vic.get(tplAPI+"/template").json(t, &m)
	if !m.Template || len(m.Variables) != 1 || m.Variables[0].Name != "project" {
		t.Fatalf("%+v", m)
	}
	for _, bad := range []string{`{"owner":"vic","name":"app","values":{"project":"../x"}}`, `{"owner":"vic","name":"app","values":{}}`, `{"owner":"vic","name":"app","values":{"project":"ok","other":"x"}}`,
		`{"owner":"vic","name":"app","values":{"project":"ok"},"visibility":"public"}`, `{"owner":"acme","name":"app","values":{"project":"ok"}}`} {
		if r := f.vic.do("POST", tplAPI+"/generate", bad); r.status < 400 || r.status >= 500 {
			t.Errorf("%s: %d %s", bad, r.status, r.body)
		}
	}
	if r := f.vic.get(f.e.srv.URL + "/api/v1/repositories/vic/app"); r.status != 404 {
		t.Fatalf("a refused use left a repository: %d", r.status)
	}
	r := f.vic.do("POST", tplAPI+"/generate", `{"owner":"vic","name":"app","values":{"project":"demo"}}`)
	if r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	work := t.TempDir()
	mustGit(t, work, "clone", "-q", withToken(f.e.srv.URL, "/git/vic/app.git", f.vicTok), "w")
	if got := strings.TrimSpace(mustGit(t, work+"/w", "log", "--format=%s")); strings.Count(got, "\n") != 0 || !strings.Contains(got, "Initial commit from the template acme/shop") {
		t.Fatalf("history of the new repository: %q", got)
	}
	if raw := mustGit(t, work+"/w", "cat-file", "commit", "HEAD"); !strings.Contains(raw, "gpgsig") {
		t.Fatalf("the first commit of a repository made from a template is not signed by the installation:\n%s", raw)
	}
	if out := mustGit(t, work+"/w", "show", "HEAD:demo/README.md"); out != "# demo\n" {
		t.Fatalf("%q", out)
	}
}

func TestApprovingTheRunOfAFirstTimeContributorsPullRequest(t *testing.T) {
	f := newForkEnv(t)
	f.ada.do("PATCH", f.acme, `{"allow_forks":"on"}`)
	f.vic.do("POST", f.acme+"/forks", `{}`)
	work := t.TempDir()
	mustGit(t, work, "clone", "-q", withToken(f.e.srv.URL, "/git/vic/shop.git", f.vicTok), "w")
	mustGit(t, work+"/w", "checkout", "-q", "-b", "topic")
	commit(t, work+"/w", "b.txt", "b\n", "change")
	mustGit(t, work+"/w", "push", "-q", "origin", "topic")
	if r := f.vic.do("POST", f.acme+"/pulls/from-fork", `{"head":"vic:topic","title":"t"}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	prURL := f.acme + "/pulls/1"
	if r := f.vic.do("POST", prURL+"/approve-run", `{}`); r.status != 403 {
		t.Fatalf("the contributor approved their own run: %d %s", r.status, r.body)
	}
	if r := f.otto.do("POST", prURL+"/approve-run", `{}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := f.otto.do("POST", f.acme+"/pulls", `{"title":"x","source_branch":"main"}`); r.status == 201 {
		t.Fatalf("an ordinary pull request was opened with the same branch as its target")
	}
	if r := f.otto.do("POST", prURL+"/maintainer-edits", `{}`); r.status == 200 {
		t.Fatal("a route that does not exist answered")
	}
}

var _ = forge.Private
