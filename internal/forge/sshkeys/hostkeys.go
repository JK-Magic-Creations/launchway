package sshkeys

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Sealer encrypts a small value with a key every server shares (internal/auth's
// Sealer does).
type Sealer interface {
	Seal(v any) (string, error)
	Open(sealed string, v any) error
}

// HostAlgorithms are the host keys a server presents, in order of preference:
// ed25519 for every modern client, ECDSA and RSA for older ones.
var HostAlgorithms = []string{"ed25519", "ecdsa", "rsa"}

// HostKey is a host key as people see it.
type HostKey struct {
	Algorithm   string    `json:"algorithm"`
	Type        string    `json:"type"`
	Fingerprint string    `json:"fingerprint"`
	PublicKey   string    `json:"public_key"`
	CreatedAt   time.Time `json:"created_at"`
}

func generate(algo string) (any, error) {
	switch algo {
	case "ed25519":
		_, k, err := ed25519.GenerateKey(rand.Reader)
		return k, err
	case "ecdsa":
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "rsa":
		return rsa.GenerateKey(rand.Reader, 3072)
	}
	return nil, fmt.Errorf("unknown host key algorithm %q", algo)
}

// HostSigners returns the host keys, making the missing ones. Servers that
// start together all end up with the same keys: each generates, one insert
// wins, and every server reads back the winner's. Keys are stored sealed.
func (s *Store) HostSigners(ctx context.Context, seal Sealer) ([]ssh.Signer, error) {
	var out []ssh.Signer
	for _, algo := range HostAlgorithms {
		sg, err := s.hostSigner(ctx, seal, algo)
		if err != nil {
			return nil, fmt.Errorf("the %s host key: %w", algo, err)
		}
		out = append(out, sg)
	}
	return out, nil
}

func (s *Store) hostSigner(ctx context.Context, seal Sealer, algo string) (ssh.Signer, error) {
	var sealed string
	err := s.db.QueryRow(ctx, `SELECT sealed FROM ssh_host_keys WHERE algo = ?`, algo).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		raw, gerr := generate(algo)
		if gerr != nil {
			return nil, gerr
		}
		der, err := x509.MarshalPKCS8PrivateKey(raw)
		if err != nil {
			return nil, err
		}
		sg, err := ssh.NewSignerFromKey(raw)
		if err != nil {
			return nil, err
		}
		blob, err := seal.Seal(der)
		if err != nil {
			return nil, err
		}
		pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sg.PublicKey())))
		if _, err := s.db.Exec(ctx, `INSERT INTO ssh_host_keys(algo, public, sealed, fingerprint, created_at) VALUES(?,?,?,?,?)
			ON CONFLICT(algo) DO NOTHING`, algo, pub, blob, ssh.FingerprintSHA256(sg.PublicKey()), s.now().UnixMilli()); err != nil {
			return nil, err
		}
		err = s.db.QueryRow(ctx, `SELECT sealed FROM ssh_host_keys WHERE algo = ?`, algo).Scan(&sealed)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	var der []byte
	if err := seal.Open(sealed, &der); err != nil {
		return nil, fmt.Errorf("cannot be opened with this server's key (was the sealing key changed?): %w", err)
	}
	raw, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(raw)
}

// HostKeys lists the public host keys, for the page where people verify the
// server's fingerprints.
func (s *Store) HostKeys(ctx context.Context) ([]HostKey, error) {
	rows, err := s.db.Query(ctx, `SELECT algo, public, fingerprint, created_at FROM ssh_host_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byAlgo := map[string]HostKey{}
	for rows.Next() {
		var h HostKey
		var at int64
		if err := rows.Scan(&h.Algorithm, &h.PublicKey, &h.Fingerprint, &at); err != nil {
			return nil, err
		}
		h.Type, _, _ = strings.Cut(h.PublicKey, " ")
		h.CreatedAt = time.UnixMilli(at).UTC()
		byAlgo[h.Algorithm] = h
	}
	var out []HostKey
	for _, a := range HostAlgorithms {
		if h, ok := byAlgo[a]; ok {
			out = append(out, h)
		}
	}
	return out, rows.Err()
}
