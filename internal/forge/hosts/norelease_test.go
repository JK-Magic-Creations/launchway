package hosts_test

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/azuredevops"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/azuredevops/azuredevopsfake"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/bitbucket"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/bitbucket/bitbucketfake"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/gitlab/gitlabfake"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// "Cut a release" on a host with no release object: the tag is made there, the release stays in Launchway, and the page says so.
func TestCutAReleaseWhereTheHostHasNoReleaseObject(t *testing.T) {
	h := bitbucketfake.New()
	defer h.Close()
	defer bitbucket.UseTestAPI(h.API())()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	t.Setenv("LW_BB_TOKEN", bitbucketfake.Access)
	g, _ := egress.New([]string{"127.0.0.1"})
	s := hosts.New(eng.Database(), eng, g)
	ctx := t.Context()
	if _, err := s.Add(ctx, hosts.AddInput{Name: "bb", Kind: forge.KindBitbucket, Credential: forge.SecretRef{Env: "LW_BB_TOKEN"}}, "dana"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, "shop", "bb", "acme/app"); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("e", 40)
	link, err := s.CutRelease(ctx, "shop", commit, forge.ReleaseSpec{Tag: "v1.0.0", Title: "One", Notes: "n"}, "Release 1.0.0")
	if err != nil || link != "" {
		t.Fatalf("the tag is made and nothing else is asked: %q %v", link, err)
	}
	if _, err := s.PublishRelease(ctx, "shop", forge.ReleaseSpec{Tag: "v1.0.0"}); !errors.Is(err, hosts.ErrNoRelease) || !strings.Contains(err.Error(), "no release object") {
		t.Fatalf("publishing says so: %v", err)
	}
	if n := s.ReleaseNote(ctx, "shop"); !strings.Contains(n, "release page and its files stay in Launchway") {
		t.Fatalf("note %q", n)
	}
	for _, r := range h.Requests {
		if strings.Contains(r, "downloads") || strings.Contains(r, "release") {
			t.Errorf("a call for a release reached the host: %s", r)
		}
	}
	if len(h.Violations) > 0 {
		t.Errorf("violations %v", h.Violations)
	}
}

func TestAHostWithReleasesHasNoNote(t *testing.T) {
	h := gitlabfake.New()
	defer h.Close()
	eng, _ := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer eng.Close()
	t.Setenv("LW_GL_TOKEN", gitlabfake.Token)
	g, _ := egress.New([]string{"127.0.0.1"})
	s := hosts.New(eng.Database(), eng, g)
	if _, err := s.Add(t.Context(), hosts.AddInput{Name: "gl", Kind: forge.KindGitLab, BaseURL: h.BaseURL(), Credential: forge.SecretRef{Env: "LW_GL_TOKEN"}}, "dana"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(t.Context(), "shop", "gl", "acme/app"); err != nil {
		t.Fatal(err)
	}
	if n := s.ReleaseNote(t.Context(), "shop"); n != "" {
		t.Errorf("note %q", n)
	}
	if n := s.ReleaseNote(t.Context(), "unattached"); n != "" {
		t.Errorf("note %q", n)
	}
}

// Azure DevOps has no release object either: the same behaviour as Bitbucket's. The annotated tag is made on the
// host (an annotated tag, which the host's refs list peels to the commit), the release stays in Launchway.
func TestCutAReleaseOnAzureDevOpsMakesTheTagOnly(t *testing.T) {
	h := azuredevopsfake.NewServer()
	defer h.Close()
	defer azuredevops.UseTestAPI(h.API(), h.Collection())()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	t.Setenv("LW_ADO_TOKEN", azuredevopsfake.Token)
	g, _ := egress.New([]string{"127.0.0.1"})
	s := hosts.New(eng.Database(), eng, g)
	ctx := t.Context()
	if _, err := s.Add(ctx, hosts.AddInput{Name: "ado", Kind: forge.KindAzureDevOps, Credential: forge.SecretRef{Env: "LW_ADO_TOKEN"}}, "dana"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, "shop", "ado", h.RepoPath()); err != nil {
		t.Fatal(err)
	}
	commit, _ := h.Branch("main")
	link, err := s.CutRelease(ctx, "shop", commit, forge.ReleaseSpec{Tag: "v1.0.0", Title: "One", Notes: "n"}, "Release 1.0.0")
	if err != nil || link != "" {
		t.Fatalf("the tag is made and nothing else is asked: %q %v", link, err)
	}
	if _, err := s.PublishRelease(ctx, "shop", forge.ReleaseSpec{Tag: "v1.0.0"}); !errors.Is(err, hosts.ErrNoRelease) || !strings.Contains(err.Error(), "no release object") {
		t.Fatalf("publishing says so: %v", err)
	}
	if n := s.ReleaseNote(ctx, "shop"); !strings.Contains(n, "release page and its files stay in Launchway") {
		t.Fatalf("note %q", n)
	}
	tagged := false
	for _, r := range h.Requests {
		tagged = tagged || strings.HasSuffix(r, "/annotatedtags")
		if strings.Contains(strings.ToLower(r), "release") {
			t.Errorf("a call for a release reached the host: %s", r)
		}
	}
	if !tagged || len(h.Violations) > 0 {
		t.Errorf("tagged %v, violations %v", tagged, h.Violations)
	}
}
