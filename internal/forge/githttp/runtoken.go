package githttp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A run token lets one run read one repository for a short time and nothing
// else: it is a MAC over the repository, the run and an expiry, made with a key
// every server shares, so no table of tokens is needed and a token cannot be
// changed. It is never able to push. (The engine mints one at lease time; the
// key is the server's, kept in the database.)

// RunTokenPrefix starts every run token.
const RunTokenPrefix = "lwr_"

// MaxRunTokenTTL is the longest a run token may be made to last.
const MaxRunTokenTTL = 24 * time.Hour

// RunTokens makes and checks run tokens with a key.
type RunTokens struct {
	Key []byte
	Now func() time.Time
}

// RunClaim is what a valid run token says.
type RunClaim struct {
	Repo, Run string
	Expires   time.Time
}

func (t RunTokens) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t RunTokens) mac(payload string) string {
	m := hmac.New(sha256.New, t.Key)
	m.Write([]byte("launchway git run token v1\x00" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:24])
}

// MintForRun makes a read-only token for a repository, to last ttl.
func (t RunTokens) MintForRun(repoID, run string, ttl time.Duration) (string, error) {
	if len(t.Key) < 16 {
		return "", errors.New("no key to sign run tokens with")
	}
	if ttl <= 0 || ttl > MaxRunTokenTTL {
		return "", fmt.Errorf("a run token lasts between 1 second and %s", MaxRunTokenTTL)
	}
	if strings.ContainsAny(repoID+run, ".|") {
		return "", errors.New("a repository or run ID cannot contain '.' or '|'")
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(repoID + "|" + run + "|" + strconv.FormatInt(t.now().Add(ttl).Unix(), 36)))
	return RunTokenPrefix + payload + "." + t.mac(payload), nil
}

// Mint makes a token with no run (the credential Access hands out). Only
// read credentials exist.
func (t RunTokens) Mint(repoID string, write bool, ttl time.Duration) (string, error) {
	if write {
		return "", errors.New("run tokens are read-only")
	}
	return t.MintForRun(repoID, "", ttl)
}

// ErrBadRunToken is every reason a run token is not accepted; the caller says
// the same thing for all of them.
var ErrBadRunToken = errors.New("not a valid run token")

// Verify checks a token's form, MAC and expiry.
func (t RunTokens) Verify(tok string) (RunClaim, error) {
	rest, ok := strings.CutPrefix(tok, RunTokenPrefix)
	if !ok || len(t.Key) < 16 {
		return RunClaim{}, ErrBadRunToken
	}
	payload, mac, ok := strings.Cut(rest, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(t.mac(payload))) {
		return RunClaim{}, ErrBadRunToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return RunClaim{}, ErrBadRunToken
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return RunClaim{}, ErrBadRunToken
	}
	exp, err := strconv.ParseInt(parts[2], 36, 64)
	if err != nil || !t.now().Before(time.Unix(exp, 0)) {
		return RunClaim{}, ErrBadRunToken
	}
	return RunClaim{Repo: parts[0], Run: parts[1], Expires: time.Unix(exp, 0)}, nil
}
