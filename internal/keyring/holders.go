package keyring

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ParseKey reads a root key as written in a key file or the environment:
// 32 bytes, base64 (standard or URL alphabet), surrounding white space
// ignored.
func ParseKey(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(text); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, errors.New("a root key is 32 random bytes written in base64 (44 characters); `launchway encryption init` makes one")
}

// FromKey is a holder for a root key in memory, of the given kind.
func FromKey(kind, where string, key []byte) (Holder, error) {
	if len(key) != 32 {
		return nil, errors.New("a root key must be 32 bytes")
	}
	why := strengthKeyFile
	switch kind {
	case "env":
		why = strengthEnv
	case "passphrase":
		why = strengthPass
	}
	return &localHolder{kind: kind, where: where, why: why, key: append([]byte(nil), key...)}, nil
}

// checkKeyFileMode refuses a key file that others can read, or that
// anyone but the owner can write.
func checkKeyFileMode(path string, mode os.FileMode) error {
	if mode&0o007 != 0 {
		return fmt.Errorf("the key file %s can be read by every user on this machine (mode %04o); make it private first: chmod 600 %s", path, mode.Perm(), path)
	}
	if mode&0o022 != 0 {
		return fmt.Errorf("the key file %s can be written by others (mode %04o); chmod 600 %s", path, mode.Perm(), path)
	}
	return nil
}

// OpenKeyFile reads a root key from a file: 32 random bytes in base64. It
// refuses a file that other users can read, so a key is never left where
// every account can take it. A Kubernetes Secret mounted with mode 0400 or
// 0440 is fine.
func OpenKeyFile(path string) (Holder, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("the root key file: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("the root key file %s is not a regular file", path)
	}
	if err := checkKeyFileMode(path, fi.Mode()); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return nil, err
	}
	key, err := ParseKey(string(b))
	if err != nil {
		return nil, fmt.Errorf("the root key file %s: %w", path, err)
	}
	return FromKey("key_file", path, key)
}

// WriteKeyFile creates a new key file with a fresh random key, mode 0600,
// refusing to replace an existing file. It returns the key's text so that a
// caller can offer it for backup; never log it.
func WriteKeyFile(path string) error {
	key, err := NewRandomKey()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; Launchway never overwrites a root key", path)
		}
		return err
	}
	_, werr := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return werr
	}
	return nil
}

// OpenEnv reads a root key from an environment variable (for containers
// and tests); get is os.Getenv.
func OpenEnv(name string, get func(string) string) (Holder, error) {
	v := get(name)
	if strings.TrimSpace(v) == "" {
		return nil, fmt.Errorf("the root key variable %s is empty", name)
	}
	key, err := ParseKey(v)
	if err != nil {
		return nil, fmt.Errorf("the root key variable %s: %w", name, err)
	}
	return FromKey("env", "the environment variable "+name, key)
}

// Keystore is the passphrase-unsealed holder's file: a random root key,
// encrypted under a key made from the passphrase with Argon2id. The root key
// is not derived from the passphrase, so the passphrase can be changed
// without re-wrapping anything.
type keystore struct {
	Version  int    `json:"version"`
	KDF      string `json:"kdf"`
	Salt     string `json:"salt"`
	Time     uint32 `json:"time"`
	MemoryK  uint32 `json:"memory_kib"`
	Threads  uint8  `json:"threads"`
	Wrapped  string `json:"wrapped_key"`
	Describe string `json:"about,omitempty"`
}

// Argon2id cost: 64 MiB, 3 passes, 4 lanes, as the OWASP password storage
// guidance's second option; stored in the file, so it can be raised later.
const (
	ksTime    = 3
	ksMemory  = 64 * 1024
	ksThreads = 4
)

func ksDerive(pass, salt []byte, t, m uint32, p uint8) []byte {
	return argon2.IDKey(pass, salt, t, m, p, 32)
}

// CreateKeystore writes a new passphrase keystore file (mode 0600), refusing
// to replace one.
func CreateKeystore(path string, passphrase []byte) error {
	if len(passphrase) < 12 {
		return errors.New("the passphrase must be at least 12 characters")
	}
	root, err := NewRandomKey()
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	h := &localHolder{key: ksDerive(passphrase, salt, ksTime, ksMemory, ksThreads)}
	wrapped, err := h.Wrap(context.Background(), root, []byte("launchway keystore v1"))
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(keystore{Version: 1, KDF: "argon2id", Salt: base64.StdEncoding.EncodeToString(salt),
		Time: ksTime, MemoryK: ksMemory, Threads: ksThreads, Wrapped: base64.StdEncoding.EncodeToString(wrapped)}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; Launchway never overwrites a keystore", path)
		}
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
	}
	return werr
}

// OpenKeystore unlocks a passphrase keystore file with the passphrase.
func OpenKeystore(path string, passphrase []byte) (Holder, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("the keystore file: %w", err)
	}
	if err := checkKeyFileMode(path, fi.Mode()); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ks keystore
	if err := json.Unmarshal(b, &ks); err != nil || ks.Version != 1 || ks.KDF != "argon2id" {
		return nil, fmt.Errorf("%s is not a Launchway keystore file", path)
	}
	salt, err1 := base64.StdEncoding.DecodeString(ks.Salt)
	wrapped, err2 := base64.StdEncoding.DecodeString(ks.Wrapped)
	if err1 != nil || err2 != nil || ks.Time == 0 || ks.MemoryK < 8*1024 || ks.Threads == 0 {
		return nil, fmt.Errorf("%s is damaged", path)
	}
	h := &localHolder{key: ksDerive(passphrase, salt, ks.Time, ks.MemoryK, ks.Threads)}
	root, err := h.Unwrap(context.Background(), wrapped, []byte("launchway keystore v1"))
	if err != nil {
		return nil, errors.New("the passphrase does not unlock the keystore " + path)
	}
	return FromKey("passphrase", path, root)
}

// ReadPassphrase reads a passphrase from a file: the first line, without
// the line ending.
func ReadPassphrase(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("the passphrase file: %w", err)
	}
	if err := checkKeyFileMode(path, fi.Mode()); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, _, _ = bytes.Cut(b, []byte("\n"))
	b = bytes.TrimRight(b, "\r")
	if len(b) == 0 {
		return nil, fmt.Errorf("the passphrase file %s is empty", path)
	}
	return b, nil
}
