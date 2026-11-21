package githttp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// A deploy token is the pipeline identity of one deploy step that writes to a
// hosted GitOps repository. It is not stored anywhere and not kept by anyone:
// the server makes it when it hands the deploy step to a deploy runner, as a
// MAC over the repository, the project, the deployment, the branch to write
// to, the mode and an expiry (at most an hour), with a key every server
// shares. It can push to that repository and branch (or, in pull_request
// mode, to the one branch the deployment names) and nothing else: not read
// another repository, not call the API. Whether a push is admitted, and
// which files it may change, is decided by the repository's pipeline writer
// rules (internal/forge/refrules) when the push arrives.

// DeployTokenPrefix starts every deploy token.
const DeployTokenPrefix = "lwd_"

// MaxDeployTokenTTL is the longest a deploy token may be made to last.
const MaxDeployTokenTTL = time.Hour

// Deploy token modes.
const (
	ModeDirect      = "direct"
	ModePullRequest = "pull_request"
)

// DeployClaim is what a valid deploy token says.
type DeployClaim struct {
	Repo       string // the repository's ID
	Project    string
	Deployment string
	Branch     string // the branch the change is for
	Mode       string // direct or pull_request
	// Merge (auto or person) and Method (merge, squash, rebase or "") are
	// the environment's policy for a pull_request deployment: the runner
	// cannot choose them, because they travel in the token.
	Merge, Method string
	// Expires ends the right to push (at most an hour). Until ends the right
	// to ask what became of the pull request, which may wait for a person
	// longer: the deploy step's own deadline.
	Expires, Until time.Time
}

// MaxDeployTokenUntil is the longest the status of a pull request may be asked after the token was made.
const MaxDeployTokenUntil = 7 * 24 * time.Hour

// WorkBranch is the branch a pull_request deployment pushes its change to.
func (c DeployClaim) WorkBranch() string { return "launchway/deploy/" + c.Deployment }

func (t RunTokens) deployMAC(payload string) string {
	m := hmac.New(sha256.New, t.Key)
	m.Write([]byte("launchway git deploy token v1\x00" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:24])
}

// MintForDeploy makes a deploy token for the claim, to last ttl.
func (t RunTokens) MintForDeploy(c DeployClaim, ttl, until time.Duration) (string, error) {
	switch {
	case len(t.Key) < 16:
		return "", errors.New("no key to sign deploy tokens with")
	case ttl <= 0 || ttl > MaxDeployTokenTTL:
		return "", fmt.Errorf("a deploy token lasts between 1 second and %s", MaxDeployTokenTTL)
	case until < ttl || until > MaxDeployTokenUntil:
		return "", fmt.Errorf("a deploy token's status window is as long as the token and at most %s", MaxDeployTokenUntil)
	case c.Mode != ModeDirect && c.Mode != ModePullRequest:
		return "", errors.New("a deploy token is for mode direct or pull_request")
	case c.Repo == "" || c.Project == "" || c.Deployment == "" || c.Branch == "":
		return "", errors.New("a deploy token names a repository, project, deployment and branch")
	}
	for _, f := range []string{c.Repo, c.Project, c.Deployment, c.Branch, c.Merge, c.Method} {
		if strings.ContainsAny(f, "\x1f\x00") {
			return "", errors.New("a deploy token's fields cannot contain control characters")
		}
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(strings.Join([]string{c.Repo, c.Project, c.Deployment, c.Branch, c.Mode, c.Merge, c.Method,
		strconv.FormatInt(t.now().Add(ttl).Unix(), 36), strconv.FormatInt(t.now().Add(until).Unix(), 36)}, "\x1f")))
	return DeployTokenPrefix + payload + "." + t.deployMAC(payload), nil
}

// CanPush reports whether the token still gives the right to push.
func (t RunTokens) CanPush(c DeployClaim) bool { return t.now().Before(c.Expires) }

// VerifyDeploy checks a deploy token's form, MAC and status window (not the
// right to push: see CanPush).
func (t RunTokens) VerifyDeploy(tok string) (DeployClaim, error) {
	rest, ok := strings.CutPrefix(tok, DeployTokenPrefix)
	if !ok || len(t.Key) < 16 {
		return DeployClaim{}, ErrBadRunToken
	}
	payload, mac, ok := strings.Cut(rest, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(t.deployMAC(payload))) {
		return DeployClaim{}, ErrBadRunToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return DeployClaim{}, ErrBadRunToken
	}
	p := strings.Split(string(raw), "\x1f")
	if len(p) != 9 {
		return DeployClaim{}, ErrBadRunToken
	}
	exp, err := strconv.ParseInt(p[7], 36, 64)
	until, err2 := strconv.ParseInt(p[8], 36, 64)
	if err != nil || err2 != nil || !t.now().Before(time.Unix(until, 0)) {
		return DeployClaim{}, ErrBadRunToken
	}
	return DeployClaim{Repo: p[0], Project: p[1], Deployment: p[2], Branch: p[3], Mode: p[4], Merge: p[5], Method: p[6], Expires: time.Unix(exp, 0), Until: time.Unix(until, 0)}, nil
}

// ChangedFiles is what a push policy may ask for: the files a push changes
// (storage.ChangedFiles), which the storage gives it through its context.
type ChangedFiles = storage.ChangedFiles

// ChangedFilesFrom returns the function a push policy uses to list the files
// the push changes, or nil outside a push.
func ChangedFilesFrom(ctx context.Context) ChangedFiles { return storage.ChangedFilesFrom(ctx) }
