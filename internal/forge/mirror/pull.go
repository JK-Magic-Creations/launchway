package mirror

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// redactText removes anything that looks like a credential in a URL from
// text that is kept or shown, and any Authorization header git echoed.
func redactText(s string) string {
	s = redact.URLs(s)
	return authLine.ReplaceAllString(s, "${1}***")
}

var authLine = regexp.MustCompile(`(?i)(authorization:\s*(?:basic|bearer|token)\s+)\S+`)

// remote is how git is told to talk to an address: the settings are given
// through the environment of one command (never on a command line or in a
// file), and the secrets are named so they are removed from anything git says.
type remote struct {
	env     []string
	secrets []string
}

// remoteFor builds the environment for talking to a mirror's address.
func (s *Service) remoteFor(m Mirror) (remote, error) {
	kv := [][2]string{
		// An address that redirects somewhere else is not followed: the
		// egress guard approved this address, not wherever it points.
		{"http.followRedirects", "false"},
		{"protocol.allow", "never"},
		{"protocol.https.allow", "always"},
		{"protocol.http.allow", "always"},
		{"http.sslVerify", "true"},
		// Old repositories carry harmless irregularities that a strict check
		// would refuse; a mirror must copy what is there.
		{"fsck.zeroPaddedFilemode", "ignore"},
		{"fsck.badTimezone", "ignore"},
		{"fsck.missingEmail", "ignore"},
		{"fsck.badName", "ignore"},
		{"fsck.missingSpaceBeforeDate", "ignore"},
		{"fsck.missingSpaceBeforeEmail", "ignore"},
		{"fsck.badDate", "ignore"},
		{"fsck.badDateOverflow", "ignore"},
		{"fsck.nullSha1", "ignore"},
		{"fsck.duplicateEntries", "ignore"},
		{"fsck.fullPathname", "ignore"},
		{"fsck.hasDot", "ignore"},
		{"fsck.hasDotdot", "ignore"},
		{"fsck.hasDotgit", "ignore"},
		{"fsck.emptyName", "ignore"},
		{"fsck.treeNotSorted", "ignore"},
	}
	var r remote
	if !m.Credential.IsZero() {
		tok, err := m.Credential.Resolve()
		if err != nil {
			return r, err
		}
		user := m.Username
		if user == "" {
			user = "launchway"
		}
		b := base64.StdEncoding.EncodeToString([]byte(user + ":" + tok))
		kv = append(kv, [2]string{"http.extraHeader", "Authorization: Basic " + b})
		r.secrets = []string{tok, b}
	}
	r.env = append(r.env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(kv)), "GIT_HTTP_LOW_SPEED_LIMIT=1000", "GIT_HTTP_LOW_SPEED_TIME=120")
	for i, p := range kv {
		r.env = append(r.env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, p[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, p[1]))
	}
	return r, nil
}

// git runs git with the remote's environment and returns what it wrote.
func (s *Service) git(ctx context.Context, r remote, gitDir string, timeout time.Duration, args ...string) (string, error) {
	res, err := gitexec.Run(ctx, gitexec.Spec{GitDir: gitDir, Args: args, Env: r.env, Inherit: gitexec.Remote, Untrusted: true, AllowAlternates: true,
		Timeout: timeout, IdleTimeout: s.o.IdleTimeout, Limits: s.o.Limits, Secrets: r.secrets})
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return res.Stdout, errors.New(explain(args[0], redactText(strings.TrimSpace(ee.Stderr+" "+ee.Stdout))))
		}
		return res.Stdout, fmt.Errorf("git %s: %w", args[0], err)
	}
	return res.Stdout, nil
}

// explain puts the common failures of talking to another server in words.
func explain(cmd, out string) string {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "could not resolve host"):
		return "the other server's name cannot be resolved from this server: " + firstLine(out)
	case strings.Contains(low, "returned error: 401"), strings.Contains(low, "authentication failed"), strings.Contains(low, "terminal prompts disabled"), strings.Contains(low, "could not read username"):
		return "the other server asked for credentials this mirror does not have or that it refused (401): check the credential reference and that its token may read the repository"
	case strings.Contains(low, "returned error: 403"):
		return "the other server refused this mirror's credentials (403): the token lacks the permission it needs (read for a pull mirror, write for a push mirror)"
	case strings.Contains(low, "returned error: 404"), strings.Contains(low, "not found"):
		return "the other server has no such repository, or the credential cannot see it (404): " + firstLine(out)
	case strings.Contains(low, "returned error: 30"):
		return "the address redirects to another address, which Launchway does not follow; use the address the redirect leads to: " + firstLine(out)
	case strings.Contains(low, "protected branch"), strings.Contains(low, "pre-receive hook declined"), strings.Contains(low, "remote rejected"):
		return "the other server rejected the push (a protected branch, or a rule of its own): " + firstLine(out)
	}
	if out == "" {
		return "git " + cmd + " failed"
	}
	return "git " + cmd + ": " + firstLine(out)
}

func firstLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	var keep []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
		if len(keep) == 3 {
			break
		}
	}
	return strings.Join(keep, " | ")
}

// Refspecs of a mirror: the managed namespaces, forced.
var fetchSpecs = []string{"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*", "+refs/notes/*:refs/notes/*"}

// runPull brings a pull mirror's repository to what its address has.
func (s *Service) runPull(ctx context.Context, m Mirror) (int, error) {
	repo, err := s.st.Repository(ctx, m.RepoID)
	if err != nil {
		return 0, err
	}
	// Only a repository that is a mirror is ever written by a sync: whatever
	// a sync is asked, a repository with content of its own is safe.
	if !repo.Mirror || store.InTrash(repo) {
		return 0, errNotMirror
	}
	if err := s.o.Guard.Check(ctx, m.URL); err != nil {
		return 0, err
	}
	r, err := s.remoteFor(m)
	if err != nil {
		return 0, err
	}
	var head string
	res, err := s.sto.Converge(ctx, repo.ID, storage.ConvergeOptions{Prefixes: Managed, Prune: m.Prune,
		Fetch: func(ctx context.Context, scratch string) error {
			if out, err := s.git(ctx, r, scratch, 5*time.Minute, "ls-remote", "--symref", m.URL, "HEAD"); err == nil {
				head = parseHead(out)
			} else {
				return err
			}
			args := append([]string{"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--quiet", "--", m.URL}, fetchSpecs...)
			_, err := s.git(ctx, r, scratch, s.o.MaxDuration, args...)
			return err
		}})
	if err != nil {
		if errors.Is(err, storage.ErrEmptySource) {
			return 0, fmt.Errorf("%w. If the other server really was emptied, stop mirroring and make this repository the primary instead", err)
		}
		var stale *storage.ErrStale
		if errors.As(err, &stale) {
			return 0, errors.New("the repository changed while the mirror was running; it will run again")
		}
		return 0, err
	}
	if head != "" && head != repo.DefaultBranch {
		if gitexec.ValidBranch(head) == nil {
			_ = s.st.SetDefaultBranchOf(ctx, repo.ID, head)
		}
	}
	if res.Changed() {
		s.changed(ctx, repo.ID)
		s.event(ctx, repo.ID, string(forge.EventRefUpdated), fmt.Sprintf("mirror sync: %d created, %d updated, %d deleted", res.Created, res.Updated, res.Deleted), "mirrored")
	}
	return res.Refs, nil
}

// parseHead reads the branch HEAD points at from `ls-remote --symref`.
func parseHead(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(l, "ref: refs/heads/"); ok {
			b, _, _ := strings.Cut(rest, "\t")
			return strings.TrimSpace(b)
		}
	}
	return ""
}

// changed tells whatever follows a repository's changes that it changed.
// Push mirrors hook in here (push.go); until then it does nothing.
func (s *Service) changed(ctx context.Context, repoID string) {
	if s.onChange != nil {
		s.onChange(ctx, repoID)
	}
}
