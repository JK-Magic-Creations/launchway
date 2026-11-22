package files

import (
	"context"
	"testing"
)

func TestPullTemplateIsTakenFromTheFirstPlaceThatHasOne(t *testing.T) {
	s, st := newService(t)
	ctx := context.Background()
	seed(t, s, st, "main", map[string]string{"README.md": "hi\n"})
	if got, err := s.PullTemplate(ctx, repoID, "main"); err != nil || got.Path != "" || got.Template != "" {
		t.Fatalf("none: %+v %v", got, err)
	}
	put(t, s, "PULL_REQUEST_TEMPLATE.md", "## top\r\nline\r\n", "", "top-level template")
	if got, err := s.PullTemplate(ctx, repoID, "main"); err != nil || got.Path != "PULL_REQUEST_TEMPLATE.md" || got.Template != "## top\nline\n" {
		t.Fatalf("top level: %+v %v", got, err)
	}
	put(t, s, ".launchway/pull_request_template.md", "## Why\n\n## Testing\n", "", "ours")
	got, err := s.PullTemplate(ctx, repoID, "main")
	if err != nil || got.Path != ".launchway/pull_request_template.md" || got.Template != "## Why\n\n## Testing\n" || got.Commit == "" {
		t.Fatalf("ours first: %+v %v", got, err)
	}
	if _, err := s.PullTemplate(ctx, repoID, "nope"); err != nil {
		t.Fatalf("a ref that is not there is a repository with no template: %v", err)
	}
}
