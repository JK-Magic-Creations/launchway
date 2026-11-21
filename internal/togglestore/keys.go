package togglestore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// SDK keys. A key belongs to one environment of one project and can do one
// thing: read toggles, never change them.
//
//	server key  lwt_srv_<id>_<secret>   a secret. It downloads the ruleset (so
//	            it can read every rule) and evaluates remotely. It is shown
//	            once, when made, and stored as a SHA-256 hash of the whole key
//	            (the secret is 256 bits of randomness, so a plain hash is the
//	            right tool: there is nothing to guess).
//	client key  lwt_cli_<id>            public by nature: it sits in a web page
//	            or an app. It can only ask for values, for a context it sends,
//	            of toggles marked client-visible; it never receives a rule, a
//	            segment or another toggle. It may be bound to the web origins
//	            allowed to use it.
//
// Rotating a key makes a new one and lets the old one work for an overlap
// (a day unless said), so that no deployment is caught between them.

const (
	KindServer = "server"
	KindClient = "client"

	keyPrefixServer = "lwt_srv_"
	keyPrefixClient = "lwt_cli_"

	maxKeysPerEnv   = 20
	maxOverlap      = 30 * 24 * time.Hour
	defaultOverlap  = 24 * time.Hour
	keyCacheSeconds = 20
)

// ErrBadKey is returned for a key that is unknown, revoked or expired; the
// caller must not say which.
var ErrBadKey = errors.New("the key is not valid")

// KeyInfo is what a key lets its holder do.
type KeyInfo struct {
	ID          string   `json:"id"`
	Project     string   `json:"project"`
	Environment string   `json:"environment"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Prefix      string   `json:"prefix"`
	Origins     []string `json:"origins,omitempty"`
	CreatedAt   int64    `json:"created_at"`
	CreatedBy   string   `json:"created_by"`
	ExpiresAt   int64    `json:"expires_at,omitempty"`
	RevokedAt   int64    `json:"revoked_at,omitempty"`
	RotatedTo   string   `json:"rotated_to,omitempty"`
	LastUsedAt  int64    `json:"last_used_at,omitempty"`
}

type cachedKey struct {
	info  KeyInfo
	until time.Time
}

type keyCache struct {
	mu sync.Mutex
	m  map[string]cachedKey
	// used remembers when each key was last used, written out by Flush.
	used map[string]int64
}

func (c *keyCache) clear() {
	c.mu.Lock()
	c.m = map[string]cachedKey{}
	c.mu.Unlock()
}

func randomText(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashKey(full string) string {
	sum := sha256.Sum256([]byte(full))
	return hex.EncodeToString(sum[:])
}

// NewKey makes a key for an environment. The key's text is returned once.
func (s *Service) NewKey(ctx context.Context, who Actor, project, env, kind, name string, origins []string) (string, *KeyInfo, error) {
	if kind != KindServer && kind != KindClient {
		return "", nil, invalid("kind is server or client")
	}
	if env == "" || project == "" {
		return "", nil, invalid("say the project and the environment")
	}
	if len(name) > 100 {
		return "", nil, invalid("a name is at most 100 characters")
	}
	for _, o := range origins {
		if !strings.HasPrefix(o, "https://") && !strings.HasPrefix(o, "http://localhost") && !strings.HasPrefix(o, "http://127.0.0.1") || strings.ContainsAny(o, " ,") || len(o) > 200 {
			return "", nil, invalid("an origin is a web address such as https://app.example.com, with no path (%q is not)", o)
		}
	}
	if kind == KindServer && len(origins) > 0 {
		return "", nil, invalid("origins are for client keys")
	}
	var n int
	if err := s.d.DB.QueryRow(ctx, `SELECT count(*) FROM toggle_keys WHERE org = ? AND project = ? AND environment = ? AND revoked_at = 0`, Org, project, env).Scan(&n); err != nil {
		return "", nil, err
	}
	if n >= maxKeysPerEnv {
		return "", nil, conflict("an environment has at most %d keys: revoke ones that are not used", maxKeysPerEnv)
	}
	var id, full, hash string
	switch kind {
	case KindServer:
		id = strings.ToLower(hex.EncodeToString(randBytes(6)))
		secret := randomText(32)
		full = keyPrefixServer + id + "_" + secret
		hash = hashKey(full)
	default:
		id = keyPrefixClient + hex.EncodeToString(randBytes(12))
		full = id
	}
	now := s.now()
	if _, err := s.d.DB.Exec(ctx, `INSERT INTO toggle_keys(id, org, project, environment, kind, name, prefix, secret_hash, origins, created_at, created_by)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, Org, project, env, kind, name, prefixOf(full), hash, strings.Join(origins, ","), now, who.User); err != nil {
		return "", nil, err
	}
	if err := s.keyEvent(ctx, who, project, env, "created "+kind+" key "+id+" "+name); err != nil {
		return "", nil, err
	}
	info, err := s.getKey(ctx, id)
	return full, info, err
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func prefixOf(full string) string {
	if len(full) > 16 {
		return full[:16] + "..."
	}
	return full
}

func (s *Service) keyEvent(ctx context.Context, who Actor, project, env, what string) error {
	ch := &Change{At: s.now(), Project: project, Level: LevelProject, Scope: project, Environment: env, Toggle: "", Action: ActionKey, Actor: who.User, Reason: what, Via: who.Via, Status: StatusApplied}
	if err := insertChange(ctx, s.d.DB, ch); err != nil {
		return err
	}
	if err := bump(ctx, s.d.DB); err != nil { // other servers drop what they cached about keys
		return err
	}
	s.noticeLocal(ctx)
	return nil
}

func scanKey(sc interface{ Scan(...any) error }) (*KeyInfo, error) {
	var k KeyInfo
	var origins string
	if err := sc.Scan(&k.ID, &k.Project, &k.Environment, &k.Kind, &k.Name, &k.Prefix, &origins, &k.CreatedAt, &k.CreatedBy, &k.ExpiresAt, &k.RevokedAt, &k.RotatedTo, &k.LastUsedAt); err != nil {
		return nil, err
	}
	if origins != "" {
		k.Origins = strings.Split(origins, ",")
	}
	return &k, nil
}

const keyCols = `id, project, environment, kind, name, prefix, origins, created_at, created_by, expires_at, revoked_at, rotated_to, last_used_at`

func (s *Service) getKey(ctx context.Context, id string) (*KeyInfo, error) {
	k, err := scanKey(s.d.DB.QueryRow(ctx, `SELECT `+keyCols+` FROM toggle_keys WHERE org = ? AND id = ?`, Org, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, missing("no such key")
	}
	return k, err
}

// Keys lists the keys of a project's environment (all environments when env is
// empty), never their secrets.
func (s *Service) Keys(ctx context.Context, project, env string) ([]KeyInfo, error) {
	q, args := `SELECT `+keyCols+` FROM toggle_keys WHERE org = ? AND project = ?`, []any{Org, project}
	if env != "" {
		q += ` AND environment = ?`
		args = append(args, env)
	}
	rows, err := s.d.DB.Query(ctx, q+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KeyInfo{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// KeyOf returns a key of a project, or not found.
func (s *Service) KeyOf(ctx context.Context, project, id string) (*KeyInfo, error) {
	k, err := s.getKey(ctx, id)
	if err != nil || k.Project != project {
		return nil, missing("no key %s in project %s", id, project)
	}
	return k, nil
}

// RevokeKey ends a key at once.
func (s *Service) RevokeKey(ctx context.Context, who Actor, project, id string) error {
	k, err := s.KeyOf(ctx, project, id)
	if err != nil {
		return err
	}
	if _, err := s.d.DB.Exec(ctx, `UPDATE toggle_keys SET revoked_at = ? WHERE org = ? AND id = ? AND revoked_at = 0`, s.now(), Org, id); err != nil {
		return err
	}
	return s.keyEvent(ctx, who, project, k.Environment, "revoked "+k.Kind+" key "+id)
}

// RotateKey makes a new key like an old one, and lets the old one work for the
// overlap; the new key's text is returned once.
func (s *Service) RotateKey(ctx context.Context, who Actor, project, id string, overlap time.Duration) (string, *KeyInfo, error) {
	old, err := s.KeyOf(ctx, project, id)
	if err != nil {
		return "", nil, err
	}
	if old.RevokedAt != 0 {
		return "", nil, conflict("the key was revoked: make a new one")
	}
	if overlap <= 0 {
		overlap = defaultOverlap
	}
	if overlap > maxOverlap {
		return "", nil, invalid("the overlap is at most %d days", int(maxOverlap.Hours()/24))
	}
	full, nk, err := s.NewKey(ctx, who, project, old.Environment, old.Kind, old.Name, old.Origins)
	if err != nil {
		return "", nil, err
	}
	until := s.now() + overlap.Milliseconds()
	if _, err := s.d.DB.Exec(ctx, `UPDATE toggle_keys SET expires_at = ?, rotated_to = ? WHERE org = ? AND id = ?`, until, nk.ID, Org, id); err != nil {
		return "", nil, err
	}
	if err := s.keyEvent(ctx, who, project, old.Environment, "rotated "+old.Kind+" key "+id+" to "+nk.ID); err != nil {
		return "", nil, err
	}
	return full, nk, nil
}

// Authenticate finds the key a token is. It answers from memory when it can;
// a key that was revoked is dropped from every server's memory when the
// revision moves (at most CheckEvery later).
func (s *Service) Authenticate(ctx context.Context, token string) (*KeyInfo, error) {
	var id string
	var kind string
	switch {
	case strings.HasPrefix(token, keyPrefixServer):
		kind = KindServer
		rest := strings.TrimPrefix(token, keyPrefixServer)
		i := strings.IndexByte(rest, '_')
		if i != 12 || len(rest) < 20 {
			return nil, ErrBadKey
		}
		id = rest[:i]
	case strings.HasPrefix(token, keyPrefixClient) && len(token) < 80:
		kind, id = KindClient, token
	default:
		return nil, ErrBadKey
	}
	if _, err := s.revision(ctx); err != nil { // lets a revocation elsewhere clear the cache
		return nil, err
	}
	h := hashKey(token)
	now := s.d.Now()
	s.keyCache.mu.Lock()
	if c, ok := s.keyCache.m[h]; ok && now.Before(c.until) && (c.info.ExpiresAt == 0 || c.info.ExpiresAt > now.UnixMilli()) {
		info := c.info
		s.markUsed(id, now)
		s.keyCache.mu.Unlock()
		return &info, nil
	}
	s.keyCache.mu.Unlock()
	var hash string
	var info *KeyInfo
	row := s.d.DB.QueryRow(ctx, `SELECT `+keyCols+`, secret_hash FROM toggle_keys WHERE org = ? AND id = ?`, Org, id)
	k := &KeyInfo{}
	var origins string
	if err := row.Scan(&k.ID, &k.Project, &k.Environment, &k.Kind, &k.Name, &k.Prefix, &origins, &k.CreatedAt, &k.CreatedBy, &k.ExpiresAt, &k.RevokedAt, &k.RotatedTo, &k.LastUsedAt, &hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrBadKey
		}
		return nil, err
	}
	if origins != "" {
		k.Origins = strings.Split(origins, ",")
	}
	info = k
	if info.Kind != kind || info.RevokedAt != 0 || (info.ExpiresAt != 0 && info.ExpiresAt <= now.UnixMilli()) {
		return nil, ErrBadKey
	}
	if kind == KindServer && subtle.ConstantTimeCompare([]byte(hash), []byte(h)) != 1 {
		return nil, ErrBadKey
	}
	s.keyCache.mu.Lock()
	if s.keyCache.m == nil {
		s.keyCache.m = map[string]cachedKey{}
	}
	s.keyCache.m[h] = cachedKey{info: *info, until: now.Add(keyCacheSeconds * time.Second)}
	s.markUsed(id, now)
	s.keyCache.mu.Unlock()
	return info, nil
}

func (s *Service) markUsed(id string, now time.Time) {
	if s.keyCache.used == nil {
		s.keyCache.used = map[string]int64{}
	}
	s.keyCache.used[id] = now.UnixMilli()
}

// FlushKeyUse writes when keys were last used.
func (s *Service) FlushKeyUse(ctx context.Context) error {
	s.keyCache.mu.Lock()
	used := s.keyCache.used
	s.keyCache.used = nil
	s.keyCache.mu.Unlock()
	for id, at := range used {
		if _, err := s.d.DB.Exec(ctx, `UPDATE toggle_keys SET last_used_at = ? WHERE org = ? AND id = ? AND last_used_at < ?`, at, Org, id, at); err != nil {
			return err
		}
	}
	return nil
}
