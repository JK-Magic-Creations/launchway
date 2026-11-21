package sealing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Tokens that a person pasted into the setup guide for a connection to another Git server are kept
// sealed in the database, as shared values (meta rows named "shared:conn-token/NAME"), so they are
// covered by the same registry entry, the same in-place sealing, the same rotation and the same
// dump test as the signing keys. The connection refers to one by the reference "sealed:NAME" (see
// forge.SecretRef); the token itself is never returned, never logged and never audited.

const connTokenPrefix = metaRowPrefix + "conn-token/"

// ErrTokenExists says a token is already kept under the name.
var ErrTokenExists = errors.New("a token is already kept under that name")

// ErrNoToken says no token is kept under the name.
var ErrNoToken = errors.New("no token is kept under that name")

// PutConnectionToken seals and keeps a token. It refuses without a root key (a token would sit in
// the clear) and refuses to replace one.
func (s *Service) PutConnectionToken(ctx context.Context, name, token string) error {
	if err := s.RequireRootKey("A pasted connection token"); err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" || token == "" {
		return errors.New("a connection token needs a name and a value")
	}
	row := connTokenPrefix + name
	sealed, err := s.keys.Seal(ctx, metaBinding(row), []byte(token))
	if err != nil {
		return err
	}
	res, err := s.kdb.Exec(ctx, `INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, row, sealed)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTokenExists
	}
	return nil
}

// ConnectionToken opens a kept token.
func (s *Service) ConnectionToken(ctx context.Context, name string) (string, error) {
	row := connTokenPrefix + name
	var v string
	err := s.db.QueryRow(ctx, `SELECT value FROM meta WHERE key = ?`, row).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrNoToken, name)
	}
	if err != nil {
		return "", err
	}
	plain, err := s.openStored(ctx, metaColumn, row, v)
	return string(plain), err
}

// DeleteConnectionToken removes a kept token (no error when there is none).
func (s *Service) DeleteConnectionToken(ctx context.Context, name string) error {
	_, err := s.kdb.Exec(ctx, `DELETE FROM meta WHERE key = ?`, connTokenPrefix+name)
	return err
}
