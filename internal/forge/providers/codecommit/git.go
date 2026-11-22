package codecommit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/awssig"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// Git over HTTPS to CodeCommit is signed, not given a token. The credential
// helper of the AWS CLI ("aws codecommit credential-helper get", whose page says it
// returns "a username SigV4 credential pair based on protocol, host and path"
// https://docs.aws.amazon.com/cli/latest/reference/codecommit/credential-helper/get.html)
// is open source (https://github.com/aws/aws-cli, awscli/customizations/codecommit.py)
// and Launchway does in process what it does, because the server image has no
// shell and no helper binary:
//
//   - the user name is the access key id, followed by "%" and the session token
//     when the credentials are temporary;
//   - the password is the timestamp (UTC, YYYYMMDDTHHMMSS) with a literal "Z",
//     then the signature;
//   - the request that is signed has the method GIT, the repository's path
//     (/v1/repos/<name>), no query, and the one header host (the host name
//     without a port), so its canonical form is
//     "GIT\n<path>\n\nhost:<host>\n\nhost\n" with no payload hash;
//   - the string to sign is "AWS4-HMAC-SHA256", the timestamp (without the Z),
//     the credential scope <date>/<region>/codecommit/aws4_request and the hex
//     SHA-256 of the canonical request, as every Signature Version 4 string to
//     sign (botocore's SigV4Auth.string_to_sign, which the helper calls); the
//     key is derived from the secret key as for any request.
//
// The signature covers the path and the time, so it is made for each clone or
// fetch (Access.Credential) and is good for a few minutes; the product already
// hands git a Credential as an Authorization header from the environment of the
// one git process, which carries a user name with a "%" and a long password as
// it is.

// gitTimestampFormat is the timestamp of the password, without the Z.
const gitTimestampFormat = "20060102T150405"

// GitCredential makes the user name and password of one Git request.
func GitCredential(c awssig.Credentials, region, host, path string, now time.Time) (user, password string) {
	ts := now.UTC().Format(gitTimestampFormat)
	canonical := "GIT\n" + path + "\n\nhost:" + host + "\n\nhost\n"
	toSign := awssig.Algorithm + "\n" + ts + "\n" + awssig.Scope(ts[:8], region, "codecommit") + "\n" + awssig.HexSHA256([]byte(canonical))
	sig := awssig.Signing(c.SecretAccessKey, ts[:8], region, "codecommit", toSign)
	user = c.AccessKeyID
	if c.SessionToken != "" {
		user += "%" + c.SessionToken
	}
	return user, ts + "Z" + sig
}

// Credential mints the credential for one clone, fetch or push of the
// repository: read and write are the same signature (what the caller may do is
// decided by its IAM permissions GitPull and GitPush on the repository).
func (a access) Credential(ctx context.Context, repo forge.Repository, _ bool) (forge.Credential, error) {
	u, err := a.p.gitURL(repo)
	if err != nil {
		return forge.Credential{}, err
	}
	s, err := a.p.session(ctx)
	if err != nil {
		return forge.Credential{}, err
	}
	user, pass := GitCredential(s.creds, a.p.ep.Region, u.Hostname(), u.Path, a.p.now())
	return forge.Credential{Username: user, Secret: pass}, nil
}

// git runs the product's own git (gitexec: no shell, a closed environment) for
// the repository, handing it the credential as an HTTP header in the
// environment of that one process, for the origin of the clone address only,
// with no redirect followed and git pinned to the addresses the egress guard
// checked. dir is the working directory.
func (p *Provider) git(ctx context.Context, repo forge.Repository, dir string, write bool, args ...string) (string, error) {
	u, err := p.gitURL(repo)
	if err != nil {
		return "", err
	}
	pin, err := p.g.Pin(ctx, u.String())
	if err != nil {
		return "", err
	}
	cred, err := p.Access().Credential(ctx, repo, write)
	if err != nil {
		return "", err
	}
	basic := base64.StdEncoding.EncodeToString([]byte(cred.Username + ":" + cred.Secret))
	origin := u.Scheme + "://" + u.Host
	kv := [][2]string{{"http.followRedirects", "false"}, {"protocol.allow", "never"}, {"protocol.https.allow", "always"}, {"protocol.http.allow", "always"},
		{"http." + origin + "/.extraHeader", "Authorization: Basic " + basic}}
	for _, r := range pin {
		kv = append(kv, [2]string{"http.curloptResolve", r})
	}
	env := []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(len(kv)), "GIT_TERMINAL_PROMPT=0", "GIT_HTTP_LOW_SPEED_LIMIT=1000", "GIT_HTTP_LOW_SPEED_TIME=120",
		"GIT_COMMITTER_NAME=Launchway", "GIT_COMMITTER_EMAIL=launchway@users.noreply.invalid"}
	for i, e := range kv {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, e[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, e[1]))
	}
	res, err := gitexec.Run(ctx, gitexec.Spec{Dir: dir, Args: args, Env: filterEnv(env), Inherit: gitexec.Remote, Untrusted: true,
		Timeout: 10 * time.Minute, IdleTimeout: 2 * time.Minute, Secrets: []string{cred.Secret, basic, cred.Username}})
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return res.Stdout, fmt.Errorf("git %s: %s", args[0], oneLine(redact.URLs(ee.Stderr+" "+ee.Stdout)))
		}
		return res.Stdout, fmt.Errorf("git %s: %w", args[0], err)
	}
	return res.Stdout, nil
}

// filterEnv drops what gitexec would refuse (GIT_TERMINAL_PROMPT is its own business).
func filterEnv(env []string) []string {
	var out []string
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		if gitexec.AllowedEnv(name) {
			out = append(out, e)
		}
	}
	return out
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

// scratch makes the empty directory a tag is made in.
func scratch() (string, error) { return os.MkdirTemp("", "launchway-codecommit-") }
