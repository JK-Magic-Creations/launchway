package anygit

import (
	"context"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

const notChecked = "Not checked: a Git server cannot be asked what an account may do without trying it, and Launchway writes nothing to test. If the permission is missing the server refuses the push or the read with its own message when the feature is first used"

// Probe reads the credential, as a connection test can only do for a Git
// server: there is no repository to try until one is chosen, no list to read
// and no call that names the account. The credential is proved against a
// repository when one is attached (Repositories().Get runs git ls-remote), and
// the write features are listed with the permission each needs.
func (p *Provider) Probe(ctx context.Context) (forge.HostReport, error) {
	w, err := p.Identity().Whoami(ctx)
	if err != nil {
		return forge.HostReport{}, err
	}
	r := forge.HostReport{Account: w.Principal.Login, RateRemaining: -1}
	for _, c := range []struct{ feature, needs, detail string }{
		{"Choose a repository and run its Sorties (checkout, polling)", "read (clone and fetch) on the repository", "Not checked here: a Git server lists no repositories, so the credential is proved when you enter a repository's path (git ls-remote)"},
		{"Tags (pushed with Git)", "write (push) of tags on the repository", notChecked},
		{"File changes and write-back (pushed with Git)", "write (push) to the branch, or to a new branch, on the repository", notChecked},
	} {
		r.Checks = append(r.Checks, forge.PermissionCheck{Feature: c.feature, Needs: c.needs, OK: true, Detail: c.detail})
	}
	return r, nil
}
