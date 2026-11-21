package source

import (
	"context"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// PathRule is the outcome of a push or pull request that a path filter kept
// from starting a run. It counts as satisfied for a pull request's required
// checks.
const PathRule = engine.OutcomePathRule

// unknownChange is a change whose files cannot be told: the filters and the
// steps' changed tests then let everything run.
func unknownChange(why string) *pipeline.ChangeSet { return &pipeline.ChangeSet{Why: why} }

// changeBetween lists the files that differ between two commits, a renamed
// file under both of its paths.
func (p *project) changeBetween(ctx context.Context, from, to string) *pipeline.ChangeSet {
	out, err := gitOut(ctx, p.dir, "diff", "--name-only", "--no-renames", "-z", from, to)
	if err != nil {
		return unknownChange("the changed files could not be read: " + err.Error())
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	if len(files) > pipeline.MaxChangedFiles {
		return unknownChange(fmt.Sprintf("more than %d files changed", pipeline.MaxChangedFiles))
	}
	return &pipeline.ChangeSet{Known: true, Files: files}
}

// pushChange is what a push to a branch changed since the previous head. The
// first push of a branch, and a force-push that left the old head out of the
// new history, are not known.
func (p *project) pushChange(ctx context.Context, old, head string) *pipeline.ChangeSet {
	if old == "" {
		return unknownChange("this is the first push of the branch, so there is no earlier head to compare with")
	}
	if _, err := gitOut(ctx, p.dir, "merge-base", "--is-ancestor", old, head); err != nil {
		return unknownChange("the branch was force-pushed, so the earlier head is not part of its history")
	}
	return p.changeBetween(ctx, old, head)
}

// pullChange is what a pull request changed since it left its target branch:
// the files between the merge base and the head.
func (p *project) pullChange(ctx context.Context, target, head string) *pipeline.ChangeSet {
	if target == "" {
		target = p.DefaultBranch
	}
	base, err := gitOut(ctx, p.dir, "merge-base", "refs/heads/"+target, head)
	if err != nil || strings.TrimSpace(base) == "" {
		return unknownChange("the branch " + target + " shares no history with the pull request")
	}
	return p.changeBetween(ctx, strings.TrimSpace(base), head)
}

// pathRule applies a trigger's path filter to a change: the reason no run
// starts, or "".
func pathRule(f pipeline.PathFilter, cs *pipeline.ChangeSet) string {
	if run, why := f.Decide(cs); !run {
		return why
	}
	return ""
}

// pushPathRule is what a push's change is, and the reason its path filter
// keeps a run from starting ("" if it does not). Only pushes to branches that
// the pipeline's trigger matches are judged; tags always run.
func (p *project) pushPathRule(ctx context.Context, pl *pipeline.Pipeline, parseErr error, r Ref, old string) (*pipeline.ChangeSet, string) {
	if parseErr != nil || pl == nil || r.Kind != "branch" || !pl.On.MatchPush(branchOf(r), "") {
		return nil, ""
	}
	cs := p.pushChange(ctx, old, r.SHA)
	return cs, pathRule(pl.On.PushPaths(), cs)
}
