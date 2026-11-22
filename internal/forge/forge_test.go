package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// providerNames may appear nowhere in the model: no type, no field, no JSON
// property.
var providerNames = []string{"github", "gitlab", "bitbucket", "azure", "devops", "gitea", "forgejo", "codecommit", "gerrit", "gogs"}

// modelTypes is every exported domain type. A new type is added here, or the
// completeness test below fails.
var modelTypes = []any{
	Connection{}, SecretRef{}, Repository{}, CloneAddresses{}, Ref{}, Result{}, Details{}, TestCounts{},
	Principal{}, Mergeability{}, Proposal{}, Revision{}, Review{}, Thread{}, Message{}, Commit{}, TreeEntry{},
	Comparison{}, RefRule{}, Event{}, Whoami{}, NewRepository{}, NewProposal{}, ProposalPatch{}, Credential{},
	Declaration{}, ErrUnsupported{}, Page[Repository]{}, RefLayout{}, TagSpec{}, ReleaseFile{}, ReleaseSpec{}, FileChange{},
}

// properties lists every JSON property name and Go field name reachable from
// a type, with its path, so a provider-shaped name anywhere is found.
func properties(t reflect.Type, path string, seen map[reflect.Type]bool, out *[]string) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	*out = append(*out, path+"/"+strings.SplitN(t.Name(), "[", 2)[0])
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" {
			name = f.Name
		}
		*out = append(*out, path+"."+f.Name, path+"."+name)
		properties(f.Type, path+"."+name, seen, out)
	}
}

func providerLeaks(v any) []string {
	var names []string
	properties(reflect.TypeOf(v), "", map[reflect.Type]bool{}, &names)
	var leaks []string
	for _, n := range names {
		low := strings.ToLower(n)
		for _, p := range providerNames {
			if strings.Contains(low, p) {
				leaks = append(leaks, n)
			}
		}
	}
	return leaks
}

func TestNoProviderNamesInTheModel(t *testing.T) {
	for _, v := range modelTypes {
		if l := providerLeaks(v); len(l) > 0 {
			t.Errorf("%T names a provider: %v", v, l)
		}
	}
}

// TestProviderNameWouldBeCaught shows the check is real: a type with a
// provider-shaped property fails it.
func TestProviderNameWouldBeCaught(t *testing.T) {
	type leaky struct {
		Title string `json:"title"`
		Extra struct {
			Github string `json:"github"`
		} `json:"extra"`
	}
	if len(providerLeaks(leaky{})) == 0 {
		t.Fatal("a property named github was not found")
	}
	type viaTag struct {
		X string `json:"gitlab_id"`
	}
	if len(providerLeaks(viaTag{})) == 0 {
		t.Fatal("a tag named gitlab_id was not found")
	}
}

// TestEveryExportedStructIsChecked fails when a new exported struct of the
// package is not in modelTypes.
func TestEveryExportedStructIsChecked(t *testing.T) {
	listed := map[string]bool{}
	for _, v := range modelTypes {
		n := reflect.TypeOf(v).Name()
		listed[strings.SplitN(n, "[", 2)[0]] = true
	}
	// The types that are not data: interfaces, functions and machinery.
	machinery := map[string]bool{"Registry": true, "Base": true, "Fake": true, "Maturity": true}
	for _, n := range exportedStructs(t) {
		if !listed[n] && !machinery[n] {
			t.Errorf("exported struct %s is not in modelTypes", n)
		}
	}
}

func TestExtIsAnOpaqueMapEverywhere(t *testing.T) {
	for _, v := range modelTypes {
		rt := reflect.TypeOf(v)
		for rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if f.Name == "Ext" && f.Type != reflect.TypeOf(map[string]any(nil)) {
				t.Errorf("%s.Ext is %v, want map[string]any", rt.Name(), f.Type)
			}
		}
	}
	// Proposal, Repository and Result carry Ext.
	for _, v := range []any{Proposal{}, Repository{}, Result{}} {
		if _, ok := reflect.TypeOf(v).FieldByName("Ext"); !ok {
			t.Errorf("%T lacks Ext", v)
		}
	}
}

func TestConnectionValidation(t *testing.T) {
	good := Connection{Name: "cluster-a", Kind: KindGitea, BaseURL: "https://git.example.com", Credential: SecretRef{Env: "FORGE_TOKEN"}, Events: EventsPolling}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Connection){
		"unknown kind":          func(c *Connection) { c.Kind = "svn" },
		"empty name":            func(c *Connection) { c.Name = "" },
		"upper-case name":       func(c *Connection) { c.Name = "Home" },
		"credential in the URL": func(c *Connection) { c.BaseURL = "https://dana:s3cret@git.example.com" },
		"token as the env name": func(c *Connection) { c.Credential = SecretRef{Env: "ghp_abcDEF123456"} },
		"both env and file":     func(c *Connection) { c.Credential = SecretRef{Env: "A", File: "/x"} },
		"relative file":         func(c *Connection) { c.Credential = SecretRef{File: "token"} },
		"ftp":                   func(c *Connection) { c.BaseURL = "ftp://x" },
		"no host":               func(c *Connection) { c.BaseURL = "https://" },
		"query":                 func(c *Connection) { c.BaseURL = "https://x.example/?a=b" },
		"unknown event mode":    func(c *Connection) { c.Events = "carrier-pigeon" },
	}
	for name, mutate := range cases {
		c := good
		mutate(&c)
		if err := c.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	if err := (Connection{Name: "local", Kind: KindPlain}).Validate(); err != nil {
		t.Errorf("plain connection without address: %v", err)
	}
}

func TestDecodeConnectionRefusesLiteralCredentials(t *testing.T) {
	for _, body := range []string{
		`{"name":"x","kind":"github","token":"ghp_whole"}`,
		`{"name":"x","kind":"github","credential":{"value":"hunter2"}}`,
		`{"name":"x","kind":"github","password":"p"}`,
	} {
		_, err := DecodeConnection([]byte(body))
		if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: %v", body, err)
		}
	}
	c, err := DecodeConnection([]byte(`{"name":"x","kind":"github","base_url":"https://api.example.com","credential":{"env":"GH_TOKEN"}}`))
	if err != nil || c.Credential.Env != "GH_TOKEN" {
		t.Fatalf("%v %v", c, err)
	}
}

func TestRepositoryAndResultValidation(t *testing.T) {
	r := Repository{Connection: "con_1", Path: "home/name", Visibility: Private, Clone: CloneAddresses{HTTPS: "https://git.example/git/home/name"}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Repository){
		"no connection": func(r *Repository) { r.Connection = "" },
		"dotdot":        func(r *Repository) { r.Path = "a/../b" },
		"leading slash": func(r *Repository) { r.Path = "/a" },
		"empty":         func(r *Repository) { r.Path = "" },
		"visibility":    func(r *Repository) { r.Visibility = "secret" },
		"credentials":   func(r *Repository) { r.Clone.HTTPS = "https://u:p@git.example/x" },
	} {
		c := r
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	res := Result{Producer: "launchway", Name: "pipeline:ci", SHA: strings.Repeat("a", 40), State: Passing}
	if err := res.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Result){
		"producer":  func(r *Result) { r.Producer = "" },
		"short sha": func(r *Result) { r.SHA = "abc" },
		"state":     func(r *Result) { r.State = "maybe" },
	} {
		c := res
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCapabilitiesAndUnsupported(t *testing.T) {
	c := NoCapabilities("plain Git has no proposals, results or rules", "open the proposal on the forge itself")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c[CapContent] = Declaration{Support: Native}
	if !c.Has(CapContent) || c.Has(CapProposals) || c.Err(CapContent) != nil {
		t.Error("Has/Err")
	}
	err := c.Err(CapProposals)
	u, ok := IsUnsupported(err)
	if !ok || u.Capability != CapProposals || u.Workaround == "" || !strings.Contains(err.Error(), u.Workaround) {
		t.Fatalf("%v", err)
	}
	wrapped := errors.Join(errors.New("context"), err)
	if _, ok := IsUnsupported(wrapped); !ok {
		t.Error("a wrapped ErrUnsupported is not found")
	}
	if _, ok := IsUnsupported(errors.New("other")); ok {
		t.Error("another error was taken for ErrUnsupported")
	}
	// An undeclared capability is unsupported too, with words.
	if _, ok := IsUnsupported((Capabilities{}).Err(CapLFS)); !ok {
		t.Error("undeclared capability must be unsupported")
	}
	// Validation: missing, incomplete, unknown.
	bad := NoCapabilities("r", "w")
	delete(bad, CapLFS)
	if bad.Validate() == nil {
		t.Error("a missing capability passed")
	}
	bad = NoCapabilities("r", "w")
	bad[CapLFS] = Declaration{Support: None}
	if bad.Validate() == nil {
		t.Error("a none without a workaround passed")
	}
	bad = NoCapabilities("r", "w")
	bad[CapLFS] = Declaration{Support: "kinda"}
	if bad.Validate() == nil {
		t.Error("unknown support passed")
	}
	bad = NoCapabilities("r", "w")
	bad["time-travel"] = Declaration{Support: Native}
	if bad.Validate() == nil {
		t.Error("unknown capability passed")
	}
	data, _ := json.Marshal(c[CapProposals])
	if !strings.Contains(string(data), "workaround") {
		t.Error("the workaround is not in the JSON")
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	caps := NoCapabilities("none", "use another")
	if err := r.Register(KindPlain, FakeFactory(KindPlain, caps)); err != nil {
		t.Fatal(err)
	}
	if r.Register(KindPlain, FakeFactory(KindPlain, caps)) == nil {
		t.Error("a kind registered twice")
	}
	if r.Register("svn", FakeFactory("svn", caps)) == nil {
		t.Error("an unknown kind registered")
	}
	r.Register(KindLaunchway, FakeFactory(KindLaunchway, caps))
	if got := r.Kinds(); len(got) != 2 || got[0] != KindLaunchway || got[1] != KindPlain {
		t.Errorf("Kinds: %v", got)
	}
	p, err := r.Open(Connection{Kind: KindPlain})
	if err != nil || p.Kind() != KindPlain || p.Maturity() != Experimental {
		t.Fatalf("%v %v", p, err)
	}
	if _, err := r.Open(Connection{Kind: KindGitHub}); err == nil {
		t.Error("an unregistered kind opened")
	}
	// A factory that returns the wrong kind, or undeclared capabilities, is refused.
	r.Register(KindGitea, FakeFactory(KindForgejo, caps))
	if _, err := r.Open(Connection{Kind: KindGitea}); err == nil {
		t.Error("kind mismatch accepted")
	}
	r.Register(KindGitLab, FakeFactory(KindGitLab, Capabilities{}))
	if _, err := r.Open(Connection{Kind: KindGitLab}); err == nil {
		t.Error("incomplete capabilities accepted")
	}
	if len(KnownKinds()) != 9 || !KindGitHub.Valid() || Kind("x").Valid() {
		t.Error("KnownKinds")
	}
}

// TestBaseRefusesEveryGroup calls every method of every group of a provider
// that has nothing, and checks each answers ErrUnsupported with a workaround.
func TestBaseRefusesEveryGroup(t *testing.T) {
	p := NewFake(KindPlain, NoCapabilities("plain Git has no proposals, results or rules", "work on the forge itself"))
	ctx := context.Background()
	var repo Repository
	check := func(name string, err error) {
		t.Helper()
		u, ok := IsUnsupported(err)
		if !ok || u.Workaround == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := p.Identity().Whoami(ctx)
	check("Whoami", err)
	_, err = p.Repositories().List(ctx, "")
	check("repos.List", err)
	_, err = p.Repositories().Get(ctx, "a")
	check("repos.Get", err)
	_, err = p.Repositories().Create(ctx, NewRepository{})
	check("repos.Create", err)
	check("repos.Delete", p.Repositories().Delete(ctx, "a"))
	check("repos.SetDefaultBranch", p.Repositories().SetDefaultBranch(ctx, "a", "b"))
	_, err = p.Content().Refs(ctx, repo)
	check("Refs", err)
	_, err = p.Content().Commit(ctx, repo, "x")
	check("Commit", err)
	_, err = p.Content().Tree(ctx, repo, "x", "")
	check("Tree", err)
	_, err = p.Content().Blob(ctx, repo, "x", "y", 1)
	check("Blob", err)
	_, err = p.Content().Compare(ctx, repo, "a", "b")
	check("Compare", err)
	pr := p.Proposals()
	_, err = pr.List(ctx, repo, ProposalOpen, "")
	check("prop.List", err)
	_, err = pr.Get(ctx, repo, 1)
	check("prop.Get", err)
	_, err = pr.Create(ctx, repo, NewProposal{})
	check("prop.Create", err)
	_, err = pr.Update(ctx, repo, 1, ProposalPatch{})
	check("prop.Update", err)
	check("prop.Close", pr.Close(ctx, repo, 1))
	check("prop.Reopen", pr.Reopen(ctx, repo, 1))
	_, err = pr.Revisions(ctx, repo, 1)
	check("prop.Revisions", err)
	rv := p.Reviews()
	_, err = rv.Reviews(ctx, repo, 1)
	check("Reviews", err)
	_, err = rv.Submit(ctx, repo, 1, Approve, "")
	check("Submit", err)
	_, err = rv.Threads(ctx, repo, 1)
	check("Threads", err)
	_, err = rv.Reply(ctx, repo, 1, "t", "b")
	check("Reply", err)
	check("Resolve", rv.Resolve(ctx, repo, 1, "t", true))
	check("Publish", p.Results().Publish(ctx, repo, Result{}))
	_, err = p.Results().List(ctx, repo, "x")
	check("results.List", err)
	l := p.Land()
	_, err = l.Readiness(ctx, repo, 1)
	check("Readiness", err)
	_, err = l.Strategies(ctx, repo)
	check("Strategies", err)
	check("Land", l.Land(ctx, repo, 1, StrategyMerge))
	check("AutoLand", l.AutoLand(ctx, repo, 1, StrategyMerge, true))
	_, err = p.Rules().List(ctx, repo)
	check("rules.List", err)
	check("rules.Set", p.Rules().Set(ctx, repo, nil))
	ev := p.Events()
	if ev.Mode() != "" {
		t.Error("mode")
	}
	_, err = ev.Verify(&http.Request{}, nil)
	check("Verify", err)
	_, _, err = ev.Poll(ctx, repo, "")
	check("Poll", err)
	_, err = p.Access().CloneAddresses(ctx, repo)
	check("CloneAddresses", err)
	_, err = p.Access().Credential(ctx, repo, true)
	check("Credential", err)

	// A feature declared native but not implemented does not pretend to work.
	caps := NoCapabilities("r", "w")
	caps[CapIdentity] = Declaration{Support: Native}
	_, err = NewFake(KindGitHub, caps).Identity().Whoami(ctx)
	if _, ok := IsUnsupported(err); !ok {
		t.Errorf("an unimplemented native group must still fail: %v", err)
	}
}

func TestEventSetIsComplete(t *testing.T) {
	want := []string{"ref.updated", "proposal.opened", "proposal.revised", "proposal.landed", "proposal.closed",
		"review.submitted", "thread.posted", "result.reported"}
	if len(EventKinds) != len(want) {
		t.Fatalf("%v", EventKinds)
	}
	for i, k := range EventKinds {
		if string(k) != want[i] {
			t.Errorf("%d: %s", i, k)
		}
	}
}

func TestCredentialSecretIsNeverSerialised(t *testing.T) {
	data, _ := json.Marshal(Credential{Username: "u", Secret: "do-not-print"})
	if strings.Contains(string(data), "do-not-print") {
		t.Fatal("a credential secret was serialised")
	}
}

func TestRefLayout(t *testing.T) {
	l := PullRefLayout
	if l.Ref(12) != "refs/pull/12/head" {
		t.Error(l.Ref(12))
	}
	if n, ok := l.Parse("refs/pull/12/head"); !ok || n != 12 {
		t.Error("parse")
	}
	for _, bad := range []string{"refs/pull/12/merge", "refs/pull/0/head", "refs/pull/012/head", "refs/pull/x/head", "refs/pull//head", "refs/heads/main", "refs/pull/-1/head", "refs/pull/12/head/x"} {
		if _, ok := l.Parse(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	gl := RefLayout{Prefix: "refs/merge-requests", Suffix: "/head"}
	if n, ok := gl.Parse(gl.Ref(7)); !ok || n != 7 {
		t.Error("other layout")
	}
	if _, ok := gl.Parse("refs/pull/7/head"); ok {
		t.Error("layouts must not cross")
	}
	var none RefLayout
	if !none.IsZero() || (NewFake(KindPlain, NoCapabilities("r", "w")).ProposalRefs() != none) {
		t.Error("zero layout")
	}
	if _, ok := none.Parse("refs/pull/1/head"); ok {
		t.Error("the zero layout parsed a ref")
	}
}
