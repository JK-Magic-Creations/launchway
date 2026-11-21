package localauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// The Argon2id parameters of every new hash, stated here and recorded in
// each stored hash so they can be raised later without locking anyone out
// (NeedsRehash says when a hash was made with weaker ones).
//
// Memory 64 MiB, 3 passes, 2 lanes: RFC 9106's second recommended
// configuration with fewer lanes, above OWASP's minimum (19 MiB, 2 passes,
// 1 lane). A sign-in costs about a tenth of a second of one core.
const (
	ArgonMemoryKiB = 64 * 1024
	ArgonPasses    = 3
	ArgonLanes     = 2
	argonSaltLen   = 16
	argonKeyLen    = 32
)

// hashSlots bounds how many hashes run at once so that a flood of sign-ins
// cannot take the memory of the server (each one holds ArgonMemoryKiB).
var hashSlots = make(chan struct{}, 4)

// HashPassword returns the encoded Argon2id hash of a password:
// $argon2id$v=19$m=65536,t=3,p=2$salt$key.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hashSlots <- struct{}{}
	key := argon2.IDKey([]byte(password), salt, ArgonPasses, ArgonMemoryKiB, ArgonLanes, argonKeyLen)
	<-hashSlots
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, ArgonMemoryKiB, ArgonPasses, ArgonLanes,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

type argonParams struct {
	m, t uint32
	p    uint8
	salt []byte
	key  []byte
}

func parseHash(encoded string) (argonParams, error) {
	var p argonParams
	f := strings.Split(encoded, "$")
	if len(f) != 6 || f[1] != "argon2id" {
		return p, errors.New("not an argon2id hash")
	}
	var v int
	if _, err := fmt.Sscanf(f[2], "v=%d", &v); err != nil || v != argon2.Version {
		return p, errors.New("unsupported argon2 version")
	}
	if _, err := fmt.Sscanf(f[3], "m=%d,t=%d,p=%d", &p.m, &p.t, &p.p); err != nil {
		return p, errors.New("unreadable argon2 parameters")
	}
	// Refuse parameters that would make verifying a hostile stored value a
	// denial of service.
	if p.m < 8 || p.m > 1<<20 || p.t < 1 || p.t > 20 || p.p < 1 || p.p > 16 {
		return p, errors.New("argon2 parameters out of range")
	}
	var err error
	if p.salt, err = base64.RawStdEncoding.DecodeString(f[4]); err != nil || len(p.salt) < 8 {
		return p, errors.New("unreadable salt")
	}
	if p.key, err = base64.RawStdEncoding.DecodeString(f[5]); err != nil || len(p.key) < 16 {
		return p, errors.New("unreadable key")
	}
	return p, nil
}

// VerifyPassword reports whether the password matches the stored hash, in
// constant time for the comparison.
func VerifyPassword(password, encoded string) bool {
	p, err := parseHash(encoded)
	if err != nil {
		return false
	}
	hashSlots <- struct{}{}
	key := argon2.IDKey([]byte(password), p.salt, p.t, p.m, p.p, uint32(len(p.key)))
	<-hashSlots
	return subtle.ConstantTimeCompare(key, p.key) == 1
}

// NeedsRehash says whether a stored hash was made with weaker parameters
// than today's, so the server can replace it after a successful sign-in.
func NeedsRehash(encoded string) bool {
	p, err := parseHash(encoded)
	if err != nil {
		return true
	}
	return p.m < ArgonMemoryKiB || p.t < ArgonPasses || p.p < ArgonLanes
}

// DummyVerify spends the time of one verification, so that an unknown
// account takes as long to refuse as a known one.
func DummyVerify() {
	hashSlots <- struct{}{}
	argon2.IDKey([]byte("launchway"), []byte("launchway-dummy!"), ArgonPasses, ArgonMemoryKiB, ArgonLanes, argonKeyLen)
	<-hashSlots
}
