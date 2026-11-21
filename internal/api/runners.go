package api

// Runners: how they authenticate, what they may act on, and the people's
// side of it (pools, join tokens, revoking and rotating).
//
// A runner request carries one of three kinds of bearer token:
//
//   - an access token (runnerauth.AccessPrefix) of an enrolled runner
//     (schema 20): the server knows which runner it is, and its pool. The
//     runner ID and pool it sends are ignored; every lease it takes is
//     bound to it, and no other runner may act on that lease.
//   - the shared runner token: any runner holding it is the same to the
//     server, which can only trust what it says about itself. Build work
//     of the default pool only, unless that pool requires identities.
//   - a deploy pool's token: deploy work of that pool's targets, unless the
//     pool requires identities.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/runnerauth"
)

type runnerIdentityKey struct{}

// callerIdentity is the enrolled runner a request came from, or nil for a
// shared or pool token.
func callerIdentity(r *http.Request) *engine.RunnerIdentity {
	id, _ := r.Context().Value(runnerIdentityKey{}).(*engine.RunnerIdentity)
	return id
}

// runnerAuth accepts an enrolled runner's access token, the shared runner
// token or a deploy pool's token. Shared and pool tokens are compared, in
// constant time, whichever matches.
func (s *Server) runnerAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if strings.HasPrefix(got, runnerauth.AccessPrefix) {
			id, err := s.Engine.RunnerByAccessToken(got)
			switch {
			case errors.Is(err, engine.ErrRunnerRevoked):
				s.runnerDenied(r, "", "a revoked runner's access token")
				writeErr(w, http.StatusUnauthorized, err.Error())
				return
			case errors.Is(err, engine.ErrNotFound), errors.Is(err, engine.ErrNeedsNewer):
				// Expired, unknown, or ended by a rotation: the runner gets a
				// new one and asks again.
				writeErr(w, http.StatusUnauthorized, "the access token is not valid (expired, unknown or replaced); get a new one")
				return
			case err != nil:
				s.fail(w, err)
				return
			}
			ctx := context.WithValue(r.Context(), runnerIdentityKey{}, id)
			if id.Kind == engine.PoolDeploy {
				pool := s.deployPool(id.Pool)
				if pool == nil {
					writeErr(w, http.StatusForbidden, "deploy pool "+id.Pool+" is not configured on this server")
					return
				}
				ctx = context.WithValue(ctx, poolKey{}, pool)
			}
			next(w, r.WithContext(ctx))
			return
		}
		shared := s.RunnerToken != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.RunnerToken)) == 1
		var pool *RunnerPool
		pools := s.cur().RunnerPools
		for i := range pools {
			p := &pools[i]
			if p.Token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(p.Token)) == 1 {
				pool = p
			}
		}
		switch {
		case pool != nil && !shared && pool.RequireIdentity:
			writeErr(w, http.StatusForbidden, "{runner_group} "+pool.Name+" takes only enrolled {runners} (require_identity); enrol this {runner} with a join token")
		case pool != nil && !shared:
			next(w, r.WithContext(context.WithValue(r.Context(), poolKey{}, pool)))
		case shared && pool == nil:
			next(w, r)
		default:
			writeErr(w, http.StatusUnauthorized, "a valid {runner} token is required")
		}
	}
}

// deployPool finds a configured deploy pool by name.
func (s *Server) deployPool(name string) *RunnerPool {
	pools := s.cur().RunnerPools
	for i := range pools {
		if pools[i].Name == name {
			return &pools[i]
		}
	}
	return nil
}

// callerInfo is what the server makes of what a runner says about itself:
// an enrolled runner's ID, pool and labels come from its identity, a pool
// token's pool from the token.
func (s *Server) callerInfo(r *http.Request, said engine.RunnerInfo) engine.RunnerInfo {
	if id := callerIdentity(r); id != nil {
		return id.Info(said)
	}
	said.Pool, said.Kind, said.Auth = "", engine.PoolBuild, engine.AuthShared
	if p := callerPool(r); p != nil {
		said.Pool, said.Kind, said.Auth = p.Name, engine.PoolDeploy, engine.AuthPoolToken
	}
	return said
}

// claimsEnrolled refuses a runner that has no identity of its own but says
// it is an enrolled runner, whose ID anyone who can read a project can see:
// it would write that runner's row and bind leases to an ID it cannot use.
// It audits and writes the refusal, and reports true. If the database cannot
// say whether the runner is enrolled it fails closed: it writes a 500 and
// reports true.
func (s *Server) claimsEnrolled(w http.ResponseWriter, r *http.Request, said engine.RunnerInfo) bool {
	if callerIdentity(r) != nil {
		return false
	}
	enrolled, err := s.Engine.IsEnrolled(said.ID)
	if err != nil {
		s.fail(w, err)
		return true
	}
	if !enrolled {
		return false
	}
	msg := "runner " + said.ID + " is enrolled; only it may say it is that runner"
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "runner:" + nonEmpty(callerID(r), "shared"), Source: s.clientAddr(r), RequestID: reqID(r),
		Action: "runner.identity_refused", Target: said.ID, Outcome: "denied",
		Detail: "a runner with a shared or pool token said it is the enrolled runner " + said.ID + " (" + r.URL.Path + ")"})
	writeErr(w, http.StatusForbidden, msg)
	return true
}

// sharedRefused says why the shared runner token may not take build work:
// the default pool takes only enrolled runners. "" if it may.
func (s *Server) sharedRefused() string {
	if p, ok := s.Engine.Pool(engine.DefaultPool); ok && p.RequireIdentity {
		return "the default runner pool takes only enrolled runners (require_identity), so the shared runner token takes no work; enrol this runner with a join token (launchway runner --join)"
	}
	return ""
}

// refusePools refuses ordinary runner work to deploy pools' tokens and
// runners, and to the shared token when the default pool requires
// identities.
func (s *Server) refusePools(w http.ResponseWriter, r *http.Request) bool {
	if p := callerPool(r); p != nil {
		writeErr(w, http.StatusForbidden, "this {runner} belongs to {runner_group} "+p.Name+", which takes deploy work only")
		return true
	}
	if callerIdentity(r) == nil {
		if why := s.sharedRefused(); why != "" {
			writeErr(w, http.StatusForbidden, why)
			return true
		}
	}
	return false
}

// leaseAllowed checks that the caller may act on a lease. A lease taken by
// an enrolled runner is that runner's alone. Deploy work needs a runner of
// a pool serving its target; other work a build runner. It writes the
// refusal and reports false.
func (s *Server) leaseAllowed(w http.ResponseWriter, r *http.Request, leaseID string) bool {
	holder, target, runID, enrolled, err := s.Engine.LeaseHolder(leaseID)
	if err != nil {
		s.fail(w, err)
		return false
	}
	id := callerIdentity(r)
	refuse := func(msg string) bool {
		who := "a runner with a shared or pool token"
		if id != nil {
			who = id.ID
		}
		project, _ := s.Engine.RunProject(runID)
		s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "runner:" + nonEmpty(callerID(r), "shared"), Source: s.clientAddr(r), RequestID: reqID(r),
			Action: "runner.lease_refused", Project: project, Target: leaseID, Outcome: "denied", Detail: who + ": " + msg})
		writeErr(w, http.StatusForbidden, msg)
		return false
	}
	switch {
	case id != nil && holder != id.ID:
		return refuse("this lease belongs to another runner")
	case id == nil && enrolled:
		return refuse("this lease belongs to an enrolled runner; only it may act on it")
	}
	pool := callerPool(r)
	switch {
	case pool == nil && target != "":
		return refuse("this lease is deploy work; only a runner pool serving target " + target + " may act on it")
	case pool != nil && !slices.Contains(pool.Targets, target):
		return refuse("runner pool " + pool.Name + " may not act on this lease")
	}
	return true
}

func callerID(r *http.Request) string {
	if id := callerIdentity(r); id != nil {
		return id.ID
	}
	if p := callerPool(r); p != nil {
		return "pool/" + p.Name
	}
	return ""
}

// deployRunner reads the runner of a deploy request and works out its
// targets; it writes the refusal and returns false if it may not.
func (s *Server) deployRunner(w http.ResponseWriter, r *http.Request, info *engine.RunnerInfo) ([]string, bool) {
	pool := callerPool(r)
	if pool == nil {
		writeErr(w, http.StatusForbidden, "deploy work needs a {runner} of a deploy pool (runner_pools in the server's configuration): its token or a {runner} enrolled in it; the shared {runner} token cannot take it")
		return nil, false
	}
	if s.claimsEnrolled(w, r, *info) {
		return nil, false
	}
	*info = s.callerInfo(r, *info)
	if info.ID == "" {
		writeErr(w, http.StatusBadRequest, "the {runner} has no ID")
		return nil, false
	}
	return poolTargets(pool, info.Targets), true
}

// runnerDenied counts a failed runner authentication and records it.
func (s *Server) runnerDenied(r *http.Request, target, why string) {
	addr, now := s.clientAddr(r), s.Now()
	l := s.limiter()
	wasBlocked := l.blocked(addr, now) > 0
	l.fail(addr, now)
	if wasBlocked {
		return
	}
	s.record(&engine.AuditEntry{At: now.UnixMilli(), User: "runner", Source: addr, RequestID: reqID(r), Action: "runner.auth", Target: target, Outcome: "denied",
		Detail: r.URL.Path + ": " + why})
}

// ---- enrolment (schema 20) ----

// runnerJoin enrols a runner with a join token.
func (s *Server) runnerJoin(w http.ResponseWriter, r *http.Request) {
	if s.tooManyFailures(w, r) {
		return
	}
	var in struct {
		JoinToken string `json:"join_token"`
		PublicKey string `json:"public_key"`
		Proof     string `json:"proof"`
		Hostname  string `json:"hostname"`
		Facts     struct {
			Name     string   `json:"name"`
			OS       string   `json:"os"`
			Arch     string   `json:"arch"`
			Version  string   `json:"version"`
			Executor string   `json:"executor"`
			Features string   `json:"features"`
			Labels   []string `json:"labels"`
		} `json:"facts"`
	}
	if !decode(w, r, &in) {
		return
	}
	f := in.Facts
	id, err := s.Engine.Join(engine.JoinRequest{Token: in.JoinToken, PublicKey: in.PublicKey, Proof: in.Proof, Hostname: in.Hostname,
		Facts: engine.RunnerInfo{Name: f.Name, OS: f.OS, Arch: f.Arch, Version: f.Version, Executor: f.Executor, Features: f.Features, Labels: f.Labels}})
	if err != nil {
		if errors.Is(err, engine.ErrNeedsNewer) {
			writeErr(w, http.StatusNotFound, "this server does not enrol {runners} (it needs schema 20); use the shared {runner} token")
			return
		}
		if errors.Is(err, engine.ErrBadJoinToken) || errors.Is(err, engine.ErrForbidden) {
			s.runnerDenied(r, in.Hostname, "join: "+err.Error())
			writeErr(w, http.StatusUnauthorized, strings.TrimPrefix(err.Error(), "not allowed: "))
			return
		}
		s.fail(w, err)
		return
	}
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "runner:" + id.ID, Source: s.clientAddr(r), RequestID: reqID(r), Action: "runner.join",
		Target: id.ID, Outcome: "ok", Detail: fmt.Sprintf("pool %s, host %s, key %s, join token %s made by %s", id.Pool, id.Hostname,
			id.KeyFingerprint, id.JoinToken, id.EnrolledBy)})
	writeJSON(w, http.StatusOK, map[string]any{"runner_id": id.ID, "pool": id.Pool, "kind": id.Kind, "labels": id.Labels,
		"ephemeral": id.Ephemeral, "server_time": s.Now().Unix(), "version": s.Version})
}

// runnerToken gives an enrolled runner an access token for a signed
// assertion.
func (s *Server) runnerToken(w http.ResponseWriter, r *http.Request) {
	if s.tooManyFailures(w, r) {
		return
	}
	var in struct {
		Assertion string `json:"assertion"`
	}
	if !decode(w, r, &in) {
		return
	}
	g, err := s.Engine.IssueAccessToken(in.Assertion)
	if err != nil {
		s.assertionFailed(w, r, in.Assertion, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_token": g.Token, "expires_at": g.ExpiresAt / 1000,
		"expires_in": int(runnerauth.AccessTTL.Seconds()), "server_time": s.Now().Unix(), "runner_id": g.Runner.ID, "pool": g.Runner.Pool,
		"rotate": g.Rotate})
}

// runnerRotate moves an enrolled runner to a new key.
func (s *Server) runnerRotate(w http.ResponseWriter, r *http.Request) {
	if s.tooManyFailures(w, r) {
		return
	}
	var in struct {
		Assertion string `json:"assertion"`
		PublicKey string `json:"public_key"`
		Proof     string `json:"proof"`
	}
	if !decode(w, r, &in) {
		return
	}
	id, err := s.Engine.RotateRunnerKey(in.Assertion, in.PublicKey, in.Proof)
	if err != nil {
		s.assertionFailed(w, r, in.Assertion, err)
		return
	}
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "runner:" + id.ID, Source: s.clientAddr(r), RequestID: reqID(r), Action: "runner.rotate",
		Target: id.ID, Outcome: "ok", Detail: "new key " + id.KeyFingerprint})
	writeJSON(w, http.StatusOK, map[string]any{"runner_id": id.ID, "key_fingerprint": id.KeyFingerprint, "server_time": s.Now().Unix()})
}

// assertionFailed answers a refused assertion. A clock too far off is
// answered with the server's time, so that the runner can correct.
func (s *Server) assertionFailed(w http.ResponseWriter, r *http.Request, assertion string, err error) {
	c, _ := runnerauth.Peek(assertion)
	switch {
	case errors.Is(err, engine.ErrNeedsNewer):
		writeErr(w, http.StatusNotFound, "this server does not know {runner} identities (it needs schema 20)")
	case errors.Is(err, runnerauth.ErrSkew):
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error(), "server_time": s.Now().Unix(), "skew": true})
	case errors.Is(err, engine.ErrInvalid):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid: "))
	case errors.Is(err, engine.ErrRunnerRevoked), errors.Is(err, engine.ErrForbidden):
		s.runnerDenied(r, c.Issuer, err.Error())
		writeErr(w, http.StatusUnauthorized, strings.TrimPrefix(err.Error(), "not allowed: "))
	default:
		s.fail(w, err)
	}
}

// ---- for people ----

// runnersRole is the role needed on every project to manage runners:
// make join tokens, revoke and rotate runners.
func (s *Server) runnersRole() auth.Role {
	if s.cur().RunnersRole == nil {
		return auth.Admin
	}
	return *s.cur().RunnersRole
}

// mayManageRunners reports whether someone may manage runners.
func (s *Server) mayManageRunners(id *identity) bool {
	need := s.runnersRole()
	if need == auth.NoRole {
		return false
	}
	if s.Auth == nil {
		return true
	}
	if id == nil || id.Via == viaNone || id.User == "" {
		return false
	}
	if id.limited() && (len(id.scope) > 0 || (id.capRole != auth.NoRole && id.capRole < need)) {
		return false
	}
	return s.cur().Policy.RoleEverywhere(id.User, id.Groups) >= need
}

func (s *Server) manageRefusal() string {
	if s.runnersRole() == auth.NoRole {
		return "managing runners is turned off (runner_admin: none)"
	}
	return fmt.Sprintf("you need the %s role on every project to manage runners (runner_admin)", s.runnersRole())
}

// PoolView is a runner pool as people see it.
type PoolView struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	Projects        []string `json:"projects"`
	Events          []string `json:"events"`
	Targets         []string `json:"targets"`
	RequireIdentity bool     `json:"require_identity"`
	// SharedToken says runners may also use a shared token: the shared
	// runner token (the default pool) or the pool's own (a deploy pool).
	SharedToken bool `json:"shared_token"`
	// Configured is false for the default pool when the configuration does
	// not name it: it then serves every project no other pool serves.
	Configured bool `json:"configured"`
}

func (s *Server) listPools(w http.ResponseWriter, r *http.Request) {
	out := []PoolView{}
	for _, p := range s.Engine.Pools() {
		v := PoolView{Name: p.Name, Kind: p.Kind, Projects: nonNil(p.Projects), Events: nonNil(p.Events), Targets: nonNil(p.Targets),
			RequireIdentity: p.RequireIdentity, Configured: p.Name != engine.DefaultPool || len(p.Projects) > 0 || len(p.Events) > 0 || p.RequireIdentity}
		switch {
		case p.Kind == engine.PoolBuild && p.Name == engine.DefaultPool:
			v.SharedToken = !p.RequireIdentity && s.RunnerToken != ""
		case p.Kind == engine.PoolDeploy:
			if dp := s.deployPool(p.Name); dp != nil {
				v.SharedToken = dp.Token != "" && !dp.RequireIdentity
				v.Targets = nonNil(dp.Targets)
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"pools": out, "manage": s.mayManageRunners(identityFrom(r)),
		"identities": s.Engine.KnowsIdentities()})
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func (s *Server) listJoinTokens(w http.ResponseWriter, r *http.Request) {
	if !s.mayManageRunners(identityFrom(r)) {
		writeErr(w, http.StatusForbidden, s.manageRefusal())
		return
	}
	list, err := s.Engine.JoinTokens()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createJoinToken(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if !s.mayManageRunners(id) {
		writeErr(w, http.StatusForbidden, s.manageRefusal())
		return
	}
	var in struct {
		Pool        string   `json:"pool"`
		Labels      []string `json:"labels"`
		Description string   `json:"description"`
		MaxUses     int      `json:"max_uses"`
		TTL         string   `json:"ttl"` // such as 1h; default 1h
		Ephemeral   bool     `json:"ephemeral"`
	}
	if !decode(w, r, &in) {
		return
	}
	var ttl time.Duration
	if in.TTL != "" {
		d, err := time.ParseDuration(in.TTL)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "ttl: "+err.Error())
			return
		}
		ttl = d
	}
	secret, t, err := s.Engine.CreateJoinToken(engine.JoinTokenSpec{Pool: in.Pool, Labels: in.Labels, Description: in.Description,
		MaxUses: in.MaxUses, TTL: ttl, Ephemeral: in.Ephemeral}, id.User)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, t.ID, fmt.Sprintf("pool %s, %d use(s), until %s", t.Pool, t.MaxUses, time.UnixMilli(t.ExpiresAt).UTC().Format(time.RFC3339)))
	writeJSON(w, http.StatusCreated, map[string]any{"token": secret, "join_token": t})
}

func (s *Server) revokeJoinToken(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if !s.mayManageRunners(id) {
		writeErr(w, http.StatusForbidden, s.manageRefusal())
		return
	}
	t, err := s.Engine.RevokeJoinToken(r.PathValue("id"), id.User)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) revokeRunner(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if !s.mayManageRunners(id) {
		writeErr(w, http.StatusForbidden, s.manageRefusal())
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	n, err := s.Engine.RevokeRunner(r.PathValue("id"), id.User, clip(in.Reason, 200))
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, "", fmt.Sprintf("%d step(s) taken back and queued again; reason: %s", n, nonEmpty(in.Reason, "none given")))
	writeJSON(w, http.StatusOK, map[string]any{"released": n})
}

func (s *Server) rotateRunner(w http.ResponseWriter, r *http.Request) {
	if !s.mayManageRunners(identityFrom(r)) {
		writeErr(w, http.StatusForbidden, s.manageRefusal())
		return
	}
	id, err := s.Engine.AskRotation(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, id)
}
