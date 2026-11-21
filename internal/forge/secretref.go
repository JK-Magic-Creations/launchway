package forge

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// maxSecretBytes bounds what a credential file may hold.
const maxSecretBytes = 64 << 10

// Resolve reads the secret the reference names: the environment variable of
// the server, or the file. The value is returned to the caller that needs it
// to talk to a remote and is never stored; errors name the reference, never
// the value.
func (s SecretRef) Resolve() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	switch {
	case s.Env != "":
		v, ok := os.LookupEnv(s.Env)
		if !ok || strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("the credential is read from the environment variable %s, which is not set on this server", s.Env)
		}
		return strings.TrimSpace(v), nil
	case strings.HasPrefix(s.File, SealedPrefix):
		name, _ := s.SealedName()
		sealedMu.RLock()
		read := sealedReader
		sealedMu.RUnlock()
		if read == nil {
			return "", fmt.Errorf("the credential %s is kept sealed by the server, which cannot open it here", s)
		}
		v, err := read(name)
		if err != nil {
			return "", fmt.Errorf("the sealed credential %s cannot be opened: %w", name, err)
		}
		return strings.TrimSpace(v), nil
	case s.File != "":
		f, err := os.Open(s.File)
		if err != nil {
			return "", fmt.Errorf("the credential file %s cannot be read: %w", s.File, errors.Unwrap(err))
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, maxSecretBytes+1))
		if err != nil || len(b) > maxSecretBytes {
			return "", fmt.Errorf("the credential file %s cannot be read or is larger than %d bytes", s.File, maxSecretBytes)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("the credential file %s is empty", s.File)
		}
		return v, nil
	}
	return "", errors.New("no credential is given")
}

// String describes the reference for people: where the secret is read from,
// never what it is.
func (s SecretRef) String() string {
	switch {
	case s.Env != "":
		return "env:" + s.Env
	case strings.HasPrefix(s.File, SealedPrefix):
		return "sealed:" + strings.TrimPrefix(s.File, SealedPrefix)
	case s.File != "":
		return "file:" + s.File
	}
	return ""
}

var (
	sealedMu     sync.RWMutex
	sealedReader func(name string) (string, error)
)

// SetSealedReader says how a sealed reference ("sealed:NAME") is opened: the server sets it once,
// from its sealing service.
func SetSealedReader(read func(name string) (string, error)) {
	sealedMu.Lock()
	sealedReader = read
	sealedMu.Unlock()
}
