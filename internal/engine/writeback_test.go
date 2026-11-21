package engine

import (
	"strings"
	"testing"
)

func TestWriteBackIsChecked(t *testing.T) {
	env := func(w *WriteBack) []Environment {
		return []Environment{{Name: "prod", Target: "cluster-a", Projects: []string{"*"}, WriteBack: w}}
	}
	if _, err := checkEnvironments(env(&WriteBack{Repo: "acme/gitops", Mode: "pull_request", Merge: "person", Method: "squash"})); err != nil {
		t.Fatal(err)
	}
	for want, w := range map[string]*WriteBack{
		"owner/name":                     {Repo: "gitops", Mode: "direct"},
		"write direct":                   {Repo: "acme/gitops", Mode: "push"},
		"merge is for pull_request mode": {Repo: "acme/gitops", Mode: "direct", Merge: "auto"},
		"write auto":                     {Repo: "acme/gitops", Mode: "pull_request", Merge: "never"},
		"write merge, squash or rebase":  {Repo: "acme/gitops", Mode: "pull_request", Method: "fast"},
	} {
		if _, err := checkEnvironments(env(w)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: want %q, got %v", w, want, err)
		}
	}
}
