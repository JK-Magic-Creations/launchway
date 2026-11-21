package sealing

import (
	"context"
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/keyring"
)

// Column is a database column that holds a secret. Every column that does
// is listed in Registry: the migration seals it, the status counts it, and
// the tests hold the code that writes it to sealing it.
type Column struct {
	Table, Column string
	// Key is the column that identifies the row (the row's binding).
	Key string
	// Where limits the rows (an SQL condition on this table), or "".
	Where   string
	Purpose string
	// Writers are the source files that write the column, relative to the
	// repository root; each must seal through this package (a test checks).
	Writers []string
	// Why says what the column holds.
	Why string

	// legacy decodes a value stored before sealing existed (nil: it was
	// stored as is).
	legacy func(ctx context.Context, s *Service, stored string) ([]byte, error)
}

func (c Column) binding(row string) keyring.Binding {
	return keyring.Binding{Purpose: c.Purpose, Table: c.Table, Column: c.Column, Row: row}
}

func (c Column) legacyOpen(ctx context.Context, s *Service, stored string) ([]byte, error) {
	if c.legacy == nil {
		return []byte(stored), nil
	}
	return c.legacy(ctx, s, stored)
}

// legacySealer builds the sealer the previous releases used: AES-256-GCM
// under a key kept in the same database (shared value name).
func legacySealer(ctx context.Context, s *Service, name, purpose string) (*auth.Sealer, error) {
	k, ok, err := s.ReadShared(ctx, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("the old sealing key %q is gone, so values sealed with it cannot be read", name)
	}
	return auth.NewSealer(k, purpose)
}

var (
	metaColumn = Column{
		Table: "meta", Column: "value", Key: "key", Where: "key LIKE 'shared:%'", Purpose: keyring.PurposeTokens,
		Writers: []string{"internal/sealing/service.go", "internal/sealing/migrate.go"},
		Why:     "the signing and sealing keys every server shares (login state, report links, Git run tokens) and the shared runner token",
	}
	hostKeysColumn = Column{
		Table: "ssh_host_keys", Column: "sealed", Key: "algo", Purpose: keyring.PurposeSecrets,
		Writers: []string{"internal/forge/sshkeys/hostkeys.go", "internal/sealing/migrate.go"},
		Why:     "the private host keys of the Git over SSH server",
		legacy: func(ctx context.Context, s *Service, stored string) ([]byte, error) {
			sl, err := legacySealer(ctx, s, "ssh_host_key_seal", "ssh-host-key")
			if err != nil {
				return nil, err
			}
			var der []byte
			if err := sl.Open(stored, &der); err != nil {
				return nil, err
			}
			return der, nil
		},
	}
	webhooksColumn = Column{
		Table: "repo_webhooks", Column: "secret_sealed", Key: "id", Purpose: keyring.PurposeSecrets,
		Writers: []string{"internal/forge/webhooks/store.go", "internal/sealing/migrate.go"},
		Why:     "the secrets that sign webhook deliveries",
		legacy: func(ctx context.Context, s *Service, stored string) ([]byte, error) {
			sl, err := legacySealer(ctx, s, "webhook_secret_seal", "webhook-secret")
			if err != nil {
				return nil, err
			}
			var secret string
			if err := sl.Open(stored, &secret); err != nil {
				return nil, err
			}
			return []byte(secret), nil
		},
	}
)

// Registry lists every column that holds a secret, in the order the
// migration seals them: the values that were sealed with a key from the
// shared values come first, because that key is itself a shared value.
//
// New code that stores a secret in the database adds its column here, in the
// same change. TestSchemaSecretColumnsAreRegistered fails on a column whose
// name looks like it holds a secret and that is neither here nor explained in
// NotSecret.
var Registry = []Column{webhooksColumn, hostKeysColumn, metaColumn}

// NotSecret lists columns whose names look like they hold a secret, and why
// they do not: a hash of a random value (a fast hash is right for 256 random
// bits), a public key, an identifier, or a reference to where a secret lives.
// TestSchemaSecretColumnsAreRegistered holds every column that looks
// secret to either this list or Registry.
var NotSecret = map[string]string{
	"sessions.token_hash":                 "SHA-256 of 256 random bits",
	"api_tokens.hash":                     "SHA-256 of 256 random bits",
	"runner_join_tokens.hash":             "SHA-256 of 256 random bits",
	"runner_access_tokens.hash":           "SHA-256 of 256 random bits",
	"runner_identities.public_key":        "a public key",
	"runner_identities.join_token":        "the join token's id, not the token",
	"ssh_keys.public":                     "a public key",
	"ssh_host_keys.public":                "a public key",
	"forge_connections.cred_env":          "the name of an environment variable",
	"forge_connections.cred_file":         "a file path",
	"repo_mirrors.cred_env":               "the name of an environment variable",
	"repo_mirrors.cred_file":              "a file path",
	"runner_assertions.token_id":          "an identifier",
	"artifact_blobs.blob_key":             "the object's name in the store",
	"cache_entries.blob_key":              "the object's name in the store",
	"blob_chunks.key":                     "the object's name in the store",
	"blob_objects.key":                    "the object's name in the store",
	"encryption_keks.wrapped":             "wrapped by the root key, useless without it",
	"encryption_deks.wrapped":             "wrapped by a key-encryption key, useless without it",
	"repo_hook_deliveries.secret_present": "a flag, not the secret",
	"ssh_keys.key_type":                   "the algorithm name",
	"variables.secret":                    "a flag saying a variable is a reference to a secret",
	"runner_pools.token_hash":             "SHA-256 of 256 random bits",
	"runner_pool_tokens.hash":             "SHA-256 of 256 random bits",
	"verifications.secret":                "a flag",
	"findings.secret":                     "a flag saying a finding is a secret was found; the secret itself is never stored",
	"findings.secret_kind":                "the kind of secret found, never the secret",
	"steps.secrets":                       "the names of the secrets a step uses, never values",
	"step_secrets.name":                   "a secret's name",
	"step_secrets.store":                  "a secret store's name",
	"step_secrets.field":                  "a field name",
	"project_secrets.name":                "a secret's name",
	"deployments.token":                   "an identifier of the deployment, not a credential",
	"runs.token_id":                       "an identifier",
	"audit.target":                        "a name",
	"push_decisions.token":                "an identifier",
	"approvals.token":                     "an identifier",
	"config_objects.spec":                 "references only: addresses with credentials and secret headers are refused when written (internal/settings)",
	"config_changes.spec":                 "references only: addresses with credentials and secret headers are refused when written",
	"config_revisions.spec":               "references only",
	"config_imports.keys":                 "names of settings",
	"repo_mirrors.url":                    "addresses with credentials are refused when written; the doctor counts any that remain",
	"repo_webhooks.url":                   "addresses with credentials are refused when written; the doctor counts any that remain",
	"forge_connections.base_url":          "addresses with credentials are refused when written; the doctor counts any that remain",
	"repo_webhooks.disabled_reason":       "a message, never the secret",
	"ssh_host_keys.fingerprint":           "a public fingerprint",
	"ssh_keys.fingerprint":                "a public fingerprint",
	"ssh_keys.last_used_from":             "an address",
	"runners.auth":                        "how a runner authenticates (a word)",
	"runner_identities.key_created_at":    "a time",
	"encryption_state.value":              "the installation id and migration progress; no key material",
	"push_decisions.detail":               "a message",
	"artifacts.sha256":                    "a digest of public content",
}

// Box seals and opens one registered column's values, each bound to its row.
// With no root key it opens what the previous releases stored, and refuses to
// store anything new.
type Box struct {
	s *Service
	c Column
}

// HostKeys is the box of the SSH host keys (ssh_host_keys.sealed).
func (s *Service) HostKeys() *Box { return &Box{s, hostKeysColumn} }

// Webhooks is the box of webhook secrets (repo_webhooks.secret_sealed).
func (s *Service) Webhooks() *Box { return &Box{s, webhooksColumn} }

// Enabled says whether new values can be sealed.
func (b *Box) Enabled() bool { return b.s.Encrypted() }

// Seal seals plain for the row named by row (the value of the column's key).
func (b *Box) Seal(ctx context.Context, row string, plain []byte) (string, error) {
	return b.s.keys.Seal(ctx, b.c.binding(row), plain)
}

// Open opens a stored value of that row.
func (b *Box) Open(ctx context.Context, row, stored string) ([]byte, error) {
	return b.s.openStored(ctx, b.c, row, stored)
}
