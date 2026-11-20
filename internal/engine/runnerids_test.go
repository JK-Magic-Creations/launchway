package engine

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/accept"
	"github.com/jk-magic-creations/launchway/internal/runnerauth"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// Pools used by the tests below: web serves web-* projects, nightly serves
// every project but only scheduled runs, deploy is a deploy pool.
var testPools = []RunnerPool{
	{Name: "web", Kind: PoolBuild, Projects: []string{"web-*"}},
	{Name: "nightly", Kind: PoolBuild, Projects: []string{"*"}, Events: []string{"manual"}},
	{Name: "locked", Kind: PoolBuild, Projects: []string{"vault-*"}, RequireIdentity: true},
	{Name: "deploy", Kind: PoolDeploy, Targets: []string{"prod"}},
}

func openPools(t *testing.T, addr string, clk *clock, pools []RunnerPool) *Engine {
	t.Helper()
	e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now, RunnerPools: pools})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func newPoolEngine(t *testing.T) (*Engine, *clock, string) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	return openPools(t, addr, clk, testPools), clk, addr
}

// enrolled is a runner with an identity, as the tests drive it.
type enrolled struct {
	id  *RunnerIdentity
	key ed25519.PrivateKey
}

func newKey(t *testing.T) ed25519.PrivateKey {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func pub(k ed25519.PrivateKey) string { return runnerauth.EncodeKey(k.Public().(ed25519.PublicKey)) }

func joinWith(e *Engine, secret string, k ed25519.PrivateKey, name string) (*RunnerIdentity, error) {
	return e.Join(JoinRequest{Token: secret, PublicKey: pub(k), Proof: runnerauth.JoinProof(k, secret), Hostname: name,
		Facts: RunnerInfo{Name: name, Version: "test", OS: "linux", Arch: "amd64", Executor: "podman", Labels: []string{"linux"}}})
}

func enrol(t *testing.T, e *Engine, pool string, labels ...string) *enrolled {
	t.Helper()
	secret, _, err := e.CreateJoinToken(JoinTokenSpec{Pool: pool, Labels: labels}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	k := newKey(t)
	id, err := joinWith(e, secret, k, "host-"+pool)
	if err != nil {
		t.Fatal(err)
	}
	return &enrolled{id: id, key: k}
}

func (r *enrolled) assertion(t *testing.T, e *Engine, aud string) string {
	t.Helper()
	a, err := runnerauth.NewAssertion(r.key, r.id.ID, "https://ci.example.com", aud, e.now())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (r *enrolled) token(t *testing.T, e *Engine) string {
	t.Helper()
	g, err := e.IssueAccessToken(r.assertion(t, e, runnerauth.AudienceToken))
	if err != nil {
		t.Fatal(err)
	}
	return g.Token
}

// info is what the API makes of the runner's requests: the identity read
// from its access token.
func (r *enrolled) info(t *testing.T, e *Engine) RunnerInfo {
	t.Helper()
	id, err := e.RunnerByAccessToken(r.token(t, e))
	if err != nil {
		t.Fatal(err)
	}
	return id.Info(RunnerInfo{ID: "whatever-it-says", Name: "x", Features: FeatureArtifacts})
}

func createIn(t *testing.T, e *Engine, project, src string, trig Trigger) string {
	t.Helper()
	r, err := e.CreateRun(NewRun{Project: project, Pipeline: parse(t, src), Trigger: trig, Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

const oneStep = "pipeline: p\nsteps:\n  a: {image: x, run: a}\n"

// A join token is used once; a second runner that got hold of it (stolen,
// or copied from a log) cannot enrol, on this server or another.
func TestStolenJoinTokenCannotBeReused(t *testing.T) {
	e, clk, addr := newPoolEngine(t)
	other := openPools(t, addr, clk, testPools)
	secret, tok, err := e.CreateJoinToken(JoinTokenSpec{Pool: "web"}, "ann")
	if err != nil || !strings.HasPrefix(secret, runnerauth.JoinPrefix) || tok.MaxUses != 1 {
		t.Fatalf("%v %+v", err, tok)
	}
	if _, err := joinWith(e, secret, newKey(t), "first"); err != nil {
		t.Fatal(err)
	}
	for _, srv := range []*Engine{e, other} {
		if _, err := joinWith(srv, secret, newKey(t), "thief"); !errors.Is(err, ErrBadJoinToken) {
			t.Fatalf("second use: %v", err)
		}
	}
	// Only the hash is stored.
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM runner_join_tokens WHERE hash = ?`, secret).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the secret is stored as it is: %d %v", n, err)
	}
	// Expired and revoked tokens enrol nobody.
	s2, _, _ := e.CreateJoinToken(JoinTokenSpec{Pool: "web", TTL: time.Minute}, "ann")
	clk.Add(2 * time.Minute)
	if _, err := joinWith(e, s2, newKey(t), "late"); !errors.Is(err, ErrBadJoinToken) {
		t.Fatalf("expired: %v", err)
	}
	s3, t3, _ := e.CreateJoinToken(JoinTokenSpec{Pool: "web"}, "ann")
	if _, err := e.RevokeJoinToken(t3.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := joinWith(e, s3, newKey(t), "after-revoke"); !errors.Is(err, ErrBadJoinToken) {
		t.Fatalf("revoked: %v", err)
	}
	// A proof made with another key, or for another token, is refused.
	s4, _, _ := e.CreateJoinToken(JoinTokenSpec{Pool: "web"}, "ann")
	k := newKey(t)
	if _, err := e.Join(JoinRequest{Token: s4, PublicKey: pub(k), Proof: runnerauth.JoinProof(newKey(t), s4)}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("proof by another key: %v", err)
	}
	list, err := e.JoinTokens()
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]int{}
	for _, l := range list {
		states[l.State]++
	}
	if states["used"] != 1 || states["expired"] != 1 || states["revoked"] != 1 || states["active"] != 1 {
		t.Fatalf("states: %v", states)
	}
}

// Runners racing for a token with max_uses 3, through two servers: exactly
// three enrol.
func TestJoinTokenUsesAreCountedAcrossServers(t *testing.T) {
	e, clk, addr := newPoolEngine(t)
	other := openPools(t, addr, clk, testPools)
	secret, _, err := e.CreateJoinToken(JoinTokenSpec{Pool: "web", MaxUses: 3, Labels: []string{"gpu"}}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	joined := 0
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			srv := []*Engine{e, other}[i%2]
			id, err := joinWith(srv, secret, newKey(t), "r")
			switch {
			case err == nil:
				mu.Lock()
				joined++
				mu.Unlock()
				if id.Pool != "web" || strings.Join(id.Labels, ",") != "gpu,linux" || !strings.HasPrefix(id.ID, "rnr_") {
					t.Errorf("identity: %+v", id)
				}
			case !errors.Is(err, ErrBadJoinToken):
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if joined != 3 {
		t.Fatalf("%d runners joined with a token for 3", joined)
	}
}

// The runner ID is the server's: whatever a runner says its ID is, the
// identity's is used, and an assertion naming another runner is refused
// because it is not signed by that runner's key.
func TestForgedRunnerIDIsRefused(t *testing.T) {
	e, _, _ := newPoolEngine(t)
	a, b := enrol(t, e, "web"), enrol(t, e, "web")
	if got := a.info(t, e); got.ID != a.id.ID || got.Auth != AuthIdentity || got.Pool != "web" {
		t.Fatalf("info: %+v", got)
	}
	// b signs an assertion that says it is a.
	forged, _ := runnerauth.NewAssertion(b.key, a.id.ID, "https://ci", runnerauth.AudienceToken, e.now())
	if _, err := e.IssueAccessToken(forged); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("forged: %v", err)
	}
	// An unknown runner ID.
	ghost, _ := runnerauth.NewAssertion(b.key, "rnr_ghost", "https://ci", runnerauth.AudienceToken, e.now())
	if _, err := e.IssueAccessToken(ghost); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ghost: %v", err)
	}
	// An assertion for the rotate endpoint is not one for tokens.
	if _, err := e.IssueAccessToken(a.assertion(t, e, runnerauth.AudienceRotate)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("audience: %v", err)
	}
}

// Each assertion is accepted once, by any server; one too far from the
// server's clock is refused with ErrSkew, so the runner can correct.
func TestAssertionReplayAndClockSkew(t *testing.T) {
	e, clk, addr := newPoolEngine(t)
	other := openPools(t, addr, clk, testPools)
	r := enrol(t, e, "web")
	as := r.assertion(t, e, runnerauth.AudienceToken)
	if _, err := e.IssueAccessToken(as); err != nil {
		t.Fatal(err)
	}
	for _, srv := range []*Engine{e, other} {
		if _, err := srv.IssueAccessToken(as); !errors.Is(err, ErrReplay) {
			t.Fatalf("replayed: %v", err)
		}
	}
	// A runner whose clock is 10 minutes fast.
	fast, _ := runnerauth.NewAssertion(r.key, r.id.ID, "https://ci", runnerauth.AudienceToken, e.now().Add(10*time.Minute))
	if _, err := e.IssueAccessToken(fast); !errors.Is(err, runnerauth.ErrSkew) {
		t.Fatalf("fast clock: %v", err)
	}
	// One minute off is within the tolerance.
	near, _ := runnerauth.NewAssertion(r.key, r.id.ID, "https://ci", runnerauth.AudienceToken, e.now().Add(time.Minute))
	if _, err := e.IssueAccessToken(near); err != nil {
		t.Fatalf("a minute off: %v", err)
	}
	// Remembered assertions are forgotten once they could not be used.
	clk.Add(10 * time.Minute)
	e.pruneIdentities()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM runner_assertions`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("assertions kept: %d %v", n, err)
	}
	// Access tokens expire.
	tok := r.token(t, e)
	clk.Add(runnerauth.AccessTTL + time.Second)
	if _, err := e.RunnerByAccessToken(tok); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired access token: %v", err)
	}
}

// A runner of a pool never gets a step of a project its pool does not
// serve, whatever it asks for; the default pool serves what no other pool
// names; event-limited pools take only those events.
func TestPoolsLimitWhichRunsARunnerTakes(t *testing.T) {
	e, _, _ := newPoolEngine(t)
	web := enrol(t, e, "web")
	nightly := enrol(t, e, "nightly")
	def := enrol(t, e, DefaultPool)
	shared := RunnerInfo{ID: "runner-shared", Name: "old", Features: FeatureArtifacts, Auth: AuthShared}

	shop := createIn(t, e, "shop", oneStep, Trigger{Event: "push"})
	// The web runner asks, says it is of another pool and has every label:
	// none of that counts, and shop is not one of its projects.
	wi := web.info(t, e)
	wi.Pool, wi.Labels = "", []string{"anything"}
	wi.Pool = web.id.Pool // what the API sets, whatever was said
	if j := leaseAs(t, e, wi); j != nil {
		t.Fatalf("web runner took %s of project %s", j.StepID, j.Project)
	}
	if j := leaseAs(t, e, nightly.info(t, e)); j != nil {
		t.Fatalf("nightly runner took a push run: %s", j.Project)
	}
	j := leaseAs(t, e, shared)
	if j == nil || j.RunID != shop {
		t.Fatalf("the shared runner (default pool) should take shop: %+v", j)
	}
	if j.Event != "push" {
		t.Fatalf("the job does not say what started the run: %q", j.Event)
	}
	// A web project goes to the web pool only, not to the default pool.
	webRun := createIn(t, e, "web-shop", oneStep, Trigger{Event: "push"})
	if j := leaseAs(t, e, def.info(t, e)); j != nil {
		t.Fatalf("default pool took %s", j.Project)
	}
	if j := leaseAs(t, e, shared); j != nil {
		t.Fatalf("shared runner took %s", j.Project)
	}
	if j := leaseAs(t, e, web.info(t, e)); j == nil || j.RunID != webRun {
		t.Fatalf("web runner should take web-shop: %+v", j)
	}
	// A scheduled run of any project goes to nightly.
	sched := createIn(t, e, "shop", oneStep, Trigger{Event: "manual"})
	if j := leaseAs(t, e, nightly.info(t, e)); j == nil || j.RunID != sched {
		t.Fatalf("nightly should take the scheduled run: %+v", j)
	}
}

// The check is made again in the transaction that grants the lease, not
// only in the query that finds candidates.
func TestPoolCheckedWhenGranting(t *testing.T) {
	e, _, _ := newPoolEngine(t)
	web := enrol(t, e, "web")
	id := createIn(t, e, "shop", oneStep, Trigger{Event: "push"})
	info := web.info(t, e)
	var job *Job
	err := e.withRun(id, func(c *cmd) error {
		if why := c.e.refusesRun(info, c.st.Project, c.st.Trigger.Event, c.st.Pipeline.Runner); why == "" {
			t.Error("web may take shop?")
		} else if !strings.Contains(why, "does not serve project shop") {
			t.Errorf("why: %s", why)
		}
		return nil
	})
	if err != nil || job != nil {
		t.Fatal(err)
	}
}

// A pool that requires identities takes no runner with a shared token.
func TestPoolRequiringIdentitiesRefusesSharedTokens(t *testing.T) {
	e, _, _ := newPoolEngine(t)
	pools := append([]RunnerPool{}, testPools...)
	_ = pools
	createIn(t, e, "vault-a", oneStep, Trigger{Event: "push"})
	if j := leaseAs(t, e, RunnerInfo{ID: "r", Pool: "locked", Auth: AuthShared, Features: FeatureArtifacts}); j != nil {
		t.Fatal("a shared-token runner took work of a pool that requires identities")
	}
	locked := enrol(t, e, "locked")
	if j := leaseAs(t, e, locked.info(t, e)); j == nil {
		t.Fatal("the enrolled runner got nothing")
	}
}

// runner: { pool, labels } in a pipeline chooses among the pools that serve
// the project; the run says why it waits when no runner may take it.
func TestPipelineChoosesRunners(t *testing.T) {
	e, _, _ := newPoolEngine(t)
	gpu := enrol(t, e, "web", "gpu")
	plain := enrol(t, e, "web")
	src := "pipeline: p\nrunner: {labels: [gpu]}\nsteps:\n  a: {image: x, run: a}\n"
	id := createIn(t, e, "web-ml", src, Trigger{Event: "push"})
	d, err := e.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.PoolHint == "" {
		// No runner has asked yet: say so.
		t.Log("no hint before runners are seen")
	}
	if j := leaseAs(t, e, plain.info(t, e)); j != nil {
		t.Fatal("a runner without the label took the step")
	}
	if d, _ := e.GetRun(id); !strings.Contains(d.PoolHint, "label gpu") {
		t.Fatalf("hint: %q", d.PoolHint)
	}
	if j := leaseAs(t, e, gpu.info(t, e)); j == nil || j.RunID != id {
		t.Fatalf("the gpu runner should take it: %+v", j)
	}
	// A pool that does not serve the project.
	bad := createIn(t, e, "shop", "pipeline: p\nrunner: {pool: web}\nsteps:\n  a: {image: x, run: a}\n", Trigger{Event: "push"})
	if d, _ := e.GetRun(bad); !strings.Contains(d.PoolHint, "does not serve project shop") {
		t.Fatalf("hint: %q", d.PoolHint)
	}
	// A pool that does not exist.
	missing := createIn(t, e, "shop", "pipeline: p\nrunner: {pool: nope}\nsteps:\n  a: {image: x, run: a}\n", Trigger{Event: "push"})
	if d, _ := e.GetRun(missing); !strings.Contains(d.PoolHint, "does not have") {
		t.Fatalf("hint: %q", d.PoolHint)
	}
	// Such runs need schema 22.
	var min int
	if err := e.db.QueryRow(`SELECT min_schema FROM runs WHERE id = ?`, id).Scan(&min); err != nil || min != schemaPools {
		t.Fatalf("min_schema %d %v", min, err)
	}
}

// What a runner's accept: section says it allows is used to leave it out
// of work it would refuse anyway.
func TestAcceptProjectsLimitLeasing(t *testing.T) {
	e, _, _ := newPoolEngine(t)
	no := false
	r := RunnerInfo{ID: "r", Features: FeatureArtifacts, Accept: &accept.Config{Projects: []string{"api"}, PullRequests: &no}}
	createIn(t, e, "shop", oneStep, Trigger{Event: "push"})
	if j := leaseAs(t, e, r); j != nil {
		t.Fatal("took a project accept.projects does not allow")
	}
	if d, _ := e.ListRuns(RunFilter{}); len(d) == 0 {
		t.Fatal("no runs")
	}
	api := createIn(t, e, "api", oneStep, Trigger{Event: "push"})
	if j := leaseAs(t, e, r); j == nil || j.RunID != api {
		t.Fatalf("%+v", j)
	}
}

// Revoking a runner in the middle of a step: its access token stops
// working at once, the step is queued again as a new attempt for another
// runner, and anything the revoked runner sends about its lease is refused.
func TestRevocationMidStep(t *testing.T) {
	e, clk, addr := newPoolEngine(t)
	other := openPools(t, addr, clk, testPools)
	r := enrol(t, e, "web")
	tok := r.token(t, e)
	id := createIn(t, e, "web-a", oneStep, Trigger{Event: "push"})
	j := leaseAs(t, e, r.info(t, e))
	if j == nil {
		t.Fatal("nothing leased")
	}
	n, err := other.RevokeRunner(r.id.ID, "bob", "lost laptop")
	if err != nil || n != 1 {
		t.Fatalf("revoke: %d %v", n, err)
	}
	for _, srv := range []*Engine{e, other} {
		if _, err := srv.RunnerByAccessToken(tok); err == nil {
			t.Fatal("the access token still works")
		}
		if _, err := srv.IssueAccessToken(r.assertion(t, srv, runnerauth.AudienceToken)); !errors.Is(err, ErrRunnerRevoked) {
			t.Fatalf("new token after revocation: %v", err)
		}
	}
	if _, err := e.Heartbeat(j.LeaseID); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("heartbeat: %v", err)
	}
	zero := 0
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", ExitCode: &zero}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("finish: %v", err)
	}
	// Even with an info that still says it is the runner, the engine gives
	// it nothing.
	if _, err := e.Lease(context.Background(), RunnerInfo{ID: r.id.ID, Pool: "web", Kind: PoolBuild, Auth: AuthIdentity}, 0); !errors.Is(err, ErrRunnerRevoked) {
		t.Fatalf("lease after revocation: %v", err)
	}
	t.Logf("after revocation: %v", statuses(t, e, id))
	r2 := enrol(t, e, "web")
	j2 := leaseAs(t, e, r2.info(t, e))
	if j2 == nil || j2.RunID != id || j2.Attempt != 2 {
		t.Fatalf("the step should be queued again as attempt 2: %+v", j2)
	}
	ids, _ := e.RunnerIdentities()
	for _, x := range ids {
		if x.ID == r.id.ID && (x.RevokedBy != "bob" || x.RevokeReason != "lost laptop") {
			t.Fatalf("%+v", x)
		}
	}
}

// Rotating the key in the middle of a step: the step carries on (its
// lease is the runner's, whichever key), the old key gets no more tokens,
// and the new one does.
func TestKeyRotationMidStep(t *testing.T) {
	e, clk, addr := newPoolEngine(t)
	other := openPools(t, addr, clk, testPools)
	r := enrol(t, e, "web")
	old := r.token(t, e)
	createIn(t, e, "web-a", oneStep, Trigger{Event: "push"})
	j := leaseAs(t, e, r.info(t, e))
	if _, err := e.AskRotation(r.id.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RunnerByAccessToken(old); err == nil {
		t.Fatal("asking for a rotation leaves the access token working")
	}
	if g, err := e.IssueAccessToken(r.assertion(t, e, runnerauth.AudienceToken)); err != nil || !g.Rotate {
		t.Fatalf("the grant should ask for a rotation: %+v %v", g, err)
	}
	nk := newKey(t)
	c := runnerauth.Claims{Issuer: r.id.ID, Subject: r.id.ID, Audience: "https://ci" + runnerauth.AudienceRotate,
		IssuedAt: e.now().Unix(), Expires: e.now().Add(time.Minute).Unix(), ID: runnerauth.Random(8), NewKey: pub(nk)}
	as, _ := runnerauth.Sign(r.key, c)
	// A proof by some other key is refused.
	if _, err := other.RotateRunnerKey(as, pub(nk), runnerauth.RotateProof(newKey(t), as)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bad proof: %v", err)
	}
	as2, _ := runnerauth.Sign(r.key, func() runnerauth.Claims { c.ID = runnerauth.Random(8); return c }())
	id, err := other.RotateRunnerKey(as2, pub(nk), runnerauth.RotateProof(nk, as2))
	if err != nil {
		t.Fatal(err)
	}
	if id.RotateAsked != 0 || id.KeyFingerprint != runnerauth.Fingerprint(pub(nk)) {
		t.Fatalf("%+v", id)
	}
	if _, err := e.RunnerByAccessToken(old); err == nil {
		t.Fatal("the old access token still works after rotation")
	}
	if _, err := e.IssueAccessToken(r.assertion(t, e, runnerauth.AudienceToken)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("old key: %v", err)
	}
	r.key = nk
	tok := r.token(t, e)
	if _, err := e.RunnerByAccessToken(tok); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Heartbeat(j.LeaseID); err != nil {
		t.Fatalf("the step's lease survives a rotation: %v", err)
	}
	ok(t, e, j, nil)
}

// Older servers leave runs that pools decide alone, and a build limited to
// schema 21 cannot be configured with build pools.
func TestOlderBuildLeavesPoolRunsAlone(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	e := openPools(t, addr, clk, testPools)
	if _, err := Open(Options{Database: addr, Now: clk.Now, schema: schemaPools - 1, RunnerPools: testPools}); err == nil {
		t.Fatal("a build of schema 21 accepted build pools")
	}
	old := mustOpenAs(t, addr, clk, schemaPools-1, &syncBuffer{})
	id := createIn(t, e, "shop", oneStep, Trigger{Event: "push"})
	if j, err := old.Lease(context.Background(), RunnerInfo{ID: "old", Features: FeatureArtifacts}, 0); err != nil || j != nil {
		t.Fatalf("the older server handed out a run pools decide: %+v %v", j, err)
	}
	if j := leaseAs(t, e, RunnerInfo{ID: "new", Features: FeatureArtifacts}); j == nil || j.RunID != id {
		t.Fatalf("%+v", j)
	}
	// Without build pools, runs are as before and older servers serve them.
	clk2 := &clock{t: clk.Now()}
	addr2 := testdb.New(t)
	plain := openPools(t, addr2, clk2, nil)
	old2 := mustOpenAs(t, addr2, clk2, schemaPools-1, &syncBuffer{})
	createIn(t, plain, "shop", oneStep, Trigger{Event: "push"})
	if j, err := old2.Lease(context.Background(), RunnerInfo{ID: "old", Features: FeatureArtifacts}, 0); err != nil || j == nil {
		t.Fatalf("without pools an older server should take ordinary runs: %v", err)
	}
	// A build of schema 20 knows no identities.
	older := mustOpenAs(t, addr2, clk2, schemaIdentity-1, &syncBuffer{})
	if _, _, err := older.CreateJoinToken(JoinTokenSpec{}, "ann"); !errors.Is(err, ErrNeedsNewer) {
		t.Fatalf("join token on schema 20: %v", err)
	}
	if _, err := older.RunnerByAccessToken("lwr_x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("access token on schema 20: %v", err)
	}
}

// Every runner with an identity is listed, with what it holds.
func TestRunnersListIdentities(t *testing.T) {
	e, _, _ := newPoolEngine(t)
	r := enrol(t, e, "web", "gpu")
	idle := enrol(t, e, "nightly")
	createIn(t, e, "web-a", oneStep, Trigger{Event: "push"})
	if j := leaseAs(t, e, r.info(t, e)); j == nil {
		t.Fatal("nothing leased")
	}
	leaseAs(t, e, RunnerInfo{ID: "runner-old", Name: "old", Auth: AuthShared})
	list, err := e.Runners()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]RunnerView{}
	for _, v := range list {
		by[v.ID] = v
	}
	got := by[r.id.ID]
	if got.Identity == nil || got.Auth != AuthIdentity || got.Pool != "web" || len(got.Work) != 1 || got.Work[0].Project != "web-a" ||
		!strings.Contains(strings.Join(got.Labels, ","), "gpu") {
		t.Fatalf("%+v", got)
	}
	if v := by[idle.id.ID]; v.Identity == nil || v.Pool != "nightly" {
		t.Fatalf("idle: %+v", v)
	}
	if v := by["runner-old"]; v.Auth != AuthShared || v.Pool != DefaultPool || v.Identity != nil {
		t.Fatalf("shared: %+v", v)
	}
}

// A runner refusing a step (its accept: section) fails it without retries,
// saying why.
func TestRefusedStepFailsWithoutRetry(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, "pipeline: p\nsteps:\n  a: {image: x, run: a, retry: 2}\n", Trigger{})
	j := take(t, e)
	if err := e.Finish(j.LeaseID, Result{Outcome: OutcomeRefused, Error: "image x is not in accept.images"}); err != nil {
		t.Fatal(err)
	}
	none(t, e)
	d, err := e.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if s := d.StepList[0]; s.Status != "failed" || !strings.Contains(s.Error, "the runner refused the step: image x is not in accept.images") {
		t.Fatalf("%+v", s)
	}
}
