package secrets

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Transit wraps and unwraps small values with a key of the transit secrets
// engine of HashiCorp Vault or OpenBao. The key never leaves the service:
// the server only sends a value to be encrypted or decrypted. It logs in the
// same ways a vault secret store does (token in a variable or a file, or
// Kubernetes auth) and uses the same connection settings.
type Transit struct {
	v     *Vault
	mount string
	key   string
}

// OpenTransit checks the connection settings (the vault fields of
// StoreConfig) and the transit mount and key names. It does not contact the
// service.
func OpenTransit(s StoreConfig, mount, key string) (*Transit, error) {
	v, err := newVault(s)
	if err != nil {
		return nil, err
	}
	mount = strings.Trim(mount, "/")
	if mount == "" {
		mount = "transit"
	}
	if !cleanPath(mount) {
		return nil, errors.New("the transit mount must be a path such as transit")
	}
	if key == "" || strings.Contains(key, "/") {
		return nil, errors.New("name the transit key (no slashes)")
	}
	return &Transit{v: v, mount: mount, key: key}, nil
}

// Where describes the service and key, never a credential.
func (t *Transit) Where() string { return t.v.addr + " (" + t.mount + "/keys/" + t.key + ")" }

// call posts one transit request, logging in again once if the token was refused.
func (t *Transit) call(ctx context.Context, op string, in map[string]string, out any) error {
	body, _ := json.Marshal(in)
	path := "/v1/" + escapePath(t.mount) + "/" + op + "/" + escapePath(t.key)
	tok, err := t.v.loginToken(ctx)
	if err != nil {
		return err
	}
	status, err := t.v.do(ctx, http.MethodPost, path, tok, body, out)
	if err == nil && status == http.StatusForbidden {
		t.v.forget(tok)
		if tok, err = t.v.loginToken(ctx); err != nil {
			return err
		}
		status, err = t.v.do(ctx, http.MethodPost, path, tok, body, out)
	}
	switch {
	case err != nil:
		return err
	case status == http.StatusForbidden:
		return notFound("permission denied: the token may not use the transit key %s/%s", t.mount, t.key)
	case status == http.StatusNotFound:
		return notFound("there is no transit key %s/%s (or no transit engine at %s)", t.mount, t.key, t.mount)
	}
	return nil
}

// Encrypt returns the service's ciphertext of plain.
func (t *Transit) Encrypt(ctx context.Context, plain []byte) (string, error) {
	var out struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := t.call(ctx, "encrypt", map[string]string{"plaintext": base64.StdEncoding.EncodeToString(plain)}, &out); err != nil {
		return "", err
	}
	if out.Data.Ciphertext == "" {
		return "", errors.New("the transit service returned no ciphertext")
	}
	return out.Data.Ciphertext, nil
}

// Decrypt reverses Encrypt.
func (t *Transit) Decrypt(ctx context.Context, ciphertext string) ([]byte, error) {
	var out struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := t.call(ctx, "decrypt", map[string]string{"ciphertext": ciphertext}, &out); err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(out.Data.Plaintext)
	if err != nil {
		return nil, errors.New("the transit service returned something that is not base64")
	}
	return b, nil
}
