package pulls

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge/signing"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

type testSealer struct{}

func (testSealer) Seal(_ context.Context, _ string, plain []byte) (string, error) {
	return "sealed:" + string(plain), nil
}
func (testSealer) Open(_ context.Context, _, stored string) ([]byte, error) {
	return []byte(strings.TrimPrefix(stored, "sealed:")), nil
}

func (w *world) signingOn(rule Rule) *signing.Service {
	w.t.Helper()
	svc := &signing.Service{Store: signing.NewStore(w.eng.Database()), Git: w.st, Seal: func(context.Context) signing.Sealer { return testSealer{} }}
	w.svc.Sign = svc.SignPayload
	w.svc.Verify = func(ctx context.Context, o []storage.PushObject) []signing.Result { return svc.VerifyRaw(ctx, o, "") }
	if err := w.svc.Store.SetRule(w.ctx, repoID, rule, "ada"); err != nil {
		w.t.Fatal(err)
	}
	return svc
}

// A rule that asks for verified commits: the merge commit and the squash
// commit Launchway makes are signed with the installation key and pass; a merge
// that brings the pull request's own unsigned commits is refused, saying which
// and why; with no way to sign, nothing is merged into the branch.
func TestRequireSignedMergesAreSignedByTheInstallation(t *testing.T) {
	rule := Rule{Pattern: "main", RequireSigned: true, RequiredApprovals: 0, ApprovalRole: RoleOperator}

	t.Run("squash is signed and verified", func(t *testing.T) {
		w, p := mergeWorld(t)
		svc := w.signingOn(rule)
		res, err := w.svc.Merge(w.ctx, repoID, p, MergeInput{Method: MethodSquash, Actor: "grace", ActorRole: RoleOperator, RepoPath: "ada/demo"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.VerifyRepo(w.ctx, repoID, []string{res.Commit})
		if err != nil || len(got) != 1 || got[0].State != signing.Verified || !got[0].Installation {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("a merge that brings unsigned commits is refused", func(t *testing.T) {
		w, p := mergeWorld(t)
		w.signingOn(rule)
		before := w.refs()["refs/heads/main"]
		_, err := w.svc.Merge(w.ctx, repoID, p, MergeInput{Method: MethodMerge, Actor: "grace", ActorRole: RoleOperator, RepoPath: "ada/demo"})
		if err == nil || !strings.Contains(err.Error(), "not verified") || !strings.Contains(err.Error(), "unsigned") || !strings.Contains(err.Error(), "squash") {
			t.Fatalf("want a refusal naming the unsigned commits and the way out: %v", err)
		}
		if w.refs()["refs/heads/main"] != before {
			t.Fatal("the target moved although the merge was refused")
		}
	})
	t.Run("without a way to sign nothing is merged", func(t *testing.T) {
		w, p := mergeWorld(t)
		if err := w.svc.Store.SetRule(w.ctx, repoID, rule, "ada"); err != nil {
			t.Fatal(err)
		}
		w.svc.Verify = func(context.Context, []storage.PushObject) []signing.Result { return []signing.Result{{Object: "abcdef0123456789", State: signing.Unsigned}} }
		_, err := w.svc.Merge(w.ctx, repoID, p, MergeInput{Method: MethodSquash, Actor: "grace", ActorRole: RoleOperator, RepoPath: "ada/demo"})
		if err == nil || !strings.Contains(err.Error(), "not verified") {
			t.Fatalf("%v", err)
		}
		w.svc.Verify = nil
		if _, err = w.svc.Merge(w.ctx, repoID, p, MergeInput{Method: MethodSquash, Actor: "grace", ActorRole: RoleOperator, RepoPath: "ada/demo"}); err == nil || !strings.Contains(err.Error(), "off on this server") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a rule that does not ask changes nothing", func(t *testing.T) {
		w, p := mergeWorld(t)
		w.signingOn(Rule{Pattern: "main", ApprovalRole: RoleOperator})
		if _, err := w.svc.Merge(w.ctx, repoID, p, MergeInput{Method: MethodMerge, Actor: "grace", ActorRole: RoleOperator, RepoPath: "ada/demo"}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestVerifiedRuleRoundTrips(t *testing.T) {
	w := newWorld(t)
	if err := w.svc.Store.SetRule(w.ctx, repoID, Rule{Pattern: "main", ApprovalRole: RoleOperator, RequireSigned: true, VerifiedOnlyPush: true, MergeQueue: true}, "ada"); err != nil {
		t.Fatal(err)
	}
	rs, err := w.svc.Store.Rules(w.ctx, repoID)
	if err != nil || len(rs) != 1 || !rs[0].RequireSigned || !rs[0].VerifiedOnlyPush || !rs[0].MergeQueue {
		t.Fatalf("%+v %v", rs, err)
	}
	if r, _ := RuleFor(rs, "main"); !r.RequireSigned || !r.VerifiedOnlyPush {
		t.Fatalf("%+v", r)
	}
}
