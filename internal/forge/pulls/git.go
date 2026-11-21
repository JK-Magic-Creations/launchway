package pulls

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// gitRunner runs git in one repository (the server's cache of it, or a
// scratch repository that borrows its objects) through the one hardened runner.
type gitRunner struct {
	dir     string
	limits  gitexec.Limits
	scratch bool
	env     []string
}

func (g gitRunner) spec(args ...string) gitexec.Spec {
	return gitexec.Spec{GitDir: g.dir, Args: args, Timeout: 2 * time.Minute, Limits: g.limits, AllowAlternates: g.scratch, Env: g.env}
}

// out runs git and returns its trimmed output.
func (g gitRunner) out(ctx context.Context, args ...string) (string, error) {
	res, err := gitexec.Run(ctx, g.spec(args...))
	if err != nil {
		return "", gitErr(args, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// raw runs git and returns its output as it is.
func (g gitRunner) raw(ctx context.Context, stdin string, args ...string) (string, error) {
	sp := g.spec(args...)
	if stdin != "" {
		sp.Stdin = strings.NewReader(stdin)
	}
	res, err := gitexec.Run(ctx, sp)
	if err != nil {
		return res.Stdout, gitErr(args, err)
	}
	return res.Stdout, nil
}

func gitErr(args []string, err error) error {
	var ee *gitexec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(ee.Stderr+" "+ee.Stdout))
	}
	return fmt.Errorf("git %s: %w", args[0], err)
}

// exitCode is git's exit status when it ran and failed, else -1.
func exitCode(err error) int {
	var ee *gitexec.ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return -1
}

func (g gitRunner) isAncestor(ctx context.Context, anc, desc string) (bool, error) {
	_, err := gitexec.Run(ctx, g.spec("merge-base", "--is-ancestor", anc, desc))
	switch {
	case err == nil:
		return true, nil
	case exitCode(err) == 1:
		return false, nil
	}
	return false, gitErr([]string{"merge-base"}, err)
}

func (g gitRunner) mergeBase(ctx context.Context, a, b string) (string, error) {
	res, err := gitexec.Run(ctx, g.spec("merge-base", a, b))
	if err != nil {
		if exitCode(err) == 1 {
			return "", nil // no common ancestor
		}
		return "", gitErr([]string{"merge-base"}, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// counts says how many commits the target has that the head lacks (behind)
// and the head has that the target lacks (ahead).
func (g gitRunner) counts(ctx context.Context, base, head string) (behind, ahead int, err error) {
	out, err := g.out(ctx, "rev-list", "--left-right", "--count", base+"..."+head)
	if err != nil {
		return 0, 0, err
	}
	l, r, ok := strings.Cut(out, "\t")
	if !ok {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", out)
	}
	behind, _ = strconv.Atoi(strings.TrimSpace(l))
	ahead, _ = strconv.Atoi(strings.TrimSpace(r))
	return behind, ahead, nil
}

// trial merges head into base without touching any ref: git merge-tree
// --write-tree writes the merged tree (into the scratch repository) and says
// which files conflict. clean reports whether the merge had no conflicts.
func (g gitRunner) trial(ctx context.Context, base, head string, extra ...string) (tree string, conflicts []string, clean bool, err error) {
	args := append([]string{"merge-tree", "--write-tree", "--name-only", "--no-messages"}, extra...)
	args = append(args, base, head)
	res, err := gitexec.Run(ctx, g.spec(args...))
	if err != nil && exitCode(err) != 1 {
		return "", nil, false, gitErr(args, err)
	}
	out := res.Stdout
	if err != nil {
		var ee *gitexec.ExitError
		_ = errors.As(err, &ee)
		out = ee.Stdout
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || len(lines[0]) < 40 {
		return "", nil, false, fmt.Errorf("git merge-tree said %q", strings.TrimSpace(out))
	}
	tree = lines[0]
	if err == nil {
		return tree, nil, true, nil
	}
	seen := map[string]bool{}
	for _, l := range lines[1:] {
		if l == "" {
			break
		}
		if !seen[l] {
			seen[l] = true
			conflicts = append(conflicts, l)
		}
	}
	return tree, conflicts, false, nil
}
