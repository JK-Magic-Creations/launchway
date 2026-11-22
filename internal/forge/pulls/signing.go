package pulls

import (
	"context"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge/signing"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// maxVerifiedCommits is the most commits one merge checks for signatures.
const maxVerifiedCommits = 500

func (s *Service) noteSignError(err error) {
	if err == nil {
		s.signErr.Store("")
		return
	}
	s.Log.Warn("a commit Launchway made could not be signed with the installation's key", "err", err)
	s.signErr.Store(err.Error())
}

// signCommit replaces a commit the runner just made with the same commit
// carrying the installation's signature, and returns its name. When it cannot
// sign it returns the commit as it is, and says why through note.
func (g gitRunner) signCommit(ctx context.Context, sha string) string {
	fail := func(err error) string {
		if g.note != nil {
			g.note(err)
		}
		return sha
	}
	raw, err := g.raw(ctx, "", "cat-file", "commit", sha)
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
	out, err := g.raw(ctx, string(signed), "hash-object", "-t", "commit", "-w", "--stdin")
	if err != nil {
		return fail(err)
	}
	if g.note != nil {
		g.note(nil)
	}
	return strings.TrimSpace(out)
}

// requireVerified enforces the rule that a merge brings only verified
// commits: the commits of the pull request (unless it is squashed, when they
// do not land) and the commit the merge makes, which Launchway signs itself.
// base and head are the tips; made is the commit the merge made, "" before it
// is made.
func (s *Service) requireVerified(ctx context.Context, g gitRunner, base, head, made, method string) error {
	if s.Verify == nil {
		return conflict("this branch requires verified commits, but verified commits are off on this server, so nothing can be merged into it")
	}
	var objs []storage.PushObject
	read := func(sha string) error {
		raw, err := g.raw(ctx, "", "cat-file", "commit", sha)
		if err != nil {
			return err
		}
		objs = append(objs, storage.PushObject{SHA: sha, Kind: "commit", Raw: []byte(raw)})
		return nil
	}
	if made != "" {
		if err := read(made); err != nil {
			return err
		}
	} else if method != MethodSquash {
		list, err := g.raw(ctx, "", "rev-list", fmt.Sprintf("--max-count=%d", maxVerifiedCommits+1), head, "^"+base)
		if err != nil {
			return err
		}
		shas := strings.Fields(list)
		if len(shas) > maxVerifiedCommits {
			return conflict("this pull request has more than %d commits; verified commits cannot be checked for so many, squash it instead", maxVerifiedCommits)
		}
		for _, c := range shas {
			if err := read(c); err != nil {
				return err
			}
		}
	}
	var bad []string
	for _, r := range s.Verify(ctx, objs) {
		if !r.OK() {
			bad = append(bad, fmt.Sprintf("%s: %s", r.Object[:min(10, len(r.Object))], strings.ToLower(string(r.State))))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	what := "these commits are not verified"
	hint := " Sign them with a key registered under Account, Signing keys (git commit -S with gpg.format ssh), or squash the pull request: Launchway signs the squash commit itself."
	if made != "" {
		what = "the commit Launchway made is not verified"
		hint = ""
		if e, _ := s.signErr.Load().(string); e != "" {
			hint = " Launchway could not sign it: " + e
		}
	}
	if len(bad) > 5 {
		bad = append(bad[:5], fmt.Sprintf("and %d more", len(bad)-5))
	}
	return conflict("%s requires verified commits and %s (%s).%s", "this branch", what, strings.Join(bad, ", "), hint)
}
