package propose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jk-magic-creations/launchway/internal/depupdate/plan"
)

// Applied is the result of making one proposal's change.
type Applied struct {
	Branch  string
	Commit  string
	Changed []string
	Log     []string
}

// Apply makes a proposal's change in the workspace and pushes it to the
// updater's own branch. The base must already be cloned. Order: copy the
// tree without Git metadata; write the manifest edits; run the toolchain
// steps in containers on the copy; take back only the files that were meant
// to change (anything else is a refusal); commit and push.
func Apply(ctx context.Context, ws *Workspace, pr plan.Proposal, work Work, runner StepRunner, prefix string, scratch string) (Applied, error) {
	a := Applied{Branch: pr.Branch}
	if err := ws.Reset(ctx); err != nil {
		return a, err
	}
	tree, err := os.MkdirTemp(scratch, "tree-")
	if err != nil {
		return a, err
	}
	defer os.RemoveAll(tree)
	if err := copyTree(ws.Dir, tree); err != nil {
		return a, err
	}
	for p, text := range work.Edits {
		full := filepath.Join(tree, filepath.FromSlash(p))
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			return a, err
		}
	}
	for _, st := range work.Steps {
		if err := runner.Run(ctx, st, tree, func(l string) { a.Log = append(a.Log, l) }); err != nil {
			return a, err
		}
	}
	changes, err := diffTrees(ws.Dir, tree)
	if err != nil {
		return a, err
	}
	allowed := map[string]bool{}
	for _, f := range work.Files {
		allowed[f] = true
	}
	for p := range work.Edits {
		allowed[p] = true
	}
	for _, st := range work.Steps {
		for _, f := range st.Allowed {
			allowed[f] = true
		}
	}
	for _, p := range paths(changes) {
		if !allowed[p] {
			return a, fmt.Errorf("the toolchain step changed %s, which is not a manifest or lock file of this update; nothing was committed", p)
		}
	}
	if len(changes) == 0 {
		return a, fmt.Errorf("the update changed no file; nothing to propose")
	}
	a.Changed = paths(changes)
	if a.Commit, err = ws.Commit(ctx, pr.Branch, CommitMessage(pr), changes); err != nil {
		return a, err
	}
	return a, ws.Push(ctx, pr.Branch, prefix)
}
