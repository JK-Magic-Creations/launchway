package forks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge/signing"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// runner runs git in a scratch repository that borrows the objects of the
// repositories a transfer reads. There is no shell and no transport.
type runner struct {
	dir    string
	limits gitexec.Limits
	env    []string
	// sign, when set, signs every commit the runner makes with commit-tree with the installation's key; a
	// failure leaves the commit unsigned and is noted (the same rule as the pull-request service's).
	sign func(ctx context.Context, payload []byte) ([]byte, error)
	note func(err error)
}

func (g runner) spec(args ...string) gitexec.Spec {
	return gitexec.Spec{GitDir: g.dir, Args: args, Timeout: 2 * time.Minute, Limits: g.limits, AllowAlternates: true, Env: g.env, Untrusted: true}
}

func gitErr(args []string, err error) error {
	var ee *gitexec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(ee.Stderr+" "+ee.Stdout))
	}
	return fmt.Errorf("git %s: %w", args[0], err)
}

func (g runner) out(ctx context.Context, stdin string, args ...string) (string, error) {
	sp := g.spec(args...)
	if stdin != "" {
		sp.Stdin = strings.NewReader(stdin)
	}
	res, err := gitexec.Run(ctx, sp)
	if err != nil {
		return "", gitErr(args, err)
	}
	sha := strings.TrimSpace(res.Stdout)
	if g.sign != nil && len(args) > 0 && args[0] == "commit-tree" {
		return g.signCommit(ctx, sha), nil
	}
	return sha, nil
}

// signCommit replaces a commit just made with the same commit carrying the installation's signature. When it
// cannot sign it returns the commit as it is and says why through note.
func (g runner) signCommit(ctx context.Context, sha string) string {
	fail := func(err error) string {
		if g.note != nil {
			g.note(err)
		}
		return sha
	}
	raw, err := g.raw(ctx, "cat-file", "commit", sha)
	if err != nil {
		return fail(err)
	}
	sig, err := g.sign(ctx, []byte(raw))
	if err != nil {
		return fail(err)
	}
	signed, err := signing.AddCommitSignature([]byte(raw), sig)
	if err != nil {
		return fail(err)
	}
	out, err := g.out(ctx, string(signed), "hash-object", "-t", "commit", "-w", "--stdin")
	if err != nil {
		return fail(err)
	}
	if g.note != nil {
		g.note(nil)
	}
	return out
}

// raw is out without trimming, for output that is data.
func (g runner) raw(ctx context.Context, args ...string) (string, error) {
	res, err := gitexec.Run(ctx, g.spec(args...))
	if err != nil {
		return "", gitErr(args, err)
	}
	return res.Stdout, nil
}

func exitCode(err error) int {
	var ee *gitexec.ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return -1
}

// counts says how many commits the head has that the base lacks (ahead) and
// the base has that the head lacks (behind).
func (g runner) counts(ctx context.Context, base, head string) (ahead, behind int, err error) {
	out, err := g.out(ctx, "", "rev-list", "--left-right", "--count", base+"..."+head)
	if err != nil {
		return 0, 0, err
	}
	l, r, ok := strings.Cut(out, "\t")
	if !ok {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", out)
	}
	behind, _ = strconv.Atoi(strings.TrimSpace(l))
	ahead, _ = strconv.Atoi(strings.TrimSpace(r))
	return ahead, behind, nil
}

// trial merges head into base without touching a ref, and says which files
// conflict.
func (g runner) trial(ctx context.Context, base, head string) (tree string, conflicts []string, clean bool, err error) {
	args := []string{"merge-tree", "--write-tree", "--name-only", "--no-messages", base, head}
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

// ident is the environment of a commit the server makes for a person.
func ident(name string, now time.Time) []string {
	date := fmt.Sprintf("%d +0000", now.Unix())
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + name + "@users.noreply.launchway.invalid", "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=Launchway", "GIT_COMMITTER_EMAIL=noreply@launchway.invalid", "GIT_COMMITTER_DATE=" + date,
	}
}
