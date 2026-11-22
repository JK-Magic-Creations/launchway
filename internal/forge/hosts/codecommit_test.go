package hosts_test

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/codecommit"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/codecommit/codecommitfake"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The real codecommit kind has no commit status (declared none) and has pull request comments: a result goes
// to the pull request's one summary comment, updated in place, and a Flight with no pull request gets nothing
// and its page says so. The same path as the comment-only double of fallback_test.go, on the real provider.
func TestCodeCommitResultsGoToTheOneUpdatedComment(t *testing.T) {
	h := codecommitfake.New()
	defer h.Close()
	defer codecommit.UseTestAPI(h.API())()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	t.Setenv("LW_CC_CRED", codecommitfake.CredentialDoc())
	g, _ := egress.New([]string{"127.0.0.1"})
	s := hosts.New(eng.Database(), eng, g)
	ctx := t.Context()
	if _, err := s.Add(ctx, hosts.AddInput{Name: "cc", Kind: forge.KindCodeCommit, Credential: forge.SecretRef{Env: "LW_CC_CRED"}}, "dana"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, "shop", "cc", h.RepoPath()); err != nil {
		t.Fatal(err)
	}
	if via := s.StatusVia(ctx, "shop"); via != hosts.ViaComment {
		t.Fatalf("via %s", via)
	}
	r := forge.Result{Producer: "launchway", Name: "launchway/ci", SHA: h.Branch("feature"), State: forge.Passing, Summary: "Sortie #1 passed"}
	if via, err := s.ReportResult(ctx, "shop", r, 1, "Sortie #1 is running"); err != nil || via != hosts.ViaComment {
		t.Fatalf("%v %v", via, err)
	}
	if via, err := s.ReportResult(ctx, "shop", r, 1, "Sortie #1 passed"); err != nil || via != hosts.ViaComment {
		t.Fatalf("%v %v", via, err)
	}
	posted := 0
	for _, line := range h.Requests {
		if strings.HasSuffix(line, ".PostCommentForPullRequest") {
			posted++
		}
	}
	if posted != 1 || h.Comments() != 1 {
		t.Fatalf("one comment, posted once and then updated in place: posted %d, comments %d", posted, h.Comments())
	}
	if h.Seen("POST CodeCommit_20150413.UpdateComment") != 1 {
		t.Errorf("the second result edits the comment: %v", h.Requests)
	}
	if ok, err := s.ReportRun(ctx, "shop", r); ok || err != nil {
		t.Errorf("a step status is not sent: %v %v", ok, err)
	}
	// A Flight with no pull request: nothing is sent, and the page says so.
	if via, err := s.ReportResult(ctx, "shop", r, 0, ""); err != nil || via != hosts.ViaNone {
		t.Errorf("no pull request: %v %v", via, err)
	}
	v, err := s.View(ctx, "shop")
	n := v.Unsupported[forge.CapResults]
	if err != nil || n.Support != forge.Emulated || !strings.Contains(n.Workaround, "summary comment") || !strings.Contains(n.Workaround, "no pull request") {
		t.Fatalf("the page says so: %+v %v", n, err)
	}
	if len(h.Violations) > 0 {
		t.Errorf("violations %v", h.Violations)
	}
}
