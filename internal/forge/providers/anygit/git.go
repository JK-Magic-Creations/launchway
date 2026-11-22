package anygit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// credential reads the connection's credential from its reference at the moment
// of a call; it is never stored.
func (p *Provider) credential() (cred, error) {
	if p.conn.Credential.IsZero() {
		return cred{}, fmt.Errorf("the connection %s has no credential", p.conn.Name)
	}
	raw, err := p.conn.Credential.Resolve()
	if err != nil {
		return cred{}, err
	}
	c, err := parseCred(raw)
	if err != nil {
		return cred{}, fmt.Errorf("the connection %s: %w", p.conn.Name, err)
	}
	return c, nil
}

// git runs the product's own git (gitexec: no shell, a closed environment) in
// dir, handing it the credential as an HTTP header in the environment of that
// one process, for the origin of the connection only, with no redirect followed
// and the addresses pinned to those the egress guard checked.
func (p *Provider) git(ctx context.Context, path, dir string, args ...string) (string, error) {
	u, err := p.gitURL(path)
	if err != nil {
		return "", err
	}
	var pin []string
	if p.g != nil {
		if pin, err = p.g.Pin(ctx, u.String()); err != nil {
			return "", err
		}
	}
	c, err := p.credential()
	if err != nil {
		return "", err
	}
	basic := base64.StdEncoding.EncodeToString([]byte(c.user + ":" + c.secret))
	origin := u.Scheme + "://" + u.Host
	kv := [][2]string{{"http.followRedirects", "false"}, {"protocol.allow", "never"}, {"protocol.https.allow", "always"},
		{"http." + origin + "/.extraHeader", "Authorization: Basic " + basic}}
	if u.Scheme == "http" { // a loopback server only (EndpointsOf)
		kv = append(kv, [2]string{"protocol.http.allow", "always"})
	}
	for _, r := range pin {
		kv = append(kv, [2]string{"http.curloptResolve", r})
	}
	env := []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(len(kv)), "GIT_HTTP_LOW_SPEED_LIMIT=1000", "GIT_HTTP_LOW_SPEED_TIME=120",
		"GIT_AUTHOR_NAME=Launchway", "GIT_AUTHOR_EMAIL=launchway@users.noreply.invalid",
		"GIT_COMMITTER_NAME=Launchway", "GIT_COMMITTER_EMAIL=launchway@users.noreply.invalid"}
	for i, e := range kv {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, e[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, e[1]))
	}
	res, err := gitexec.Run(ctx, gitexec.Spec{Dir: dir, Args: args, Env: env, Inherit: gitexec.Remote, Untrusted: true,
		Timeout: 10 * time.Minute, IdleTimeout: 2 * time.Minute, Secrets: []string{c.secret, basic}})
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return res.Stdout, serverError(args[0], ee.Stderr+" "+ee.Stdout)
		}
		return res.Stdout, fmt.Errorf("git %s: %w", args[0], err)
	}
	return res.Stdout, nil
}

// serverError is git's own message, on one line, with addresses made safe. A
// "not found" answer is wrapped as forge.ErrNotFound.
func serverError(verb, stderr string) error {
	msg := oneLine(redact.URLs(stderr))
	low := strings.ToLower(msg)
	if strings.Contains(low, "repository not found") || (strings.Contains(low, "repository '") && strings.Contains(low, "' not found")) || strings.Contains(low, "returned error: 404") || strings.Contains(low, "does not appear to be a git repository") || strings.Contains(low, "couldn't find remote ref") {
		return fmt.Errorf("%w: git %s: %s", forge.ErrNotFound, verb, msg)
	}
	switch {
	case strings.Contains(low, "could not read username") || strings.Contains(low, "authentication failed") || strings.Contains(low, "returned error: 401"):
		return fmt.Errorf("git %s: the host answered 401: the server did not accept the connection's credential (%s)", verb, msg)
	case strings.Contains(low, "returned error: 403"):
		return fmt.Errorf("git %s: the host answered 403: the account may not do this on the repository (%s)", verb, msg)
	}
	return fmt.Errorf("git %s: %s", verb, msg)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

// scratch makes the empty directory a tag or a file change is made in.
func scratch() (string, error) { return os.MkdirTemp("", "launchway-anygit-") }

// lsRemote lists the refs the server shows for the patterns (all refs when none)
// and, when symref is set, the branch HEAD points to ("" when the server shows none).
func (p *Provider) lsRemote(ctx context.Context, path, dir string, symref bool, patterns ...string) (refs []forge.Ref, head string, err error) {
	u, err := p.gitURL(path)
	if err != nil {
		return nil, "", err
	}
	args := []string{"ls-remote"}
	if symref {
		args = append(args, "--symref")
	}
	args = append(args, u.String())
	out, err := p.git(ctx, path, dir, append(args, patterns...)...)
	if err != nil {
		return nil, "", err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "ref: "); ok {
			if target, _, ok := strings.Cut(rest, "\t"); ok {
				head = target
			}
			continue
		}
		id, ref, ok := strings.Cut(line, "\t")
		if ok && len(id) == 40 {
			refs = append(refs, forge.Ref{Name: ref, Commit: id})
		}
	}
	return refs, head, nil
}
