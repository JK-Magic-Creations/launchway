package registry

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Action is what a caller does to a repository.
type Action string

// The actions of the Distribution API's token scopes.
const (
	Pull   Action = "pull"
	Push   Action = "push"
	Delete Action = "delete"
)

// Principal is a caller the registry has identified. Can decides, for one
// repository, whether the caller may do an action; the decision is made by
// whoever built the principal (Launchway's roles for a person, the project
// of a run for a step).
type Principal struct {
	Subject string // "ann", or "run run_ab12"
	Run     string // the run, for a run credential
	// Project is the project a run credential belongs to ("" for a person).
	Project string
	Via     string // "token", "run" or "bearer"
	Can     func(ctx context.Context, repo string, a Action) bool
	// Expires, if not zero, is when the credential behind this principal
	// ends; a bearer token made for it never outlives it.
	Expires time.Time
}

// Access is what a bearer token grants: one repository and some actions.
type Access struct {
	Name    string   `json:"name"`
	Actions []Action `json:"actions"`
}

// TokenTTL is how long a bearer token lasts. Short on purpose: it is a
// snapshot of what the caller could do, so a revoked role or credential is
// felt within minutes.
const TokenTTL = 5 * time.Minute

// Prefixes of what travels in the Authorization header.
const (
	BearerPrefix = "lwo_" // a bearer token the registry made
	RunPrefix    = "lwi_" // a run's registry credential
)

// MaxRunCredentialTTL is the longest a run credential may be made to last.
const MaxRunCredentialTTL = 24 * time.Hour

// Signer makes and checks tokens with a key every server of a group shares.
type Signer struct {
	Key []byte
	Now func() time.Time
}

func (g Signer) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g Signer) mac(domain, payload string) string {
	m := hmac.New(sha256.New, g.Key)
	m.Write([]byte("launchway registry " + domain + " v1\x00" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:24])
}

type bearerClaims struct {
	Sub    string   `json:"sub"`
	Run    string   `json:"run,omitempty"`
	Proj   string   `json:"proj,omitempty"`
	Exp    int64    `json:"exp"`
	Access []Access `json:"access"`
}

// ErrBadToken is every reason a token is not accepted.
var ErrBadToken = errors.New("not a valid registry token")

// IssueBearer makes a bearer token granting access for ttl (at most
// TokenTTL).
func (g Signer) IssueBearer(p *Principal, access []Access, ttl time.Duration) (string, time.Time, error) {
	if len(g.Key) < 16 {
		return "", time.Time{}, errors.New("no key to sign registry tokens with")
	}
	if ttl <= 0 || ttl > TokenTTL {
		ttl = TokenTTL
	}
	exp := g.now().Add(ttl)
	if !p.Expires.IsZero() && p.Expires.Before(exp) {
		exp = p.Expires
	}
	c := bearerClaims{Sub: p.Subject, Run: p.Run, Proj: p.Project, Exp: exp.Unix(), Access: access}
	raw, _ := json.Marshal(c)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return BearerPrefix + payload + "." + g.mac("bearer", payload), exp, nil
}

// VerifyBearer checks a bearer token and returns the principal it stands
// for: one that can do exactly what the token grants.
func (g Signer) VerifyBearer(tok string) (*Principal, error) {
	rest, ok := strings.CutPrefix(tok, BearerPrefix)
	if !ok || len(g.Key) < 16 {
		return nil, ErrBadToken
	}
	payload, mac, ok := strings.Cut(rest, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(g.mac("bearer", payload))) {
		return nil, ErrBadToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, ErrBadToken
	}
	var c bearerClaims
	if json.Unmarshal(raw, &c) != nil || !g.now().Before(time.Unix(c.Exp, 0)) {
		return nil, ErrBadToken
	}
	granted := map[string]map[Action]bool{}
	for _, a := range c.Access {
		m := granted[a.Name]
		if m == nil {
			m = map[Action]bool{}
			granted[a.Name] = m
		}
		for _, act := range a.Actions {
			m[act] = true
		}
	}
	return &Principal{Subject: c.Sub, Run: c.Run, Project: c.Proj, Via: "bearer", Expires: time.Unix(c.Exp, 0),
		Can: func(_ context.Context, repo string, a Action) bool { return granted[repo][a] }}, nil
}

// RunClaim is what a run credential says.
type RunClaim struct {
	Project string
	Run     string
	Push    bool
	Expires time.Time
}

// MintRun makes a run's credential: it lets one run pull from the
// repositories of its own project (and of projects whose policy allows it)
// and, only when push is true, push to its own project's. It is a MAC over
// the project, run and expiry, so no table of credentials exists and none
// can be changed; it travels only inside the run.
func (g Signer) MintRun(project, run string, push bool, ttl time.Duration) (string, error) {
	if len(g.Key) < 16 {
		return "", errors.New("no key to sign run credentials with")
	}
	if ttl <= 0 || ttl > MaxRunCredentialTTL {
		return "", fmt.Errorf("a run credential lasts between 1 second and %s", MaxRunCredentialTTL)
	}
	if strings.ContainsAny(project+run, ".|") {
		return "", errors.New("a project or run ID cannot contain '.' or '|'")
	}
	p := "0"
	if push {
		p = "1"
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(project + "|" + run + "|" + p + "|" + fmt.Sprint(g.now().Add(ttl).Unix())))
	return RunPrefix + payload + "." + g.mac("run", payload), nil
}

// VerifyRun checks a run credential.
func (g Signer) VerifyRun(tok string) (RunClaim, error) {
	rest, ok := strings.CutPrefix(tok, RunPrefix)
	if !ok || len(g.Key) < 16 {
		return RunClaim{}, ErrBadToken
	}
	payload, mac, ok := strings.Cut(rest, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(g.mac("run", payload))) {
		return RunClaim{}, ErrBadToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return RunClaim{}, ErrBadToken
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 4 {
		return RunClaim{}, ErrBadToken
	}
	var exp int64
	if _, err := fmt.Sscan(parts[3], &exp); err != nil || !g.now().Before(time.Unix(exp, 0)) {
		return RunClaim{}, ErrBadToken
	}
	return RunClaim{Project: parts[0], Run: parts[1], Push: parts[2] == "1", Expires: time.Unix(exp, 0)}, nil
}

// RunPrincipal is the principal a valid run credential stands for: it may
// pull its own project's repositories and those a project's policy shares
// with it, push to its own project when the credential allows, and delete
// nothing.
func (s *Service) RunPrincipal(c RunClaim) *Principal {
	return &Principal{Subject: "run " + c.Run, Run: c.Run, Project: c.Project, Via: "run", Expires: c.Expires,
		Can: func(ctx context.Context, repo string, a Action) bool {
			project := ProjectOf(repo)
			switch a {
			case Pull:
				return s.MayRunPull(ctx, c.Project, project)
			case Push:
				return c.Push && project == c.Project
			}
			return false
		}}
}
