package api

// A guarded way to take a recovery copy of the root key (the setup guide's encryption step). The
// administrator proves who they are again (password and second factor), types a passphrase twice, and
// receives the key wrapped under that passphrase: scrypt, then AES-256-GCM, in a small JSON file.
// Launchway records THAT a copy was taken and when (the audit trail), never the copy.
//
// NEEDS THE SEALING PACKAGE AT MERGE TIME. On this branch the root key is read where the chart puts it
// (LAUNCHWAY_ROOT_KEY_FILE or LAUNCHWAY_ROOT_KEY), in the format `launchway encryption init` writes.
// When integrate/release is merged, call SetRootKeySource with a source backed by internal/keyring's
// holder, and fold the encryption status (`GET /api/v1/encryption`) into the guide step; nothing else
// here changes.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// RootKeySource gives the root key to the guarded export.
type RootKeySource interface {
	// Available says a root key is configured.
	Available() bool
	// Key returns the 32-byte root key. The caller zeroes it.
	Key() ([]byte, error)
}

var (
	rootKeyMu  sync.RWMutex
	rootKeySrc RootKeySource = envRootKey{}
)

// SetRootKeySource replaces where the guarded export reads the key from.
func SetRootKeySource(s RootKeySource) {
	rootKeyMu.Lock()
	rootKeySrc = s
	rootKeyMu.Unlock()
}

func currentRootKeySource() RootKeySource {
	rootKeyMu.RLock()
	defer rootKeyMu.RUnlock()
	return rootKeySrc
}

// envRootKey reads the key as the chart and the installer provide it.
type envRootKey struct{}

func (envRootKey) Available() bool {
	return os.Getenv("LAUNCHWAY_ROOT_KEY_FILE") != "" || os.Getenv("LAUNCHWAY_ROOT_KEY") != ""
}

func (envRootKey) Key() ([]byte, error) {
	v := strings.TrimSpace(os.Getenv("LAUNCHWAY_ROOT_KEY"))
	if f := os.Getenv("LAUNCHWAY_ROOT_KEY_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, errors.New("the root key file cannot be read by the server")
		}
		v = strings.TrimSpace(string(b))
	}
	k, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(k) != 32 {
		return nil, errors.New("the root key is not 32 bytes in base64")
	}
	return k, nil
}

// canExportRootKey says whether this person can take a copy now, and if not why and what would help.
func (s *Server) canExportRootKey(r *http.Request) (ok bool, why, would string) {
	if !currentRootKeySource().Available() {
		return false, "This server has no root key configured.", "Turn on encryption at rest first (the guide Encryption at rest)."
	}
	if !s.localOn() {
		return false, "Taking a copy asks you to type your password and a code again, which needs a built-in account.", "Sign in with a built-in administrator account."
	}
	id := identityFrom(r)
	a, err := s.Engine.LocalAccountByName(id.User)
	if err != nil || a == nil {
		return false, "You are not signed in with a built-in account, which is how Launchway checks it is really you.", "Sign in with a built-in administrator account."
	}
	if !a.TOTPConfirmed {
		return false, "Your account has no authenticator yet, and taking a copy of the key needs one.", "Set up a second factor in your account first."
	}
	return true, "", ""
}

// rootKeyCopyTaken reads the audit trail for the last copy.
func (s *Server) rootKeyCopyTaken() (bool, int64) {
	es, err := s.Engine.AuditLog(engine.AuditFilter{Action: "encryption.root_key_copy", Limit: 5})
	if err != nil {
		return false, 0
	}
	for _, e := range es {
		if e.Outcome == "ok" {
			return true, e.At
		}
	}
	return false, 0
}

type rootKeyCopyIn struct {
	Password        string `json:"current_password"`
	Code            string `json:"code"`
	Passphrase      string `json:"passphrase"`
	PassphraseAgain string `json:"passphrase_again"`
}

// POST /api/v1/encryption/root-key-copy
func (s *Server) rootKeyCopy(w http.ResponseWriter, r *http.Request) {
	var in rootKeyCopyIn
	if !decode(w, r, &in) {
		return
	}
	deny := func(code int, c, msg, next string) {
		writeJSON(w, code, map[string]string{"error": msg, "code": c, "next": next})
	}
	if ok, why, would := s.canExportRootKey(r); !ok {
		deny(http.StatusConflict, "unavailable", why, would)
		return
	}
	if len([]rune(in.Passphrase)) < 12 {
		deny(http.StatusUnprocessableEntity, "weak_passphrase", "the passphrase is shorter than 12 characters", "Use a few unrelated words; you will need it to read the copy.")
		return
	}
	if in.Passphrase != in.PassphraseAgain {
		deny(http.StatusUnprocessableEntity, "passphrase_mismatch", "the two passphrases are not the same", "Type it again, the same way twice.")
		return
	}
	a, ok := s.myAccount(w, r)
	if !ok || !s.reauth(w, r, a, in.Password, in.Code) {
		return
	}
	key, err := currentRootKeySource().Key()
	if err != nil {
		s.fail(w, err)
		return
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	file, err := WrapRootKey(key, in.Passphrase, s.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, "", "a recovery copy of the root key was taken; the copy is not kept")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="launchway-root-key-copy.json"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(file)
}

// GET /api/v1/encryption/root-key-copy: whether and when a copy was taken (never the copy).
func (s *Server) rootKeyCopyStatus(w http.ResponseWriter, r *http.Request) {
	taken, at := s.rootKeyCopyTaken()
	ok, why, would := s.canExportRootKey(r)
	out := map[string]any{"taken": taken, "can_take": ok}
	if taken {
		out["taken_at"] = at
	}
	if !ok {
		out["reason"], out["next"] = why, would
	}
	writeJSON(w, http.StatusOK, out)
}

// KeyCopy is the file a recovery copy is saved as.
type KeyCopy struct {
	Format     string `json:"format"`
	KDF        string `json:"kdf"`
	N          int    `json:"n"`
	R          int    `json:"r"`
	P          int    `json:"p"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
	Created    string `json:"created"`
	Note       string `json:"note"`
}

const (
	copyFormat = "launchway-root-key-copy-v1"
	copyN      = 1 << 15
	copyR      = 8
	copyP      = 1
)

// WrapRootKey encrypts a root key under a passphrase.
func WrapRootKey(key []byte, passphrase string, now time.Time) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	dk, err := scrypt.Key([]byte(passphrase), salt, copyN, copyR, copyP, 32)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(dk)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, key, []byte(copyFormat))
	return json.MarshalIndent(KeyCopy{Format: copyFormat, KDF: "scrypt", N: copyN, R: copyR, P: copyP,
		Salt: base64.StdEncoding.EncodeToString(salt), Nonce: base64.StdEncoding.EncodeToString(nonce), Ciphertext: base64.StdEncoding.EncodeToString(ct),
		Created: now.UTC().Format(time.RFC3339), Note: "A recovery copy of a Launchway root key, wrapped with a passphrase. Keep it apart from the database backups."}, "", "  ")
}

// UnwrapRootKey reads a copy back with its passphrase (the restore side; also used by the tests).
func UnwrapRootKey(file []byte, passphrase string) ([]byte, error) {
	var c KeyCopy
	if err := json.Unmarshal(file, &c); err != nil || c.Format != copyFormat {
		return nil, errors.New("this is not a Launchway root key copy")
	}
	salt, e1 := base64.StdEncoding.DecodeString(c.Salt)
	nonce, e2 := base64.StdEncoding.DecodeString(c.Nonce)
	ct, e3 := base64.StdEncoding.DecodeString(c.Ciphertext)
	if e1 != nil || e2 != nil || e3 != nil {
		return nil, errors.New("the copy is damaged")
	}
	dk, err := scrypt.Key([]byte(passphrase), salt, c.N, c.R, c.P, 32)
	if err != nil {
		return nil, err
	}
	blk, _ := aes.NewCipher(dk)
	gcm, _ := cipher.NewGCM(blk)
	k, err := gcm.Open(nil, nonce, ct, []byte(copyFormat))
	if err != nil {
		return nil, errors.New("the passphrase is not right, or the copy is damaged")
	}
	return k, nil
}
