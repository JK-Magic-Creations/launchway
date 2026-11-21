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
		Writers: []string{"internal/sealing/service.go", "internal/sealing/conn_tokens.go"},
		Why:     "the signing and sealing keys every server shares (login state, report links, Git run tokens), the shared runner token, and the tokens pasted into the setup guide for connections to other Git servers",
	}
	hostKeysColumn = Column{
		Table: "ssh_host_keys", Column: "sealed", Key: "algo", Purpose: keyring.PurposeSecrets,
		Writers: []string{"internal/forge/sshkeys/hostkeys.go"},
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
	webhookURLColumn = Column{
		Table: "repo_webhooks", Column: "url", Key: "id", Purpose: keyring.PurposeSecrets,
		Writers: []string{"internal/forge/webhooks/store.go"},
		Why:     "the address a webhook delivers to: chat services' incoming webhooks carry their token in the path",
	}
	totpColumn = Column{
		Table: "local_accounts", Column: "totp_secret", Key: "username", Purpose: keyring.PurposeSecrets,
		Writers: []string{"internal/engine/accounts.go"},
		Why:     "the authenticator (TOTP) secrets of built-in accounts: with one, a code can be made for that person",
	}
	webhooksColumn = Column{
		Table: "repo_webhooks", Column: "secret_sealed", Key: "id", Purpose: keyring.PurposeSecrets,
		Writers: []string{"internal/forge/webhooks/store.go"},
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
// ExplainedColumns.
var Registry = []Column{webhooksColumn, webhookURLColumn, totpColumn, hostKeysColumn, metaColumn}

// ExplainedColumns lists columns whose names look like they hold a secret, and why
// they do not: a hash of a random value (a fast hash is right for 256 random
// bits), a public key, an identifier, or a reference to where a secret lives.
// TestSchemaSecretColumnsAreRegistered holds every column that looks
// secret to either this list or Registry.
var ExplainedColumns = func() map[string]string {
	out := make(map[string]string, len(explained))
	for _, e := range explained {
		out[e.column] = e.why
	}
	return out
}()

// explained is ExplainedColumns as written: a column and the reason.
var explained = []struct{ column, why string }{
	// Hashes of random values, or of public content: a fast hash is right for 256 random bits, and nothing can be recovered from it.
	{"sessions.id_hash", "SHA-256 of a 256-bit random session id"},
	{"api_tokens.hash", "SHA-256 of a 256-bit random token"},
	{"work_agents.token_hash", "SHA-256 of a 160-bit random agent token"},
	{"work_agents.token_hint", "the last four characters of the agent's token, to tell tokens apart"},
	{"work_agents.token_at", "when the token was made"},
	{"work_proposals.group_key", "the name of a kind of finding that proposals are grouped under: an identifier"},
	{"work_proposals.dedupe_key", "the stable identifier of a finding: an identifier"},
	{"work_usage.tokens", "a count of model tokens an agent reported spending: a number, not a credential"},
	{"work_agents.budget_tokens_day", "a count of model tokens an agent may report spending: a number, not a credential"},
	{"work_agents.item_tokens", "a count of model tokens an agent may report spending on one item: a number, not a credential"},
	{"work_items.source_key", "the key an item had where it was imported from, or the proposal group that made it: an identifier"},
	{"runner_access_tokens.hash", "SHA-256 of a 256-bit random token"},
	{"runner_join_tokens.hash", "SHA-256 of a 256-bit random token"},
	{"deployments.pod_hash", "a digest of a pod template, not a credential"},
	{"runs.pipeline_hash", "a digest of the pipeline text"},
	{"webhook_deliveries.body_hash", "a digest of a received body, kept to detect repeats"},
	{"artifact_blobs.hash_state", "the state of a running digest of public content"},
	{"caches.hash_state", "the state of a running digest of cache content"},

	// References to where a secret lives, and public keys: never the secret.
	{"forge_connections.cred_env", "the name of an environment variable"},
	{"forge_connections.cred_file", "a file path, or sealed:NAME, the name of a token kept sealed in meta"},
	{"repo_mirrors.cred_env", "the name of an environment variable"},
	{"repo_mirrors.cred_file", "a file path"},
	{"runner_identities.public_key", "a public key; the private half stays on the runner"},
	{"runner_identities.join_token", "the join token's id, not the token (whose hash is runner_join_tokens.hash)"},
	{"findings.secret", "a flag saying a finding is a secret that was found; the secret itself is never stored"},

	// Built-in accounts: hashes, never the value.
	{"local_accounts.password_set_at", "a time"},
	{"local_accounts.password_hash", "an Argon2id hash of the password (salted, 64 MiB, 3 passes), never the password"},
	{"local_recovery_codes.code_hash", "an Argon2id hash of a single-use recovery code (salted), never the code"},
	{"local_links.token_hash", "SHA-256 of a 160-bit random one-time link token"},
	{"setup_state.code_hash", "SHA-256 of a 123-bit random one-time setup code"},

	// Names, identifiers and keys of rows, not secrets.
	{"blob_objects.key", "the object's name in the blob store"},
	{"artifact_blobs.store_key", "the object's name in the blob store"},
	{"blob_uploads.key", "the object's name in the blob store"},
	{"caches.blob_key", "the object's name in the blob store"},
	{"caches.key", "the cache's key as the pipeline names it"},
	{"repo_packs.blob_key", "the object's name in the blob store"},
	{"deployments.snapshot_key", "the name of a stored snapshot"},
	{"lane_holders.lane_key", "the name of a lane"},
	{"lane_waiting.lane_key", "the name of a lane"},
	{"runs.lane_key", "the name of a lane"},
	{"sbom_components.name_key", "a normalized component name"},
	{"schedule_fires.sched_key", "the name of a schedule"},
	{"schedule_seen.sched_key", "the name of a schedule"},
	{"step_inputs.origin_key", "where an input came from"},
	{"test_cases.test_key", "a test's identity"},
	{"notifications.event_key", "the identity of the event a notification is about (run, kind), kept so that no message is sent twice; never a credential"},
	{"encryption_state.key", "the name of a fact (the table holds the installation id and migration progress, no key material)"},
	{"meta.key", "the name of a meta row"},

	// Counts.
	{"ai_explanations.input_tokens", "a count of model tokens"},
	{"ai_explanations.output_tokens", "a count of model tokens"},
	{"ai_usage.input_tokens", "a count of model tokens"},
	{"ai_usage.output_tokens", "a count of model tokens"},
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

// HookBox is the box of webhook secrets (repo_webhooks.secret_sealed), which
// also seals the hook's address (repo_webhooks.url, SealURL and OpenURL).
type HookBox struct {
	*Box
	urls *Box
}

// TOTP is the box of built-in accounts' authenticator secrets
// (local_accounts.totp_secret); the engine seals and opens through it.
func (s *Service) TOTP() *Box { return &Box{s, totpColumn} }

// Webhooks is the box of webhook secrets and addresses.
func (s *Service) Webhooks() *HookBox {
	return &HookBox{&Box{s, webhooksColumn}, &Box{s, webhookURLColumn}}
}

// SealURL seals a hook's address, bound to the hook.
func (b *HookBox) SealURL(ctx context.Context, row string, plain []byte) (string, error) {
	return b.urls.Seal(ctx, row, plain)
}

// OpenURL opens the stored address of a hook.
func (b *HookBox) OpenURL(ctx context.Context, row, stored string) ([]byte, error) {
	return b.urls.Open(ctx, row, stored)
}

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

// SealRow is Seal for callers that have no context to give (the engine's
// account calls): it runs under one that is never cancelled, so that a request
// that gave up cannot leave a half-sealed account behind.
func (b *Box) SealRow(row string, plain []byte) (string, error) {
	return b.Seal(context.Background(), row, plain)
}

// OpenRow is Open under the same rule.
func (b *Box) OpenRow(row, stored string) ([]byte, error) {
	return b.Open(context.Background(), row, stored)
}
