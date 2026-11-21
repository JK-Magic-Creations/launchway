package api

import (
	"context"
	"net/http"
	"sync"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/sealing"
)

// secretsService is the service that seals the server's stored secrets
// (internal/sealing). It is named here so that Server's field needs no import.
type secretsService = sealing.Service

// secretStoreMu guards the lazily made service (not sealerMu, which callers of
// sharedSecret already hold).
var secretStoreMu sync.Mutex

// secretStore returns the server's sealing service. cmd/launchway sets it
// after unlocking the root key; a Server made without one (tests, tools)
// gets, on first use, a service with no root key, which keeps the signing
// keys in the clear exactly as a server with no root key does.
func (s *Server) secretStore() (*sealing.Service, error) {
	secretStoreMu.Lock()
	defer secretStoreMu.Unlock()
	if s.Secrets != nil {
		return s.Secrets, nil
	}
	svc, err := sealing.Start(context.Background(), sealing.Options{DB: s.Engine.Database(), Log: s.Log})
	if err != nil {
		return nil, err
	}
	s.Secrets = svc
	return svc, nil
}

// sharedSecret returns the secret every server of the installation shares
// under name (a signing or sealing key), made on first use, sealed at rest
// when a root key is in use.
func (s *Server) sharedSecret(name string) (string, error) {
	svc, err := s.secretStore()
	if err != nil {
		return "", err
	}
	return svc.SharedValue(name, auth.NewKey())
}

// writeNoRootKey answers a request for something that stores secrets on a
// server with no root key: 409 with a code a program can branch on, and the
// exact way to create one.
func writeNoRootKey(w http.ResponseWriter, what string) {
	writeJSON(w, http.StatusConflict, map[string]string{
		"error": what + " cannot be turned on: they would be stored in the clear, because this server has no root key for encryption at rest.",
		"code":  "no_root_key",
		"next":  "Create a root key: " + keyring.HowToCreateARootKey + ".",
	})
}
