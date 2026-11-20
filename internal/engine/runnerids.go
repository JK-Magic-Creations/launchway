package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/runnerauth"
)

// Runner identities (schema 20). A person makes a join token for a pool;
// a runner exchanges it, once, for an identity: a runner ID the server
// chooses and the public half of a key the runner made and keeps. With
// that key the runner signs short assertions for short-lived access
// tokens. Everything is in the database, so any server can answer any
// request, and a revocation is seen by every server at the runner's next
// request: each one reads the identity again.

// Errors a runner sees when it authenticates.
var (
	// ErrBadJoinToken: unknown, used up, expired or revoked. Which one is
	// not said, so that guessing tokens teaches nothing.
	ErrBadJoinToken = errors.New("the join token is not valid: it is unknown, used up, expired or revoked")
	// ErrRunnerRevoked: the identity was revoked.
	ErrRunnerRevoked = errors.New("this runner's identity has been revoked")
	// ErrReplay: an assertion was used before.
	ErrReplay = errors.New("the assertion has been used before")
)

// RotateEvery is how old a runner's key may get before the server asks the
// runner to make a new one.
const RotateEvery = 30 * 24 * time.Hour

// JoinTokenSpec is what a person asks for when making a join token.
type JoinTokenSpec struct {
	Pool        string
	Labels      []string
	Description string
	MaxUses     int           // default 1
	TTL         time.Duration // default runnerauth.JoinTTL
	// Ephemeral identities are revoked when their runner leaves, as pods
	// that enrol at start should be.
	Ephemeral bool
}

// JoinToken is a join token as listed; the secret itself is shown once,
// when it is made, and never stored.
type JoinToken struct {
	ID          string   `json:"id"`
	Pool        string   `json:"pool"`
	Labels      []string `json:"labels"`
	Description string   `json:"description"`
	MaxUses     int      `json:"max_uses"`
	Uses        int      `json:"uses"`
	Ephemeral   bool     `json:"ephemeral"`
	CreatedBy   string   `json:"created_by"`
	CreatedAt   int64    `json:"created_at"`
	ExpiresAt   int64    `json:"expires_at"`
	LastUsedAt  int64    `json:"last_used_at,omitempty"`
	RevokedBy   string   `json:"revoked_by,omitempty"`
	RevokedAt   int64    `json:"revoked_at,omitempty"`
	// State is active, used (up), expired or revoked.
	State string `json:"state"`
}

// RunnerIdentity is an enrolled runner.
type RunnerIdentity struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Pool           string   `json:"pool"`
	Kind           string   `json:"kind"` // of its pool, build or deploy
	KeyFingerprint string   `json:"key_fingerprint"`
	KeyCreatedAt   int64    `json:"key_created_at"`
	JoinToken      string   `json:"join_token"` // its ID
	EnrolledBy     string   `json:"enrolled_by"`
	EnrolledAt     int64    `json:"enrolled_at"`
	Labels         []string `json:"labels"`
	Hostname       string   `json:"hostname"`
	OS             string   `json:"os"`
	Arch           string   `json:"arch"`
	Version        string   `json:"version"`
	Executor       string   `json:"executor"`
	Ephemeral      bool     `json:"ephemeral"`
	LastSeen       int64    `json:"last_seen"`
	RevokedAt      int64    `json:"revoked_at,omitempty"`
	RevokedBy      string   `json:"revoked_by,omitempty"`
	RevokeReason   string   `json:"revoke_reason,omitempty"`
	RotateAsked    int64    `json:"rotate_asked,omitempty"`

	publicKey string
}

// identityError refuses identity calls on a build that does not know them.
func (e *Engine) identityError() error {
	if !e.knows(schemaIdentity) {
		return fmt.Errorf("%w: runner identities need schema %d", ErrNeedsNewer, schemaIdentity)
	}
	return nil
}

// CreateJoinToken makes a join token for a pool. The secret is returned
// once; only its hash is kept.
func (e *Engine) CreateJoinToken(spec JoinTokenSpec, actor string) (string, *JoinToken, error) {
	if err := e.identityError(); err != nil {
		return "", nil, err
	}
	pool, ok := e.pools[nonEmpty(spec.Pool, DefaultPool)]
	if !ok {
		return "", nil, fmt.Errorf("%w: there is no runner pool %q (runner_pools in the server's configuration)", ErrInvalid, spec.Pool)
	}
	if spec.MaxUses == 0 {
		spec.MaxUses = 1
	}
	if spec.TTL == 0 {
		spec.TTL = runnerauth.JoinTTL
	}
	switch {
	case spec.MaxUses < 1 || spec.MaxUses > 10000:
		return "", nil, fmt.Errorf("%w: max_uses is between 1 and 10000", ErrInvalid)
	case spec.TTL < time.Minute || spec.TTL > runnerauth.MaxJoinTTL:
		return "", nil, fmt.Errorf("%w: a join token lasts between a minute and %d days", ErrInvalid, int(runnerauth.MaxJoinTTL.Hours()/24))
	case len(spec.Description) > 200:
		return "", nil, fmt.Errorf("%w: the description is longer than 200 characters", ErrInvalid)
	}
	if err := checkLabels(spec.Labels); err != nil {
		return "", nil, err
	}
	secret := runnerauth.NewSecret(runnerauth.JoinPrefix)
	now := e.now()
	t := &JoinToken{ID: ids.New("join"), Pool: pool.Name, Labels: sortedLabels(spec.Labels), Description: spec.Description,
		MaxUses: spec.MaxUses, Ephemeral: spec.Ephemeral, CreatedBy: actor, CreatedAt: now.UnixMilli(),
		ExpiresAt: now.Add(spec.TTL).UnixMilli(), State: "active"}
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`INSERT INTO runner_join_tokens(id, hash, pool, labels, description, max_uses, ephemeral, created_by, created_at, expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, t.ID, runnerauth.Hash(secret), t.Pool, labelText(t.Labels), t.Description, t.MaxUses,
		boolInt(t.Ephemeral), actor, t.CreatedAt, t.ExpiresAt)
	if err != nil {
		return "", nil, err
	}
	return secret, t, nil
}

// JoinTokens lists the join tokens made in the last 30 days, newest first.
func (e *Engine) JoinTokens() ([]JoinToken, error) {
	if !e.knows(schemaIdentity) {
		return []JoinToken{}, nil
	}
	rows, err := e.db.Query(`SELECT id, pool, labels, description, max_uses, uses, ephemeral, created_by, created_at, expires_at,
		last_used_at, revoked_by, revoked_at FROM runner_join_tokens WHERE created_at >= ? ORDER BY created_at DESC LIMIT 500`,
		e.now().Add(-30*24*time.Hour).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := e.now().UnixMilli()
	out := []JoinToken{}
	for rows.Next() {
		var t JoinToken
		var labels string
		var eph int
		if err := rows.Scan(&t.ID, &t.Pool, &labels, &t.Description, &t.MaxUses, &t.Uses, &eph, &t.CreatedBy, &t.CreatedAt,
			&t.ExpiresAt, &t.LastUsedAt, &t.RevokedBy, &t.RevokedAt); err != nil {
			return nil, err
		}
		t.Labels, t.Ephemeral = splitLabels(labels), eph != 0
		switch {
		case t.RevokedAt != 0:
			t.State = "revoked"
		case t.Uses >= t.MaxUses:
			t.State = "used"
		case t.ExpiresAt <= now:
			t.State = "expired"
		default:
			t.State = "active"
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeJoinToken stops a join token from enrolling more runners. Runners
// it enrolled keep their identities.
func (e *Engine) RevokeJoinToken(id, actor string) (*JoinToken, error) {
	if err := e.identityError(); err != nil {
		return nil, err
	}
	unlock := e.writeLock()
	res, err := e.db.Exec(`UPDATE runner_join_tokens SET revoked_at = ?, revoked_by = ? WHERE id = ? AND revoked_at = 0`,
		e.now().UnixMilli(), actor, id)
	unlock()
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: no join token %s that is not already revoked", ErrNotFound, id)
	}
	list, err := e.JoinTokens()
	if err != nil {
		return nil, err
	}
	for _, t := range list {
		if t.ID == id {
			return &t, nil
		}
	}
	return &JoinToken{ID: id, State: "revoked"}, nil
}

// JoinRequest is what a runner sends to enrol.
type JoinRequest struct {
	Token     string     // the join token
	PublicKey string     // runnerauth.EncodeKey
	Proof     string     // runnerauth.JoinProof
	Hostname  string     // what the runner says about itself
	Facts     RunnerInfo // name, executor, version, labels, OS, arch
}

// Join enrols a runner: it uses up one use of the join token, and records
// an identity with a new ID and the runner's public key, in one
// transaction, so that a token is never used more often than it allows
// whichever servers the runners reach.
func (e *Engine) Join(req JoinRequest) (*RunnerIdentity, error) {
	if err := e.identityError(); err != nil {
		return nil, err
	}
	key, err := runnerauth.DecodeKey(req.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if !runnerauth.VerifyJoinProof(key, req.Token, req.Proof) {
		return nil, fmt.Errorf("%w: the proof does not show that the runner holds the key it registers", ErrForbidden)
	}
	if err := checkLabels(req.Facts.Labels); err != nil {
		return nil, err
	}
	f := req.Facts
	if len(f.Name) > 200 || len(req.Hostname) > 255 || len(f.Version) > 100 || len(f.OS) > 50 || len(f.Arch) > 50 || len(f.Executor) > 50 {
		return nil, fmt.Errorf("%w: a name, hostname, version, OS, architecture or executor is too long", ErrInvalid)
	}
	now := e.now().UnixMilli()
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var tokID, pool, labels, by string
	var eph int
	err = tx.QueryRow(`UPDATE runner_join_tokens SET uses = uses + 1, last_used_at = ?
		WHERE hash = ? AND revoked_at = 0 AND expires_at > ? AND uses < max_uses
		RETURNING id, pool, labels, ephemeral, created_by`, now, runnerauth.Hash(req.Token), now).
		Scan(&tokID, &pool, &labels, &eph, &by)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBadJoinToken
	}
	if err != nil {
		return nil, err
	}
	p, ok := e.pools[pool]
	if !ok {
		return nil, fmt.Errorf("%w: the join token is for pool %s, which this server's configuration no longer has", ErrForbidden, pool)
	}
	id := &RunnerIdentity{ID: ids.New("rnr"), Name: nonEmpty(f.Name, req.Hostname), Pool: pool, Kind: p.Kind,
		KeyCreatedAt: now, JoinToken: tokID, EnrolledBy: by, EnrolledAt: now,
		Labels: sortedLabels(append(splitLabels(labels), f.Labels...)), Hostname: req.Hostname, OS: f.OS, Arch: f.Arch,
		Version: f.Version, Executor: f.Executor, Ephemeral: eph != 0, LastSeen: now, publicKey: req.PublicKey}
	id.KeyFingerprint = runnerauth.Fingerprint(req.PublicKey)
	if _, err := tx.Exec(`INSERT INTO runner_identities(id, name, pool, public_key, key_created_at, join_token, enrolled_by, enrolled_at,
		labels, hostname, os, arch, version, executor, ephemeral, last_seen) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id.ID, id.Name, id.Pool, req.PublicKey, now, tokID, by, now, labelText(id.Labels), id.Hostname, id.OS, id.Arch,
		id.Version, id.Executor, eph, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return id, nil
}

// Info is what the server knows of an enrolled runner asking for work:
// what it says about itself, with the ID, pool and labels of its identity
// in place of anything it says about those.
func (id *RunnerIdentity) Info(said RunnerInfo) RunnerInfo {
	r := said
	r.ID, r.Kind, r.Auth = id.ID, id.Kind, AuthIdentity
	r.Pool = id.Pool
	if id.Kind == PoolBuild && id.Pool == DefaultPool {
		r.Pool = "" // as the shared token's runners, which it serves with
	}
	r.Labels = sortedLabels(append(slices.Clone(id.Labels), validLabels(said.Labels)...))
	if r.Name == "" {
		r.Name = id.Name
	}
	return r
}

// AccessGrant is an access token given to a runner.
type AccessGrant struct {
	Token     string
	ExpiresAt int64
	Runner    *RunnerIdentity
	// Rotate asks the runner to move to a new key: a person asked, or its
	// key is older than RotateEvery.
	Rotate bool
}

// IssueAccessToken checks an assertion signed by a runner's key and gives
// the runner a new access token. Each assertion is accepted once, by any
// server: its ID is kept until no clock within runnerauth.MaxSkew could
// accept it.
func (e *Engine) IssueAccessToken(assertion string) (*AccessGrant, error) {
	id, c, err := e.checkAssertion(assertion, runnerauth.AudienceToken)
	if err != nil {
		return nil, err
	}
	now := e.now()
	g := &AccessGrant{Token: runnerauth.NewSecret(runnerauth.AccessPrefix), ExpiresAt: now.Add(runnerauth.AccessTTL).UnixMilli(), Runner: id,
		Rotate: id.RotateAsked != 0 || now.Sub(time.UnixMilli(id.KeyCreatedAt)) > RotateEvery}
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := useAssertion(tx, c); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO runner_access_tokens(hash, runner, expires_at) VALUES(?,?,?)`,
		runnerauth.Hash(g.Token), id.ID, g.ExpiresAt); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE runner_identities SET last_seen = ? WHERE id = ?`, now.UnixMilli(), id.ID); err != nil {
		return nil, err
	}
	return g, tx.Commit()
}

// RotateRunnerKey moves a runner to a new key. The assertion is signed by
// the old key and names the new one; proof is signed by the new key. The
// runner's access tokens end, so that from now on only the new key gets it
// any: it asks for a new one at once.
func (e *Engine) RotateRunnerKey(assertion, newKey, proof string) (*RunnerIdentity, error) {
	id, c, err := e.checkAssertion(assertion, runnerauth.AudienceRotate)
	if err != nil {
		return nil, err
	}
	nk, err := runnerauth.DecodeKey(newKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if c.NewKey != newKey {
		return nil, fmt.Errorf("%w: the assertion does not name the new key", ErrForbidden)
	}
	if !runnerauth.VerifyRotateProof(nk, assertion, proof) {
		return nil, fmt.Errorf("%w: the proof does not show that the runner holds the new key", ErrForbidden)
	}
	if newKey == id.publicKey {
		return nil, fmt.Errorf("%w: the new key is the old one", ErrInvalid)
	}
	now := e.now().UnixMilli()
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := useAssertion(tx, c); err != nil {
		return nil, err
	}
	// Only from the key that was checked: two rotations at once cannot
	// both win.
	res, err := tx.Exec(`UPDATE runner_identities SET public_key = ?, key_created_at = ?, rotate_asked = 0, last_seen = ?
		WHERE id = ? AND public_key = ? AND revoked_at = 0`, newKey, now, now, id.ID, id.publicKey)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%w: the runner's key changed meanwhile, or it was revoked", ErrForbidden)
	}
	if _, err := tx.Exec(`DELETE FROM runner_access_tokens WHERE runner = ?`, id.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	id.publicKey, id.KeyCreatedAt, id.RotateAsked = newKey, now, 0
	id.KeyFingerprint = runnerauth.Fingerprint(newKey)
	return id, nil
}

// checkAssertion finds the runner an assertion names and checks it against
// the runner's key; useAssertion, in the transaction that acts on it,
// makes sure it is used only once.
func (e *Engine) checkAssertion(assertion, audience string) (*RunnerIdentity, runnerauth.Claims, error) {
	if err := e.identityError(); err != nil {
		return nil, runnerauth.Claims{}, err
	}
	c, err := runnerauth.Peek(assertion)
	if err != nil {
		return nil, c, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	id, err := e.RunnerIdentity(c.Issuer)
	if errors.Is(err, ErrNotFound) {
		return nil, c, fmt.Errorf("%w: no runner %s is enrolled", ErrForbidden, c.Issuer)
	}
	if err != nil {
		return nil, c, err
	}
	if id.RevokedAt != 0 {
		return nil, c, ErrRunnerRevoked
	}
	key, err := runnerauth.DecodeKey(id.publicKey)
	if err != nil {
		return nil, c, err
	}
	if c, err = runnerauth.Verify(assertion, key, audience, e.now()); err != nil {
		if errors.Is(err, runnerauth.ErrSkew) {
			return nil, c, err // the caller tells the runner the time
		}
		return nil, c, fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	return id, c, nil
}

func useAssertion(tx *txx, c runnerauth.Claims) error {
	res, err := tx.Exec(`INSERT INTO runner_assertions(id, expires_at) VALUES(?,?) ON CONFLICT(id) DO NOTHING`,
		c.Issuer+"/"+c.ID, runnerauth.KeepJTIUntil(c).UnixMilli())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %w", ErrForbidden, ErrReplay)
	}
	return nil
}

// RunnerByAccessToken finds the runner an access token belongs to. It
// reads the database every time, so a revocation or a rotation on any
// server is seen at once.
func (e *Engine) RunnerByAccessToken(token string) (*RunnerIdentity, error) {
	if !e.knows(schemaIdentity) {
		return nil, ErrNotFound
	}
	var runner string
	err := e.db.QueryRow(`SELECT runner FROM runner_access_tokens WHERE hash = ? AND expires_at > ?`,
		runnerauth.Hash(token), e.now().UnixMilli()).Scan(&runner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	id, err := e.RunnerIdentity(runner)
	if err != nil {
		return nil, err
	}
	if id.RevokedAt != 0 {
		return nil, ErrRunnerRevoked
	}
	return id, nil
}

const identityCols = `id, name, pool, public_key, key_created_at, join_token, enrolled_by, enrolled_at, labels, hostname, os, arch,
	version, executor, ephemeral, last_seen, revoked_at, revoked_by, revoke_reason, rotate_asked`

func (e *Engine) scanIdentity(sc interface{ Scan(...any) error }) (*RunnerIdentity, error) {
	var id RunnerIdentity
	var labels string
	var eph int
	if err := sc.Scan(&id.ID, &id.Name, &id.Pool, &id.publicKey, &id.KeyCreatedAt, &id.JoinToken, &id.EnrolledBy, &id.EnrolledAt,
		&labels, &id.Hostname, &id.OS, &id.Arch, &id.Version, &id.Executor, &eph, &id.LastSeen, &id.RevokedAt, &id.RevokedBy,
		&id.RevokeReason, &id.RotateAsked); err != nil {
		return nil, err
	}
	id.Labels, id.Ephemeral = splitLabels(labels), eph != 0
	id.KeyFingerprint = runnerauth.Fingerprint(id.publicKey)
	id.Kind = PoolBuild
	if p, ok := e.pools[id.Pool]; ok {
		id.Kind = p.Kind
	}
	return &id, nil
}

// RunnerIdentity reads an enrolled runner.
func (e *Engine) RunnerIdentity(runnerID string) (*RunnerIdentity, error) {
	if err := e.identityError(); err != nil {
		return nil, err
	}
	id, err := e.scanIdentity(e.db.QueryRow(`SELECT `+identityCols+` FROM runner_identities WHERE id = ?`, runnerID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: runner %s", ErrNotFound, runnerID)
	}
	return id, err
}

// RunnerIdentities lists enrolled runners, those revoked in the last 30
// days included.
func (e *Engine) RunnerIdentities() ([]*RunnerIdentity, error) {
	if !e.knows(schemaIdentity) {
		return nil, nil
	}
	rows, err := e.db.Query(`SELECT `+identityCols+` FROM runner_identities WHERE revoked_at = 0 OR revoked_at >= ? ORDER BY enrolled_at DESC`,
		e.now().Add(-30*24*time.Hour).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RunnerIdentity
	for rows.Next() {
		id, err := e.scanIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// identityUsable refuses a runner whose identity has been revoked.
func (e *Engine) identityUsable(runnerID string) error {
	var revoked int64
	err := e.db.QueryRow(`SELECT revoked_at FROM runner_identities WHERE id = ?`, runnerID).Scan(&revoked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: no runner %s is enrolled", ErrForbidden, runnerID)
	case err != nil:
		return err
	case revoked != 0:
		return ErrRunnerRevoked
	}
	return nil
}

// AskRotation asks a runner to move to a new key. Its access tokens end,
// so it asks for a new one at its next request, and the answer tells it to
// rotate.
func (e *Engine) AskRotation(runnerID string) (*RunnerIdentity, error) {
	if err := e.identityError(); err != nil {
		return nil, err
	}
	unlock := e.writeLock()
	res, err := e.db.Exec(`UPDATE runner_identities SET rotate_asked = ? WHERE id = ? AND revoked_at = 0`, e.now().UnixMilli(), runnerID)
	if err == nil {
		if n, _ := res.RowsAffected(); n == 0 {
			err = fmt.Errorf("%w: no enrolled runner %s that is not revoked", ErrNotFound, runnerID)
		}
	}
	if err == nil {
		_, err = e.db.Exec(`DELETE FROM runner_access_tokens WHERE runner = ?`, runnerID)
	}
	unlock()
	if err != nil {
		return nil, err
	}
	return e.RunnerIdentity(runnerID)
}

// RevokeRunner revokes an identity: its access tokens end at once, and
// every server refuses its next request. The steps it holds are taken from
// it and queued again as new attempts, so that whatever it still sends
// about them is refused, and the runs it held may go to other runners at
// once. It returns how many steps were taken back.
func (e *Engine) RevokeRunner(runnerID, actor, reason string) (int, error) {
	if err := e.identityError(); err != nil {
		return 0, err
	}
	now := e.now().UnixMilli()
	unlock := e.writeLock()
	tx, err := e.db.Begin()
	if err != nil {
		unlock()
		return 0, err
	}
	res, err := tx.Exec(`UPDATE runner_identities SET revoked_at = ?, revoked_by = ?, revoke_reason = ? WHERE id = ? AND revoked_at = 0`,
		now, actor, reason, runnerID)
	if err == nil {
		if n, _ := res.RowsAffected(); n == 0 {
			err = fmt.Errorf("%w: no enrolled runner %s that is not already revoked", ErrNotFound, runnerID)
		}
	}
	if err == nil {
		_, err = tx.Exec(`DELETE FROM runner_access_tokens WHERE runner = ?`, runnerID)
	}
	if err == nil {
		_, err = tx.Exec(`DELETE FROM runners WHERE id = ?`, runnerID)
	}
	if err == nil {
		err = e.notifyOthers(tx, note{Kind: noteWake})
	}
	if err == nil {
		err = tx.Commit()
	} else {
		tx.Rollback()
	}
	unlock()
	if err != nil {
		return 0, err
	}
	n, err := e.takeLeasesFrom(runnerID, "the runner was revoked")
	e.notify()
	return n, err
}

// takeLeasesFrom ends every lease a runner holds, and queues the steps
// again as lost.
func (e *Engine) takeLeasesFrom(runnerID, reason string) (int, error) {
	rows, err := e.db.Query(`SELECT id, run_id, step_id FROM leases WHERE runner = ?`, runnerID)
	if err != nil {
		return 0, err
	}
	type held struct{ id, run, step string }
	var list []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.id, &h.run, &h.step); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, h)
	}
	rows.Close()
	n := 0
	for _, h := range list {
		err := e.withRun(h.run, func(c *cmd) error {
			res, err := c.tx.Exec(`DELETE FROM leases WHERE id = ?`, h.id)
			if err != nil {
				return err
			}
			if k, _ := res.RowsAffected(); k == 0 {
				return nil // it ended meanwhile
			}
			if s := c.st.Steps[h.step]; s != nil && s.LeaseID == h.id && s.Status.Active() {
				c.lose(h.step, reason)
				c.advance()
				n++
			}
			return nil
		})
		if err != nil && !errors.Is(err, ErrNeedsNewer) && !errors.Is(err, ErrNotFound) {
			return n, err
		}
	}
	return n, nil
}

// LeaseHolder says which runner holds a lease, the target of its step
// (deploy work), and whether that runner is an enrolled identity.
func (e *Engine) LeaseHolder(leaseID string) (runner, target, runID string, enrolled bool, err error) {
	q := `SELECT l.runner, '', l.run_id, 0 FROM leases l WHERE l.id = ?`
	switch {
	case e.knows(schemaIdentity):
		q = `SELECT l.runner, COALESCE(s.target, ''), l.run_id, CASE WHEN i.id IS NULL THEN 0 ELSE 1 END FROM leases l
			LEFT JOIN steps s ON s.run_id = l.run_id AND s.step_id = l.step_id
			LEFT JOIN runner_identities i ON i.id = l.runner WHERE l.id = ?`
	case e.knows(schemaDeploy):
		q = `SELECT l.runner, COALESCE(s.target, ''), l.run_id, 0 FROM leases l
			LEFT JOIN steps s ON s.run_id = l.run_id AND s.step_id = l.step_id WHERE l.id = ?`
	}
	var enr int
	err = e.db.QueryRow(q, leaseID).Scan(&runner, &target, &runID, &enr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", false, ErrLeaseLost
	}
	return runner, target, runID, enr != 0, err
}

// pruneIdentities forgets assertion IDs and access tokens that can no
// longer be used.
func (e *Engine) pruneIdentities() {
	if !e.knows(schemaIdentity) {
		return
	}
	unlock := e.writeLock()
	defer unlock()
	now := e.now().UnixMilli()
	if _, err := e.db.Exec(`DELETE FROM runner_assertions WHERE expires_at < ?`, now); err != nil {
		e.log.Error("forgetting old runner assertions", "err", err)
	}
	if _, err := e.db.Exec(`DELETE FROM runner_access_tokens WHERE expires_at < ?`, now); err != nil {
		e.log.Error("forgetting expired runner access tokens", "err", err)
	}
}

// checkLabels checks runner labels.
func checkLabels(labels []string) error {
	if len(labels) > pipeline.MaxRunnerLabels*2 {
		return fmt.Errorf("%w: at most %d labels", ErrInvalid, pipeline.MaxRunnerLabels*2)
	}
	for _, l := range labels {
		if !pipeline.ValidLabel(l) {
			return fmt.Errorf("%w: %q is not a label: use lower-case letters, digits, '.', '_' and '-'", ErrInvalid, l)
		}
	}
	return nil
}

func sortedLabels(l []string) []string {
	s := slices.Clone(l)
	slices.Sort(s)
	s = slices.Compact(s)
	if s == nil {
		s = []string{}
	}
	return s
}

func splitLabels(s string) []string {
	if f := strings.Fields(s); f != nil {
		return f
	}
	return []string{}
}

// encodeAccept writes what a runner's accept: section allows, for the
// runners table.
func encodeAccept(r RunnerInfo) string {
	if r.Accept.Empty() {
		return ""
	}
	b, _ := json.Marshal(r.Accept)
	if len(b) > 8192 {
		return ""
	}
	return string(b)
}

// RunInfosFor is RunInfos for an enrolled runner: only runs of projects
// its pool serves, so that a runner learns nothing of other projects' runs.
func (e *Engine) RunInfosFor(r RunnerInfo, list []string) ([]RunInfo, error) {
	all, err := e.RunInfos(list)
	if err != nil || len(all) == 0 {
		return all, err
	}
	pool, ok := e.pools[runnerPoolName(r)]
	if !ok {
		return []RunInfo{}, nil
	}
	out := all[:0]
	for _, ri := range all {
		var project, event string
		if err := e.db.QueryRow(`SELECT project, event FROM runs WHERE id = ?`, ri.ID).Scan(&project, &event); err != nil {
			continue
		}
		if pool.Kind == PoolDeploy || e.poolServes(pool, project, event) == "" {
			out = append(out, ri)
		}
	}
	return out, nil
}

// IsEnrolled reports whether a runner ID belongs to an enrolled runner. A
// database error is returned, not read as "not enrolled": callers use this to
// refuse impersonation and must fail closed.
func (e *Engine) IsEnrolled(runnerID string) (bool, error) {
	if !e.knows(schemaIdentity) {
		return false, nil
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM runner_identities WHERE id = ?`, runnerID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// KnowsIdentities reports whether this build enrols runners.
func (e *Engine) KnowsIdentities() bool { return e.knows(schemaIdentity) }
