package sealing

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/forge/webhooks"
	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const (
	cMirrorEnv  = "CANARY-mirror-token-in-the-environment-6a1f3c8e"
	cInlineCred = "CANARY-inline-credential-in-an-address-2d7b9e40"
	cVarValue   = "CANARY-text-given-as-a-secret-variable-8c4e1a97"
)

// noStorage is the repository storage of a mirror that is never synced here.
type noStorage struct{ mirror.Storage }

func (noStorage) Refs(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}

// One record of every kind that holds data about secrets without holding the
// secrets: a pull request with a review and comments, a webhook with its
// delivery records (the receiver echoes the signature back, so the response
// column holds it), a rollback, a pull mirror whose credential is a reference,
// and a secret variable that is a reference. The server's own sealed secrets
// (the markers of the test above) are present beside them; none of the markers
// of either set may be in a full dump, on SQLite and on PostgreSQL.
func TestCanaryEveryKindOfStoredDataHoldsNoSecret(t *testing.T) {
	t.Setenv("CANARY_MIRROR_TOKEN", cMirrorEnv)
	addr := testdb.New(t)
	e := openEngine(t, addr)
	s := start(t, e, newHolder(t))
	db := e.Database()
	for key, val := range map[string]string{"runner_token": cRunner, "login_state_key": cLogin, "hosted_report_key": cReport, "git_run_token_key": cRunTok} {
		if _, err := s.SharedValue(key, val); err != nil {
			t.Fatal(err)
		}
	}

	// A webhook, and a delivery whose receiver answers with the signature it saw.
	var sawSignature string
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawSignature = r.Header.Get("X-Launchway-Signature-256")
		_, _ = w.Write([]byte("received " + sawSignature))
	}))
	defer recv.Close()
	hooks := webhooks.New(webhooks.Options{DB: db, Sealer: s.Webhooks(), Log: quiet})
	hook, _, err := hooks.Create(ctx, webhooks.NewHook{RepoID: "repo1", Name: "ci", URL: recv.URL, Events: []string{"push", "pull_request"}, Secret: cWebhook, CreatedBy: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := hooks.Ping(ctx, hook.ID, webhooks.Repo{Path: "acme/shop", Owner: "acme", Name: "shop"}, "ada")
	if err != nil || d.Status != 200 || !strings.Contains(d.Response, sawSignature) || sawSignature == "" {
		t.Fatalf("the delivery did not happen: %+v %v", d, err)
	}

	// A pull request with a review and comments (one with a suggestion).
	ps := pulls.NewStore(db)
	pr, err := ps.Create(ctx, "repo1", "ada", pulls.New{Title: "Fix rounding", Body: "Rounds half up.", SourceBranch: "fix", TargetBranch: "main"}, "1111111", "2222222", "2222222")
	if err != nil {
		t.Fatal(err)
	}
	rev, err := ps.AddReview(ctx, pr.ID, pulls.Review{Author: "bob", Verdict: "approve", Body: "Looks right.", CommitSHA: "1111111", Role: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddComment(ctx, pr.ID, pulls.Comment{Author: "bob", ReviewID: rev.ID, Body: "Use half-even?", Path: "round.go", Side: "new", Line: 3, Suggestion: "return math.RoundToEven(x)", HasSuggestion: true}); err != nil {
		t.Fatal(err)
	}

	// A rollback, as the event projection writes it (engine/rollbacks_review.go).
	if _, err := db.Exec(ctx, `INSERT INTO rollbacks(run_id, project, run_number, environment, service, to_deployment, to_version, to_run_id,
		to_run_number, from_deployment, from_version, reason, actor, emergency, review_required, created_at)
		VALUES('run_rb','shop',9,'prod','api','dep_1','v1','run_1',1,'dep_2','v2','bad release','ada',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rollback_reverts(run_id, deployment, position) VALUES('run_rb','dep_2',0)`); err != nil {
		t.Fatal(err)
	}

	// A pull mirror: the credential is the name of an environment variable.
	fs := store.New(db)
	conn, err := fs.CreateConnection(ctx, forge.Connection{Name: "home", Kind: forge.KindLaunchway, Enabled: true}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := fs.CreateRepository(ctx, forge.Repository{Connection: conn.ID, Name: "mirrored", Path: "acme/mirrored", DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	guard, err := egress.New([]string{"198.51.100.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	ms := mirror.New(db, fs, noStorage{}, conn, mirror.Options{Guard: guard, Log: quiet})
	if _, err := ms.SetPull(ctx, repo.Path, mirror.Spec{URL: "https://" + "198.51.100.7/acme/src.git", Username: "mirror", Credential: forge.SecretRef{Env: "CANARY_MIRROR_TOKEN"}}, "ada"); err != nil {
		t.Fatal(err)
	}
	// A credential written into the address is refused, never stored.
	if _, err := ms.SetPull(ctx, repo.Path, mirror.Spec{URL: "https://mirror:" + cInlineCred + "@198.51.100.7/acme/src.git"}, "ada"); err == nil {
		t.Fatal("an address with a credential in it was accepted")
	}

	// A secret variable is a reference; the secret itself, given as text, is refused.
	vs := settings.NewVariableService(settings.NewVariableStore(e), nil)
	key := settings.VarKey{Level: settings.LevelInstallation, Name: "deploy_token"}
	ref, _ := json.Marshal(map[string]string{"store": "vault", "key": "apps/shop", "field": "token"})
	if _, err := vs.Set(ctx, "ada", settings.SetInput{VarKey: key, Type: settings.VarSecret, Value: ref}); err != nil {
		t.Fatal(err)
	}
	text, _ := json.Marshal(cVarValue)
	if _, err := vs.Set(ctx, "ada", settings.SetInput{VarKey: settings.VarKey{Level: settings.LevelInstallation, Name: "pasted"}, Type: settings.VarSecret, Value: text}); err == nil {
		t.Fatal("a secret given as text was accepted")
	}

	dump := dumpAll(t, e, addr)
	for what, v := range map[string]string{"the shared runner token": cRunner, "the login state key": cLogin, "the report link key": cReport, "the Git run token key": cRunTok,
		"a webhook secret": cWebhook, "the mirror's credential, read from the environment": cMirrorEnv, "a credential written into a mirror address": cInlineCred,
		"a secret variable given as text": cVarValue} {
		mustNotAppear(t, dump, what, []byte(v))
	}
	// The webhook's own signature, which the delivery record holds (the receiver echoed it), is not the secret.
	if !bytes.Contains(dump, []byte(sawSignature)) {
		t.Log("the delivery response is not in the dump: nothing to check there")
	}
	// What was stored is there (the test saw the records it made).
	for _, want := range []string{"Fix rounding", "bad release", "198.51.100.7", "CANARY_MIRROR_TOKEN", "apps/shop"} {
		if !bytes.Contains(dump, []byte(want)) {
			t.Errorf("%q is not in the dump: the test did not create every kind of record", want)
		}
	}
}
